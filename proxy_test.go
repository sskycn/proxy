package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestRunProxyForwardsMixedTrafficUnchanged(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})

	received := make(chan []byte, 1)
	serverErr := make(chan error, 1)
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					serverErr <- err
				}
				return
			}
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			if n == 0 {
				if err != nil && !errors.Is(err, io.EOF) {
					serverErr <- err
				}
				if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					serverErr <- err
				}
				continue
			}
			received <- append([]byte(nil), buf[:n]...)
			if _, err := conn.Write([]byte("ok")); err != nil {
				serverErr <- err
			}
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				serverErr <- err
			}
			return
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddr := local.Addr().String()
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}

	_, portText, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runProxy(ctx, config{
			ListenAddr:  listenAddr,
			GatewayIP:   "127.0.0.1",
			GatewayPort: port,
			DialTimeout: time.Second,
			BufferSize:  4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	client, err := net.DialTimeout("tcp", listenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close client: %v", err)
		}
	})

	payload := []byte{0x05, 0x01, 0x00}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply, []byte("ok")) {
		t.Fatalf("reply = %q, want ok", reply)
	}

	got := <-received
	if !bytes.Equal(got, payload) {
		t.Fatalf("upstream received %v, want %v", got, payload)
	}
	select {
	case err := <-serverErr:
		t.Fatal(err)
	default:
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy did not stop")
	}
}

func waitForTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("close readiness probe: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener %s did not become ready", addr)
}

func TestRunProxyForwardsHTTPRequestStart(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})

	requestLine := make(chan string, 1)
	serverErr := make(chan error, 1)
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					serverErr <- err
				}
				return
			}
			line, err := bufio.NewReader(conn).ReadString('\n')
			if line == "" {
				if err != nil && !errors.Is(err, io.EOF) {
					serverErr <- err
				}
				if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					serverErr <- err
				}
				continue
			}
			requestLine <- line
			if _, err := conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")); err != nil {
				serverErr <- err
			}
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				serverErr <- err
			}
			return
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddr := local.Addr().String()
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}

	_, portText, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runProxy(ctx, config{
			ListenAddr:  listenAddr,
			GatewayIP:   "127.0.0.1",
			GatewayPort: port,
			DialTimeout: time.Second,
			BufferSize:  4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	client, err := net.DialTimeout("tcp", listenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close client: %v", err)
		}
	})
	if _, err := client.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	line := <-requestLine
	if line != "CONNECT example.com:443 HTTP/1.1\r\n" {
		t.Fatalf("request line = %q", line)
	}
	select {
	case err := <-serverErr:
		t.Fatal(err)
	default:
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy did not stop")
	}
}
