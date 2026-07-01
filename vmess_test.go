package tcptun

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestVMessAEADRequestAndResponse(t *testing.T) {
	token := "33333333-3333-4333-8333-333333333333"
	var wire bytes.Buffer
	session, err := writeVMessTCPRequest(&wire, token, "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	req, err := readVMessTCPRequest(bytes.NewReader(wire.Bytes()), token)
	if err != nil {
		t.Fatal(err)
	}
	if req.host != "example.com" || req.port != 443 {
		t.Fatalf("request = %s:%d, want example.com:443", req.host, req.port)
	}
	if req.vmessSession == nil {
		t.Fatal("missing VMess session")
	}
	if *req.vmessSession != session {
		t.Fatal("server session does not match client session")
	}

	var response bytes.Buffer
	if err := writeVMessResponseHeader(&response, *req.vmessSession); err != nil {
		t.Fatal(err)
	}
	serverSide, err := newVMessResponseConn(writeOnlyConn{Writer: &response}, *req.vmessSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serverSide.Write([]byte("OK")); err != nil {
		t.Fatal(err)
	}
	clientReader := bytes.NewReader(response.Bytes())
	clientSide := newVMessClientConn(readOnlyConn{Reader: clientReader}, session)
	reply := make([]byte, 2)
	if _, err := io.ReadFull(clientSide, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "OK" {
		t.Fatalf("reply = %q, want OK", reply)
	}
}

func TestProtocolUDPFrameCodecs(t *testing.T) {
	var vlessWire bytes.Buffer
	if err := writeLengthUDPFrame(&vlessWire, []byte("dns")); err != nil {
		t.Fatal(err)
	}
	vlessPayload, err := readLengthUDPFrame(&vlessWire)
	if err != nil {
		t.Fatal(err)
	}
	if string(vlessPayload) != "dns" {
		t.Fatalf("vless udp payload = %q, want dns", vlessPayload)
	}

	trojanFrame := protocolUDPFrame{host: "example.com", port: 53, payload: []byte("query")}
	var trojanWire bytes.Buffer
	if err := writeTrojanUDPFrame(&trojanWire, trojanFrame); err != nil {
		t.Fatal(err)
	}
	gotTrojanFrame, err := readTrojanUDPFrame(&trojanWire)
	if err != nil {
		t.Fatal(err)
	}
	if gotTrojanFrame.host != trojanFrame.host || gotTrojanFrame.port != trojanFrame.port || !bytes.Equal(gotTrojanFrame.payload, trojanFrame.payload) {
		t.Fatalf("trojan udp frame = %#v, want %#v", gotTrojanFrame, trojanFrame)
	}

	iv := []byte("1234567890abcdef")
	var vmessWire bytes.Buffer
	vmessWriter := newVMessPacketWriter(&vmessWire, iv, true)
	if err := vmessWriter.WritePacket([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := vmessWriter.WritePacket([]byte("two")); err != nil {
		t.Fatal(err)
	}
	vmessReader := newVMessPacketReader(&vmessWire, iv, true)
	for _, want := range []string{"one", "two"} {
		got, err := vmessReader.ReadPacket()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("vmess udp payload = %q, want %s", got, want)
		}
	}
}

type writeOnlyConn struct {
	io.Writer
}

func (writeOnlyConn) Read([]byte) (int, error)         { return 0, io.ErrClosedPipe }
func (writeOnlyConn) Close() error                     { return nil }
func (writeOnlyConn) LocalAddr() net.Addr              { return addrString{network: "test", address: "local"} }
func (writeOnlyConn) RemoteAddr() net.Addr             { return addrString{network: "test", address: "remote"} }
func (writeOnlyConn) SetDeadline(time.Time) error      { return nil }
func (writeOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (writeOnlyConn) SetWriteDeadline(time.Time) error { return nil }

type readOnlyConn struct {
	io.Reader
}

func (readOnlyConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (readOnlyConn) Close() error                     { return nil }
func (readOnlyConn) LocalAddr() net.Addr              { return addrString{network: "test", address: "local"} }
func (readOnlyConn) RemoteAddr() net.Addr             { return addrString{network: "test", address: "remote"} }
func (readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (readOnlyConn) SetWriteDeadline(time.Time) error { return nil }
