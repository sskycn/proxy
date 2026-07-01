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

const trojanMaxUDPPayload = 8192

type protocolUDPFrame struct {
	host    string
	port    uint16
	payload []byte
}

type protocolUDPUpstream struct {
	tcp          net.Conn
	reader       *bufio.Reader
	label        string
	protocol     string
	host         string
	port         uint16
	vmessSession *vmessSession
	vmessReader  *vmessPacketReader
	vmessWriter  *vmessPacketWriter
	writeMu      sync.Mutex
}

func (s *proxyServer) handleProtocolTunnelUDP(ctx context.Context, conn net.Conn, reader *bufio.Reader, req protocolTunnelRequest) error {
	if req.host == "" || req.port == 0 {
		return errProtocolInvalidAddress
	}
	if s.cfg.TunnelProtocol == tunnelProtocolVLESS {
		if err := s.validateVLESSFlow(req); err != nil {
			return err
		}
	}
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return err
	}
	defer closeUDPWithLog(udpConn, s.log, "protocol tunnel udp target")
	if err := s.writeProtocolUDPResponseHeader(conn, req); err != nil {
		return err
	}

	done := make(chan error, 2)
	var writeMu sync.Mutex
	go s.protocolUDPClientToRemote(ctx, reader, req, udpConn, done)
	go s.protocolUDPRemoteToClient(ctx, conn, req, udpConn, &writeMu, done)

	if err := <-done; err != nil && !isExpectedNetworkClose(err) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (s *proxyServer) protocolUDPClientToRemote(ctx context.Context, reader *bufio.Reader, req protocolTunnelRequest, udpConn *net.UDPConn, done chan<- error) {
	vmessReader, err := newServerVMessUDPReader(reader, req)
	if err != nil {
		done <- err
		return
	}
	for {
		frame, err := s.readProtocolUDPFrame(reader, vmessReader, req)
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
					if logErr := logf(s.log, "drop %s udp %s: %v\n", s.cfg.TunnelProtocol, targetText, err); logErr != nil {
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
		if s.cfg.Verbose {
			if err := logf(s.log, "%s udp %s\n", s.cfg.TunnelProtocol, targetText); err != nil {
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

func (s *proxyServer) protocolUDPRemoteToClient(ctx context.Context, conn net.Conn, req protocolTunnelRequest, udpConn *net.UDPConn, writeMu *sync.Mutex, done chan<- error) {
	vmessWriter, err := newServerVMessUDPWriter(conn, req)
	if err != nil {
		done <- err
		return
	}
	buf := make([]byte, udpBufferSize)
	for {
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			done <- err
			return
		}
		writeMu.Lock()
		err = s.writeProtocolUDPFrame(conn, vmessWriter, req, protocolUDPFrame{
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

func (s *proxyServer) connectViaProtocolTunnelUDP(ctx context.Context, host string, port uint16) (*protocolUDPUpstream, error) {
	target := s.cfg.ServerAddr
	conn, err := s.dialTunnelTransport(ctx)
	if err != nil {
		return nil, err
	}
	if err := tuneTCP(conn); err != nil {
		return nil, closeAfterError(conn, err)
	}
	reader := bufio.NewReader(conn)
	var vmessSession *vmessSession
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		if err := writeVLESSRequest(conn, s.cfg.Token, s.cfg.TunnelFlow, protocolCmdUDP, host, port); err != nil {
			return nil, closeAfterError(conn, err)
		}
		if err := readVLESSResponse(reader); err != nil {
			return nil, closeAfterError(conn, err)
		}
	case tunnelProtocolTrojan:
		if err := writeTrojanRequest(conn, s.cfg.Token, protocolCmdUDP, host, port); err != nil {
			return nil, closeAfterError(conn, err)
		}
	case tunnelProtocolVMess:
		session, err := writeVMessUDPRequest(conn, s.cfg.Token, host, port)
		if err != nil {
			return nil, closeAfterError(conn, err)
		}
		vmessSession = &session
		if err := readVMessResponseHeader(reader, session); err != nil {
			return nil, closeAfterError(conn, err)
		}
	default:
		return nil, closeAfterError(conn, fmt.Errorf("UDP tunnel is unsupported for %s protocol", s.cfg.TunnelProtocol))
	}
	upstream := &protocolUDPUpstream{
		tcp:          conn,
		reader:       reader,
		label:        target,
		protocol:     s.cfg.TunnelProtocol,
		host:         host,
		port:         port,
		vmessSession: vmessSession,
	}
	if vmessSession != nil {
		upstream.vmessReader = newVMessPacketReader(reader, vmessResponseIV(*vmessSession), vmessSession.options&vmessOptionChunkMasking != 0)
		upstream.vmessWriter = newVMessPacketWriter(conn, vmessSession.requestBodyIV[:], vmessSession.options&vmessOptionChunkMasking != 0)
	}
	return upstream, nil
}

func (s *proxyServer) writeProtocolUDPResponseHeader(w io.Writer, req protocolTunnelRequest) error {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return writeVLESSResponse(w)
	case tunnelProtocolTrojan:
		return nil
	case tunnelProtocolVMess:
		if req.vmessSession == nil {
			return errProtocolInvalidResponse
		}
		return writeVMessResponseHeader(w, *req.vmessSession)
	default:
		return fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (s *proxyServer) readProtocolUDPFrame(reader *bufio.Reader, vmessReader *vmessPacketReader, req protocolTunnelRequest) (protocolUDPFrame, error) {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		payload, err := readLengthUDPFrame(reader)
		if err != nil {
			return protocolUDPFrame{}, err
		}
		return protocolUDPFrame{host: req.host, port: req.port, payload: payload}, nil
	case tunnelProtocolTrojan:
		return readTrojanUDPFrame(reader)
	case tunnelProtocolVMess:
		if vmessReader == nil {
			return protocolUDPFrame{}, errProtocolInvalidResponse
		}
		payload, err := vmessReader.ReadPacket()
		if err != nil {
			return protocolUDPFrame{}, err
		}
		return protocolUDPFrame{host: req.host, port: req.port, payload: payload}, nil
	default:
		return protocolUDPFrame{}, fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (s *proxyServer) writeProtocolUDPFrame(w io.Writer, vmessWriter *vmessPacketWriter, req protocolTunnelRequest, frame protocolUDPFrame) error {
	switch s.cfg.TunnelProtocol {
	case tunnelProtocolVLESS:
		return writeLengthUDPFrame(w, frame.payload)
	case tunnelProtocolTrojan:
		return writeTrojanUDPFrame(w, frame)
	case tunnelProtocolVMess:
		if vmessWriter == nil {
			return errProtocolInvalidResponse
		}
		return vmessWriter.WritePacket(frame.payload)
	default:
		return fmt.Errorf("unsupported tunnel protocol %q", s.cfg.TunnelProtocol)
	}
}

func (u *protocolUDPUpstream) writeFrame(frame protocolUDPFrame) error {
	switch u.protocol {
	case tunnelProtocolVLESS:
		return writeLengthUDPFrame(u.tcp, frame.payload)
	case tunnelProtocolTrojan:
		return writeTrojanUDPFrame(u.tcp, frame)
	case tunnelProtocolVMess:
		if u.vmessWriter == nil {
			return errProtocolInvalidResponse
		}
		return u.vmessWriter.WritePacket(frame.payload)
	default:
		return fmt.Errorf("unsupported tunnel protocol %q", u.protocol)
	}
}

func (u *protocolUDPUpstream) readFrame() (protocolUDPFrame, error) {
	switch u.protocol {
	case tunnelProtocolVLESS:
		payload, err := readLengthUDPFrame(u.reader)
		if err != nil {
			return protocolUDPFrame{}, err
		}
		return protocolUDPFrame{host: u.host, port: u.port, payload: payload}, nil
	case tunnelProtocolTrojan:
		return readTrojanUDPFrame(u.reader)
	case tunnelProtocolVMess:
		if u.vmessReader == nil {
			return protocolUDPFrame{}, errProtocolInvalidResponse
		}
		payload, err := u.vmessReader.ReadPacket()
		if err != nil {
			return protocolUDPFrame{}, err
		}
		return protocolUDPFrame{host: u.host, port: u.port, payload: payload}, nil
	default:
		return protocolUDPFrame{}, fmt.Errorf("unsupported tunnel protocol %q", u.protocol)
	}
}

func newServerVMessUDPReader(reader io.Reader, req protocolTunnelRequest) (*vmessPacketReader, error) {
	if req.vmessSession == nil {
		return nil, nil
	}
	return newVMessPacketReader(reader, req.vmessSession.requestBodyIV[:], req.vmessSession.options&vmessOptionChunkMasking != 0), nil
}

func newServerVMessUDPWriter(writer io.Writer, req protocolTunnelRequest) (*vmessPacketWriter, error) {
	if req.vmessSession == nil {
		return nil, nil
	}
	return newVMessPacketWriter(writer, vmessResponseIV(*req.vmessSession), req.vmessSession.options&vmessOptionChunkMasking != 0), nil
}

func vmessResponseIV(session vmessSession) []byte {
	_, iv := vmessResponseKeyIV(session)
	return iv[:]
}

func writeLengthUDPFrame(w io.Writer, payload []byte) error {
	if len(payload) > 0xffff {
		return errTunnelInvalidLength
	}
	header := []byte{byte(len(payload) >> 8), byte(len(payload))}
	return writeBuffers(w, header, payload)
}

func readLengthUDPFrame(reader io.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(header))
	if size > tunnelMaxUDPPayload {
		return nil, errTunnelInvalidLength
	}
	payload := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func writeTrojanUDPFrame(w io.Writer, frame protocolUDPFrame) error {
	if len(frame.payload) > trojanMaxUDPPayload {
		return errTunnelInvalidLength
	}
	header := make([]byte, 0, 1+len(frame.host)+6)
	var err error
	header, err = appendSocksAddress(header, frame.host, frame.port)
	if err != nil {
		return err
	}
	header = appendUint16(header, uint16(len(frame.payload)))
	header = append(header, '\r', '\n')
	return writeBuffers(w, header, frame.payload)
}

func readTrojanUDPFrame(reader io.Reader) (protocolUDPFrame, error) {
	host, port, err := readSocksAddress(reader)
	if err != nil {
		return protocolUDPFrame{}, err
	}
	lengthBuf := make([]byte, 2)
	if _, err := io.ReadFull(reader, lengthBuf); err != nil {
		return protocolUDPFrame{}, err
	}
	payloadLen := int(binary.BigEndian.Uint16(lengthBuf))
	if payloadLen > trojanMaxUDPPayload {
		return protocolUDPFrame{}, errTunnelInvalidLength
	}
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(reader, crlf); err != nil {
		return protocolUDPFrame{}, err
	}
	if crlf[0] != '\r' || crlf[1] != '\n' {
		return protocolUDPFrame{}, errProtocolUnauthorized
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return protocolUDPFrame{}, err
		}
	}
	return protocolUDPFrame{host: host, port: port, payload: payload}, nil
}
