package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	tunnelMagic          = "PSK1"
	tunnelVersion        = byte(0x01)
	tunnelCmdTCPConnect  = byte(0x01)
	tunnelCmdUDPRelay    = byte(0x02)
	tunnelCmdMux         = byte(0x03)
	tunnelStatusOK       = byte(0x00)
	tunnelStatusError    = byte(0x01)
	tunnelMaxTokenLength = 4096
	tunnelMaxHostLength  = 255
	tunnelMaxErrorLength = 4096
	tunnelMaxUDPPayload  = 64 * 1024
)

var (
	errTunnelBadMagic      = errors.New("invalid tunnel magic")
	errTunnelBadVersion    = errors.New("invalid tunnel version")
	errTunnelUnauthorized  = errors.New("tunnel unauthorized")
	errTunnelUnsupported   = errors.New("unsupported tunnel command")
	errTunnelInvalidLength = errors.New("invalid tunnel length")
)

type tunnelRequest struct {
	cmd   byte
	token string
	host  string
	port  uint16
}

type tunnelUDPFrame struct {
	host    string
	port    uint16
	payload []byte
}

type customUDPUpstream struct {
	tcp     net.Conn
	reader  *bufio.Reader
	label   string
	writeMu sync.Mutex
}

func runTunnelServer(ctx context.Context, cfg config, log io.Writer) error {
	switch cfg.TunnelTransport {
	case tunnelTransportRaw:
		return runRawTunnelServer(ctx, cfg, log)
	case tunnelTransportWS, tunnelTransportH2, tunnelTransportH3:
		return runHTTPTunnelServer(ctx, cfg, log)
	default:
		return fmt.Errorf("unsupported tunnel transport %q", cfg.TunnelTransport)
	}
}

func runRawTunnelServer(ctx context.Context, cfg config, log io.Writer) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(log, "close tunnel listener: %v\n", err); logErr != nil {
				return
			}
		}
	}()

	server := &proxyServer{
		cfg: cfg,
		dialer: net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		},
		log: log,
	}
	server.bufferPool.New = func() any {
		buf := make([]byte, cfg.BufferSize)
		return &buf
	}

	if err := logf(log, "tunnel server listening on %s via %s\n", listener.Addr(), cfg.TunnelTransport); err != nil {
		return err
	}

	closeErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr <- err
			return
		}
		closeErr <- errListenerClosedByContext
	}()

	var tempDelay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				select {
				case closeErrValue := <-closeErr:
					if closeErrValue != nil && !errors.Is(closeErrValue, errListenerClosedByContext) {
						return closeErrValue
					}
				default:
				}
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := time.Second; tempDelay > max {
					tempDelay = max
				}
				time.Sleep(tempDelay)
				continue
			}
			return err
		}
		tempDelay = 0
		go server.handleTunnelConn(ctx, conn)
	}
}

func (s *proxyServer) handleTunnelConn(ctx context.Context, conn net.Conn) {
	if err := s.handleTunnelConnError(ctx, conn); err != nil && s.cfg.Verbose {
		if logErr := logf(s.log, "tunnel connection error for %s: %v\n", conn.RemoteAddr(), err); logErr != nil {
			return
		}
	}
}

func (s *proxyServer) handleTunnelConnError(ctx context.Context, conn net.Conn) error {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(s.log, "close tunnel client %s: %v\n", conn.RemoteAddr(), err); logErr != nil {
				return
			}
		}
	}()
	if err := tuneTCP(conn); err != nil {
		return fmt.Errorf("tune tunnel client tcp: %w", err)
	}

	reader := bufio.NewReader(conn)
	req, err := readTunnelRequest(reader)
	if err != nil {
		return err
	}
	if !tokenMatches(s.cfg.Token, req.token) {
		if writeErr := writeTunnelResponse(conn, tunnelStatusError, errTunnelUnauthorized.Error()); writeErr != nil {
			return errors.Join(errTunnelUnauthorized, writeErr)
		}
		return errTunnelUnauthorized
	}

	switch req.cmd {
	case tunnelCmdTCPConnect:
		return s.handleTunnelTCP(ctx, conn, reader, req)
	case tunnelCmdUDPRelay:
		return s.handleTunnelUDP(ctx, conn, reader)
	case tunnelCmdMux:
		return s.handleTunnelMux(ctx, conn, reader)
	default:
		if writeErr := writeTunnelResponse(conn, tunnelStatusError, errTunnelUnsupported.Error()); writeErr != nil {
			return errors.Join(errTunnelUnsupported, writeErr)
		}
		return errTunnelUnsupported
	}
}

func (s *proxyServer) handleTunnelMux(ctx context.Context, conn net.Conn, reader *bufio.Reader) error {
	if err := writeTunnelResponse(conn, tunnelStatusOK, ""); err != nil {
		return err
	}
	session := newMuxSession(conn, reader, false)
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		if err := session.Close(); err != nil && !errors.Is(err, errMuxClosed) && !isExpectedNetworkClose(err) {
			if logErr := logf(s.log, "close mux session %s: %v\n", conn.RemoteAddr(), err); logErr != nil {
				return
			}
		}
	}()
	for {
		stream, err := session.accept(ctx)
		if err != nil {
			if errors.Is(err, errMuxClosed) || errors.Is(err, io.EOF) || ctx.Err() != nil || isExpectedNetworkClose(err) {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleTunnelMuxStream(ctx, stream)
		}()
	}
}

func (s *proxyServer) handleTunnelMuxStream(ctx context.Context, stream net.Conn) {
	if err := s.handleTunnelMuxStreamError(ctx, stream); err != nil && s.cfg.Verbose {
		if logErr := logf(s.log, "mux stream error for %s: %v\n", stream.RemoteAddr(), err); logErr != nil {
			return
		}
	}
}

func (s *proxyServer) handleTunnelMuxStreamError(ctx context.Context, stream net.Conn) error {
	defer func() {
		if err := stream.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errMuxClosed) {
			if logErr := logf(s.log, "close mux stream %s: %v\n", stream.RemoteAddr(), err); logErr != nil {
				return
			}
		}
	}()

	reader := bufio.NewReader(stream)
	req, err := readTunnelRequest(reader)
	if err != nil {
		return err
	}
	if !tokenMatches(s.cfg.Token, req.token) {
		if writeErr := writeTunnelResponse(stream, tunnelStatusError, errTunnelUnauthorized.Error()); writeErr != nil {
			return errors.Join(errTunnelUnauthorized, writeErr)
		}
		return errTunnelUnauthorized
	}
	switch req.cmd {
	case tunnelCmdTCPConnect:
		return s.handleTunnelTCP(ctx, stream, reader, req)
	case tunnelCmdUDPRelay:
		return s.handleTunnelUDP(ctx, stream, reader)
	default:
		if writeErr := writeTunnelResponse(stream, tunnelStatusError, errTunnelUnsupported.Error()); writeErr != nil {
			return errors.Join(errTunnelUnsupported, writeErr)
		}
		return errTunnelUnsupported
	}
}

func (s *proxyServer) handleTunnelTCP(ctx context.Context, conn net.Conn, reader *bufio.Reader, req tunnelRequest) error {
	if req.host == "" || req.port == 0 {
		err := errors.New("invalid tunnel tcp target")
		if writeErr := writeTunnelResponse(conn, tunnelStatusError, err.Error()); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return nil
	}
	target := net.JoinHostPort(req.host, strconv.Itoa(int(req.port)))
	logTarget := accessTarget(req.host, strconv.Itoa(int(req.port)))
	outbound, err := s.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		if writeErr := writeTunnelResponse(conn, tunnelStatusError, err.Error()); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return nil
	}
	defer closeConnWithLog(outbound, s.log, "tunnel tcp target "+target)
	if err := tuneTCP(outbound); err != nil {
		if writeErr := writeTunnelResponse(conn, tunnelStatusError, err.Error()); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}
	if err := writeTunnelResponse(conn, tunnelStatusOK, ""); err != nil {
		return err
	}
	if err := s.bridge(outbound, conn, reader); err != nil {
		if logErr := accessLog(s.log, accessSource("tunnel", conn.RemoteAddr()), "-", logTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource("tunnel", conn.RemoteAddr()), "-", logTarget, "ok")
}

func (s *proxyServer) handleTunnelUDP(ctx context.Context, conn net.Conn, reader *bufio.Reader) error {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		if writeErr := writeTunnelResponse(conn, tunnelStatusError, err.Error()); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return nil
	}
	defer closeUDPWithLog(udpConn, s.log, "tunnel udp target")
	if err := writeTunnelResponse(conn, tunnelStatusOK, ""); err != nil {
		return err
	}

	done := make(chan error, 2)
	var writeMu sync.Mutex
	go s.tunnelUDPClientToRemote(ctx, reader, udpConn, done)
	go s.tunnelUDPRemoteToClient(ctx, conn, udpConn, &writeMu, done)

	if err := <-done; err != nil && !isExpectedNetworkClose(err) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (s *proxyServer) tunnelUDPClientToRemote(ctx context.Context, reader *bufio.Reader, udpConn *net.UDPConn, done chan<- error) {
	for {
		frame, err := readTunnelUDPFrame(reader)
		if err != nil {
			done <- err
			return
		}
		targetText := net.JoinHostPort(frame.host, strconv.Itoa(int(frame.port)))
		target, err := net.ResolveUDPAddr("udp", targetText)
		if err != nil {
			done <- err
			return
		}
		if _, err := udpConn.WriteToUDP(frame.payload, target); err != nil {
			done <- err
			return
		}
		if s.cfg.Verbose {
			if err := logf(s.log, "tunnel udp %s\n", targetText); err != nil {
				done <- err
				return
			}
		}
		if ctx.Err() != nil {
			done <- ctx.Err()
			return
		}
	}
}

func (s *proxyServer) tunnelUDPRemoteToClient(ctx context.Context, conn net.Conn, udpConn *net.UDPConn, writeMu *sync.Mutex, done chan<- error) {
	buf := make([]byte, udpBufferSize)
	for {
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			done <- err
			return
		}
		writeMu.Lock()
		err = writeTunnelUDPFrame(conn, tunnelUDPFrame{
			host:    addr.IP.String(),
			port:    uint16(addr.Port),
			payload: buf[:n],
		})
		writeMu.Unlock()
		if err != nil {
			done <- err
			return
		}
		if ctx.Err() != nil {
			done <- ctx.Err()
			return
		}
	}
}

func (s *proxyServer) connectViaTunnelTCP(ctx context.Context, req socksRequest) (net.Conn, string, error) {
	target := s.cfg.ServerAddr
	conn, err := s.openTunnelConn(ctx)
	if err != nil {
		return nil, target, err
	}
	if err := tuneTCP(conn); err != nil {
		return nil, target, closeAfterError(conn, err)
	}
	if err := writeTunnelRequest(conn, tunnelRequest{
		cmd:   tunnelCmdTCPConnect,
		token: s.cfg.Token,
		host:  req.host,
		port:  req.port,
	}); err != nil {
		return nil, target, closeAfterError(conn, err)
	}
	if err := readTunnelResponse(conn); err != nil {
		return nil, target, closeAfterError(conn, err)
	}
	return conn, target, nil
}

func (s *proxyServer) connectViaTunnelUDP(ctx context.Context) (*customUDPUpstream, error) {
	target := s.cfg.ServerAddr
	conn, err := s.openTunnelConn(ctx)
	if err != nil {
		return nil, err
	}
	if err := tuneTCP(conn); err != nil {
		return nil, closeAfterError(conn, err)
	}
	reader := bufio.NewReader(conn)
	if err := writeTunnelRequest(conn, tunnelRequest{
		cmd:   tunnelCmdUDPRelay,
		token: s.cfg.Token,
	}); err != nil {
		return nil, closeAfterError(conn, err)
	}
	if err := readTunnelResponse(reader); err != nil {
		return nil, closeAfterError(conn, err)
	}
	return &customUDPUpstream{tcp: conn, reader: reader, label: target}, nil
}

func (s *proxyServer) openTunnelConn(ctx context.Context) (net.Conn, error) {
	if !s.cfg.TunnelMux {
		return s.dialTunnelTransport(ctx)
	}
	conn, err := s.openTunnelMuxStream(ctx)
	if err == nil {
		return conn, nil
	}
	if s.cfg.Verbose {
		if logErr := logf(s.log, "open mux stream failed: %v; falling back to single tunnel connection\n", err); logErr != nil {
			return nil, errors.Join(err, logErr)
		}
	}
	return s.dialTunnelTransport(ctx)
}

type tunnelMuxClient struct {
	mu      sync.Mutex
	session *muxSession
}

func (s *proxyServer) openTunnelMuxStream(ctx context.Context) (net.Conn, error) {
	session, err := s.tunnelMuxSession(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := session.openStream(ctx)
	if err == nil {
		return stream, nil
	}
	s.resetTunnelMuxSession(session)
	session, err = s.tunnelMuxSession(ctx)
	if err != nil {
		return nil, err
	}
	return session.openStream(ctx)
}

func (s *proxyServer) tunnelMuxSession(ctx context.Context) (*muxSession, error) {
	s.mux.mu.Lock()
	defer s.mux.mu.Unlock()
	if s.mux.session != nil {
		select {
		case <-s.mux.session.done:
			s.mux.session = nil
		default:
			return s.mux.session, nil
		}
	}

	conn, err := s.dialTunnelTransport(ctx)
	if err != nil {
		return nil, err
	}
	if err := tuneTCP(conn); err != nil {
		return nil, closeAfterError(conn, err)
	}
	reader := bufio.NewReader(conn)
	if err := writeTunnelRequest(conn, tunnelRequest{
		cmd:   tunnelCmdMux,
		token: s.cfg.Token,
	}); err != nil {
		return nil, closeAfterError(conn, err)
	}
	if err := readTunnelResponse(reader); err != nil {
		return nil, closeAfterError(conn, err)
	}
	s.mux.session = newMuxSession(conn, reader, true)
	return s.mux.session, nil
}

func (s *proxyServer) resetTunnelMuxSession(session *muxSession) {
	s.mux.mu.Lock()
	defer s.mux.mu.Unlock()
	if s.mux.session == session {
		if err := session.Close(); err != nil && !errors.Is(err, errMuxClosed) && !isExpectedNetworkClose(err) {
			if logErr := logf(s.log, "close failed mux session: %v\n", err); logErr != nil {
				return
			}
		}
		s.mux.session = nil
	}
}

func writeTunnelRequest(w io.Writer, req tunnelRequest) error {
	if len(req.token) > tunnelMaxTokenLength || len(req.host) > tunnelMaxHostLength {
		return errTunnelInvalidLength
	}
	header := make([]byte, 12)
	copy(header[0:4], []byte(tunnelMagic))
	header[4] = tunnelVersion
	header[5] = req.cmd
	binary.BigEndian.PutUint16(header[6:8], uint16(len(req.token)))
	binary.BigEndian.PutUint16(header[8:10], uint16(len(req.host)))
	binary.BigEndian.PutUint16(header[10:12], req.port)
	if err := writeAll(w, header); err != nil {
		return err
	}
	if req.token != "" {
		if err := writeAll(w, []byte(req.token)); err != nil {
			return err
		}
	}
	if req.host != "" {
		if err := writeAll(w, []byte(req.host)); err != nil {
			return err
		}
	}
	return nil
}

func readTunnelRequest(reader io.Reader) (tunnelRequest, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(reader, header); err != nil {
		return tunnelRequest{}, err
	}
	if string(header[0:4]) != tunnelMagic {
		return tunnelRequest{}, errTunnelBadMagic
	}
	if header[4] != tunnelVersion {
		return tunnelRequest{}, errTunnelBadVersion
	}
	tokenLen := int(binary.BigEndian.Uint16(header[6:8]))
	hostLen := int(binary.BigEndian.Uint16(header[8:10]))
	if tokenLen > tunnelMaxTokenLength || hostLen > tunnelMaxHostLength {
		return tunnelRequest{}, errTunnelInvalidLength
	}
	token, err := readStringN(reader, tokenLen)
	if err != nil {
		return tunnelRequest{}, err
	}
	host, err := readStringN(reader, hostLen)
	if err != nil {
		return tunnelRequest{}, err
	}
	return tunnelRequest{
		cmd:   header[5],
		token: token,
		host:  host,
		port:  binary.BigEndian.Uint16(header[10:12]),
	}, nil
}

func writeTunnelResponse(w io.Writer, status byte, message string) error {
	if len(message) > tunnelMaxErrorLength {
		message = message[:tunnelMaxErrorLength]
	}
	header := make([]byte, 8)
	copy(header[0:4], []byte(tunnelMagic))
	header[4] = tunnelVersion
	header[5] = status
	binary.BigEndian.PutUint16(header[6:8], uint16(len(message)))
	if err := writeAll(w, header); err != nil {
		return err
	}
	if message != "" {
		return writeAll(w, []byte(message))
	}
	return nil
}

func readTunnelResponse(reader io.Reader) error {
	header := make([]byte, 8)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if string(header[0:4]) != tunnelMagic {
		return errTunnelBadMagic
	}
	if header[4] != tunnelVersion {
		return errTunnelBadVersion
	}
	messageLen := int(binary.BigEndian.Uint16(header[6:8]))
	if messageLen > tunnelMaxErrorLength {
		return errTunnelInvalidLength
	}
	message, err := readStringN(reader, messageLen)
	if err != nil {
		return err
	}
	if header[5] != tunnelStatusOK {
		if message == "" {
			return errors.New("tunnel request failed")
		}
		return errors.New(message)
	}
	return nil
}

func writeTunnelUDPFrame(w io.Writer, frame tunnelUDPFrame) error {
	if len(frame.host) > tunnelMaxHostLength || len(frame.payload) > tunnelMaxUDPPayload {
		return errTunnelInvalidLength
	}
	header := make([]byte, 9)
	header[0] = tunnelVersion
	binary.BigEndian.PutUint16(header[1:3], uint16(len(frame.host)))
	binary.BigEndian.PutUint16(header[3:5], frame.port)
	binary.BigEndian.PutUint32(header[5:9], uint32(len(frame.payload)))
	if err := writeAll(w, header); err != nil {
		return err
	}
	if frame.host != "" {
		if err := writeAll(w, []byte(frame.host)); err != nil {
			return err
		}
	}
	if len(frame.payload) > 0 {
		if err := writeAll(w, frame.payload); err != nil {
			return err
		}
	}
	return nil
}

func readTunnelUDPFrame(reader io.Reader) (tunnelUDPFrame, error) {
	header := make([]byte, 9)
	if _, err := io.ReadFull(reader, header); err != nil {
		return tunnelUDPFrame{}, err
	}
	if header[0] != tunnelVersion {
		return tunnelUDPFrame{}, errTunnelBadVersion
	}
	hostLen := int(binary.BigEndian.Uint16(header[1:3]))
	payloadLen := int(binary.BigEndian.Uint32(header[5:9]))
	if hostLen > tunnelMaxHostLength || payloadLen > tunnelMaxUDPPayload {
		return tunnelUDPFrame{}, errTunnelInvalidLength
	}
	host, err := readStringN(reader, hostLen)
	if err != nil {
		return tunnelUDPFrame{}, err
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return tunnelUDPFrame{}, err
		}
	}
	return tunnelUDPFrame{
		host:    host,
		port:    binary.BigEndian.Uint16(header[3:5]),
		payload: payload,
	}, nil
}

func readStringN(reader io.Reader, n int) (string, error) {
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func tokenMatches(expected string, actual string) bool {
	return expected == "" || expected == actual
}
