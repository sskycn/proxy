package proxy

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
	"time"
)

const (
	socksVersion5        = byte(0x05)
	socksAuthVersion     = byte(0x01)
	socksMethodNoAuth    = byte(0x00)
	socksMethodUserPass  = byte(0x02)
	socksMethodNoAccept  = byte(0xff)
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
		if errors.Is(err, errHTTPMalformedRequest) && s.cfg.Mode == proxyModeLocal && s.cfg.UpstreamProtocol == upstreamProtocolMixed {
			var initial []byte
			if req != nil {
				initial = req.raw
			}
			return s.proxyViaUpstreamRaw(ctx, client, reader, initial, "mixed", "unknown")
		}
		if !errors.Is(err, errHTTPMalformedRequest) {
			return err
		}
	}

	if s.cfg.Mode == proxyModeLocal && s.cfg.UpstreamProtocol == upstreamProtocolMixed {
		return s.proxyViaUpstreamRaw(ctx, client, reader, nil, "mixed", "unknown")
	}
	return accessLog(s.log, accessSource("mixed", client.RemoteAddr()), "", "unknown", "unsupported mixed traffic")
}

func (s *proxyServer) handleSocks5(ctx context.Context, client net.Conn, reader *bufio.Reader) error {
	method, err := readSocks5Greeting(reader, s.localSOCKS5AuthRequired())
	if err != nil {
		if errors.Is(err, errSocksUnsupportedMethod) {
			return writeAll(client, []byte{socksVersion5, socksMethodNoAccept})
		}
		return err
	}
	if err := writeAll(client, []byte{socksVersion5, method}); err != nil {
		return err
	}
	if method == socksMethodUserPass {
		if err := s.authenticateLocalSOCKS5(reader, client); err != nil {
			return err
		}
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
	logTarget := accessTarget(req.host, strconv.Itoa(int(req.port)))
	cacheKey := directCacheKey("tcp", req.host, strconv.Itoa(int(req.port)))
	if !s.routes.shouldForceUpstream(req.host) {
		direct, tried, err := s.connectDirectTCP(ctx, cacheKey, req.host, target)
		if err != nil && s.cfg.Verbose {
			if logErr := logf(s.log, "direct socks %s failed, fallback upstream: %v\n", target, err); logErr != nil {
				return logErr
			}
		}
		if tried && err == nil {
			if err := writeSocks5Reply(client, socksReplySucceeded); err != nil {
				closeConnWithLog(direct, s.log, "direct socks target "+target)
				return err
			}
			outbound, outboundReader, upstreamTarget, directOK, err := s.probeDirectTunnelOrFallback(ctx, client, reader, direct, cacheKey, req.host, func(ctx context.Context) (net.Conn, io.Reader, string, error) {
				upstream, upstreamTarget, err := s.connectViaUpstreamTCP(ctx, req)
				if err != nil {
					return nil, nil, upstreamTarget, err
				}
				return upstream, upstream, upstreamTarget, nil
			})
			if err != nil {
				if logErr := accessLog(s.log, accessSource("socks5", client.RemoteAddr()), upstreamTarget, logTarget, err.Error()); logErr != nil {
					return errors.Join(err, logErr)
				}
				return err
			}
			closeLabel := "upstream socks " + upstreamTarget
			proxyField := upstreamTarget
			if directOK {
				closeLabel = "direct socks target " + target
				proxyField = "-"
			}
			defer closeConnWithLog(outbound, s.log, closeLabel)
			if s.cfg.Verbose {
				kind := "proxy"
				if directOK {
					kind = "direct"
				}
				if err := logf(s.log, "%s socks %s -> %s\n", kind, client.RemoteAddr(), target); err != nil {
					return err
				}
			}
			if err := s.bridgeWithReaders(outbound, client, outboundReader, reader); err != nil {
				if logErr := accessLog(s.log, accessSource("socks5", client.RemoteAddr()), proxyField, logTarget, err.Error()); logErr != nil {
					return errors.Join(err, logErr)
				}
				return err
			}
			return accessLog(s.log, accessSource("socks5", client.RemoteAddr()), proxyField, logTarget, "ok")
		}
	} else if s.cfg.Verbose {
		if err := logf(s.log, "force upstream socks %s\n", target); err != nil {
			return err
		}
	}

	requestedTarget := logTarget
	upstream, target, err := s.connectViaUpstreamTCP(ctx, req)
	if err != nil {
		if logErr := accessLog(s.log, accessSource("socks5", client.RemoteAddr()), target, requestedTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
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
	if err := s.bridge(upstream, client, reader); err != nil {
		if logErr := accessLog(s.log, accessSource("socks5", client.RemoteAddr()), target, requestedTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource("socks5", client.RemoteAddr()), target, requestedTarget, "ok")
}

func (s *proxyServer) handleHTTPProxy(ctx context.Context, client net.Conn, reader *bufio.Reader, req *httpProxyRequest) error {
	logProtocol := httpAccessProtocol(req)
	host, port, err := requestHostPort(req)
	if err != nil {
		if s.cfg.Mode == proxyModeLocal && s.cfg.UpstreamProtocol == upstreamProtocolMixed {
			return s.proxyViaUpstreamRaw(ctx, client, reader, req.raw, logProtocol, "unknown")
		}
		return err
	}
	targetHost := trimHostBrackets(host)
	directTarget := net.JoinHostPort(targetHost, port)
	logTarget := accessTarget(host, port)

	cacheKey := directCacheKey("tcp", targetHost, port)
	if !s.routes.shouldForceUpstream(targetHost) {
		direct, tried, err := s.connectDirectTCP(ctx, cacheKey, targetHost, directTarget)
		if err != nil && s.cfg.Verbose {
			if logErr := logf(s.log, "direct http %s failed, fallback upstream: %v\n", directTarget, err); logErr != nil {
				return logErr
			}
		}
		if tried && err == nil {
			if strings.EqualFold(req.method, "CONNECT") {
				if err := writeAll(client, []byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
					closeConnWithLog(direct, s.log, "direct http target "+directTarget)
					return err
				}
				socksReq, err := socksRequestFromHostPort(targetHost, port)
				if err != nil {
					closeConnWithLog(direct, s.log, "direct http target "+directTarget)
					return err
				}
				outbound, outboundReader, upstreamTarget, directOK, err := s.probeDirectTunnelOrFallback(ctx, client, reader, direct, cacheKey, targetHost, func(ctx context.Context) (net.Conn, io.Reader, string, error) {
					return s.connectHTTPConnectFallback(ctx, req, socksReq)
				})
				if err != nil {
					if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, logTarget, err.Error()); logErr != nil {
						return errors.Join(err, logErr)
					}
					return err
				}
				closeLabel := "upstream http " + upstreamTarget
				proxyField := upstreamTarget
				if directOK {
					closeLabel = "direct http target " + directTarget
					proxyField = "-"
				}
				defer closeConnWithLog(outbound, s.log, closeLabel)
				if s.cfg.Verbose {
					kind := "proxy"
					if directOK {
						kind = "direct"
					}
					if err := logf(s.log, "%s connect %s -> %s\n", kind, client.RemoteAddr(), directTarget); err != nil {
						return err
					}
				}
				if err := s.bridgeWithReaders(outbound, client, outboundReader, reader); err != nil {
					if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), proxyField, logTarget, err.Error()); logErr != nil {
						return errors.Join(err, logErr)
					}
					return err
				}
				return accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), proxyField, logTarget, "ok")
			}
			defer closeConnWithLog(direct, s.log, "direct http target "+directTarget)
			rewritten, err := rewriteHTTPProxyRequest(req)
			if err != nil {
				return err
			}
			if err := writeAll(direct, rewritten); err != nil {
				if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), "-", logTarget, err.Error()); logErr != nil {
					return errors.Join(err, logErr)
				}
				return err
			}
			directReader := io.Reader(direct)
			directUsable := true
			if shouldProbeDirectHTTPResponse(req) {
				probedReader, probeErr := probeDirectHTTPResponse(direct, s.cfg.DirectProbeTimeout)
				if probeErr != nil {
					s.direct.markUpstreamOnly(cacheKey, targetHost)
					if closeErr := direct.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
						probeErr = errors.Join(probeErr, closeErr)
					}
					if s.cfg.Verbose {
						if logErr := logf(s.log, "direct http %s response probe failed, fallback upstream: %v\n", directTarget, probeErr); logErr != nil {
							return logErr
						}
					}
					directUsable = false
				} else {
					directReader = probedReader
				}
			}
			if directUsable {
				if s.cfg.Verbose {
					if err := logf(s.log, "direct http %s -> %s\n", client.RemoteAddr(), directTarget); err != nil {
						return err
					}
				}
				if err := s.bridgeWithReaders(direct, client, directReader, reader); err != nil {
					if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), "-", logTarget, err.Error()); logErr != nil {
						return errors.Join(err, logErr)
					}
					return err
				}
				return accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), "-", logTarget, "ok")
			}
		}
	} else if s.cfg.Verbose {
		if err := logf(s.log, "force upstream http %s\n", directTarget); err != nil {
			return err
		}
	}

	if s.cfg.Mode == proxyModeLocal && s.cfg.UpstreamProtocol == upstreamProtocolMixed {
		return s.proxyViaUpstreamRaw(ctx, client, reader, req.raw, logProtocol, logTarget)
	}
	return s.handleHTTPUpstreamSocks5(ctx, client, reader, req, targetHost, port, logTarget)
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

func (s *proxyServer) handleHTTPUpstreamSocks5(ctx context.Context, client net.Conn, reader *bufio.Reader, req *httpProxyRequest, host string, port string, target string) error {
	logProtocol := httpAccessProtocol(req)
	socksReq, err := socksRequestFromHostPort(host, port)
	if err != nil {
		if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), s.upstreamTarget(), target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}

	upstream, upstreamTarget, err := s.connectViaUpstreamTCP(ctx, socksReq)
	if err != nil {
		if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		if writeErr := writeHTTPBadGateway(client); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return nil
	}
	defer closeConnWithLog(upstream, s.log, "upstream http "+upstreamTarget)

	if strings.EqualFold(req.method, "CONNECT") {
		if err := writeAll(client, []byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
				return errors.Join(err, logErr)
			}
			return err
		}
		if err := s.bridge(upstream, client, reader); err != nil {
			if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
				return errors.Join(err, logErr)
			}
			return err
		}
		return accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, "ok")
	}

	rewritten, err := rewriteHTTPProxyRequest(req)
	if err != nil {
		if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	if err := writeAll(upstream, rewritten); err != nil {
		if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	if err := s.bridge(upstream, client, reader); err != nil {
		if logErr := accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource(logProtocol, client.RemoteAddr()), upstreamTarget, target, "ok")
}

func (s *proxyServer) proxyViaUpstreamRaw(ctx context.Context, client net.Conn, reader *bufio.Reader, initial []byte, protocol string, target string) error {
	upstream, upstreamTarget, err := s.connectUpstreamRaw(ctx)
	if err != nil {
		if logErr := accessLog(s.log, accessSource(protocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	defer closeConnWithLog(upstream, s.log, "upstream mixed "+upstreamTarget)

	if len(initial) > 0 {
		if err := writeAll(upstream, initial); err != nil {
			if logErr := accessLog(s.log, accessSource(protocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
				return errors.Join(err, logErr)
			}
			return err
		}
	}
	if err := s.bridge(upstream, client, reader); err != nil {
		if logErr := accessLog(s.log, accessSource(protocol, client.RemoteAddr()), upstreamTarget, target, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource(protocol, client.RemoteAddr()), upstreamTarget, target, "ok")
}

func shouldProbeDirectHTTPResponse(req *httpProxyRequest) bool {
	if req == nil || strings.EqualFold(req.method, "CONNECT") {
		return false
	}
	if httpRequestMayHaveBody(req) {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(req.method)) {
	case "GET", "HEAD", "OPTIONS", "TRACE":
		return true
	default:
		return false
	}
}

func httpRequestMayHaveBody(req *httpProxyRequest) bool {
	for _, line := range strings.Split(string(req.headerRaw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "content-length":
			trimmed := strings.TrimSpace(value)
			if trimmed != "" && trimmed != "0" {
				return true
			}
		case "transfer-encoding":
			trimmed := strings.TrimSpace(value)
			if trimmed != "" && !strings.EqualFold(trimmed, "identity") {
				return true
			}
		}
	}
	return false
}

func probeDirectHTTPResponse(conn net.Conn, timeout time.Duration) (*bufio.Reader, error) {
	if conn == nil {
		return nil, errors.New("direct connection is nil")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("invalid direct probe timeout: %s", timeout)
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	if _, err := reader.Peek(1); err != nil {
		clearErr := conn.SetReadDeadline(time.Time{})
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			err = fmt.Errorf("direct response probe timed out after %s: %w", timeout, err)
		}
		if clearErr != nil {
			return nil, errors.Join(err, clearErr)
		}
		return nil, err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return reader, nil
}

func (s *proxyServer) probeDirectTunnelOrFallback(ctx context.Context, client net.Conn, clientReader *bufio.Reader, direct net.Conn, cacheKey string, host string, fallback func(context.Context) (net.Conn, io.Reader, string, error)) (net.Conn, io.Reader, string, bool, error) {
	payload, ok, err := readClientTunnelProbePayload(client, clientReader, s.cfg.DirectProbeTimeout)
	if err != nil {
		closeConnWithLog(direct, s.log, "direct probe target")
		return nil, nil, "", false, err
	}
	if !ok {
		return direct, direct, "-", true, nil
	}

	probeErr := writeAll(direct, payload)
	var directReader io.Reader
	if probeErr == nil {
		directReader, probeErr = probeDirectHTTPResponse(direct, s.cfg.DirectProbeTimeout)
	}
	if probeErr == nil {
		return direct, directReader, "-", true, nil
	}

	s.direct.markUpstreamOnly(cacheKey, host)
	if closeErr := direct.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		probeErr = errors.Join(probeErr, closeErr)
	}
	upstream, upstreamReader, upstreamTarget, fallbackErr := fallback(ctx)
	if fallbackErr != nil {
		return nil, nil, upstreamTarget, false, errors.Join(probeErr, fallbackErr)
	}
	if err := writeAll(upstream, payload); err != nil {
		return nil, nil, upstreamTarget, false, closeAfterError(upstream, errors.Join(probeErr, err))
	}
	return upstream, upstreamReader, upstreamTarget, false, nil
}

func readClientTunnelProbePayload(client net.Conn, reader *bufio.Reader, timeout time.Duration) ([]byte, bool, error) {
	if client == nil {
		return nil, false, errors.New("client connection is nil")
	}
	if reader == nil {
		return nil, false, errors.New("client reader is nil")
	}
	if timeout <= 0 {
		return nil, false, fmt.Errorf("invalid direct probe timeout: %s", timeout)
	}
	if reader.Buffered() == 0 {
		if err := client.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, false, err
		}
	}
	if _, err := reader.Peek(1); err != nil {
		clearErr := client.SetReadDeadline(time.Time{})
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			if clearErr != nil {
				return nil, false, clearErr
			}
			return nil, false, nil
		}
		if clearErr != nil {
			return nil, false, errors.Join(err, clearErr)
		}
		return nil, false, err
	}
	if err := client.SetReadDeadline(time.Time{}); err != nil {
		return nil, false, err
	}
	buffered := reader.Buffered()
	if buffered <= 0 {
		return nil, false, errors.New("client probe payload is empty after peek")
	}
	peeked, err := reader.Peek(buffered)
	if err != nil {
		return nil, false, err
	}
	payload := append([]byte(nil), peeked...)
	if _, err := reader.Discard(buffered); err != nil {
		return nil, false, err
	}
	return payload, true, nil
}

func (s *proxyServer) connectHTTPConnectFallback(ctx context.Context, req *httpProxyRequest, socksReq socksRequest) (net.Conn, io.Reader, string, error) {
	if s.cfg.Mode == proxyModeLocal && s.cfg.UpstreamProtocol == upstreamProtocolMixed {
		upstream, upstreamTarget, err := s.connectUpstreamRaw(ctx)
		if err != nil {
			return nil, nil, upstreamTarget, err
		}
		if err := writeAll(upstream, req.raw); err != nil {
			return nil, nil, upstreamTarget, closeAfterError(upstream, err)
		}
		reader, err := readHTTPConnectUpstreamResponse(upstream)
		if err != nil {
			return nil, nil, upstreamTarget, closeAfterError(upstream, err)
		}
		return upstream, reader, upstreamTarget, nil
	}

	upstream, upstreamTarget, err := s.connectViaUpstreamTCP(ctx, socksReq)
	if err != nil {
		return nil, nil, upstreamTarget, err
	}
	return upstream, upstream, upstreamTarget, nil
}

func readHTTPConnectUpstreamResponse(conn net.Conn) (*bufio.Reader, error) {
	if conn == nil {
		return nil, errors.New("upstream connection is nil")
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "2") {
		return nil, fmt.Errorf("upstream CONNECT response line = %q", line)
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if header == "\r\n" || header == "\n" {
			break
		}
	}
	return reader, nil
}

func socksRequestFromHostPort(host string, port string) (socksRequest, error) {
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return socksRequest{}, err
	}
	if portNumber <= 0 || portNumber > 65535 {
		return socksRequest{}, fmt.Errorf("invalid target port %s", port)
	}
	return socksRequest{cmd: socksCmdConnect, host: host, port: uint16(portNumber)}, nil
}

func (s *proxyServer) connectViaUpstreamSocks5(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	upstream, target, err := s.connectUpstreamRaw(ctx)
	if err != nil {
		return nil, target, err
	}
	if err := s.authenticateUpstreamSOCKS5(upstream); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	if err := writeAll(upstream, buildSocks5ConnectRequest(req)); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	if err := readSocks5ConnectReply(upstream); err != nil {
		return nil, target, closeAfterError(upstream, err)
	}
	return upstream, target, nil
}

func (s *proxyServer) connectViaUpstreamTCP(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	return s.upstreamDialer().dialTCP(ctx, req)
}

func (s *proxyServer) localSOCKS5AuthRequired() bool {
	return strings.TrimSpace(s.cfg.SOCKS5Username) != "" || strings.TrimSpace(s.cfg.SOCKS5Password) != ""
}

func (s *proxyServer) upstreamSOCKS5AuthRequired() bool {
	return strings.TrimSpace(s.cfg.UpstreamSOCKS5Username) != "" || strings.TrimSpace(s.cfg.UpstreamSOCKS5Password) != ""
}

func (s *proxyServer) authenticateLocalSOCKS5(reader *bufio.Reader, client net.Conn) error {
	ok, err := readSocks5UserPassAuth(reader, s.cfg.SOCKS5Username, s.cfg.SOCKS5Password)
	if err != nil {
		if writeErr := writeAll(client, []byte{socksAuthVersion, 0x01}); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}
	if !ok {
		if err := writeAll(client, []byte{socksAuthVersion, 0x01}); err != nil {
			return err
		}
		return errors.New("invalid socks username/password")
	}
	return writeAll(client, []byte{socksAuthVersion, 0x00})
}

func (s *proxyServer) authenticateUpstreamSOCKS5(upstream net.Conn) error {
	if s.upstreamSOCKS5AuthRequired() {
		if err := writeAll(upstream, []byte{socksVersion5, 0x01, socksMethodUserPass}); err != nil {
			return err
		}
	} else if err := writeAll(upstream, []byte{socksVersion5, 0x01, socksMethodNoAuth}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(upstream, reply); err != nil {
		return err
	}
	if reply[0] != socksVersion5 {
		return fmt.Errorf("upstream socks auth reply %v", reply)
	}
	switch reply[1] {
	case socksMethodNoAuth:
		if s.upstreamSOCKS5AuthRequired() {
			return fmt.Errorf("upstream socks selected no-auth when username/password was required")
		}
		return nil
	case socksMethodUserPass:
		if !s.upstreamSOCKS5AuthRequired() {
			return fmt.Errorf("upstream socks requested username/password but no credentials are configured")
		}
		return writeSocks5UserPassAuth(upstream, s.cfg.UpstreamSOCKS5Username, s.cfg.UpstreamSOCKS5Password)
	default:
		return fmt.Errorf("upstream socks auth reply %v", reply)
	}
}

func readSocks5Greeting(reader *bufio.Reader, requireUserPass bool) (byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(reader, head); err != nil {
		return 0, err
	}
	if head[0] != socksVersion5 {
		return 0, errSocksUnsupportedVersion
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return 0, err
	}
	want := socksMethodNoAuth
	if requireUserPass {
		want = socksMethodUserPass
	}
	for _, method := range methods {
		if method == want {
			return want, nil
		}
	}
	return 0, errSocksUnsupportedMethod
}

func readSocks5UserPassAuth(reader *bufio.Reader, username string, password string) (bool, error) {
	version, err := reader.ReadByte()
	if err != nil {
		return false, err
	}
	if version != socksAuthVersion {
		return false, fmt.Errorf("unsupported socks username/password auth version %d", version)
	}
	ulen, err := reader.ReadByte()
	if err != nil {
		return false, err
	}
	user := make([]byte, int(ulen))
	if _, err := io.ReadFull(reader, user); err != nil {
		return false, err
	}
	plen, err := reader.ReadByte()
	if err != nil {
		return false, err
	}
	pass := make([]byte, int(plen))
	if _, err := io.ReadFull(reader, pass); err != nil {
		return false, err
	}
	return string(user) == username && string(pass) == password, nil
}

func writeSocks5UserPassAuth(conn io.ReadWriter, username string, password string) error {
	if len(username) > 255 {
		return fmt.Errorf("socks username is too long: %d bytes", len(username))
	}
	if len(password) > 255 {
		return fmt.Errorf("socks password is too long: %d bytes", len(password))
	}
	packet := []byte{socksAuthVersion, byte(len(username))}
	packet = append(packet, []byte(username)...)
	packet = append(packet, byte(len(password)))
	packet = append(packet, []byte(password)...)
	if err := writeAll(conn, packet); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != socksAuthVersion || reply[1] != 0x00 {
		return fmt.Errorf("upstream socks username/password auth failed with reply %v", reply)
	}
	return nil
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
	raw := []byte(firstLine)
	if !looksLikeHTTPRequestLine(firstLine) {
		return &httpProxyRequest{raw: raw}, errHTTPMalformedRequest
	}

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
		return &httpProxyRequest{raw: raw}, errHTTPMalformedRequest
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

func httpAccessProtocol(req *httpProxyRequest) string {
	if req != nil && strings.EqualFold(req.method, "CONNECT") {
		return "httpc"
	}
	return "http"
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
