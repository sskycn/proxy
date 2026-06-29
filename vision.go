package proxy

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

const (
	visionCommandContinue = byte(0x00)
	visionCommandEnd      = byte(0x01)
	visionCommandDirect   = byte(0x02)

	visionFrameHeaderSize = 5
	visionMaxFrameSize    = 8192
	visionMaxContentSize  = visionMaxFrameSize - 21
)

var errVisionInvalidFrame = errors.New("invalid vision frame")

type visionConn struct {
	net.Conn

	userUUID []byte

	readHeader     func(io.Reader) error
	readHeaderDone bool
	readHeaderErr  error
	readFrom       io.Reader

	readMu           sync.Mutex
	readDecoded      []byte
	readRaw          []byte
	readScratch      []byte
	readDirect       bool
	readDirectConn   net.Conn
	readCommandBytes int
	readContentLeft  int
	readPaddingLeft  int
	readCurrentCmd   byte
	readInitialized  bool

	writeMu       sync.Mutex
	writeUserUUID []byte
}

func newVisionConn(conn net.Conn, userUUID [16]byte, readHeader func(io.Reader) error) *visionConn {
	return newVisionConnWithReader(conn, userUUID, readHeader, conn)
}

func newVisionConnWithReader(conn net.Conn, userUUID [16]byte, readHeader func(io.Reader) error, readFrom io.Reader) *visionConn {
	if readFrom == nil {
		readFrom = conn
	}
	uuid := make([]byte, len(userUUID))
	copy(uuid, userUUID[:])
	writeUUID := make([]byte, len(userUUID))
	copy(writeUUID, userUUID[:])
	return &visionConn{
		Conn:          conn,
		userUUID:      uuid,
		readHeader:    readHeader,
		readFrom:      readFrom,
		readScratch:   make([]byte, 32*1024),
		writeUserUUID: writeUUID,
	}
}

func (c *visionConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if err := c.ensureReadHeader(); err != nil {
		return 0, err
	}
	for {
		if len(c.readDecoded) > 0 {
			n := copy(p, c.readDecoded)
			c.readDecoded = c.readDecoded[n:]
			return n, nil
		}
		reader := c.readFrom
		if c.readDirectConn != nil {
			reader = c.readDirectConn
		}
		n, err := reader.Read(c.readScratch)
		if n > 0 {
			if c.readDirect {
				c.readDecoded = append(c.readDecoded, c.readScratch[:n]...)
			} else if decodeErr := c.decodeVisionBytes(c.readScratch[:n]); decodeErr != nil {
				if len(c.readDecoded) > 0 {
					nn := copy(p, c.readDecoded)
					c.readDecoded = c.readDecoded[nn:]
					return nn, nil
				}
				return 0, decodeErr
			}
			if len(c.readDecoded) > 0 {
				nn := copy(p, c.readDecoded)
				c.readDecoded = c.readDecoded[nn:]
				return nn, nil
			}
		}
		if err != nil {
			if len(c.readRaw) > 0 && !c.readDirect {
				return 0, errors.Join(err, io.ErrUnexpectedEOF)
			}
			return 0, err
		}
	}
}

func (c *visionConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	written := 0
	for written < len(p) {
		end := written + visionMaxContentSize
		if end > len(p) {
			end = len(p)
		}
		if err := c.writeVisionFrameLocked(p[written:end], visionCommandContinue, false); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

func (c *visionConn) WriteInitialPadding() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeVisionFrameLocked(nil, visionCommandContinue, true)
}

func (c *visionConn) ensureReadHeader() error {
	if c.readHeaderDone {
		return c.readHeaderErr
	}
	c.readHeaderDone = true
	if c.readHeader == nil {
		return nil
	}
	c.readHeaderErr = c.readHeader(c.Conn)
	return c.readHeaderErr
}

func (c *visionConn) decodeVisionBytes(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	c.readRaw = append(c.readRaw, raw...)
	for {
		if c.readDirect {
			c.readDecoded = append(c.readDecoded, c.readRaw...)
			c.readRaw = c.readRaw[:0]
			return nil
		}
		if !c.readInitialized {
			if len(c.readRaw) < len(c.userUUID)+visionFrameHeaderSize {
				return nil
			}
			if !equalBytes(c.readRaw[:len(c.userUUID)], c.userUUID) {
				c.readDirect = true
				continue
			}
			c.readRaw = c.readRaw[len(c.userUUID):]
			c.readCommandBytes = visionFrameHeaderSize
			c.readInitialized = true
		}
		if c.readCommandBytes > 0 {
			if len(c.readRaw) < c.readCommandBytes {
				return nil
			}
			header := c.readRaw[:visionFrameHeaderSize]
			c.readRaw = c.readRaw[visionFrameHeaderSize:]
			c.readCurrentCmd = header[0]
			c.readContentLeft = int(binary.BigEndian.Uint16(header[1:3]))
			c.readPaddingLeft = int(binary.BigEndian.Uint16(header[3:5]))
			if c.readContentLeft > visionMaxFrameSize || c.readPaddingLeft > visionMaxFrameSize {
				return errVisionInvalidFrame
			}
			c.readCommandBytes = 0
		}
		if c.readContentLeft > 0 {
			if len(c.readRaw) == 0 {
				return nil
			}
			n := c.readContentLeft
			if n > len(c.readRaw) {
				n = len(c.readRaw)
			}
			c.readDecoded = append(c.readDecoded, c.readRaw[:n]...)
			c.readRaw = c.readRaw[n:]
			c.readContentLeft -= n
			if c.readContentLeft > 0 {
				return nil
			}
		}
		if c.readPaddingLeft > 0 {
			if len(c.readRaw) == 0 {
				return nil
			}
			n := c.readPaddingLeft
			if n > len(c.readRaw) {
				n = len(c.readRaw)
			}
			c.readRaw = c.readRaw[n:]
			c.readPaddingLeft -= n
			if c.readPaddingLeft > 0 {
				return nil
			}
		}
		switch c.readCurrentCmd {
		case visionCommandContinue:
			c.readCommandBytes = visionFrameHeaderSize
		case visionCommandEnd:
			c.readDirect = true
		case visionCommandDirect:
			directConn, err := unwrapVisionDirectConn(c.Conn)
			if err != nil {
				return err
			}
			c.readDirect = true
			c.readDirectConn = directConn
		default:
			return fmt.Errorf("%w: unknown command %d", errVisionInvalidFrame, c.readCurrentCmd)
		}
		if len(c.readRaw) == 0 {
			return nil
		}
	}
}

func (c *visionConn) writeVisionFrameLocked(content []byte, command byte, longPadding bool) error {
	if len(content) > visionMaxContentSize {
		return errTunnelInvalidLength
	}
	paddingLen, err := visionPaddingLen(len(content), longPadding)
	if err != nil {
		return err
	}
	frameLen := visionFrameHeaderSize + len(content) + paddingLen
	if len(c.writeUserUUID) > 0 {
		frameLen += len(c.writeUserUUID)
	}
	frame := make([]byte, 0, frameLen)
	if len(c.writeUserUUID) > 0 {
		frame = append(frame, c.writeUserUUID...)
		c.writeUserUUID = nil
	}
	frame = append(frame, command, byte(len(content)>>8), byte(len(content)), byte(paddingLen>>8), byte(paddingLen))
	frame = append(frame, content...)
	if paddingLen > 0 {
		paddingStart := len(frame)
		frame = append(frame, make([]byte, paddingLen)...)
		if _, err := rand.Read(frame[paddingStart:]); err != nil {
			return fmt.Errorf("generate vision padding: %w", err)
		}
	}
	return writeAll(c.Conn, frame)
}

func visionPaddingLen(contentLen int, longPadding bool) (int, error) {
	var random [2]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, fmt.Errorf("generate vision padding length: %w", err)
	}
	value := int(binary.BigEndian.Uint16(random[:]))
	var paddingLen int
	if longPadding && contentLen < 900 {
		paddingLen = 900 - contentLen + value%500
	} else {
		paddingLen = value % 256
	}
	maxPadding := visionMaxFrameSize - 21 - contentLen
	if maxPadding < 0 {
		return 0, errTunnelInvalidLength
	}
	if paddingLen > maxPadding {
		paddingLen = maxPadding
	}
	return paddingLen, nil
}

type visionRawConner interface {
	NetConn() net.Conn
}

func unwrapVisionDirectConn(conn net.Conn) (net.Conn, error) {
	raw, ok := conn.(visionRawConner)
	if !ok {
		return nil, errors.New("vision direct mode requires access to raw connection")
	}
	direct := raw.NetConn()
	if direct == nil {
		return nil, errors.New("vision direct mode raw connection is nil")
	}
	return direct, nil
}

func equalBytes(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
