package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestMuxSessionMultiplexesStreams(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newMuxSession(clientConn, clientConn, true)
	server := newMuxSession(serverConn, serverConn, false)
	defer func() {
		if err := client.Close(); err != nil && !errors.Is(err, errMuxClosed) && !isExpectedNetworkClose(err) {
			t.Errorf("close client mux: %v", err)
		}
		if err := server.Close(); err != nil && !errors.Is(err, errMuxClosed) && !isExpectedNetworkClose(err) {
			t.Errorf("close server mux: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	serverErr := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			stream, err := server.accept(ctx)
			if err != nil {
				serverErr <- err
				return
			}
			defer func() {
				if err := stream.Close(); err != nil && !errors.Is(err, errMuxClosed) {
					serverErr <- err
				}
			}()
			buf := make([]byte, 2)
			if _, err := io.ReadFull(stream, buf); err != nil {
				serverErr <- err
				return
			}
			reply := []byte{buf[1], buf[0]}
			if _, err := stream.Write(reply); err != nil {
				serverErr <- err
				return
			}
			serverErr <- nil
		}()
	}

	first, err := client.openStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.openStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Write([]byte("xy")); err != nil {
		t.Fatal(err)
	}
	assertMuxReply(t, first, "ba")
	assertMuxReply(t, second, "yx")
	if err := first.Close(); err != nil && !errors.Is(err, errMuxClosed) {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil && !errors.Is(err, errMuxClosed) {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := <-serverErr; err != nil {
			t.Fatal(err)
		}
	}
}

func assertMuxReply(t *testing.T, stream net.Conn, want string) {
	t.Helper()
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != want {
		t.Fatalf("mux reply = %q, want %q", buf, want)
	}
}
