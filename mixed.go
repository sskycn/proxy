package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	socksVersion5        = byte(0x05)
	socksCmdConnect      = byte(0x01)
	socksCmdUDPAssociate = byte(0x03)
	socksAtypIPv4        = byte(0x01)
	socksAtypDomain      = byte(0x03)
	socksAtypIPv6        = byte(0x04)
	maxHTTPHeaderBytes   = 64 * 1024
	defaultHTTPPort      = "80"
	defaultHTTPSPort     = "443"
	socksReplySucceeded  = byte(0x00)
)

var (
	errSocksUnsupportedVersion = errors.New("unsupported socks version")
	errSocksUnsupportedMethod  = errors.New("unsupported socks auth method")
	errSocksUnsupportedCommand = errors.New("unsupported socks command")
	errSocksUnsupportedAddress = errors.New("unsupported socks address")
	errHTTPHeaderTooLarge      = errors.New("http header too large")
	errHTTPMalformedRequest    = errors.New("malformed http proxy request")
)

type socksRequest struct {
	cmd  byte
	host string
	port uint16
}

type httpProxyRequest struct {
	raw       []byte
	method    string
	target    string
	proto     string
	host      string
	headerRaw []byte
}

func (s *proxyServer) routeMixed(ctx context.Context, client net.Conn, reader *bufio.Reader) error {
	first, err := reader.Peek(1)
	if err != nil {
		return err
	}
	if len(first) == 0 {
		return io.ErrUnexpectedEOF
	}

	if first[0] == socksVersion5 {
		return s.handleSocks5(ctx, client, reader)
	}
	if mayStartHTTP(first[0]) {
		req, err := readHTTPProxyRequest(reader)
		if err == nil {
			return s.handleHTTPProxy(ctx, client, reader, req)
		}
		if !errors.Is(err, errHTTPMalformedRequest) {
			return err
		}
	}

	return s.proxyViaUpstream(ctx, client, reader, nil, "unknown")
}

func (s *proxyServer) handleSocks5(ctx context.Context, client net.Conn, reader *bufio.Reader) error {
	if err := readSocks5Greeting(reader); err != nil {
		if errors.Is(err, errSocksUnsupportedMethod) {
			return writeAll(client, []byte{socksVersion5, 0xff})
		}
		return err
	}
	if err := writeAll(client, []byte{socksVersion5, 0x00}); err != nil {
		return err
	}

	req, err := readSocks5Request(reader)
	if err != nil {
		if errors.Is(err, errSocksUnsupportedCommand) || errors.Is(err, errSocksUnsupportedAddress) {
			return writeSocks5Reply(client, 0x07)
		}
		return err
	}

	switch req.cmd {
	case socksCmdConnect:
		return s.handleSocks5Connect(ctx, client, reader, req)
	case socksCmdUDPAssociate:
		return s.handleSocks5UDPAssociate(ctx, client, reader, req)
	default:
		return writeSocks5Reply(client, 0x07)
	}
}

func (s *proxyServer) handleSocks5Connect(ctx context.Context, client net.Conn, reader *bufio.Reader, req socksRequest) error {
	target := net.JoinHostPort(req.host, strconv.Itoa(int(req.port)))
	cacheKey := directCacheKey("tcp", req.host, strconv.Itoa(int(req.port)))
	if !s.routes.shouldForceUpstream(req.host) {
		direct, tried, err := s.connectDirectTCP(ctx, cacheKey, req.host, target)
		if err != nil && s.cfg.Verbose {
			if logErr := logf(s.log, "direct socks %s failed, fallback upstream: %v\n", target, err); logErr != nil {
				return logErr
			}
		}
		if tried && err == nil {
			defer closeConnWithLog(direct, s.log, "direct socks target "+target)
			if err := writeSocks5Reply(client, socksReplySucceeded); err != nil {
				return err
			}
			if s.cfg.Verbose {
				if err := logf(s.log, "direct socks %s -> %s\n", client.RemoteAddr(), target); err != nil {
					return err
				}
			}
			if err := accessLog(s.log, client.RemoteAddr().String(), target, "direct"); err != nil {
				return err
			}
			return s.bridge(direct, client, reader)
		}
	} else if s.cfg.Verbose {
		if err := logf(s.log, "force upstream socks %s\n", target); err != nil {
			return err
		}
	}

	upstream, target, err := s.connectViaUpstreamSocks5(ctx, req)
	if err != nil {
		if writeErr := writeSocks5Reply(client, 0x01); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return nil
	}
	defer closeConnWithLog(upstream, s.log, "upstream socks "+target)
	if err := writeSocks5Reply(client, socksReplySucceeded); err != nil {
		return err
	}
	if s.cfg.Verbose {
		if err := logf(s.log, "proxy socks %s -> %s via %s\n", client.RemoteAddr(), net.JoinHostPort(req.host, strconv.Itoa(int(req.port))), target); err != nil {
			return err
		}
	}
	if err := accessLog(s.log, client.RemoteAddr().String(), net.JoinHostPort(req.host, strconv.Itoa(int(req.port))), "proxy"); err != nil {
		return err
	}
	return s.bridge(upstream, client, reader)
}

func (s *proxyServer) handleHTTPProxy(ctx context.Context, client net.Conn, reader *bufio.Reader, req *httpProxyRequest) error {
	host, port, err := requestHostPort(req)
	if err != nil {
		return err
	}
	targetHost := trimHostBrackets(host)
	directTarget := net.JoinHostPort(targetHost, port)

	cacheKey := directCacheKey("tcp", targetHost, port)
	if !s.routes.shouldForceUpstream(targetHost) {
		direct, tried, err := s.connectDirectTCP(ctx, cacheKey, targetHost, directTarget)
		if err != nil && s.cfg.Verbose {
			if logErr := logf(s.log, "direct http %s failed, fallback upstream: %v\n", directTarget, err); logErr != nil {
				return logErr
			}
		}
		if tried && err == nil {
			defer closeConnWithLog(direct, s.log, "direct http target "+directTarget)
			if strings.EqualFold(req.method, "CONNECT") {
				if err := writeAll(client, []byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
					return err
				}
				if s.cfg.Verbose {
					if err := logf(s.log, "direct connect %s -> %s\n", client.RemoteAddr(), directTarget); err != nil {
						return err
					}
				}
				if err := accessLog(s.log, client.RemoteAddr().String(), directTarget, "direct"); err != nil {
					return err
				}
				return s.bridge(direct, client, reader)
			}
			rewritten, err := rewriteHTTPProxyRequest(req)
			if err != nil {
				return err
			}
			if err := writeAll(direct, rewritten); err != nil {
				return err
			}
			if s.cfg.Verbose {
				if err := logf(s.log, "direct http %s -> %s\n", client.RemoteAddr(), directTarget); err != nil {
					return err
				}
			}
			if err := accessLog(s.log, client.RemoteAddr().String(), directTarget, "direct"); err != nil {
				return err
			}
			return s.bridge(direct, client, reader)
		}
	} else if s.cfg.Verbose {
		if err := logf(s.log, "force upstream http %s\n", directTarget); err != nil {
			return err
		}
	}

	return s.proxyViaUpstream(ctx, client, reader, req.raw, directTarget)
}

func (s *proxyServer) connectDirectTCP(ctx context.Context, cacheKey string, host string, target string) (net.Conn, bool, error) {
	if !s.direct.shouldTry(cacheKey) {
		return nil, false, nil
	}
	conn, err := s.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		s.direct.markUpstreamOnly(cacheKey, host)
		return nil, true, err
	}
	if err := tuneTCP(conn); err != nil {
		closeErr := conn.Close()
		s.direct.markUpstreamOnly(cacheKey, host)
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			return nil, true, errors.Join(err, closeErr)
		}
		return nil, true, err
	}
	return conn, true, nil
}

func directCacheKey(network string, host string, port string) string {
	return network + ":" + strings.ToLower(trimHostBrackets(host)) + ":" + port
}

func (s *proxyServer) connectViaUpstreamSocks5(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	upstream, target, err := s.connectUpstreamRaw(ctx)
	if err != nil {
		return nil, target, err
	}
	if err := writeAll(upstream, []byte{socksVersion5, 0x01, 0x00}); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(upstream, reply); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	if reply[0] != socksVersion5 || reply[1] != 0x00 {
		return nil, target, closeAfterError(upstream, fmt.Errorf("upstream socks auth reply %v", reply))
	}
	if err := writeAll(upstream, buildSocks5ConnectRequest(req)); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	if err := readSocks5ConnectReply(upstream); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	return upstream, target, nil
}

func readSocks5Greeting(reader *bufio.Reader) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(reader, head); err != nil {
		return err
	}
	if head[0] != socksVersion5 {
		return errSocksUnsupportedVersion
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == 0x00 {
			return nil
		}
	}
	return errSocksUnsupportedMethod
}

func readSocks5Request(reader *bufio.Reader) (socksRequest, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(reader, head); err != nil {
		return socksRequest{}, err
	}
	if head[0] != socksVersion5 {
		return socksRequest{}, errSocksUnsupportedVersion
	}
	var host string
	switch head[3] {
	case socksAtypIPv4:
		addr := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return socksRequest{}, err
		}
		host = net.IP(addr).String()
	case socksAtypIPv6:
		addr := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return socksRequest{}, err
		}
		host = net.IP(addr).String()
	case socksAtypDomain:
		length, err := reader.ReadByte()
		if err != nil {
			return socksRequest{}, err
		}
		addr := make([]byte, int(length))
		if _, err := io.ReadFull(reader, addr); err != nil {
			return socksRequest{}, err
		}
		host = string(addr)
	default:
		return socksRequest{}, errSocksUnsupportedAddress
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return socksRequest{}, err
	}
	return socksRequest{cmd: head[1], host: host, port: binary.BigEndian.Uint16(portBytes)}, nil
}

func writeSocks5Reply(w io.Writer, reply byte) error {
	return writeSocks5ReplyAddr(w, reply, &net.TCPAddr{IP: net.IPv4zero, Port: 0})
}

func writeSocks5ReplyAddr(w io.Writer, reply byte, addr net.Addr) error {
	host, port, err := addrHostPort(addr)
	if err != nil {
		return err
	}
	packet := []byte{socksVersion5, reply, 0x00}
	packet = append(packet, encodeSocksAddr(host)...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	packet = append(packet, portBytes...)
	return writeAll(w, packet)
}

func buildSocks5ConnectRequest(req socksRequest) []byte {
	return buildSocks5Request(socksCmdConnect, req.host, req.port)
}

func buildSocks5UDPAssociateRequest(host string, port uint16) []byte {
	return buildSocks5Request(socksCmdUDPAssociate, host, port)
}

func buildSocks5Request(cmd byte, host string, port uint16) []byte {
	buf := []byte{socksVersion5, cmd, 0x00}
	buf = append(buf, encodeSocksAddr(host)...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, port)
	buf = append(buf, portBytes...)
	return buf
}

func encodeSocksAddr(host string) []byte {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return append([]byte{socksAtypIPv4}, v4...)
		} else {
			return append([]byte{socksAtypIPv6}, ip.To16()...)
		}
	}
	buf := []byte{socksAtypDomain, byte(len(host))}
	buf = append(buf, host...)
	return buf
}

func addrHostPort(addr net.Addr) (string, int, error) {
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, err
	}
	if port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in address %s", addr)
	}
	return trimHostBrackets(host), port, nil
}

func readSocks5ConnectReply(reader io.Reader) error {
	head := make([]byte, 4)
	if _, err := io.ReadFull(reader, head); err != nil {
		return err
	}
	if head[0] != socksVersion5 {
		return errSocksUnsupportedVersion
	}
	if head[1] != socksReplySucceeded {
		return fmt.Errorf("upstream socks connect failed with reply %d", head[1])
	}
	var skip int
	switch head[3] {
	case socksAtypIPv4:
		skip = net.IPv4len
	case socksAtypIPv6:
		skip = net.IPv6len
	case socksAtypDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return err
		}
		skip = int(length[0])
	default:
		return errSocksUnsupportedAddress
	}
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, reader, int64(skip)); err != nil {
			return err
		}
	}
	if _, err := io.CopyN(io.Discard, reader, 2); err != nil {
		return err
	}
	return nil
}

func readHTTPProxyRequest(reader *bufio.Reader) (*httpProxyRequest, error) {
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !looksLikeHTTPRequestLine(firstLine) {
		return nil, errHTTPMalformedRequest
	}

	raw := []byte(firstLine)
	headerRaw := make([]byte, 0, 512)
	host := ""
	for {
		if len(raw) > maxHTTPHeaderBytes {
			return nil, errHTTPHeaderTooLarge
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		raw = append(raw, line...)
		if line == "\r\n" || line == "\n" {
			break
		}
		headerRaw = append(headerRaw, line...)
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			host = strings.TrimSpace(line[len("host:"):])
		}
	}

	parts := strings.SplitN(strings.TrimRight(firstLine, "\r\n"), " ", 3)
	if len(parts) != 3 {
		return nil, errHTTPMalformedRequest
	}
	return &httpProxyRequest{
		raw:       raw,
		method:    parts[0],
		target:    parts[1],
		proto:     parts[2],
		host:      host,
		headerRaw: headerRaw,
	}, nil
}

func requestHostPort(req *httpProxyRequest) (string, string, error) {
	if strings.EqualFold(req.method, "CONNECT") {
		host, port, err := net.SplitHostPort(req.target)
		if err != nil {
			return "", "", err
		}
		return host, port, nil
	}
	if strings.HasPrefix(req.target, "http://") || strings.HasPrefix(req.target, "https://") {
		parsed, err := url.Parse(req.target)
		if err != nil {
			return "", "", err
		}
		host := parsed.Hostname()
		port := parsed.Port()
		if port == "" {
			if parsed.Scheme == "https" {
				port = defaultHTTPSPort
			} else {
				port = defaultHTTPPort
			}
		}
		return host, port, nil
	}
	if req.host == "" {
		return "", "", errHTTPMalformedRequest
	}
	host, port, err := splitOptionalPort(req.host, defaultHTTPPort)
	if err != nil {
		return "", "", err
	}
	return host, port, nil
}

func rewriteHTTPProxyRequest(req *httpProxyRequest) ([]byte, error) {
	target := req.target
	if strings.HasPrefix(req.target, "http://") || strings.HasPrefix(req.target, "https://") {
		parsed, err := url.Parse(req.target)
		if err != nil {
			return nil, err
		}
		target = parsed.RequestURI()
		if target == "" {
			target = "/"
		}
	}
	var out bytes.Buffer
	if _, err := fmt.Fprintf(&out, "%s %s %s\r\n", req.method, target, req.proto); err != nil {
		return nil, err
	}
	if _, err := out.Write(req.headerRaw); err != nil {
		return nil, err
	}
	if _, err := out.Write([]byte("\r\n")); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func mayStartHTTP(b byte) bool {
	return b >= 'A' && b <= 'Z'
}

func looksLikeHTTPRequestLine(line string) bool {
	trimmed := strings.TrimRight(line, "\r\n")
	parts := strings.SplitN(trimmed, " ", 3)
	return len(parts) == 3 && strings.HasPrefix(parts[2], "HTTP/")
}

func hostIsInternal(ctx context.Context, host string) bool {
	host = trimHostBrackets(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ipIsInternal(ip)
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return true
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ipIsInternal(ip) {
			return true
		}
	}
	return false
}

func ipIsInternal(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

func trimHostBrackets(host string) string {
	return strings.Trim(strings.TrimSpace(host), "[]")
}

func splitOptionalPort(hostport string, defaultPort string) (string, string, error) {
	host := strings.TrimSpace(hostport)
	if host == "" {
		return "", "", errHTTPMalformedRequest
	}
	parsedHost, parsedPort, err := net.SplitHostPort(host)
	if err == nil {
		return parsedHost, parsedPort, nil
	}
	if strings.Contains(err.Error(), "missing port in address") {
		return trimHostBrackets(host), defaultPort, nil
	}
	return "", "", err
}

func writeHTTPBadGateway(w io.Writer) error {
	return writeAll(w, []byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func closeAfterError(conn net.Conn, cause error) error {
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return errors.Join(cause, err)
	}
	return cause
}

func closeConnWithLog(conn net.Conn, log io.Writer, label string) {
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		if logErr := logf(log, "close %s: %v\n", label, err); logErr != nil {
			return
		}
	}
}
