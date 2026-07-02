package tcptun

import (
	"bytes"
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
	xrayMuxCoolHost = "v1.mux.cool"
	xrayMuxCoolPort = uint16(9527)

	xrayMuxStatusNew       = byte(0x01)
	xrayMuxStatusKeep      = byte(0x02)
	xrayMuxStatusEnd       = byte(0x03)
	xrayMuxStatusKeepAlive = byte(0x04)

	xrayMuxOptionData  = byte(0x01)
	xrayMuxOptionError = byte(0x02)

	xrayMuxNetworkTCP = byte(0x01)
	xrayMuxNetworkUDP = byte(0x02)

	xrayMuxMaxMeta    = 512
	xrayMuxMaxPayload = 64 * 1024
	xrayMuxChunkSize  = 8 * 1024
	xrayMuxReadQueue  = 128

	protocolXrayMux = "xray-mux"
)

var (
	errXrayMuxClosed        = errors.New("xray mux session closed")
	errXrayMuxInvalidFrame  = errors.New("invalid xray mux frame")
	errXrayMuxPayloadTooBig = errors.New("xray mux payload too large")
)

type xrayMuxTarget struct {
	network byte
	host    string
	port    uint16
}

type xrayMuxFrame struct {
	sessionID uint16
	status    byte
	option    byte
	target    xrayMuxTarget
	payload   []byte
}

type xrayMuxReadItem struct {
	target  xrayMuxTarget
	payload []byte
}

type xrayMuxClient struct {
	mu      sync.Mutex
	session *xrayMuxSession
}

type xrayMuxSession struct {
	conn       net.Conn
	reader     io.Reader
	server     bool
	writeMu    sync.Mutex
	mu         sync.Mutex
	streams    map[uint16]*xrayMuxStream
	acceptCh   chan *xrayMuxStream
	done       chan struct{}
	err        error
	nextID     uint16
	lastActive time.Time
	closeOnce  sync.Once
	localAddr  net.Addr
	remoteAddr net.Addr
}

type xrayMuxStream struct {
	session    *xrayMuxSession
	id         uint16
	target     xrayMuxTarget
	readCh     chan xrayMuxReadItem
	writeMu    sync.Mutex
	readMu     sync.Mutex
	readBuf    []byte
	readErr    error
	readClosed bool
	openSent   bool
	closeOnce  sync.Once
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newXrayMuxSession(conn net.Conn, reader io.Reader, server bool) *xrayMuxSession {
	session := &xrayMuxSession{
		conn:       conn,
		reader:     reader,
		server:     server,
		streams:    make(map[uint16]*xrayMuxStream),
		acceptCh:   make(chan *xrayMuxStream, 64),
		done:       make(chan struct{}),
		nextID:     1,
		lastActive: time.Now(),
		localAddr:  conn.LocalAddr(),
		remoteAddr: conn.RemoteAddr(),
	}
	go session.readLoop()
	return session
}

func (s *xrayMuxSession) openTCPStream(ctx context.Context, req socksRequest) (*xrayMuxStream, error) {
	return s.openStream(ctx, xrayMuxTarget{network: xrayMuxNetworkTCP, host: req.host, port: req.port})
}

func (s *xrayMuxSession) openUDPStream(ctx context.Context, host string, port uint16) (*xrayMuxStream, error) {
	return s.openStream(ctx, xrayMuxTarget{network: xrayMuxNetworkUDP, host: host, port: port})
}

func (s *xrayMuxSession) openStream(ctx context.Context, target xrayMuxTarget) (*xrayMuxStream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	stream, err := s.registerLocalStream(target)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (s *xrayMuxSession) registerLocalStream(target xrayMuxTarget) (*xrayMuxStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	select {
	case <-s.done:
		return nil, errXrayMuxClosed
	default:
	}
	id := s.nextID
	s.nextID++
	if s.nextID == 0 {
		s.nextID = 1
	}
	stream := newXrayMuxStream(s, id, target)
	s.streams[id] = stream
	s.lastActive = time.Now()
	return stream, nil
}

func newXrayMuxStream(session *xrayMuxSession, id uint16, target xrayMuxTarget) *xrayMuxStream {
	return &xrayMuxStream{
		session: session,
		id:      id,
		target:  target,
		readCh:  make(chan xrayMuxReadItem, xrayMuxReadQueue),
		localAddr: addrString{
			network: protocolXrayMux,
			address: session.localAddr.String(),
		},
		remoteAddr: addrString{
			network: protocolXrayMux,
			address: session.remoteAddr.String(),
		},
	}
}

func (s *xrayMuxSession) accept(ctx context.Context) (*xrayMuxStream, error) {
	select {
	case stream, ok := <-s.acceptCh:
		if !ok {
			return nil, s.errValue()
		}
		return stream, nil
	case <-s.done:
		return nil, s.errValue()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *xrayMuxSession) readLoop() {
	for {
		frame, err := readXrayMuxFrame(s.reader)
		if err != nil {
			s.closeWithError(err)
			return
		}
		s.touch()
		if err := s.handleFrame(frame); err != nil {
			s.closeWithError(err)
			return
		}
	}
}

func (s *xrayMuxSession) handleFrame(frame xrayMuxFrame) error {
	switch frame.status {
	case xrayMuxStatusNew:
		if s.server {
			stream, err := s.acceptRemoteStream(frame.sessionID, frame.target)
			if err != nil {
				return err
			}
			if frame.option&xrayMuxOptionData != 0 {
				return stream.deliver(frame.target, frame.payload)
			}
			return nil
		}
		return nil
	case xrayMuxStatusKeep, xrayMuxStatusKeepAlive:
		if frame.option&xrayMuxOptionData == 0 {
			return nil
		}
		stream := s.stream(frame.sessionID)
		if stream == nil {
			return nil
		}
		return stream.deliver(frame.target, frame.payload)
	case xrayMuxStatusEnd:
		stream := s.stream(frame.sessionID)
		if stream != nil {
			if frame.option&xrayMuxOptionData != 0 {
				if err := stream.deliver(frame.target, frame.payload); err != nil {
					return err
				}
			}
			stream.closeRead(io.EOF)
			s.removeStream(frame.sessionID)
		}
		return nil
	default:
		return errXrayMuxInvalidFrame
	}
}

func (s *xrayMuxSession) acceptRemoteStream(streamID uint16, target xrayMuxTarget) (*xrayMuxStream, error) {
	if target.host == "" || target.port == 0 {
		return nil, errProtocolInvalidAddress
	}
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return nil, s.err
	}
	if _, exists := s.streams[streamID]; exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("duplicate xray mux stream id %d", streamID)
	}
	stream := newXrayMuxStream(s, streamID, target)
	stream.openSent = true
	s.streams[streamID] = stream
	s.lastActive = time.Now()
	s.mu.Unlock()

	select {
	case s.acceptCh <- stream:
		return stream, nil
	case <-s.done:
		stream.closeRead(errXrayMuxClosed)
		return nil, errXrayMuxClosed
	}
}

func (s *xrayMuxSession) stream(streamID uint16) *xrayMuxStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[streamID]
}

func (s *xrayMuxSession) removeStream(streamID uint16) {
	s.mu.Lock()
	delete(s.streams, streamID)
	s.lastActive = time.Now()
	s.mu.Unlock()
}

func (s *xrayMuxSession) writeFrame(frame xrayMuxFrame) error {
	if len(frame.payload) > xrayMuxMaxPayload {
		return errXrayMuxPayloadTooBig
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := writeXrayMuxFrame(s.conn, frame); err != nil {
		s.closeWithError(err)
		return err
	}
	s.touch()
	return nil
}

func (s *xrayMuxSession) touch() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

func (s *xrayMuxSession) idleDelay(now time.Time, timeout time.Duration) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) > 0 {
		return timeout
	}
	idle := now.Sub(s.lastActive)
	if idle >= timeout {
		return 0
	}
	return timeout - idle
}

func (s *xrayMuxSession) errValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return errXrayMuxClosed
}

func (s *xrayMuxSession) closeWithError(cause error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if cause == nil {
			cause = errXrayMuxClosed
		}
		s.err = cause
		streams := make([]*xrayMuxStream, 0, len(s.streams))
		for _, stream := range s.streams {
			streams = append(streams, stream)
		}
		s.streams = make(map[uint16]*xrayMuxStream)
		close(s.done)
		close(s.acceptCh)
		s.mu.Unlock()

		for _, stream := range streams {
			stream.closeRead(cause)
		}
		if err := s.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.mu.Lock()
			s.err = errors.Join(s.err, err)
			s.mu.Unlock()
		}
	})
}

func (s *xrayMuxSession) Close() error {
	s.closeWithError(errXrayMuxClosed)
	return s.errValue()
}

func (m *xrayMuxStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(m.readBuf) == 0 {
		item, ok := <-m.readCh
		if !ok {
			m.readMu.Lock()
			err := m.readErr
			m.readMu.Unlock()
			if err == nil {
				err = io.EOF
			}
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		m.readBuf = item.payload
	}
	n := copy(p, m.readBuf)
	m.readBuf = m.readBuf[n:]
	return n, nil
}

func (m *xrayMuxStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	written := 0
	for written < len(p) {
		end := written + xrayMuxChunkSize
		if end > len(p) {
			end = len(p)
		}
		status := xrayMuxStatusKeep
		target := xrayMuxTarget{}
		if !m.openSent {
			status = xrayMuxStatusNew
			target = m.target
			m.openSent = true
		}
		if err := m.session.writeFrame(xrayMuxFrame{
			sessionID: m.id,
			status:    status,
			option:    xrayMuxOptionData,
			target:    target,
			payload:   p[written:end],
		}); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

func (m *xrayMuxStream) writePacket(frame protocolUDPFrame) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	status := xrayMuxStatusKeep
	target := xrayMuxTarget{network: xrayMuxNetworkUDP, host: frame.host, port: frame.port}
	if !m.openSent {
		status = xrayMuxStatusNew
		m.openSent = true
	}
	return m.session.writeFrame(xrayMuxFrame{
		sessionID: m.id,
		status:    status,
		option:    xrayMuxOptionData,
		target:    target,
		payload:   frame.payload,
	})
}

func (m *xrayMuxStream) readPacket() (protocolUDPFrame, error) {
	for {
		item, ok := <-m.readCh
		if !ok {
			m.readMu.Lock()
			err := m.readErr
			m.readMu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return protocolUDPFrame{}, err
		}
		target := item.target
		if target.host == "" {
			target = m.target
		}
		if target.host == "" || target.port == 0 {
			return protocolUDPFrame{}, errProtocolInvalidAddress
		}
		return protocolUDPFrame{host: target.host, port: target.port, payload: item.payload}, nil
	}
}

func (m *xrayMuxStream) Close() error {
	var err error
	m.closeOnce.Do(func() {
		m.writeMu.Lock()
		if m.openSent {
			err = m.session.writeFrame(xrayMuxFrame{
				sessionID: m.id,
				status:    xrayMuxStatusEnd,
			})
		}
		m.writeMu.Unlock()
		m.session.removeStream(m.id)
		m.closeRead(io.ErrClosedPipe)
	})
	return err
}

func (m *xrayMuxStream) LocalAddr() net.Addr {
	return m.localAddr
}

func (m *xrayMuxStream) RemoteAddr() net.Addr {
	return m.remoteAddr
}

func (m *xrayMuxStream) SetDeadline(time.Time) error {
	return nil
}

func (m *xrayMuxStream) SetReadDeadline(time.Time) error {
	return nil
}

func (m *xrayMuxStream) SetWriteDeadline(time.Time) error {
	return nil
}

func (m *xrayMuxStream) deliver(target xrayMuxTarget, payload []byte) error {
	m.readMu.Lock()
	if m.readClosed {
		m.readMu.Unlock()
		return errXrayMuxClosed
	}
	select {
	case m.readCh <- xrayMuxReadItem{target: target, payload: payload}:
		m.readMu.Unlock()
		return nil
	case <-m.session.done:
		m.readMu.Unlock()
		return errXrayMuxClosed
	}
}

func (m *xrayMuxStream) closeRead(cause error) {
	m.readMu.Lock()
	defer m.readMu.Unlock()
	if m.readClosed {
		return
	}
	m.readErr = cause
	m.readClosed = true
	close(m.readCh)
}

func (s *proxyServer) openXrayMuxTCPStream(ctx context.Context, req socksRequest) (net.Conn, error) {
	session, err := s.xrayMuxSession(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := session.openTCPStream(ctx, req)
	if err == nil {
		return stream, nil
	}
	s.resetXrayMuxSession(session)
	session, err = s.xrayMuxSession(ctx)
	if err != nil {
		return nil, err
	}
	return session.openTCPStream(ctx, req)
}

func (s *proxyServer) openXrayMuxUDPStream(ctx context.Context, host string, port uint16) (*xrayMuxStream, error) {
	session, err := s.xrayMuxSession(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := session.openUDPStream(ctx, host, port)
	if err == nil {
		return stream, nil
	}
	s.resetXrayMuxSession(session)
	session, err = s.xrayMuxSession(ctx)
	if err != nil {
		return nil, err
	}
	return session.openUDPStream(ctx, host, port)
}

func (s *proxyServer) xrayMuxSession(ctx context.Context) (*xrayMuxSession, error) {
	s.xrayMux.mu.Lock()
	defer s.xrayMux.mu.Unlock()
	if s.xrayMux.session != nil {
		select {
		case <-s.xrayMux.session.done:
			s.xrayMux.session = nil
		default:
			return s.xrayMux.session, nil
		}
	}
	conn, reader, err := s.openXrayMuxConn(ctx)
	if err != nil {
		return nil, err
	}
	s.xrayMux.session = newXrayMuxSession(conn, reader, false)
	go s.closeIdleXrayMuxSession(s.xrayMux.session)
	return s.xrayMux.session, nil
}

func (s *proxyServer) openXrayMuxConn(ctx context.Context) (net.Conn, io.Reader, error) {
	conn, err := s.dialTunnelTransport(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := tuneTCP(conn, s.cfg.HeartbeatInterval); err != nil {
		return nil, nil, closeAfterError(conn, err)
	}
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		if err := writeVLESSMuxRequest(conn, s.cfg.Token, s.cfg.TunnelFlow); err != nil {
			return nil, nil, closeAfterError(conn, err)
		}
		if isVisionFlow(s.cfg.TunnelFlow) {
			userID, err := parseUUIDToken(s.cfg.Token)
			if err != nil {
				return nil, nil, closeAfterError(conn, err)
			}
			vision := newVisionConn(conn, userID, readVLESSResponse)
			if err := vision.WriteInitialPadding(); err != nil {
				return nil, nil, closeAfterError(vision, err)
			}
			return vision, vision, nil
		}
		clientConn := newVLESSClientConn(conn)
		return clientConn, clientConn, nil
	case tunnelProtocolVMess:
		session, err := writeVMessTCPRequest(conn, s.cfg.Token, xrayMuxCoolHost, xrayMuxCoolPort)
		if err != nil {
			return nil, nil, closeAfterError(conn, err)
		}
		vmessConn := newVMessClientConn(conn, session)
		return vmessConn, vmessConn, nil
	default:
		return nil, nil, closeAfterError(conn, fmt.Errorf("xray mux is unsupported for %s protocol", s.cfg.TunnelProtocol))
	}
}

func (s *proxyServer) resetXrayMuxSession(session *xrayMuxSession) {
	s.xrayMux.mu.Lock()
	defer s.xrayMux.mu.Unlock()
	if s.xrayMux.session == session {
		if err := session.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
			if logErr := logf(s.log, "close failed xray mux session: %v\n", err); logErr != nil {
				return
			}
		}
		s.xrayMux.session = nil
	}
}

func (s *proxyServer) resetCurrentXrayMuxSession() {
	s.xrayMux.mu.Lock()
	session := s.xrayMux.session
	s.xrayMux.session = nil
	s.xrayMux.mu.Unlock()
	if session == nil {
		return
	}
	if err := session.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
		if logErr := logf(s.log, "close failed xray mux session: %v\n", err); logErr != nil {
			return
		}
	}
}

func (s *proxyServer) closeIdleXrayMuxSession(session *xrayMuxSession) {
	timeout := s.cfg.ConnectionIdleTimeout
	if session == nil || timeout <= 0 {
		return
	}
	for {
		delay := session.idleDelay(time.Now(), timeout)
		if delay <= 0 {
			s.resetXrayMuxSession(session)
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-session.done:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *proxyServer) serveXrayMuxSession(ctx context.Context, conn net.Conn, reader io.Reader) error {
	session := newXrayMuxSession(conn, reader, true)
	defer func() {
		if err := session.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
			if logErr := logf(s.log, "close xray mux session %s: %v\n", conn.RemoteAddr(), err); logErr != nil {
				return
			}
		}
	}()
	for {
		stream, err := session.accept(ctx)
		if err != nil {
			if errors.Is(err, errXrayMuxClosed) || errors.Is(err, io.EOF) || ctx.Err() != nil || isExpectedNetworkClose(err) {
				return nil
			}
			return err
		}
		go s.handleXrayMuxStream(ctx, stream)
	}
}

func (s *proxyServer) handleXrayMuxStream(ctx context.Context, stream *xrayMuxStream) {
	if err := s.handleXrayMuxStreamError(ctx, stream); err != nil && s.cfg.Verbose {
		if logErr := logf(s.log, "xray mux stream error for %s: %v\n", stream.RemoteAddr(), err); logErr != nil {
			return
		}
	}
}

func (s *proxyServer) handleXrayMuxStreamError(ctx context.Context, stream *xrayMuxStream) error {
	defer func() {
		if err := stream.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
			if logErr := logf(s.log, "close xray mux stream %s: %v\n", stream.RemoteAddr(), err); logErr != nil {
				return
			}
		}
	}()
	switch stream.target.network {
	case xrayMuxNetworkTCP:
		return s.handleXrayMuxTCPStream(ctx, stream)
	case xrayMuxNetworkUDP:
		return s.handleXrayMuxUDPStream(ctx, stream)
	default:
		return errProtocolUnsupported
	}
}

func (s *proxyServer) handleXrayMuxTCPStream(ctx context.Context, stream *xrayMuxStream) error {
	logTarget := accessTarget(stream.target.host, strconv.Itoa(int(stream.target.port)))
	target, err := s.publicTCPTarget(ctx, stream.target.host, stream.target.port)
	if err != nil {
		return err
	}
	outbound, err := s.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer closeConnWithLog(outbound, s.log, "xray mux tcp target "+target)
	if err := tuneTCP(outbound, s.cfg.HeartbeatInterval); err != nil {
		return err
	}
	if err := s.bridge(outbound, stream, stream); err != nil {
		if logErr := accessLog(s.log, accessSource(protocolXrayMux, stream.RemoteAddr()), "-", logTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource(protocolXrayMux, stream.RemoteAddr()), "-", logTarget, "ok")
}

func (s *proxyServer) handleXrayMuxUDPStream(ctx context.Context, stream *xrayMuxStream) error {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return err
	}
	defer closeUDPWithLog(udpConn, s.log, "xray mux udp target")
	done := make(chan error, 2)
	go s.xrayMuxUDPClientToRemote(ctx, stream, udpConn, done)
	go s.xrayMuxUDPRemoteToClient(ctx, stream, udpConn, done)
	if err := waitUDPProxyDone(ctx, stream, udpConn, done); err != nil && !isExpectedNetworkClose(err) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (s *proxyServer) xrayMuxUDPClientToRemote(ctx context.Context, stream *xrayMuxStream, udpConn *net.UDPConn, done chan<- error) {
	for {
		frame, err := stream.readPacket()
		if err != nil {
			done <- err
			return
		}
		if len(frame.payload) == 0 {
			if ctx.Err() != nil {
				done <- ctx.Err()
				return
			}
			continue
		}
		targetText := net.JoinHostPort(frame.host, strconv.Itoa(int(frame.port)))
		target, err := s.publicUDPTarget(ctx, frame.host, frame.port)
		if err != nil {
			if errors.Is(err, errServerTargetNotPublic) {
				if s.cfg.Verbose {
					if logErr := logf(s.log, "drop xray mux udp %s: %v\n", targetText, err); logErr != nil {
						done <- logErr
						return
					}
				}
				if ctx.Err() != nil {
					done <- ctx.Err()
					return
				}
				continue
			}
			done <- err
			return
		}
		if _, err := udpConn.WriteToUDP(frame.payload, target); err != nil {
			done <- err
			return
		}
		if err := refreshUDPReadDeadline(udpConn, s.cfg.UDPSessionTimeout); err != nil {
			done <- err
			return
		}
		if s.cfg.Verbose {
			if err := logf(s.log, "xray mux udp %s\n", targetText); err != nil {
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

func (s *proxyServer) xrayMuxUDPRemoteToClient(ctx context.Context, stream *xrayMuxStream, udpConn *net.UDPConn, done chan<- error) {
	buf := make([]byte, udpBufferSize)
	for {
		if err := refreshUDPReadDeadline(udpConn, s.cfg.UDPSessionTimeout); err != nil {
			done <- err
			return
		}
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if isNetworkTimeout(err) {
				done <- nil
				return
			}
			done <- err
			return
		}
		if err := stream.writePacket(protocolUDPFrame{
			host:    addr.IP.String(),
			port:    uint16(addr.Port),
			payload: buf[:n],
		}); err != nil {
			done <- err
			return
		}
		if ctx.Err() != nil {
			done <- ctx.Err()
			return
		}
	}
}

func readXrayMuxFrame(reader io.Reader) (xrayMuxFrame, error) {
	metaLenBuf := make([]byte, 2)
	if _, err := io.ReadFull(reader, metaLenBuf); err != nil {
		return xrayMuxFrame{}, err
	}
	metaLen := int(binary.BigEndian.Uint16(metaLenBuf))
	if metaLen < 4 || metaLen > xrayMuxMaxMeta {
		return xrayMuxFrame{}, errXrayMuxInvalidFrame
	}
	meta := make([]byte, metaLen)
	if _, err := io.ReadFull(reader, meta); err != nil {
		return xrayMuxFrame{}, err
	}
	frame := xrayMuxFrame{
		sessionID: binary.BigEndian.Uint16(meta[0:2]),
		status:    meta[2],
		option:    meta[3],
	}
	if frame.status == xrayMuxStatusNew || (frame.status == xrayMuxStatusKeep && len(meta) > 4 && meta[4] == xrayMuxNetworkUDP) {
		if len(meta) < 8 {
			return xrayMuxFrame{}, errXrayMuxInvalidFrame
		}
		target, err := parseXrayMuxTarget(meta[4:])
		if err != nil {
			return xrayMuxFrame{}, err
		}
		frame.target = target
	}
	if frame.option&xrayMuxOptionData != 0 {
		payload, err := readXrayMuxPayload(reader)
		if err != nil {
			return xrayMuxFrame{}, err
		}
		frame.payload = payload
	}
	return frame, nil
}

func readXrayMuxPayload(reader io.Reader) ([]byte, error) {
	lengthBuf := make([]byte, 2)
	if _, err := io.ReadFull(reader, lengthBuf); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(lengthBuf))
	if size > xrayMuxMaxPayload {
		return nil, errXrayMuxPayloadTooBig
	}
	payload := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func parseXrayMuxTarget(src []byte) (xrayMuxTarget, error) {
	if len(src) < 4 {
		return xrayMuxTarget{}, io.ErrUnexpectedEOF
	}
	network := src[0]
	reader := bytes.NewReader(src[1:])
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBuf); err != nil {
		return xrayMuxTarget{}, err
	}
	atyp, err := reader.ReadByte()
	if err != nil {
		return xrayMuxTarget{}, err
	}
	host, err := readVLESSAddress(reader, atyp)
	if err != nil {
		return xrayMuxTarget{}, err
	}
	return xrayMuxTarget{network: network, host: host, port: binary.BigEndian.Uint16(portBuf)}, nil
}

func writeXrayMuxFrame(w io.Writer, frame xrayMuxFrame) error {
	meta := make([]byte, 0, 64+len(frame.target.host))
	meta = appendUint16(meta, frame.sessionID)
	meta = append(meta, frame.status, frame.option)
	if frame.status == xrayMuxStatusNew || (frame.status == xrayMuxStatusKeep && frame.target.network == xrayMuxNetworkUDP && frame.target.host != "") {
		var err error
		meta, err = appendXrayMuxTarget(meta, frame.target)
		if err != nil {
			return err
		}
	}
	if len(meta) > xrayMuxMaxMeta {
		return errTunnelInvalidLength
	}
	header := make([]byte, 0, 4+len(meta)+len(frame.payload))
	header = appendUint16(header, uint16(len(meta)))
	header = append(header, meta...)
	if frame.option&xrayMuxOptionData == 0 {
		return writeAll(w, header)
	}
	if len(frame.payload) > 0xffff {
		return errXrayMuxPayloadTooBig
	}
	header = appendUint16(header, uint16(len(frame.payload)))
	return writeBuffers(w, header, frame.payload)
}

func appendXrayMuxTarget(dst []byte, target xrayMuxTarget) ([]byte, error) {
	if target.host == "" || target.port == 0 {
		return nil, errProtocolInvalidAddress
	}
	switch target.network {
	case xrayMuxNetworkTCP, xrayMuxNetworkUDP:
	default:
		return nil, errProtocolUnsupported
	}
	dst = append(dst, target.network)
	dst = appendUint16(dst, target.port)
	return appendVLESSAddress(dst, target.host)
}

func isXrayCompatibleMuxProtocol(protocol string) bool {
	return protocol == tunnelProtocolVLESS || protocol == tunnelProtocolVMess
}

func (s *proxyServer) canUseXrayMuxClient() bool {
	if !isXrayCompatibleMuxProtocol(s.cfg.TunnelProtocol) {
		return false
	}
	return s.cfg.TunnelProtocol != tunnelProtocolVLESS || !isVisionFlow(s.cfg.TunnelFlow)
}

func isXrayMuxTarget(host string, port uint16) bool {
	return stringsEqualFoldASCII(host, xrayMuxCoolHost) && port == xrayMuxCoolPort
}

func stringsEqualFoldASCII(a string, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ac := a[i]
		bc := b[i]
		if 'A' <= ac && ac <= 'Z' {
			ac += 'a' - 'A'
		}
		if 'A' <= bc && bc <= 'Z' {
			bc += 'a' - 'A'
		}
		if ac != bc {
			return false
		}
	}
	return true
}
