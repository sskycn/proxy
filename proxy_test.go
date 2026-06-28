package main

import (
	"bufio"
	"bytes"
	"context"
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
	defer upstream.Close()

	received := make(chan []byte, 1)
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := conn.Read(buf)
			if n == 0 {
				_ = conn.Close()
				continue
			}
			received <- append([]byte(nil), buf[:n]...)
			_, _ = conn.Write([]byte("ok"))
			_ = conn.Close()
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
	_ = local.Close()

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
	defer client.Close()

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
			_ = conn.Close()
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
	defer upstream.Close()

	requestLine := make(chan string, 1)
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			if line == "" {
				_ = conn.Close()
				continue
			}
			requestLine <- line
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
			_ = conn.Close()
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
	_ = local.Close()

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
	defer client.Close()
	if _, err := client.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	line := <-requestLine
	if line != "CONNECT example.com:443 HTTP/1.1\r\n" {
		t.Fatalf("request line = %q", line)
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
