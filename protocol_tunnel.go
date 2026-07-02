package tcptun

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	protocolCmdTCP = byte(0x01)
	protocolCmdUDP = byte(0x02)
	protocolCmdMux = byte(0x03)

	protocolPrivateMuxHost = "mux.tcptun.invalid"
	protocolPrivateMuxPort = uint16(1)

	trojanCmdUDP = byte(0x03)

	vlessVersion      = byte(0x00)
	vlessAtypIPv4     = byte(0x01)
	vlessAtypDomain   = byte(0x02)
	vlessAtypIPv6     = byte(0x03)
	vlessMaxAddonSize = 1024

	protocolMaxLineLength = 256
)

var (
	errProtocolUnauthorized    = errors.New("protocol tunnel unauthorized")
	errProtocolUnsupported     = errors.New("protocol tunnel command unsupported")
	errProtocolInvalidAddress  = errors.New("protocol tunnel invalid address")
	errProtocolInvalidResponse = errors.New("protocol tunnel invalid response")
)

type protocolTunnelRequest struct {
	cmd          byte
	host         string
	port         uint16
	flow         string
	vmessSession *vmessSession
}

type vlessClientConn struct {
	net.Conn
	headerRead bool
}

func newVLESSClientConn(conn net.Conn) net.Conn {
	return &vlessClientConn{Conn: conn}
}

func (c *vlessClientConn) Read(p []byte) (int, error) {
	if !c.headerRead {
		if err := readVLESSResponse(c.Conn); err != nil {
			return 0, err
		}
		c.headerRead = true
	}
	return c.Conn.Read(p)
}

func (s *proxyServer) handleProtocolTunnelConn(ctx context.Context, conn net.Conn, reader *bufio.Reader) error {
	req, err := s.readProtocolTunnelRequest(reader)
	if err != nil {
		return err
	}
	if s.isProtocolPrivateMuxRequest(req) {
		return s.handleProtocolPrivateMux(ctx, conn, reader, req)
	}
	if s.isProtocolXrayMuxRequest(req) {
		return s.handleProtocolXrayMux(ctx, conn, reader, req)
	}
	switch req.cmd {
	case protocolCmdTCP:
		return s.handleProtocolTunnelTCP(ctx, conn, reader, req)
	case protocolCmdUDP:
		return s.handleProtocolTunnelUDP(ctx, conn, reader, req)
	case protocolCmdMux:
		return s.handleProtocolTunnelMux(ctx, conn, reader, req)
	default:
		return errProtocolUnsupported
	}
}

func (s *proxyServer) isProtocolXrayMuxRequest(req protocolTunnelRequest) bool {
	if !s.cfg.TunnelMux || !isXrayCompatibleMuxProtocol(s.cfg.TunnelProtocol) {
		return false
	}
	if s.cfg.TunnelProtocol == tunnelProtocolVLESS {
		return req.cmd == protocolCmdMux
	}
	return req.cmd == protocolCmdTCP && isXrayMuxTarget(req.host, req.port)
}

func (s *proxyServer) isProtocolPrivateMuxRequest(req protocolTunnelRequest) bool {
	if !s.cfg.TunnelMux {
		return false
	}
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return req.cmd == protocolCmdMux
	case tunnelProtocolVMess, tunnelProtocolTrojan:
		return req.cmd == protocolCmdTCP && strings.EqualFold(req.host, protocolPrivateMuxHost) && req.port == protocolPrivateMuxPort
	default:
		return false
	}
}

func (s *proxyServer) readProtocolTunnelRequest(reader *bufio.Reader) (protocolTunnelRequest, error) {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return readVLESSRequest(reader, s.cfg.Token)
	case tunnelProtocolTrojan:
		return readTrojanRequest(reader, s.cfg.Token)
	case tunnelProtocolVMess:
		return readVMessRequest(reader, s.cfg.Token)
	default:
		return protocolTunnelRequest{}, fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (s *proxyServer) handleProtocolTunnelTCP(ctx context.Context, conn net.Conn, reader *bufio.Reader, req protocolTunnelRequest) error {
	if req.host == "" || req.port == 0 {
		return errProtocolInvalidAddress
	}
	clientConn := conn
	var clientReader io.Reader = reader
	if s.cfg.TunnelProtocol == tunnelProtocolVLESS {
		vision, err := s.prepareVLESSBodyConn(conn, reader, req)
		if err != nil {
			return err
		}
		if vision != nil {
			clientConn = vision
			clientReader = vision
		}
	}
	if s.cfg.TunnelProtocol == tunnelProtocolVMess {
		if req.vmessSession == nil {
			return errProtocolInvalidResponse
		}
		vmessConn, err := newVMessResponseConn(conn, *req.vmessSession)
		if err != nil {
			return err
		}
		clientConn = vmessConn
		clientReader = newVMessRequestReader(reader, *req.vmessSession)
	}
	logTarget := accessTarget(req.host, strconv.Itoa(int(req.port)))
	target, err := s.publicTCPTarget(ctx, req.host, req.port)
	if err != nil {
		return err
	}
	outbound, err := s.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer closeConnWithLog(outbound, s.log, "protocol tunnel tcp target "+target)
	if err := tuneTCP(outbound, s.cfg.HeartbeatInterval); err != nil {
		return err
	}
	if s.cfg.TunnelProtocol == tunnelProtocolVMess {
		if req.vmessSession == nil {
			return errProtocolInvalidResponse
		}
		if err := writeVMessResponseHeader(conn, *req.vmessSession); err != nil {
			return err
		}
	} else if err := s.writeProtocolTunnelResponse(conn); err != nil {
		return err
	}
	if err := s.bridge(outbound, clientConn, clientReader); err != nil {
		if logErr := accessLog(s.log, accessSource(s.cfg.TunnelProtocol, conn.RemoteAddr()), "-", logTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource(s.cfg.TunnelProtocol, conn.RemoteAddr()), "-", logTarget, "ok")
}

func (s *proxyServer) prepareVLESSBodyConn(conn net.Conn, reader *bufio.Reader, req protocolTunnelRequest) (net.Conn, error) {
	if err := s.validateVLESSFlow(req); err != nil {
		return nil, err
	}
	if req.flow == "" {
		return nil, nil
	}
	if !isVisionFlow(req.flow) {
		return nil, fmt.Errorf("unsupported vless flow %q", req.flow)
	}
	userID, err := parseUUIDToken(s.cfg.Token)
	if err != nil {
		return nil, err
	}
	return newVisionConnWithReader(conn, userID, nil, reader), nil
}

func (s *proxyServer) validateVLESSFlow(req protocolTunnelRequest) error {
	configuredFlow := strings.TrimSpace(s.cfg.TunnelFlow)
	if req.flow != configuredFlow {
		return fmt.Errorf("vless flow mismatch: got %q, configured %q", req.flow, configuredFlow)
	}
	if req.flow != "" && !isVisionFlow(req.flow) {
		return fmt.Errorf("unsupported vless flow %q", req.flow)
	}
	return nil
}

func (s *proxyServer) writeProtocolTunnelResponse(w io.Writer) error {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return writeVLESSResponse(w)
	case tunnelProtocolVMess:
		return errProtocolInvalidResponse
	case tunnelProtocolTrojan:
		return nil
	default:
		return fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (s *proxyServer) connectViaProtocolTunnelTCP(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	target := s.cfg.ServerAddr
	if s.cfg.TunnelMux {
		if s.canUseXrayMuxClient() {
			conn, err := s.openXrayMuxTCPStream(ctx, req)
			if err == nil {
				return conn, target, nil
			}
			s.resetCurrentXrayMuxSession()
			if s.cfg.Verbose {
				if logErr := logf(s.log, "open xray mux tcp stream failed: %v; falling back to private mux or single protocol connection\n", err); logErr != nil {
					return nil, target, errors.Join(err, logErr)
				}
			}
		}
		conn, err := s.openTunnelMuxStream(ctx)
		if err != nil {
			if s.cfg.Verbose {
				if logErr := logf(s.log, "open protocol mux tcp stream failed: %v; falling back to single protocol connection\n", err); logErr != nil {
					return nil, target, errors.Join(err, logErr)
				}
			}
		} else {
			if err := s.writeMuxStreamTCPRequest(conn, req); err != nil {
				if shouldFallbackProtocolMux(err) {
					s.resetCurrentTunnelMuxSession()
				} else {
					return nil, target, err
				}
			} else {
				return conn, target, nil
			}
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errMuxClosed) && !isExpectedNetworkClose(err) {
				return nil, target, err
			}
		}
	}
	conn, err := s.dialTunnelTransport(ctx)
	if err != nil {
		return nil, target, err
	}
	if err := tuneTCP(conn, s.cfg.HeartbeatInterval); err != nil {
		return nil, target, closeAfterError(conn, err)
	}
	var vmessSession *vmessSession
	if s.cfg.TunnelProtocol == tunnelProtocolVMess {
		session, err := writeVMessTCPRequest(conn, s.cfg.Token, req.host, req.port)
		if err != nil {
			return nil, target, closeAfterError(conn, err)
		}
		vmessSession = &session
	} else if err := s.writeProtocolTunnelRequest(conn, req); err != nil {
		return nil, target, closeAfterError(conn, err)
	}
	if s.cfg.TunnelProtocol == tunnelProtocolVLESS && isVisionFlow(s.cfg.TunnelFlow) {
		userID, err := parseUUIDToken(s.cfg.Token)
		if err != nil {
			return nil, target, closeAfterError(conn, err)
		}
		vision := newVisionConn(conn, userID, readVLESSResponse)
		if err := vision.WriteInitialPadding(); err != nil {
			return nil, target, closeAfterError(vision, err)
		}
		return vision, target, nil
	}
	if vmessSession != nil {
		return newVMessClientConn(conn, *vmessSession), target, nil
	}
	if s.cfg.TunnelProtocol == tunnelProtocolVLESS {
		return newVLESSClientConn(conn), target, nil
	}
	if err := s.readProtocolTunnelResponse(conn); err != nil {
		return nil, target, closeAfterError(conn, err)
	}
	return conn, target, nil
}

func (s *proxyServer) writeMuxStreamTCPRequest(conn net.Conn, req socksRequest) error {
	if err := writeTunnelRequest(conn, tunnelRequest{
		cmd:   tunnelCmdTCPConnect,
		token: s.cfg.Token,
		host:  req.host,
		port:  req.port,
	}); err != nil {
		return closeAfterError(conn, err)
	}
	if err := readTunnelResponse(conn); err != nil {
		return closeAfterError(conn, err)
	}
	return nil
}

func shouldFallbackProtocolMux(err error) bool {
	return errors.Is(err, errTunnelBadMagic) ||
		errors.Is(err, errTunnelBadVersion) ||
		errors.Is(err, errMuxClosed) ||
		isExpectedNetworkClose(err)
}

func (s *proxyServer) openProtocolMuxConn(conn net.Conn, reader *bufio.Reader) (net.Conn, io.Reader, error) {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		if err := writeVLESSMuxRequest(conn, s.cfg.Token, s.cfg.TunnelFlow); err != nil {
			return nil, nil, err
		}
		if isVisionFlow(s.cfg.TunnelFlow) {
			userID, err := parseUUIDToken(s.cfg.Token)
			if err != nil {
				return nil, nil, err
			}
			vision := newVisionConn(conn, userID, readVLESSResponse)
			if err := vision.WriteInitialPadding(); err != nil {
				return nil, nil, err
			}
			return vision, vision, nil
		}
		if err := s.readProtocolMuxVLESSResponse(conn, reader); err != nil {
			return nil, nil, err
		}
		return conn, reader, nil
	case tunnelProtocolVMess:
		session, err := writeVMessTCPRequest(conn, s.cfg.Token, protocolPrivateMuxHost, protocolPrivateMuxPort)
		if err != nil {
			return nil, nil, err
		}
		vmessConn := newVMessClientConn(conn, session)
		return vmessConn, vmessConn, nil
	case tunnelProtocolTrojan:
		if err := writeTrojanRequest(conn, s.cfg.Token, protocolCmdTCP, protocolPrivateMuxHost, protocolPrivateMuxPort); err != nil {
			return nil, nil, err
		}
		return conn, reader, nil
	default:
		return nil, nil, fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (s *proxyServer) readProtocolMuxVLESSResponse(conn net.Conn, reader io.Reader) error {
	timeout := s.cfg.DialTimeout
	if timeout <= 0 {
		timeout = defaultConfig().DialTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	err := readVLESSResponse(reader)
	clearErr := conn.SetReadDeadline(time.Time{})
	if err != nil {
		return errors.Join(err, clearErr)
	}
	return clearErr
}

func (s *proxyServer) writeProtocolTunnelRequest(w io.Writer, req socksRequest) error {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return writeVLESSRequest(w, s.cfg.Token, s.cfg.TunnelFlow, protocolCmdTCP, req.host, req.port)
	case tunnelProtocolTrojan:
		return writeTrojanRequest(w, s.cfg.Token, protocolCmdTCP, req.host, req.port)
	case tunnelProtocolVMess:
		_, err := writeVMessTCPRequest(w, s.cfg.Token, req.host, req.port)
		return err
	default:
		return fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (s *proxyServer) readProtocolTunnelResponse(reader io.Reader) error {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return readVLESSResponse(reader)
	case tunnelProtocolVMess:
		return errProtocolInvalidResponse
	case tunnelProtocolTrojan:
		return nil
	default:
		return fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func writeVLESSRequest(w io.Writer, token string, flow string, cmd byte, host string, port uint16) error {
	userID, err := parseUUIDToken(token)
	if err != nil {
		return err
	}
	addons, err := vlessHeaderAddons(flow)
	if err != nil {
		return err
	}
	header := make([]byte, 0, 64+len(host))
	header = append(header, vlessVersion)
	header = append(header, userID[:]...)
	header = append(header, byte(len(addons)))
	header = append(header, addons...)
	header = append(header, cmd)
	header = appendUint16(header, port)
	var appendErr error
	header, appendErr = appendVLESSAddress(header, host)
	if appendErr != nil {
		return appendErr
	}
	return writeAll(w, header)
}

func writeVLESSTCPRequest(w io.Writer, token string, flow string, host string, port uint16) error {
	return writeVLESSRequest(w, token, flow, protocolCmdTCP, host, port)
}

func writeVLESSMuxRequest(w io.Writer, token string, flow string) error {
	userID, err := parseUUIDToken(token)
	if err != nil {
		return err
	}
	addons, err := vlessHeaderAddons(flow)
	if err != nil {
		return err
	}
	header := make([]byte, 0, 19+len(addons))
	header = append(header, vlessVersion)
	header = append(header, userID[:]...)
	header = append(header, byte(len(addons)))
	header = append(header, addons...)
	header = append(header, protocolCmdMux)
	return writeAll(w, header)
}

func vlessHeaderAddons(flow string) ([]byte, error) {
	trimmed := strings.TrimSpace(flow)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 252 {
		return nil, errTunnelInvalidLength
	}
	addons := make([]byte, 0, len(trimmed)+2)
	addons = append(addons, 0x0a)
	addons = appendVarint(addons, uint64(len(trimmed)))
	addons = append(addons, trimmed...)
	if len(addons) > 255 {
		return nil, errTunnelInvalidLength
	}
	return addons, nil
}

func isVisionFlow(flow string) bool {
	return strings.TrimSpace(flow) == "xtls-rprx-vision"
}

func appendVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func readVLESSRequest(reader io.Reader, expectedToken string) (protocolTunnelRequest, error) {
	header := make([]byte, 18)
	if _, err := io.ReadFull(reader, header); err != nil {
		return protocolTunnelRequest{}, err
	}
	if header[0] != vlessVersion {
		return protocolTunnelRequest{}, errTunnelBadVersion
	}
	userID := [16]byte{}
	copy(userID[:], header[1:17])
	if err := verifyUUIDToken(expectedToken, userID); err != nil {
		return protocolTunnelRequest{}, err
	}
	addonLen := int(header[17])
	if addonLen > vlessMaxAddonSize {
		return protocolTunnelRequest{}, errTunnelInvalidLength
	}
	var flow string
	if addonLen > 0 {
		addons := make([]byte, addonLen)
		if _, err := io.ReadFull(reader, addons); err != nil {
			return protocolTunnelRequest{}, err
		}
		var flowErr error
		flow, flowErr = vlessAddonsFlow(addons)
		if flowErr != nil {
			return protocolTunnelRequest{}, flowErr
		}
	}
	cmdBuf := make([]byte, 1)
	if _, err := io.ReadFull(reader, cmdBuf); err != nil {
		return protocolTunnelRequest{}, err
	}
	cmd := cmdBuf[0]
	switch cmd {
	case protocolCmdTCP, protocolCmdUDP:
	case protocolCmdMux:
		return protocolTunnelRequest{
			cmd:  cmd,
			host: xrayMuxCoolHost,
			port: xrayMuxCoolPort,
			flow: flow,
		}, nil
	default:
		return protocolTunnelRequest{}, errProtocolUnsupported
	}
	tail := make([]byte, 3)
	if _, err := io.ReadFull(reader, tail); err != nil {
		return protocolTunnelRequest{}, err
	}
	host, err := readVLESSAddress(reader, tail[2])
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	return protocolTunnelRequest{
		cmd:  cmd,
		host: host,
		port: binary.BigEndian.Uint16(tail[0:2]),
		flow: flow,
	}, nil
}

func readVLESSTCPRequest(reader io.Reader, expectedToken string) (protocolTunnelRequest, error) {
	req, err := readVLESSRequest(reader, expectedToken)
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	if req.cmd != protocolCmdTCP {
		return protocolTunnelRequest{}, errProtocolUnsupported
	}
	return req, nil
}

func vlessAddonsFlow(addons []byte) (string, error) {
	for len(addons) > 0 {
		key, n, err := consumeVarint(addons)
		if err != nil {
			return "", err
		}
		addons = addons[n:]
		fieldNumber := key >> 3
		wireType := key & 0x07
		switch wireType {
		case 0:
			_, consumed, err := consumeVarint(addons)
			if err != nil {
				return "", err
			}
			addons = addons[consumed:]
		case 2:
			length, consumed, err := consumeVarint(addons)
			if err != nil {
				return "", err
			}
			addons = addons[consumed:]
			if length > uint64(len(addons)) {
				return "", errTunnelInvalidLength
			}
			value := addons[:int(length)]
			addons = addons[int(length):]
			if fieldNumber == 1 {
				return string(value), nil
			}
		default:
			return "", fmt.Errorf("unsupported vless addon wire type %d", wireType)
		}
	}
	return "", nil
}

func consumeVarint(src []byte) (uint64, int, error) {
	var value uint64
	for i, b := range src {
		if i == 10 {
			return 0, 0, errTunnelInvalidLength
		}
		value |= uint64(b&0x7f) << (uint(i) * 7)
		if b < 0x80 {
			return value, i + 1, nil
		}
	}
	return 0, 0, io.ErrUnexpectedEOF
}

func writeVLESSResponse(w io.Writer) error {
	return writeAll(w, []byte{vlessVersion, 0x00})
}

func readVLESSResponse(reader io.Reader) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	addonLen := int(header[1])
	if addonLen > vlessMaxAddonSize {
		return errTunnelInvalidLength
	}
	if addonLen == 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, reader, int64(addonLen)); err != nil {
		return err
	}
	return nil
}

func appendVLESSAddress(dst []byte, host string) ([]byte, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, errProtocolInvalidAddress
	}
	if addr, err := netip.ParseAddr(trimHostBrackets(trimmed)); err == nil {
		if addr.Is4() {
			ip4 := addr.As4()
			dst = append(dst, vlessAtypIPv4)
			return append(dst, ip4[:]...), nil
		}
		ip16 := addr.As16()
		dst = append(dst, vlessAtypIPv6)
		return append(dst, ip16[:]...), nil
	}
	if len(trimmed) > tunnelMaxHostLength {
		return nil, errTunnelInvalidLength
	}
	dst = append(dst, vlessAtypDomain, byte(len(trimmed)))
	return append(dst, trimmed...), nil
}

func readVLESSAddress(reader io.Reader, atyp byte) (string, error) {
	switch atyp {
	case vlessAtypIPv4:
		buf := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case vlessAtypIPv6:
		buf := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case vlessAtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(reader, lenBuf); err != nil {
			return "", err
		}
		return readStringN(reader, int(lenBuf[0]))
	default:
		return "", errProtocolInvalidAddress
	}
}

func writeTrojanRequest(w io.Writer, password string, cmd byte, host string, port uint16) error {
	header := make([]byte, 0, 96+len(host))
	header = append(header, []byte(trojanPasswordHash(password))...)
	header = append(header, '\r', '\n', cmd)
	var appendErr error
	header, appendErr = appendSocksAddress(header, host, port)
	if appendErr != nil {
		return appendErr
	}
	header = append(header, '\r', '\n')
	return writeAll(w, header)
}

func readTrojanRequest(reader *bufio.Reader, expectedPassword string) (protocolTunnelRequest, error) {
	line, err := readLineLimited(reader, protocolMaxLineLength)
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return protocolTunnelRequest{}, errProtocolUnauthorized
	}
	hash := strings.TrimSuffix(line, "\r\n")
	if expectedPassword != "" && hash != trojanPasswordHash(expectedPassword) {
		return protocolTunnelRequest{}, errProtocolUnauthorized
	}
	cmd, err := reader.ReadByte()
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	if cmd != protocolCmdTCP && cmd != trojanCmdUDP {
		return protocolTunnelRequest{}, errProtocolUnsupported
	}
	host, port, err := readSocksAddress(reader)
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(reader, crlf); err != nil {
		return protocolTunnelRequest{}, err
	}
	if crlf[0] != '\r' || crlf[1] != '\n' {
		return protocolTunnelRequest{}, errProtocolUnauthorized
	}
	if cmd == trojanCmdUDP {
		cmd = protocolCmdUDP
	}
	return protocolTunnelRequest{cmd: cmd, host: host, port: port}, nil
}

func trojanPasswordHash(password string) string {
	sum := sha256.Sum224([]byte(password))
	return hex.EncodeToString(sum[:])
}

func appendSocksAddress(dst []byte, host string, port uint16) ([]byte, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, errProtocolInvalidAddress
	}
	if addr, err := netip.ParseAddr(trimHostBrackets(trimmed)); err == nil {
		if addr.Is4() {
			ip4 := addr.As4()
			dst = append(dst, socksAtypIPv4)
			dst = append(dst, ip4[:]...)
			return appendUint16(dst, port), nil
		}
		ip16 := addr.As16()
		dst = append(dst, socksAtypIPv6)
		dst = append(dst, ip16[:]...)
		return appendUint16(dst, port), nil
	}
	if len(trimmed) > tunnelMaxHostLength {
		return nil, errTunnelInvalidLength
	}
	dst = append(dst, socksAtypDomain, byte(len(trimmed)))
	dst = append(dst, trimmed...)
	return appendUint16(dst, port), nil
}

func readSocksAddress(reader io.Reader) (string, uint16, error) {
	atypBuf := make([]byte, 1)
	if _, err := io.ReadFull(reader, atypBuf); err != nil {
		return "", 0, err
	}
	var host string
	switch atypBuf[0] {
	case socksAtypIPv4:
		buf := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", 0, err
		}
		host = net.IP(buf).String()
	case socksAtypIPv6:
		buf := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", 0, err
		}
		host = net.IP(buf).String()
	case socksAtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(reader, lenBuf); err != nil {
			return "", 0, err
		}
		var err error
		host, err = readStringN(reader, int(lenBuf[0]))
		if err != nil {
			return "", 0, err
		}
	default:
		return "", 0, errProtocolInvalidAddress
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBuf); err != nil {
		return "", 0, err
	}
	return host, binary.BigEndian.Uint16(portBuf), nil
}

func parseUUIDToken(token string) ([16]byte, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(token)), "-", "")
	if len(normalized) != 32 {
		return [16]byte{}, errors.New("tunnel protocol token must be a UUID")
	}
	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return [16]byte{}, err
	}
	if len(decoded) != 16 {
		return [16]byte{}, errors.New("decoded UUID has invalid length")
	}
	userID := [16]byte{}
	copy(userID[:], decoded)
	return userID, nil
}

func verifyUUIDToken(expectedToken string, actual [16]byte) error {
	if strings.TrimSpace(expectedToken) == "" {
		return nil
	}
	expected, err := parseUUIDToken(expectedToken)
	if err != nil {
		return err
	}
	if expected != actual {
		return errProtocolUnauthorized
	}
	return nil
}

func appendUint16(dst []byte, value uint16) []byte {
	return append(dst, byte(value>>8), byte(value))
}

func readLineLimited(reader *bufio.Reader, max int) (string, error) {
	var builder strings.Builder
	for {
		part, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if builder.Len()+len(part) > max {
			return "", errTunnelInvalidLength
		}
		if _, err := builder.WriteString(part); err != nil {
			return "", err
		}
		if strings.HasSuffix(part, "\n") {
			return builder.String(), nil
		}
	}
}
