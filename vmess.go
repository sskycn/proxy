package tcptun

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

const (
	vmessVersion = byte(0x01)

	vmessCommandTCP = byte(0x01)
	vmessCommandUDP = byte(0x02)
	vmessCommandMux = byte(0x03)

	vmessAddrIPv4   = byte(0x01)
	vmessAddrDomain = byte(0x02)
	vmessAddrIPv6   = byte(0x03)

	vmessSecurityNone = byte(0x05)

	vmessOptionChunkStream         = byte(0x01)
	vmessOptionChunkMasking        = byte(0x04)
	vmessOptionGlobalPadding       = byte(0x08)
	vmessOptionAuthenticatedLength = byte(0x10)

	vmessAEADAuthIDEncryptionKey           = "AES Auth ID Encryption"
	vmessAEADRespHeaderLenKey              = "AEAD Resp Header Len Key"
	vmessAEADRespHeaderLenIV               = "AEAD Resp Header Len IV"
	vmessAEADRespHeaderPayloadKey          = "AEAD Resp Header Key"
	vmessAEADRespHeaderPayloadIV           = "AEAD Resp Header IV"
	vmessAEADKDF                           = "VMess AEAD KDF"
	vmessHeaderPayloadAEADKey              = "VMess Header AEAD Key"
	vmessHeaderPayloadAEADIV               = "VMess Header AEAD Nonce"
	vmessHeaderPayloadLengthAEADKey        = "VMess Header AEAD Key_Length"
	vmessHeaderPayloadLengthAEADIV         = "VMess Header AEAD Nonce_Length"
	vmessCmdKeySalt                        = "c48619fe-8f02-49e0-b9e9-edf763e17e21"
	vmessAEADMaxClockSkewSeconds    int64  = 120
	vmessAEADMaxHeaderPayloadLength uint16 = 4096
)

var (
	errVMessUnsupportedSecurity = errors.New("unsupported VMess security")
	errVMessUnsupportedOption   = errors.New("unsupported VMess option")
	errVMessInvalidChecksum     = errors.New("invalid VMess checksum")
)

type vmessSession struct {
	requestBodyKey [16]byte
	requestBodyIV  [16]byte
	responseHeader byte
	options        byte
}

type vmessResponseConn struct {
	net.Conn
	chunkWriter *vmessChunkWriter
}

func newVMessResponseConn(conn net.Conn, session vmessSession) (net.Conn, error) {
	_, iv := vmessResponseKeyIV(session)
	responseConn := &vmessResponseConn{
		Conn: conn,
	}
	if session.options&vmessOptionChunkStream != 0 {
		responseConn.chunkWriter = newVMessChunkWriter(responseConn, iv[:], session.options&vmessOptionChunkMasking != 0)
	}
	return responseConn, nil
}

func (c *vmessResponseConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.chunkWriter != nil {
		return c.chunkWriter.Write(p)
	}
	return c.Conn.Write(p)
}

func (c *vmessResponseConn) Close() error {
	var writeErr error
	if c.chunkWriter != nil {
		writeErr = c.chunkWriter.WriteFinal()
	}
	return errors.Join(writeErr, c.Conn.Close())
}

type vmessClientConn struct {
	net.Conn
	chunkReader io.Reader
	session     vmessSession
	headerRead  bool
}

func newVMessClientConn(conn net.Conn, session vmessSession) net.Conn {
	return &vmessClientConn{
		Conn:    conn,
		session: session,
	}
}

func (c *vmessClientConn) Read(p []byte) (int, error) {
	if !c.headerRead {
		if err := readVMessResponseHeader(c.Conn, c.session); err != nil {
			return 0, err
		}
		c.headerRead = true
	}
	if c.chunkReader == nil && c.session.options&vmessOptionChunkStream != 0 {
		c.chunkReader = newVMessResponseChunkReader(c.Conn, c.session)
	}
	if c.chunkReader != nil {
		return c.chunkReader.Read(p)
	}
	return c.Conn.Read(p)
}

func newVMessResponseChunkReader(reader io.Reader, session vmessSession) io.Reader {
	_, iv := vmessResponseKeyIV(session)
	return newVMessChunkReader(reader, iv[:], session.options&vmessOptionChunkMasking != 0)
}

func writeVMessTCPRequest(w io.Writer, token string, host string, port uint16) (vmessSession, error) {
	return writeVMessRequest(w, token, vmessCommandTCP, host, port)
}

func writeVMessUDPRequest(w io.Writer, token string, host string, port uint16) (vmessSession, error) {
	return writeVMessRequest(w, token, vmessCommandUDP, host, port)
}

func writeVMessRequest(w io.Writer, token string, cmd byte, host string, port uint16) (vmessSession, error) {
	userID, err := parseUUIDToken(token)
	if err != nil {
		return vmessSession{}, err
	}
	var session vmessSession
	randomBytes := [33]byte{}
	if _, err := io.ReadFull(rand.Reader, randomBytes[:]); err != nil {
		return vmessSession{}, err
	}
	copy(session.requestBodyKey[:], randomBytes[:16])
	copy(session.requestBodyIV[:], randomBytes[16:32])
	session.responseHeader = randomBytes[32]

	payload, err := buildVMessRequestPayload(session, cmd, host, port)
	if err != nil {
		return vmessSession{}, err
	}
	cmdKey := vmessCmdKey(userID)
	sealed, err := sealVMessAEADHeader(cmdKey, payload)
	if err != nil {
		return vmessSession{}, err
	}
	if err := writeAll(w, sealed); err != nil {
		return vmessSession{}, err
	}
	return session, nil
}

func readVMessRequest(reader io.Reader, expectedToken string) (protocolTunnelRequest, error) {
	userID, err := parseUUIDToken(expectedToken)
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	cmdKey := vmessCmdKey(userID)
	authID := [16]byte{}
	if _, err := io.ReadFull(reader, authID[:]); err != nil {
		return protocolTunnelRequest{}, err
	}
	if err := verifyVMessAuthID(cmdKey, authID); err != nil {
		return protocolTunnelRequest{}, err
	}
	payload, err := openVMessAEADHeader(cmdKey, authID, reader)
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	return parseVMessRequestPayload(payload)
}

func readVMessTCPRequest(reader io.Reader, expectedToken string) (protocolTunnelRequest, error) {
	req, err := readVMessRequest(reader, expectedToken)
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	if req.cmd != protocolCmdTCP {
		return protocolTunnelRequest{}, errProtocolUnsupported
	}
	return req, nil
}

func buildVMessRequestPayload(session vmessSession, cmd byte, host string, port uint16) ([]byte, error) {
	payload := make([]byte, 0, 48+len(host))
	payload = append(payload, vmessVersion)
	payload = append(payload, session.requestBodyIV[:]...)
	payload = append(payload, session.requestBodyKey[:]...)
	payload = append(payload, session.responseHeader)
	payload = append(payload, session.options)
	payload = append(payload, vmessSecurityNone)
	payload = append(payload, 0x00)
	payload = append(payload, cmd)
	payload = appendUint16(payload, port)
	var appendErr error
	payload, appendErr = appendVMessAddress(payload, host)
	if appendErr != nil {
		return nil, appendErr
	}
	checksum := fnv32a(payload)
	payload = append(payload, byte(checksum>>24), byte(checksum>>16), byte(checksum>>8), byte(checksum))
	return payload, nil
}

func parseVMessRequestPayload(payload []byte) (protocolTunnelRequest, error) {
	if len(payload) < 42 {
		return protocolTunnelRequest{}, io.ErrUnexpectedEOF
	}
	checksumStart := len(payload) - 4
	actual := fnv32a(payload[:checksumStart])
	expected := binary.BigEndian.Uint32(payload[checksumStart:])
	if actual != expected {
		return protocolTunnelRequest{}, errVMessInvalidChecksum
	}
	if payload[0] != vmessVersion {
		return protocolTunnelRequest{}, errTunnelBadVersion
	}
	var session vmessSession
	copy(session.requestBodyIV[:], payload[1:17])
	copy(session.requestBodyKey[:], payload[17:33])
	session.responseHeader = payload[33]
	options := payload[34]
	session.options = options
	security := payload[35] & 0x0f
	paddingLen := int(payload[35] >> 4)
	command := payload[37]
	if options&(vmessOptionGlobalPadding|vmessOptionAuthenticatedLength) != 0 {
		return protocolTunnelRequest{}, errVMessUnsupportedOption
	}
	if options&^(vmessOptionChunkStream|vmessOptionChunkMasking) != 0 {
		return protocolTunnelRequest{}, errVMessUnsupportedOption
	}
	if security != vmessSecurityNone {
		return protocolTunnelRequest{}, errVMessUnsupportedSecurity
	}
	if command != vmessCommandTCP && command != vmessCommandUDP && command != vmessCommandMux {
		return protocolTunnelRequest{}, errProtocolUnsupported
	}
	if command == vmessCommandMux {
		if paddingLen != checksumStart-38 {
			return protocolTunnelRequest{}, errTunnelInvalidLength
		}
		return protocolTunnelRequest{
			cmd:          protocolCmdTCP,
			host:         xrayMuxCoolHost,
			port:         xrayMuxCoolPort,
			vmessSession: &session,
		}, nil
	}
	host, port, consumed, err := readVMessAddressFromPayload(payload[38:checksumStart])
	if err != nil {
		return protocolTunnelRequest{}, err
	}
	if consumed+paddingLen != checksumStart-38 {
		return protocolTunnelRequest{}, errTunnelInvalidLength
	}
	return protocolTunnelRequest{
		cmd:          vmessProtocolCommand(command),
		host:         host,
		port:         port,
		vmessSession: &session,
	}, nil
}

func newVMessRequestReader(reader io.Reader, session vmessSession) io.Reader {
	if session.options&vmessOptionChunkStream == 0 {
		return reader
	}
	return newVMessChunkReader(reader, session.requestBodyIV[:], session.options&vmessOptionChunkMasking != 0)
}

func vmessProtocolCommand(command byte) byte {
	if command == vmessCommandUDP {
		return protocolCmdUDP
	}
	return protocolCmdTCP
}

func writeVMessResponseHeader(w io.Writer, session vmessSession) error {
	key, iv := vmessResponseKeyIV(session)
	header := []byte{session.responseHeader, 0x00, 0x00, 0x00}

	lengthAEAD, err := newAesGCM(vmessKDF16(key[:], vmessAEADRespHeaderLenKey))
	if err != nil {
		return err
	}
	lengthNonce := vmessKDF(iv[:], vmessAEADRespHeaderLenIV)[:12]
	lengthPlain := []byte{0x00, byte(len(header))}
	lengthCipher := lengthAEAD.Seal(nil, lengthNonce, lengthPlain, nil)

	payloadAEAD, err := newAesGCM(vmessKDF16(key[:], vmessAEADRespHeaderPayloadKey))
	if err != nil {
		return err
	}
	payloadNonce := vmessKDF(iv[:], vmessAEADRespHeaderPayloadIV)[:12]
	payloadCipher := payloadAEAD.Seal(nil, payloadNonce, header, nil)

	return writeBuffers(w, lengthCipher, payloadCipher)
}

func readVMessResponseHeader(reader io.Reader, session vmessSession) error {
	key, iv := vmessResponseKeyIV(session)
	lengthCipher := make([]byte, 18)
	if _, err := io.ReadFull(reader, lengthCipher); err != nil {
		return err
	}
	lengthAEAD, err := newAesGCM(vmessKDF16(key[:], vmessAEADRespHeaderLenKey))
	if err != nil {
		return err
	}
	lengthNonce := vmessKDF(iv[:], vmessAEADRespHeaderLenIV)[:12]
	lengthPlain, err := lengthAEAD.Open(nil, lengthNonce, lengthCipher, nil)
	if err != nil {
		return err
	}
	if len(lengthPlain) != 2 {
		return errProtocolInvalidResponse
	}
	payloadLen := int(binary.BigEndian.Uint16(lengthPlain))
	if payloadLen < 4 || payloadLen > int(vmessAEADMaxHeaderPayloadLength) {
		return errProtocolInvalidResponse
	}
	payloadCipher := make([]byte, payloadLen+16)
	if _, err := io.ReadFull(reader, payloadCipher); err != nil {
		return err
	}
	payloadAEAD, err := newAesGCM(vmessKDF16(key[:], vmessAEADRespHeaderPayloadKey))
	if err != nil {
		return err
	}
	payloadNonce := vmessKDF(iv[:], vmessAEADRespHeaderPayloadIV)[:12]
	payload, err := payloadAEAD.Open(nil, payloadNonce, payloadCipher, nil)
	if err != nil {
		return err
	}
	if len(payload) < 4 || payload[0] != session.responseHeader {
		return errProtocolInvalidResponse
	}
	return nil
}

func writeBuffers(w io.Writer, buffers ...[]byte) error {
	if len(buffers) == 0 {
		return nil
	}
	expected := int64(0)
	for _, buffer := range buffers {
		expected += int64(len(buffer))
	}
	if conn, ok := w.(net.Conn); ok {
		netBuffers := net.Buffers(buffers)
		written, err := netBuffers.WriteTo(conn)
		if err != nil {
			return err
		}
		if written != expected {
			return io.ErrShortWrite
		}
		return nil
	}
	for _, buffer := range buffers {
		if err := writeAll(w, buffer); err != nil {
			return err
		}
	}
	return nil
}

func sealVMessAEADHeader(key [16]byte, payload []byte) ([]byte, error) {
	if len(payload) > int(vmessAEADMaxHeaderPayloadLength) {
		return nil, errTunnelInvalidLength
	}
	authID, err := createVMessAuthID(key, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	lengthPlain := []byte{byte(len(payload) >> 8), byte(len(payload))}
	lengthKey := vmessKDF16(key[:], vmessHeaderPayloadLengthAEADKey, string(authID[:]), string(nonce))
	lengthNonce := vmessKDF(key[:], vmessHeaderPayloadLengthAEADIV, string(authID[:]), string(nonce))[:12]
	lengthAEAD, err := newAesGCM(lengthKey)
	if err != nil {
		return nil, err
	}
	lengthCipher := lengthAEAD.Seal(nil, lengthNonce, lengthPlain, authID[:])

	payloadKey := vmessKDF16(key[:], vmessHeaderPayloadAEADKey, string(authID[:]), string(nonce))
	payloadNonce := vmessKDF(key[:], vmessHeaderPayloadAEADIV, string(authID[:]), string(nonce))[:12]
	payloadAEAD, err := newAesGCM(payloadKey)
	if err != nil {
		return nil, err
	}
	payloadCipher := payloadAEAD.Seal(nil, payloadNonce, payload, authID[:])

	out := make([]byte, 0, 16+len(lengthCipher)+8+len(payloadCipher))
	out = append(out, authID[:]...)
	out = append(out, lengthCipher...)
	out = append(out, nonce...)
	out = append(out, payloadCipher...)
	return out, nil
}

func openVMessAEADHeader(key [16]byte, authID [16]byte, reader io.Reader) ([]byte, error) {
	lengthCipher := make([]byte, 18)
	if _, err := io.ReadFull(reader, lengthCipher); err != nil {
		return nil, err
	}
	nonce := make([]byte, 8)
	if _, err := io.ReadFull(reader, nonce); err != nil {
		return nil, err
	}
	lengthKey := vmessKDF16(key[:], vmessHeaderPayloadLengthAEADKey, string(authID[:]), string(nonce))
	lengthNonce := vmessKDF(key[:], vmessHeaderPayloadLengthAEADIV, string(authID[:]), string(nonce))[:12]
	lengthAEAD, err := newAesGCM(lengthKey)
	if err != nil {
		return nil, err
	}
	lengthPlain, err := lengthAEAD.Open(nil, lengthNonce, lengthCipher, authID[:])
	if err != nil {
		return nil, err
	}
	if len(lengthPlain) != 2 {
		return nil, errTunnelInvalidLength
	}
	payloadLen := binary.BigEndian.Uint16(lengthPlain)
	if payloadLen > vmessAEADMaxHeaderPayloadLength {
		return nil, errTunnelInvalidLength
	}
	payloadCipher := make([]byte, int(payloadLen)+16)
	if _, err := io.ReadFull(reader, payloadCipher); err != nil {
		return nil, err
	}
	payloadKey := vmessKDF16(key[:], vmessHeaderPayloadAEADKey, string(authID[:]), string(nonce))
	payloadNonce := vmessKDF(key[:], vmessHeaderPayloadAEADIV, string(authID[:]), string(nonce))[:12]
	payloadAEAD, err := newAesGCM(payloadKey)
	if err != nil {
		return nil, err
	}
	return payloadAEAD.Open(nil, payloadNonce, payloadCipher, authID[:])
}

func createVMessAuthID(cmdKey [16]byte, timestamp int64) ([16]byte, error) {
	buf := [16]byte{}
	binary.BigEndian.PutUint64(buf[:8], uint64(timestamp))
	if _, err := io.ReadFull(rand.Reader, buf[8:12]); err != nil {
		return [16]byte{}, err
	}
	crc := crc32.ChecksumIEEE(buf[:12])
	binary.BigEndian.PutUint32(buf[12:], crc)
	block, err := aes.NewCipher(vmessKDF16(cmdKey[:], vmessAEADAuthIDEncryptionKey))
	if err != nil {
		return [16]byte{}, err
	}
	var authID [16]byte
	block.Encrypt(authID[:], buf[:])
	return authID, nil
}

func verifyVMessAuthID(cmdKey [16]byte, authID [16]byte) error {
	block, err := aes.NewCipher(vmessKDF16(cmdKey[:], vmessAEADAuthIDEncryptionKey))
	if err != nil {
		return err
	}
	plain := [16]byte{}
	block.Decrypt(plain[:], authID[:])
	crc := binary.BigEndian.Uint32(plain[12:])
	if crc != crc32.ChecksumIEEE(plain[:12]) {
		return errProtocolUnauthorized
	}
	timestamp := int64(binary.BigEndian.Uint64(plain[:8]))
	if timestamp < 0 {
		return errProtocolUnauthorized
	}
	now := time.Now().Unix()
	if math.Abs(float64(timestamp-now)) > float64(vmessAEADMaxClockSkewSeconds) {
		return errProtocolUnauthorized
	}
	return nil
}

func vmessResponseKeyIV(session vmessSession) ([16]byte, [16]byte) {
	keyHash := sha256.Sum256(session.requestBodyKey[:])
	ivHash := sha256.Sum256(session.requestBodyIV[:])
	key := [16]byte{}
	iv := [16]byte{}
	copy(key[:], keyHash[:16])
	copy(iv[:], ivHash[:16])
	return key, iv
}

func vmessCmdKey(userID [16]byte) [16]byte {
	hash := md5.New()
	_, _ = hash.Write(userID[:])
	_, _ = hash.Write([]byte(vmessCmdKeySalt))
	key := [16]byte{}
	sum := hash.Sum(nil)
	copy(key[:], sum)
	return key
}

func vmessKDF(key []byte, path ...string) []byte {
	hmacf := hmac.New(sha256.New, []byte(vmessAEADKDF))
	for _, value := range path {
		previous := hmacf
		first := true
		hmacf = hmac.New(func() hash.Hash {
			if first {
				first = false
				return hashWrapper{Hash: previous}
			}
			return previous
		}, []byte(value))
	}
	if _, err := hmacf.Write(key); err != nil {
		panic(err)
	}
	return hmacf.Sum(nil)
}

func fnv32a(data []byte) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	hash := uint32(offset32)
	for _, b := range data {
		hash ^= uint32(b)
		hash *= prime32
	}
	return hash
}

func vmessKDF16(key []byte, path ...string) []byte {
	sum := vmessKDF(key, path...)
	return sum[:16]
}

type hashWrapper struct {
	hash.Hash
}

func newAesGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type vmessChunkReader struct {
	reader    io.Reader
	shake     sha3.ShakeHash
	remaining int
	sizeBuf   [2]byte
	maskBuf   [2]byte
}

func newVMessChunkReader(reader io.Reader, iv []byte, masked bool) *vmessChunkReader {
	var shake sha3.ShakeHash
	if masked {
		shake = sha3.NewShake128()
		if _, err := shake.Write(iv); err != nil {
			panic(err)
		}
	}
	return &vmessChunkReader{
		reader: reader,
		shake:  shake,
	}
}

func (r *vmessChunkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for r.remaining == 0 {
		size, err := r.readSize()
		if err != nil {
			return 0, err
		}
		if size == 0 {
			return 0, io.EOF
		}
		r.remaining = int(size)
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := io.ReadFull(r.reader, p)
	r.remaining -= n
	if err != nil {
		return n, err
	}
	return n, nil
}

func (r *vmessChunkReader) readSize() (uint16, error) {
	if _, err := io.ReadFull(r.reader, r.sizeBuf[:]); err != nil {
		return 0, err
	}
	size := binary.BigEndian.Uint16(r.sizeBuf[:])
	if r.shake == nil {
		return size, nil
	}
	if _, err := io.ReadFull(r.shake, r.maskBuf[:]); err != nil {
		return 0, err
	}
	return size ^ binary.BigEndian.Uint16(r.maskBuf[:]), nil
}

type vmessPacketReader struct {
	reader *vmessChunkReader
}

func newVMessPacketReader(reader io.Reader, iv []byte, masked bool) *vmessPacketReader {
	return &vmessPacketReader{reader: newVMessChunkReader(reader, iv, masked)}
}

func (r *vmessPacketReader) ReadPacket() ([]byte, error) {
	size, err := r.reader.readSize()
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, io.EOF
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r.reader.reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

type vmessPacketWriter struct {
	writer io.Writer
	shake  sha3.ShakeHash
	size   [2]byte
	mask   [2]byte
}

func newVMessPacketWriter(writer io.Writer, iv []byte, masked bool) *vmessPacketWriter {
	var shake sha3.ShakeHash
	if masked {
		shake = sha3.NewShake128()
		if _, err := shake.Write(iv); err != nil {
			panic(err)
		}
	}
	return &vmessPacketWriter{
		writer: writer,
		shake:  shake,
	}
}

func (w *vmessPacketWriter) WritePacket(payload []byte) error {
	if len(payload) > 0xffff {
		return errTunnelInvalidLength
	}
	size := uint16(len(payload))
	if w.shake != nil {
		if _, err := io.ReadFull(w.shake, w.mask[:]); err != nil {
			return err
		}
		size ^= binary.BigEndian.Uint16(w.mask[:])
	}
	binary.BigEndian.PutUint16(w.size[:], size)
	return writeBuffers(w.writer, w.size[:], payload)
}

type vmessChunkWriter struct {
	conn   *vmessResponseConn
	shake  sha3.ShakeHash
	closed bool
	size   [2]byte
	mask   [2]byte
}

func newVMessChunkWriter(conn *vmessResponseConn, iv []byte, masked bool) *vmessChunkWriter {
	var shake sha3.ShakeHash
	if masked {
		shake = sha3.NewShake128()
		if _, err := shake.Write(iv); err != nil {
			panic(err)
		}
	}
	return &vmessChunkWriter{
		conn:  conn,
		shake: shake,
	}
}

func (w *vmessChunkWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, net.ErrClosed
	}
	written := 0
	for len(p) > 0 {
		size := len(p)
		if size > 8192 {
			size = 8192
		}
		if err := w.writeChunk(p[:size]); err != nil {
			return written, err
		}
		written += size
		p = p[size:]
	}
	return written, nil
}

func (w *vmessChunkWriter) WriteFinal() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.writeSize(0)
}

func (w *vmessChunkWriter) writeChunk(p []byte) error {
	size, err := w.encodeSize(uint16(len(p)))
	if err != nil {
		return err
	}
	buffers := net.Buffers{size, p}
	written, err := buffers.WriteTo(w.conn.Conn)
	if err != nil {
		return err
	}
	if written != int64(len(size)+len(p)) {
		return io.ErrShortWrite
	}
	return nil
}

func (w *vmessChunkWriter) writeSize(size uint16) error {
	encoded, err := w.encodeSize(size)
	if err != nil {
		return err
	}
	n, err := w.conn.Conn.Write(encoded)
	if err != nil {
		return err
	}
	if n != len(encoded) {
		return io.ErrShortWrite
	}
	return nil
}

func (w *vmessChunkWriter) encodeSize(size uint16) ([]byte, error) {
	encoded := size
	if w.shake != nil {
		if _, err := io.ReadFull(w.shake, w.mask[:]); err != nil {
			return nil, err
		}
		encoded ^= binary.BigEndian.Uint16(w.mask[:])
	}
	binary.BigEndian.PutUint16(w.size[:], encoded)
	return w.size[:], nil
}

func appendVMessAddress(dst []byte, host string) ([]byte, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, errProtocolInvalidAddress
	}
	if addr, err := netip.ParseAddr(trimHostBrackets(trimmed)); err == nil {
		if addr.Is4() {
			ip4 := addr.As4()
			dst = append(dst, vmessAddrIPv4)
			return append(dst, ip4[:]...), nil
		}
		ip16 := addr.As16()
		dst = append(dst, vmessAddrIPv6)
		return append(dst, ip16[:]...), nil
	}
	if len(trimmed) > tunnelMaxHostLength {
		return nil, errTunnelInvalidLength
	}
	dst = append(dst, vmessAddrDomain, byte(len(trimmed)))
	return append(dst, trimmed...), nil
}

func readVMessAddressFromPayload(src []byte) (string, uint16, int, error) {
	if len(src) < 3 {
		return "", 0, 0, io.ErrUnexpectedEOF
	}
	port := binary.BigEndian.Uint16(src[:2])
	atyp := src[2]
	offset := 3
	switch atyp {
	case vmessAddrIPv4:
		if len(src) < offset+net.IPv4len {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		host := net.IP(src[offset : offset+net.IPv4len]).String()
		return host, port, offset + net.IPv4len, nil
	case vmessAddrIPv6:
		if len(src) < offset+net.IPv6len {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		host := net.IP(src[offset : offset+net.IPv6len]).String()
		return host, port, offset + net.IPv6len, nil
	case vmessAddrDomain:
		if len(src) < offset+1 {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		size := int(src[offset])
		offset++
		if size == 0 || len(src) < offset+size {
			return "", 0, 0, errTunnelInvalidLength
		}
		host := string(src[offset : offset+size])
		return host, port, offset + size, nil
	default:
		return "", 0, 0, errProtocolInvalidAddress
	}
}
