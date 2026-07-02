package tcptun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestXrayMuxSessionMultiplexesTCPStreams(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newXrayMuxSession(clientConn, clientConn, false)
	server := newXrayMuxSession(serverConn, serverConn, true)
	t.Cleanup(func() {
		if err := client.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
			t.Errorf("close client xray mux: %v", err)
		}
		if err := server.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
			t.Errorf("close server xray mux: %v", err)
		}
	})

	done := make(chan error, 2)
	go func() {
		for i := 0; i < 2; i++ {
			stream, err := server.accept(context.Background())
			if err != nil {
				done <- err
				return
			}
			go func(stream *xrayMuxStream) {
				defer func() {
					if err := stream.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
						done <- err
					}
				}()
				buf := make([]byte, 2)
				if _, err := io.ReadFull(stream, buf); err != nil {
					done <- err
					return
				}
				buf[0], buf[1] = buf[1], buf[0]
				if _, err := stream.Write(buf); err != nil {
					done <- err
					return
				}
				done <- nil
			}(stream)
		}
	}()

	first, err := client.openTCPStream(context.Background(), socksRequest{host: "example.com", port: 80})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.openTCPStream(context.Background(), socksRequest{host: "example.org", port: 443})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("ab")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := second.Write([]byte("xy")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	assertXrayMuxReply(t, first, "ba")
	assertXrayMuxReply(t, second, "yx")
	if err := first.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
		t.Fatalf("close first: %v", err)
	}
	if err := second.Close(); err != nil && !errors.Is(err, errXrayMuxClosed) && !isExpectedNetworkClose(err) {
		t.Fatalf("close second: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for xray mux echo")
		}
	}
}

func TestXrayMuxFrameRoundTripUDPMetadata(t *testing.T) {
	var buf bytes.Buffer
	want := xrayMuxFrame{
		sessionID: 7,
		status:    xrayMuxStatusKeep,
		option:    xrayMuxOptionData,
		target: xrayMuxTarget{
			network: xrayMuxNetworkUDP,
			host:    "1.1.1.1",
			port:    53,
		},
		payload: []byte("dns"),
	}
	if err := writeXrayMuxFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := readXrayMuxFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.sessionID != want.sessionID || got.status != want.status || got.option != want.option {
		t.Fatalf("metadata = id %d status %d option %d, want id %d status %d option %d", got.sessionID, got.status, got.option, want.sessionID, want.status, want.option)
	}
	if got.target != want.target {
		t.Fatalf("target = %#v, want %#v", got.target, want.target)
	}
	if string(got.payload) != string(want.payload) {
		t.Fatalf("payload = %q, want %q", got.payload, want.payload)
	}
}

func assertXrayMuxReply(t *testing.T, stream net.Conn, want string) {
	t.Helper()
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != want {
		t.Fatalf("xray mux reply = %q, want %q", buf, want)
	}
}
