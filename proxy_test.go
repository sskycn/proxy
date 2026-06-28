package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunProxyForwardsUnknownTrafficUnchanged(t *testing.T) {
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

	payload := []byte{0x16, 0x03, 0x01, 0x00}
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

func TestRunProxyDirectsInternalHTTPConnect(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directErr := make(chan error, 1)
	go echoOnce(direct, directErr)

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	upstreamHit := make(chan struct{}, 1)
	go acceptSignal(upstream, upstreamHit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := reserveTCPAddr(t)
	_, upstreamPortText, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	upstreamPort, err := strconv.Atoi(upstreamPortText)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runProxy(ctx, config{
			ListenAddr:  listenAddr,
			GatewayIP:   "127.0.0.1",
			GatewayPort: upstreamPort,
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
	request := "CONNECT " + direct.Addr().String() + " HTTP/1.1\r\nHost: " + direct.Addr().String() + "\r\n\r\n"
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	responseReader := bufio.NewReader(client)
	header, err := responseReader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header, "200") {
		t.Fatalf("CONNECT response line = %q", header)
	}
	for {
		line, err := responseReader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if _, err := client.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(responseReader, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "OK" {
		t.Fatalf("reply = %q, want OK", reply)
	}
	select {
	case err := <-directErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case <-upstreamHit:
		t.Fatal("internal CONNECT was forwarded to upstream")
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

func TestRunProxyDirectsInternalSOCKS5(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directErr := make(chan error, 1)
	go echoOnce(direct, directErr)

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	upstreamHit := make(chan struct{}, 1)
	go acceptSignal(upstream, upstreamHit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := reserveTCPAddr(t)
	_, upstreamPortText, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	upstreamPort, err := strconv.Atoi(upstreamPortText)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runProxy(ctx, config{
			ListenAddr:  listenAddr,
			GatewayIP:   "127.0.0.1",
			GatewayPort: upstreamPort,
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
	if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(method, []byte{0x05, 0x00}) {
		t.Fatalf("method = %v", method)
	}
	_, directPortText, err := net.SplitHostPort(direct.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	directPort, err := strconv.Atoi(directPortText)
	if err != nil {
		t.Fatal(err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(directPort >> 8), byte(directPort)}
	if _, err := client.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("socks reply = %v", reply)
	}
	if _, err := client.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 2)
	if _, err := io.ReadFull(client, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != "OK" {
		t.Fatalf("reply = %q, want OK", echo)
	}
	select {
	case err := <-directErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case <-upstreamHit:
		t.Fatal("internal SOCKS5 was forwarded to upstream")
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

func TestRunProxyDirectsInternalSOCKS5UDP(t *testing.T) {
	direct, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct udp listener: %v", err)
		}
	})
	directErr := make(chan error, 1)
	go udpEchoOnce(direct, directErr)

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	upstreamHit := make(chan struct{}, 1)
	go acceptSignal(upstream, upstreamHit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := reserveTCPAddr(t)
	_, upstreamPortText, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	upstreamPort, err := strconv.Atoi(upstreamPortText)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runProxy(ctx, config{
			ListenAddr:  listenAddr,
			GatewayIP:   "127.0.0.1",
			GatewayPort: upstreamPort,
			DialTimeout: time.Second,
			BufferSize:  4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	control, err := net.DialTimeout("tcp", listenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := control.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close socks control: %v", err)
		}
	})
	if _, err := control.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(control, method); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(method, []byte{0x05, 0x00}) {
		t.Fatalf("method = %v", method)
	}
	if _, err := control.Write(buildSocks5UDPAssociateRequest("0.0.0.0", 0)); err != nil {
		t.Fatal(err)
	}
	relayHost, relayPort, err := readSocks5ReplyEndpoint(control)
	if err != nil {
		t.Fatal(err)
	}
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(relayHost, strconv.Itoa(int(relayPort))))
	if err != nil {
		t.Fatal(err)
	}

	udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := udpClient.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close udp client: %v", err)
		}
	})
	targetPort := uint16(direct.LocalAddr().(*net.UDPAddr).Port)
	packet := buildSocksUDPDatagram("127.0.0.1", targetPort, []byte("hi"))
	if _, err := udpClient.WriteToUDP(packet, relayAddr); err != nil {
		t.Fatal(err)
	}
	if err := udpClient.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, udpBufferSize)
	n, _, err := udpClient.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	dgram, err := parseSocksUDPDatagram(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if string(dgram.payload) != "OK" {
		t.Fatalf("udp payload = %q, want OK", dgram.payload)
	}
	select {
	case err := <-directErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case <-upstreamHit:
		t.Fatal("internal SOCKS5 UDP was forwarded to upstream")
	default:
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
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

func reserveTCPAddr(t *testing.T) string {
	t.Helper()
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := local.Addr().String()
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func echoOnce(listener net.Listener, errCh chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
		return
	}
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
	}()
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		errCh <- err
		return
	}
	if string(buf) != "hi" {
		errCh <- fmt.Errorf("direct received %q, want hi", buf)
		return
	}
	if _, err := conn.Write([]byte("OK")); err != nil {
		errCh <- err
		return
	}
	errCh <- nil
}

func acceptSignal(listener net.Listener, hit chan<- struct{}) {
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return
		}
	}()
	hit <- struct{}{}
}

func udpEchoOnce(conn *net.UDPConn, errCh chan<- error) {
	buf := make([]byte, 64)
	n, addr, err := conn.ReadFromUDP(buf)
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
		return
	}
	if string(buf[:n]) != "hi" {
		errCh <- fmt.Errorf("direct udp received %q, want hi", buf[:n])
		return
	}
	if _, err := conn.WriteToUDP([]byte("OK"), addr); err != nil {
		errCh <- err
		return
	}
	errCh <- nil
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
