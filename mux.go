package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	muxVersion        = byte(0x01)
	muxFrameData      = byte(0x01)
	muxFrameOpen      = byte(0x02)
	muxFrameClose     = byte(0x03)
	muxFrameReset     = byte(0x04)
	muxFrameHeaderLen = 10
	muxMaxPayload     = 32 * 1024
	muxReadQueueSize  = 128
)

var (
	errMuxClosed        = errors.New("mux session closed")
	errMuxBadVersion    = errors.New("invalid mux version")
	errMuxBadFrameType  = errors.New("invalid mux frame type")
	errMuxPayloadTooBig = errors.New("mux payload too large")
)

type muxReadItem struct {
	data []byte
}

type muxSession struct {
	conn       net.Conn
	reader     io.Reader
	writeMu    sync.Mutex
	mu         sync.Mutex
	streams    map[uint32]*muxStream
	acceptCh   chan *muxStream
	done       chan struct{}
	err        error
	nextID     uint32
	closeOnce  sync.Once
	localAddr  net.Addr
	remoteAddr net.Addr
}

type muxStream struct {
	session    *muxSession
	id         uint32
	readCh     chan muxReadItem
	readMu     sync.Mutex
	readBuf    []byte
	readErr    error
	readClosed bool
	closeOnce  sync.Once
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newMuxSession(conn net.Conn, reader io.Reader, client bool) *muxSession {
	nextID := uint32(2)
	if client {
		nextID = 1
	}
	session := &muxSession{
		conn:       conn,
		reader:     reader,
		streams:    make(map[uint32]*muxStream),
		acceptCh:   make(chan *muxStream, 64),
		done:       make(chan struct{}),
		nextID:     nextID,
		localAddr:  conn.LocalAddr(),
		remoteAddr: conn.RemoteAddr(),
	}
	go session.readLoop()
	return session
}

func (s *muxSession) openStream(ctx context.Context) (*muxStream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	stream, err := s.registerLocalStream()
	if err != nil {
		return nil, err
	}
	if err := s.writeFrame(muxFrameOpen, stream.id, nil); err != nil {
		s.removeStream(stream.id)
		stream.closeRead(err)
		return nil, err
	}
	return stream, nil
}

func (s *muxSession) registerLocalStream() (*muxStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	select {
	case <-s.done:
		return nil, errMuxClosed
	default:
	}
	id := s.nextID
	s.nextID += 2
	stream := newMuxStream(s, id)
	s.streams[id] = stream
	return stream, nil
}

func newMuxStream(session *muxSession, id uint32) *muxStream {
	return &muxStream{
		session: session,
		id:      id,
		readCh:  make(chan muxReadItem, muxReadQueueSize),
		localAddr: addrString{
			network: "mux",
			address: session.localAddr.String(),
		},
		remoteAddr: addrString{
			network: "mux",
			address: session.remoteAddr.String(),
		},
	}
}

func (s *muxSession) accept(ctx context.Context) (*muxStream, error) {
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

func (s *muxSession) errValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return errMuxClosed
}

func (s *muxSession) readLoop() {
	for {
		frameType, streamID, payload, err := readMuxFrame(s.reader)
		if err != nil {
			s.closeWithError(err)
			return
		}
		if err := s.handleFrame(frameType, streamID, payload); err != nil {
			s.closeWithError(err)
			return
		}
	}
}

func (s *muxSession) handleFrame(frameType byte, streamID uint32, payload []byte) error {
	switch frameType {
	case muxFrameOpen:
		if len(payload) != 0 {
			return errMuxPayloadTooBig
		}
		return s.acceptRemoteStream(streamID)
	case muxFrameData:
		stream := s.stream(streamID)
		if stream == nil {
			return nil
		}
		return stream.deliver(payload)
	case muxFrameClose:
		stream := s.stream(streamID)
		if stream != nil {
			stream.closeRead(io.EOF)
			s.removeStream(streamID)
		}
		return nil
	case muxFrameReset:
		stream := s.stream(streamID)
		if stream != nil {
			stream.closeRead(errMuxClosed)
			s.removeStream(streamID)
		}
		return nil
	default:
		return errMuxBadFrameType
	}
}

func (s *muxSession) acceptRemoteStream(streamID uint32) error {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return s.err
	}
	if _, exists := s.streams[streamID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("duplicate mux stream id %d", streamID)
	}
	stream := newMuxStream(s, streamID)
	s.streams[streamID] = stream
	s.mu.Unlock()

	select {
	case s.acceptCh <- stream:
		return nil
	case <-s.done:
		stream.closeRead(errMuxClosed)
		return errMuxClosed
	}
}

func (s *muxSession) stream(streamID uint32) *muxStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[streamID]
}

func (s *muxSession) removeStream(streamID uint32) {
	s.mu.Lock()
	delete(s.streams, streamID)
	s.mu.Unlock()
}

func (s *muxSession) writeFrame(frameType byte, streamID uint32, payload []byte) error {
	if len(payload) > muxMaxPayload {
		return errMuxPayloadTooBig
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	header := make([]byte, muxFrameHeaderLen)
	header[0] = muxVersion
	header[1] = frameType
	binary.BigEndian.PutUint32(header[2:6], streamID)
	binary.BigEndian.PutUint32(header[6:10], uint32(len(payload)))
	if err := writeAll(s.conn, header); err != nil {
		s.closeWithError(err)
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	if err := writeAll(s.conn, payload); err != nil {
		s.closeWithError(err)
		return err
	}
	return nil
}

func readMuxFrame(reader io.Reader) (byte, uint32, []byte, error) {
	header := make([]byte, muxFrameHeaderLen)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, 0, nil, err
	}
	if header[0] != muxVersion {
		return 0, 0, nil, errMuxBadVersion
	}
	payloadLen := int(binary.BigEndian.Uint32(header[6:10]))
	if payloadLen > muxMaxPayload {
		return 0, 0, nil, errMuxPayloadTooBig
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return 0, 0, nil, err
		}
	}
	return header[1], binary.BigEndian.Uint32(header[2:6]), payload, nil
}

func (s *muxSession) closeWithError(cause error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if cause == nil {
			cause = errMuxClosed
		}
		s.err = cause
		streams := make([]*muxStream, 0, len(s.streams))
		for _, stream := range s.streams {
			streams = append(streams, stream)
		}
		s.streams = make(map[uint32]*muxStream)
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

func (s *muxSession) Close() error {
	s.closeWithError(errMuxClosed)
	return s.errValue()
}

func (m *muxStream) Read(p []byte) (int, error) {
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
		m.readBuf = item.data
	}
	n := copy(p, m.readBuf)
	m.readBuf = m.readBuf[n:]
	return n, nil
}

func (m *muxStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(p) {
		end := written + muxMaxPayload
		if end > len(p) {
			end = len(p)
		}
		if err := m.session.writeFrame(muxFrameData, m.id, p[written:end]); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

func (m *muxStream) Close() error {
	var err error
	m.closeOnce.Do(func() {
		err = m.session.writeFrame(muxFrameClose, m.id, nil)
		m.session.removeStream(m.id)
		m.closeRead(io.ErrClosedPipe)
	})
	return err
}

func (m *muxStream) LocalAddr() net.Addr {
	return m.localAddr
}

func (m *muxStream) RemoteAddr() net.Addr {
	return m.remoteAddr
}

func (m *muxStream) SetDeadline(time.Time) error {
	return nil
}

func (m *muxStream) SetReadDeadline(time.Time) error {
	return nil
}

func (m *muxStream) SetWriteDeadline(time.Time) error {
	return nil
}

func (m *muxStream) deliver(data []byte) error {
	m.readMu.Lock()
	if m.readClosed {
		m.readMu.Unlock()
		return errMuxClosed
	}
	select {
	case m.readCh <- muxReadItem{data: data}:
		m.readMu.Unlock()
		return nil
	case <-m.session.done:
		m.readMu.Unlock()
		return errMuxClosed
	}
}

func (m *muxStream) closeRead(cause error) {
	m.readMu.Lock()
	defer m.readMu.Unlock()
	if m.readClosed {
		return
	}
	m.readErr = cause
	m.readClosed = true
	close(m.readCh)
}
