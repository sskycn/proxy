package main

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
		target, err := net.ResolveUDPAddr("udp", net.JoinHostPort(dgram.host, strconv.Itoa(int(dgram.port))))
		if err != nil {
			return err
		}
		if _, err = r.conn.WriteToUDP(dgram.payload, target); err != nil {
			return err
		}
		return accessLog(r.server.log, addr.String(), target.String(), "direct")
	}
	upstream, err := r.ensureUpstream(ctx)
	if err != nil {
		return err
	}
	_, err = r.conn.WriteToUDP(packet, upstream.relayAddr)
	if err != nil {
		return err
	}
	if r.server.cfg.Verbose {
		if err := logf(r.server.log, "udp proxy %s -> %s via %s\n", addr, net.JoinHostPort(dgram.host, strconv.Itoa(int(dgram.port))), upstream.label); err != nil {
			return err
		}
	}
	return accessLog(r.server.log, addr.String(), net.JoinHostPort(dgram.host, strconv.Itoa(int(dgram.port))), "proxy")
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
	upstream, err := r.server.connectViaUpstreamUDP(ctx)
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

func (s *proxyServer) connectViaUpstreamUDP(ctx context.Context) (*udpUpstream, error) {
	upstream, target, err := s.connectUpstreamRaw(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeAll(upstream, []byte{socksVersion5, 0x01, 0x00}); err != nil {
		return nil, closeAfterError(upstream, err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(upstream, reply); err != nil {
		return nil, closeAfterError(upstream, err)
	}
	if reply[0] != socksVersion5 || reply[1] != 0x00 {
		return nil, closeAfterError(upstream, fmt.Errorf("upstream socks auth reply %v", reply))
	}
	if err := writeAll(upstream, buildSocks5UDPAssociateRequest("0.0.0.0", 0)); err != nil {
		return nil, closeAfterError(upstream, err)
	}
	host, port, err := readSocks5ReplyEndpoint(upstream)
	if err != nil {
		return nil, closeAfterError(upstream, err)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		upstreamHost, _, splitErr := net.SplitHostPort(target)
		if splitErr != nil {
			return nil, closeAfterError(upstream, splitErr)
		}
		host = trimHostBrackets(upstreamHost)
	}
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return nil, closeAfterError(upstream, err)
	}
	return &udpUpstream{tcp: upstream, relayAddr: relayAddr, label: target}, nil
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
