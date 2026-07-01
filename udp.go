package tcptun

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
)

const udpBufferSize = 64 * 1024

var (
	errSocksUDPFragmentUnsupported = errors.New("socks udp fragments are unsupported")
	errSocksUDPInvalidHeader       = errors.New("invalid socks udp header")
)

type socksUDPDatagram struct {
	host    string
	port    uint16
	payload []byte
	raw     []byte
}

type udpUpstream struct {
	tcp       net.Conn
	relayAddr *net.UDPAddr
	label     string
}

type udpRelay struct {
	server     *proxyServer
	conn       *net.UDPConn
	clientAddr *net.UDPAddr
	upstream   *udpUpstream
	native     *nativeUDPUpstream
	protocols  map[string]*protocolUDPUpstream
	mu         sync.Mutex
}

func (s *proxyServer) handleSocks5UDPAssociate(ctx context.Context, client net.Conn, reader *bufio.Reader, req socksRequest) error {
	bindAddr, err := udpBindAddrForTCP(client)
	if err != nil {
		return err
	}
	udpConn, err := net.ListenUDP("udp", bindAddr)
	if err != nil {
		if writeErr := writeSocks5Reply(client, 0x01); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return nil
	}
	defer closeUDPWithLog(udpConn, s.log, "local udp relay")

	if err := writeSocks5ReplyAddr(client, socksReplySucceeded, udpConn.LocalAddr()); err != nil {
		return err
	}
	if s.cfg.Verbose {
		if err := logf(s.log, "udp associate %s -> %s\n", client.RemoteAddr(), udpConn.LocalAddr()); err != nil {
			return err
		}
	}

	relay := &udpRelay{server: s, conn: udpConn}
	return relay.run(ctx, reader)
}

func udpBindAddrForTCP(conn net.Conn) (*net.UDPAddr, error) {
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(trimHostBrackets(host))
	if ip == nil {
		return nil, fmt.Errorf("cannot parse local TCP bind IP %q", host)
	}
	return &net.UDPAddr{IP: ip, Port: 0}, nil
}

func (r *udpRelay) run(ctx context.Context, controlReader *bufio.Reader) error {
	udpErr := make(chan error, 1)
	go func() {
		udpErr <- r.loop(ctx)
	}()

	controlErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, controlReader)
		if err != nil && !isExpectedNetworkClose(err) {
			controlErr <- err
			return
		}
		controlErr <- nil
	}()

	select {
	case err := <-udpErr:
		r.closeUpstream()
		return err
	case err := <-controlErr:
		closeErr := r.conn.Close()
		r.closeUpstream()
		udpLoopErr := <-udpErr
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			return errors.Join(err, closeErr, udpLoopErr)
		}
		if udpLoopErr != nil && !errors.Is(udpLoopErr, net.ErrClosed) {
			return errors.Join(err, udpLoopErr)
		}
		return err
	case <-ctx.Done():
		closeErr := r.conn.Close()
		r.closeUpstream()
		udpLoopErr := <-udpErr
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			return errors.Join(ctx.Err(), closeErr, udpLoopErr)
		}
		if udpLoopErr != nil && !errors.Is(udpLoopErr, net.ErrClosed) {
			return errors.Join(ctx.Err(), udpLoopErr)
		}
		return ctx.Err()
	}
}

func (r *udpRelay) loop(ctx context.Context) error {
	buf := make([]byte, udpBufferSize)
	for {
		n, addr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		packet := append([]byte(nil), buf[:n]...)
		if r.fromClient(addr) {
			if err := r.handleClientPacket(ctx, addr, packet); err != nil {
				return err
			}
			continue
		}
		if err := r.handleRemotePacket(addr, packet); err != nil {
			return err
		}
	}
}

func (r *udpRelay) fromClient(addr *net.UDPAddr) bool {
	if r.clientAddr == nil {
		r.clientAddr = cloneUDPAddr(addr)
		return true
	}
	return udpAddrEqual(r.clientAddr, addr)
}

func (r *udpRelay) handleClientPacket(ctx context.Context, addr *net.UDPAddr, packet []byte) error {
	dgram, err := parseSocksUDPDatagram(packet)
	if err != nil {
		if errors.Is(err, errSocksUDPFragmentUnsupported) {
			return nil
		}
		return err
	}
	if !r.server.routes.shouldForceUpstream(dgram.host) && hostIsInternal(ctx, dgram.host) {
		targetText := net.JoinHostPort(dgram.host, strconv.Itoa(int(dgram.port)))
		logTarget := accessTarget(dgram.host, strconv.Itoa(int(dgram.port)))
		target, err := net.ResolveUDPAddr("udp", targetText)
		if err != nil {
			if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), "-", logTarget, err.Error()); logErr != nil {
				return errors.Join(err, logErr)
			}
			return err
		}
		if _, err = r.conn.WriteToUDP(dgram.payload, target); err != nil {
			if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), "-", logTarget, err.Error()); logErr != nil {
				return errors.Join(err, logErr)
			}
			return err
		}
		return accessLog(r.server.log, accessSource("socks5-udp", addr), "-", logTarget, "ok")
	}
	logTarget := accessTarget(dgram.host, strconv.Itoa(int(dgram.port)))
	if r.server.cfg.Mode == proxyModeClient {
		return r.handleClientPacketTunnel(ctx, addr, dgram, logTarget)
	}
	upstream, err := r.ensureUpstream(ctx)
	if err != nil {
		if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), r.server.upstreamTarget(), logTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	_, err = r.conn.WriteToUDP(packet, upstream.relayAddr)
	if err != nil {
		if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), upstream.label, logTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	if r.server.cfg.Verbose {
		if err := logf(r.server.log, "udp tcptun %s -> %s via %s\n", addr, net.JoinHostPort(dgram.host, strconv.Itoa(int(dgram.port))), upstream.label); err != nil {
			return err
		}
	}
	return accessLog(r.server.log, accessSource("socks5-udp", addr), upstream.label, logTarget, "ok")
}

func (r *udpRelay) handleRemotePacket(addr *net.UDPAddr, packet []byte) error {
	client := r.clientAddr
	if client == nil {
		return nil
	}
	upstream := r.currentUpstream()
	if upstream != nil && udpAddrEqual(upstream.relayAddr, addr) {
		_, err := r.conn.WriteToUDP(packet, client)
		return err
	}
	wrapped := buildSocksUDPDatagram(addr.IP.String(), uint16(addr.Port), packet)
	_, err := r.conn.WriteToUDP(wrapped, client)
	return err
}

func (r *udpRelay) ensureUpstream(ctx context.Context) (*udpUpstream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.upstream != nil {
		return r.upstream, nil
	}
	upstream, err := r.server.connectViaUpstreamUDP(ctx, upstreamStickySource(r.clientAddr))
	if err != nil {
		return nil, err
	}
	r.upstream = upstream
	return upstream, nil
}

func (r *udpRelay) currentUpstream() *udpUpstream {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upstream
}

func (r *udpRelay) closeUpstream() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.native != nil {
		if err := r.native.tcp.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(r.server.log, "close tunnel udp upstream %s: %v\n", r.native.label, err); logErr != nil {
				r.native = nil
				return
			}
		}
		r.native = nil
	}
	for key, upstream := range r.protocols {
		if upstream == nil {
			continue
		}
		if err := upstream.tcp.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(r.server.log, "close protocol udp upstream %s: %v\n", upstream.label, err); logErr != nil {
				delete(r.protocols, key)
				return
			}
		}
		delete(r.protocols, key)
	}
	if r.upstream == nil {
		return
	}
	if err := r.upstream.tcp.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		if logErr := logf(r.server.log, "close udp upstream %s: %v\n", r.upstream.label, err); logErr != nil {
			r.upstream = nil
			return
		}
	}
	r.upstream = nil
}

func (r *udpRelay) handleClientPacketTunnel(ctx context.Context, addr *net.UDPAddr, dgram socksUDPDatagram, targetText string) error {
	if r.server.cfg.TunnelProtocol == tunnelProtocolNative {
		return r.handleClientPacketNativeTunnel(ctx, addr, dgram, targetText)
	}
	return r.handleClientPacketProtocolTunnel(ctx, addr, dgram, targetText)
}

func (r *udpRelay) handleClientPacketNativeTunnel(ctx context.Context, addr *net.UDPAddr, dgram socksUDPDatagram, targetText string) error {
	upstream, err := r.ensureNativeTunnelUpstream(ctx)
	if err != nil {
		if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), r.server.upstreamTarget(), targetText, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	upstream.writeMu.Lock()
	err = writeTunnelUDPFrame(upstream.tcp, tunnelUDPFrame{
		host:    dgram.host,
		port:    dgram.port,
		payload: dgram.payload,
	})
	upstream.writeMu.Unlock()
	if err != nil {
		if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), upstream.label, targetText, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(r.server.log, accessSource("socks5-udp", addr), upstream.label, targetText, "ok")
}

func (r *udpRelay) handleClientPacketProtocolTunnel(ctx context.Context, addr *net.UDPAddr, dgram socksUDPDatagram, targetText string) error {
	upstream, err := r.ensureProtocolTunnelUpstream(ctx, dgram.host, dgram.port)
	if err != nil {
		if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), r.server.upstreamTarget(), targetText, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	upstream.writeMu.Lock()
	err = upstream.writeFrame(protocolUDPFrame{
		host:    dgram.host,
		port:    dgram.port,
		payload: dgram.payload,
	})
	upstream.writeMu.Unlock()
	if err != nil {
		if logErr := accessLog(r.server.log, accessSource("socks5-udp", addr), upstream.label, targetText, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(r.server.log, accessSource("socks5-udp", addr), upstream.label, targetText, "ok")
}

func (r *udpRelay) ensureNativeTunnelUpstream(ctx context.Context) (*nativeUDPUpstream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.native != nil {
		return r.native, nil
	}
	upstream, err := r.server.connectViaTunnelUDP(ctx)
	if err != nil {
		return nil, err
	}
	r.native = upstream
	go r.forwardTunnelUDPResponses(upstream)
	return upstream, nil
}

func (r *udpRelay) ensureProtocolTunnelUpstream(ctx context.Context, host string, port uint16) (*protocolUDPUpstream, error) {
	key := net.JoinHostPort(host, strconv.Itoa(int(port)))
	r.mu.Lock()
	if r.protocols == nil {
		r.protocols = make(map[string]*protocolUDPUpstream)
	}
	if upstream := r.protocols[key]; upstream != nil {
		r.mu.Unlock()
		return upstream, nil
	}
	r.mu.Unlock()

	upstream, err := r.server.connectViaProtocolTunnelUDP(ctx, host, port)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if existing := r.protocols[key]; existing != nil {
		r.mu.Unlock()
		if err := upstream.tcp.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return nil, err
		}
		return existing, nil
	}
	r.protocols[key] = upstream
	r.mu.Unlock()
	go r.forwardProtocolUDPResponses(key, upstream)
	return upstream, nil
}

func (r *udpRelay) forwardTunnelUDPResponses(upstream *nativeUDPUpstream) {
	for {
		frame, err := readTunnelUDPFrame(upstream.reader)
		if err != nil {
			if !isExpectedNetworkClose(err) {
				if logErr := logf(r.server.log, "tunnel udp read %s: %v\n", upstream.label, err); logErr != nil {
					return
				}
			}
			return
		}
		client := r.clientAddr
		if client == nil {
			continue
		}
		wrapped := buildSocksUDPDatagram(frame.host, frame.port, frame.payload)
		if _, err := r.conn.WriteToUDP(wrapped, client); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				if logErr := logf(r.server.log, "tunnel udp write client %s: %v\n", client, err); logErr != nil {
					return
				}
			}
			return
		}
	}
}

func (r *udpRelay) forwardProtocolUDPResponses(key string, upstream *protocolUDPUpstream) {
	defer r.removeProtocolUDPUpstream(key, upstream)
	for {
		frame, err := upstream.readFrame()
		if err != nil {
			if !isExpectedNetworkClose(err) {
				if logErr := logf(r.server.log, "%s udp read %s: %v\n", upstream.protocol, upstream.label, err); logErr != nil {
					return
				}
			}
			return
		}
		client := r.clientAddr
		if client == nil {
			continue
		}
		wrapped := buildSocksUDPDatagram(frame.host, frame.port, frame.payload)
		if _, err := r.conn.WriteToUDP(wrapped, client); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				if logErr := logf(r.server.log, "%s udp write client %s: %v\n", upstream.protocol, client, err); logErr != nil {
					return
				}
			}
			return
		}
	}
}

func (r *udpRelay) removeProtocolUDPUpstream(key string, upstream *protocolUDPUpstream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.protocols[key] == upstream {
		delete(r.protocols, key)
	}
}

func (s *proxyServer) connectViaUpstreamUDP(ctx context.Context, source string) (*udpUpstream, error) {
	if s.cfg.Mode == proxyModeClient {
		return nil, errors.New("SOCKS5 UDP upstream is unsupported in client mode")
	}
	if s.resolver == nil {
		return nil, errors.New("upstream resolver is nil")
	}
	preferred := s.sticky.get(source)
	targets := s.resolver.orderedTargetsFor(preferred)
	if len(targets) == 0 {
		return nil, errors.New("no upstream targets available")
	}

	var errs []error
	for _, candidate := range targets {
		target := candidate.target
		upstream, err := s.connectUpstreamRawTarget(ctx, target)
		if err != nil {
			s.sticky.clear(source, target)
			errs = append(errs, err)
			if ctx.Err() != nil {
				return nil, errors.Join(errs...)
			}
			continue
		}
		if err := s.authenticateUpstreamSOCKS5(upstream); err != nil {
			s.resolver.recordFailure(target)
			s.sticky.clear(source, target)
			errs = append(errs, fmt.Errorf("%s: %w", target, closeAfterError(upstream, err)))
			continue
		}
		if err := writeAll(upstream, buildSocks5UDPAssociateRequest("0.0.0.0", 0)); err != nil {
			s.resolver.recordFailure(target)
			s.sticky.clear(source, target)
			errs = append(errs, fmt.Errorf("%s: %w", target, closeAfterError(upstream, err)))
			continue
		}
		host, port, err := readSocks5ReplyEndpoint(upstream)
		if err != nil {
			s.resolver.recordFailure(target)
			s.sticky.clear(source, target)
			errs = append(errs, fmt.Errorf("%s: %w", target, closeAfterError(upstream, err)))
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			upstreamHost, _, splitErr := net.SplitHostPort(target)
			if splitErr != nil {
				s.resolver.recordFailure(target)
				s.sticky.clear(source, target)
				errs = append(errs, fmt.Errorf("%s: %w", target, closeAfterError(upstream, splitErr)))
				continue
			}
			host = trimHostBrackets(upstreamHost)
		}
		relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			s.resolver.recordFailure(target)
			s.sticky.clear(source, target)
			errs = append(errs, fmt.Errorf("%s: %w", target, closeAfterError(upstream, err)))
			continue
		}
		s.sticky.set(source, target)
		return &udpUpstream{tcp: upstream, relayAddr: relayAddr, label: target}, nil
	}
	return nil, errors.Join(errs...)
}

func parseSocksUDPDatagram(packet []byte) (socksUDPDatagram, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 {
		return socksUDPDatagram{}, errSocksUDPInvalidHeader
	}
	if packet[2] != 0 {
		return socksUDPDatagram{}, errSocksUDPFragmentUnsupported
	}
	host, offset, err := parseSocksAddrBytes(packet, 3)
	if err != nil {
		return socksUDPDatagram{}, err
	}
	if len(packet) < offset+2 {
		return socksUDPDatagram{}, errSocksUDPInvalidHeader
	}
	port := binary.BigEndian.Uint16(packet[offset : offset+2])
	payload := packet[offset+2:]
	return socksUDPDatagram{host: host, port: port, payload: payload, raw: packet}, nil
}

func buildSocksUDPDatagram(host string, port uint16, payload []byte) []byte {
	packet := []byte{0x00, 0x00, 0x00}
	packet = append(packet, encodeSocksAddr(host)...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, port)
	packet = append(packet, portBytes...)
	packet = append(packet, payload...)
	return packet
}

func readSocks5ReplyEndpoint(reader io.Reader) (string, uint16, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(reader, head); err != nil {
		return "", 0, err
	}
	if head[0] != socksVersion5 {
		return "", 0, errSocksUnsupportedVersion
	}
	if head[1] != socksReplySucceeded {
		return "", 0, fmt.Errorf("upstream socks command failed with reply %d", head[1])
	}
	host, err := readSocksAddr(reader, head[3])
	if err != nil {
		return "", 0, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", 0, err
	}
	return host, binary.BigEndian.Uint16(portBytes), nil
}

func readSocksAddr(reader io.Reader, atyp byte) (string, error) {
	switch atyp {
	case socksAtypIPv4:
		addr := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", err
		}
		return net.IP(addr).String(), nil
	case socksAtypIPv6:
		addr := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", err
		}
		return net.IP(addr).String(), nil
	case socksAtypDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", err
		}
		addr := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, addr); err != nil {
			return "", err
		}
		return string(addr), nil
	default:
		return "", errSocksUnsupportedAddress
	}
}

func parseSocksAddrBytes(packet []byte, offset int) (string, int, error) {
	if len(packet) <= offset {
		return "", 0, errSocksUDPInvalidHeader
	}
	atyp := packet[offset]
	offset++
	switch atyp {
	case socksAtypIPv4:
		if len(packet) < offset+net.IPv4len {
			return "", 0, errSocksUDPInvalidHeader
		}
		return net.IP(packet[offset : offset+net.IPv4len]).String(), offset + net.IPv4len, nil
	case socksAtypIPv6:
		if len(packet) < offset+net.IPv6len {
			return "", 0, errSocksUDPInvalidHeader
		}
		return net.IP(packet[offset : offset+net.IPv6len]).String(), offset + net.IPv6len, nil
	case socksAtypDomain:
		if len(packet) <= offset {
			return "", 0, errSocksUDPInvalidHeader
		}
		length := int(packet[offset])
		offset++
		if len(packet) < offset+length {
			return "", 0, errSocksUDPInvalidHeader
		}
		return string(packet[offset : offset+length]), offset + length, nil
	default:
		return "", 0, errSocksUnsupportedAddress
	}
}

func udpAddrEqual(a *net.UDPAddr, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Port == b.Port && a.IP.Equal(b.IP)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func closeUDPWithLog(conn *net.UDPConn, log io.Writer, label string) {
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		if logErr := logf(log, "close %s: %v\n", label, err); logErr != nil {
			return
		}
	}
}
