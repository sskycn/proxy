package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunProxyRejectsUnknownTrafficWithoutUpstreamForward(t *testing.T) {
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
	_, err = io.ReadFull(client, reply)
	if err == nil {
		t.Fatalf("unknown traffic read succeeded with %q, want close", reply)
	}
	select {
	case <-upstreamHit:
		t.Fatal("unknown traffic was forwarded to upstream")
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

func TestRunProxyCachesDirectFailureAndSkipsRetry(t *testing.T) {
	targetAddr := reserveTCPAddr(t)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	upstreamTargets := make(chan string, 2)
	upstreamErr := make(chan error, 1)
	go socks5ConnectUpstream(upstream, upstreamTargets, upstreamErr, 2)

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
			DialTimeout: 100 * time.Millisecond,
			BufferSize:  4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	first := dialHTTPConnect(t, listenAddr, targetAddr)
	if err := first.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target := <-upstreamTargets; target != targetAddr {
		t.Fatalf("first upstream target = %q, want %s", target, targetAddr)
	}

	direct, err := net.Listen("tcp", targetAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close late direct listener: %v", err)
		}
	})
	directHit := make(chan struct{}, 1)
	go acceptSignal(direct, directHit)

	second := dialHTTPConnect(t, listenAddr, targetAddr)
	if err := second.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target := <-upstreamTargets; target != targetAddr {
		t.Fatalf("second upstream target = %q, want %s", target, targetAddr)
	}
	select {
	case <-directHit:
		t.Fatal("direct target was retried after cached failure")
	default:
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
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

func TestRunProxyForceUpstreamCIDRSkipsDirect(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directHit := make(chan struct{}, 1)
	go acceptSignal(direct, directHit)

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	upstreamTargets := make(chan string, 1)
	upstreamErr := make(chan error, 1)
	go socks5ConnectUpstream(upstream, upstreamTargets, upstreamErr, 1)

	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{"force_upstream":{"ip_cidrs":["127.0.0.1/32"]}}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

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
			ConfigPath:  configPath,
			DialTimeout: time.Second,
			BufferSize:  4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	client := dialHTTPConnect(t, listenAddr, direct.Addr().String())
	if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target := <-upstreamTargets; target != direct.Addr().String() {
		t.Fatalf("upstream target = %q, want %s", target, direct.Addr())
	}
	select {
	case <-directHit:
		t.Fatal("force upstream target was sent directly")
	default:
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
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

func TestLoadRouteRulesForceUpstreamMatchers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"force_upstream": {
			"domains": ["exact.example.com"],
			"domain_prefixes": ["api."],
			"domain_suffixes": ["x.com"],
			"ip_cidrs": ["203.0.113.0/24"],
			"ips": ["2001:db8::1"]
		}
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := loadRouteRules(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{
		"exact.example.com",
		"api.example.com",
		"x.com",
		"assets.x.com",
		"203.0.113.8",
		"[2001:db8::1]",
	} {
		if !rules.shouldForceUpstream(host) {
			t.Fatalf("host %s should force upstream", host)
		}
	}
	for _, host := range []string{
		"other.example.com",
		"notx.com",
		"203.0.114.1",
		"2001:db8::2",
	} {
		if rules.shouldForceUpstream(host) {
			t.Fatalf("host %s should not force upstream", host)
		}
	}
}

func TestPersistDirectFailuresCreatesAndDedupesConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	hosts := []string{
		"Example.COM.",
		"example.com",
		"203.0.113.8",
		"[2001:db8::8]",
	}

	if err := persistDirectFailures(configPath, hosts); err != nil {
		t.Fatal(err)
	}
	if err := persistDirectFailures(configPath, hosts); err != nil {
		t.Fatal(err)
	}

	cfg, err := readRouteConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.ForceUpstream.Domains, ","), "example.com"; got != want {
		t.Fatalf("domains = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.ForceUpstream.IPs, ","), "2001:db8::8,203.0.113.8"; got != want {
		t.Fatalf("ips = %q, want %q", got, want)
	}
}

func TestPersistDirectFailuresSkipsCoveredRules(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"force_upstream": {
			"domain_suffixes": ["x.com"],
			"ip_cidrs": ["203.0.113.0/24"]
		}
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := []string{"api.x.com", "203.0.113.8", "new.example.com"}
	if err := persistDirectFailures(configPath, hosts); err != nil {
		t.Fatal(err)
	}

	cfg, err := readRouteConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.ForceUpstream.Domains, ","), "new.example.com"; got != want {
		t.Fatalf("domains = %q, want %q", got, want)
	}
	if len(cfg.ForceUpstream.IPs) != 0 {
		t.Fatalf("ips = %v, want empty", cfg.ForceUpstream.IPs)
	}
}

func TestResolveConfigPathUsesExecutableDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfigPath("config.json")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(executable), "config.json")
	if got != want {
		t.Fatalf("resolved config path = %q, want %q", got, want)
	}

	absolute := filepath.Join(t.TempDir(), "custom.json")
	got, err = resolveConfigPath(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if got != absolute {
		t.Fatalf("absolute config path = %q, want %q", got, absolute)
	}

	disabled, err := resolveConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	if disabled != "" {
		t.Fatalf("disabled config path = %q, want empty", disabled)
	}
}

func TestUpstreamRefreshSkipsDiscoveryWhenLocalIPUnchanged(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	hit := make(chan struct{}, 1)
	go acceptSignal(upstream, hit)

	signature, err := localIPv4Signature()
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	resolver := &upstreamResolver{
		cfg: config{
			GatewayIP:   "not-an-ip",
			GatewayPort: upstream.Addr().(*net.TCPAddr).Port,
			DialTimeout: time.Second,
			ScanTimeout: time.Nanosecond,
			ScanWorkers: 1,
			Verbose:     true,
		},
		log:              &log,
		currentTarget:    upstream.Addr().String(),
		localIPSignature: signature,
	}

	if err := resolver.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := resolver.target(), upstream.Addr().String(); got != want {
		t.Fatalf("target = %s, want %s", got, want)
	}
	if log.Len() != 0 {
		t.Fatalf("log = %q, want empty", log.String())
	}
	select {
	case <-hit:
		t.Fatal("current upstream was probed without local IP change")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUpstreamRefreshRunsWhenLocalIPChanged(t *testing.T) {
	resolver := &upstreamResolver{
		cfg: config{
			GatewayIP:   "127.0.0.1",
			GatewayPort: 1080,
			DialTimeout: time.Second,
			Verbose:     true,
		},
		log:              io.Discard,
		currentTarget:    "192.0.2.1:1080",
		localIPSignature: "old-local-ip",
	}

	if err := resolver.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := resolver.target(), "127.0.0.1:1080"; got != want {
		t.Fatalf("target = %s, want %s", got, want)
	}
	if resolver.localSignature() == "old-local-ip" {
		t.Fatal("local IP signature was not updated")
	}
}

func TestAccessLogIdentifiesRoute(t *testing.T) {
	var buf bytes.Buffer
	if err := accessLog(&buf, accessSource("http", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}), "-", "x.com:443", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := accessLog(&buf, accessSource("httpc", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1235}), "10.0.0.1:1080", "example.com:443", "connect failed"); err != nil {
		t.Fatal(err)
	}
	if err := accessLog(&buf, accessSource("socks5", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1236}), "10.0.0.1:1080", "example.com:443", "connect failed"); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	for _, want := range []string{
		"http/localhost:1234 -> x.com:443 ok",
		"httpc/localhost:1235 -> 10.0.0.1:1080 -> example.com:443 connect failed",
		"socks5/localhost:1236 -> 10.0.0.1:1080 -> example.com:443 connect failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("access log = %q, missing %q", got, want)
		}
	}
}

func TestHTTPAccessProtocol(t *testing.T) {
	for _, tc := range []struct {
		req  *httpProxyRequest
		want string
	}{
		{req: &httpProxyRequest{method: "CONNECT"}, want: "httpc"},
		{req: &httpProxyRequest{method: "connect"}, want: "httpc"},
		{req: &httpProxyRequest{method: "GET"}, want: "http"},
		{req: nil, want: "http"},
	} {
		if got := httpAccessProtocol(tc.req); got != tc.want {
			t.Fatalf("httpAccessProtocol(%v) = %q, want %q", tc.req, got, tc.want)
		}
	}
}

func TestNormalizeUpstreamProtocol(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
	}{
		{value: "", want: upstreamProtocolSOCKS5},
		{value: "SOCKS5", want: upstreamProtocolSOCKS5},
		{value: " mixed ", want: upstreamProtocolMixed},
	} {
		got, err := normalizeUpstreamProtocol(tc.value)
		if err != nil {
			t.Fatalf("normalize %q: %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("normalize %q = %q, want %q", tc.value, got, tc.want)
		}
	}
	if _, err := normalizeUpstreamProtocol("http"); err == nil {
		t.Fatal("normalize unsupported protocol succeeded")
	}
}

func TestRunProxyMixedUpstreamFromConfigForwardsUnknownTraffic(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})

	payload := []byte("SSH-2.0-test\r\n")
	upstreamPayload := make(chan []byte, 1)
	upstreamErr := make(chan error, 1)
	go fixedPayloadUpstream(upstream, len(payload), upstreamPayload, upstreamErr)

	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{"upstream_protocol":"mixed","force_upstream":{}}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := reserveTCPAddr(t)
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
			ConfigPath:  configPath,
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
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "OK" {
		t.Fatalf("reply = %q, want OK", reply)
	}
	if got := <-upstreamPayload; !bytes.Equal(got, payload) {
		t.Fatalf("upstream payload = %v, want %v", got, payload)
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
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

func TestRunProxyMixedUpstreamForwardsHTTPProxyRequestRaw(t *testing.T) {
	targetAddr := reserveTCPAddr(t)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})

	upstreamLine := make(chan string, 1)
	upstreamErr := make(chan error, 1)
	go rawHTTPUpstream(upstream, upstreamLine, upstreamErr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := reserveTCPAddr(t)
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
			ListenAddr:       listenAddr,
			GatewayIP:        "127.0.0.1",
			GatewayPort:      port,
			UpstreamProtocol: upstreamProtocolMixed,
			DialTimeout:      100 * time.Millisecond,
			BufferSize:       4096,
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
	request := "GET http://" + targetAddr + "/path?q=1 HTTP/1.1\r\nHost: " + targetAddr + "\r\n\r\n"
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("HTTP/1.1 204"))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "HTTP/1.1 204" {
		t.Fatalf("response prefix = %q, want HTTP/1.1 204", response)
	}

	if line := <-upstreamLine; line != "GET http://"+targetAddr+"/path?q=1 HTTP/1.1\r\n" {
		t.Fatalf("upstream http line = %q", line)
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
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

func dialHTTPConnect(t *testing.T, proxyAddr string, targetAddr string) net.Conn {
	t.Helper()
	client, err := net.DialTimeout("tcp", proxyAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := "CONNECT " + targetAddr + " HTTP/1.1\r\nHost: " + targetAddr + "\r\n\r\n"
	if _, err := client.Write([]byte(request)); err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	line, err := reader.ReadString('\n')
	if err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	if !strings.Contains(line, "200") {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(closeErr)
		}
		t.Fatalf("CONNECT response line = %q", line)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				t.Fatal(errors.Join(err, closeErr))
			}
			t.Fatal(err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return client
}

func socks5ConnectUpstream(listener net.Listener, targets chan<- string, errCh chan<- error, count int) {
	defer close(targets)
	for i := 0; i < count; i++ {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				errCh <- err
			}
			return
		}
		reader := bufio.NewReader(conn)
		target, err := readSocks5ConnectTarget(reader, conn)
		if err != nil {
			errCh <- err
			return
		}
		targets <- target
		if err := writeSocks5Reply(conn, socksReplySucceeded); err != nil {
			errCh <- errors.Join(err, conn.Close())
			return
		}
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errCh <- err
			return
		}
	}
	errCh <- nil
}

func socks5HTTPUpstream(listener net.Listener, targetCh chan<- string, lineCh chan<- string, errCh chan<- error) {
	defer close(targetCh)
	defer close(lineCh)

	conn, err := listener.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
		return
	}
	reader := bufio.NewReader(conn)
	target, err := readSocks5ConnectTarget(reader, conn)
	if err != nil {
		errCh <- err
		return
	}
	targetCh <- target
	if err := writeSocks5Reply(conn, socksReplySucceeded); err != nil {
		errCh <- errors.Join(err, conn.Close())
		return
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		errCh <- errors.Join(err, conn.Close())
		return
	}
	lineCh <- line
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			errCh <- errors.Join(err, conn.Close())
			return
		}
		if header == "\r\n" || header == "\n" {
			break
		}
	}
	if _, err := conn.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")); err != nil {
		errCh <- errors.Join(err, conn.Close())
		return
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errCh <- err
		return
	}
	errCh <- nil
}

func fixedPayloadUpstream(listener net.Listener, size int, payloadCh chan<- []byte, errCh chan<- error) {
	defer close(payloadCh)
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
	payload := make([]byte, size)
	if _, err := io.ReadFull(conn, payload); err != nil {
		errCh <- err
		return
	}
	payloadCh <- payload
	if _, err := conn.Write([]byte("OK")); err != nil {
		errCh <- err
		return
	}
	errCh <- nil
}

func rawHTTPUpstream(listener net.Listener, lineCh chan<- string, errCh chan<- error) {
	defer close(lineCh)
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
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		errCh <- err
		return
	}
	lineCh <- line
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		if header == "\r\n" || header == "\n" {
			break
		}
	}
	if _, err := conn.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")); err != nil {
		errCh <- err
		return
	}
	errCh <- nil
}

func readSocks5ConnectTarget(reader *bufio.Reader, conn net.Conn) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(reader, head); err != nil {
		return "", errors.Join(err, conn.Close())
	}
	if head[0] != socksVersion5 {
		return "", errors.Join(fmt.Errorf("socks version = %d, want 5", head[0]), conn.Close())
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return "", errors.Join(err, conn.Close())
	}
	if err := writeAll(conn, []byte{socksVersion5, 0x00}); err != nil {
		return "", errors.Join(err, conn.Close())
	}
	reqHead := make([]byte, 4)
	if _, err := io.ReadFull(reader, reqHead); err != nil {
		return "", errors.Join(err, conn.Close())
	}
	if reqHead[0] != socksVersion5 || reqHead[1] != socksCmdConnect {
		return "", errors.Join(fmt.Errorf("socks request head = %v", reqHead), conn.Close())
	}
	host, err := readSocksAddr(reader, reqHead[3])
	if err != nil {
		return "", errors.Join(err, conn.Close())
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", errors.Join(err, conn.Close())
	}
	port := binary.BigEndian.Uint16(portBytes)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
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

func stopProxy(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
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

func dialSocks5UDPAssociate(t *testing.T, proxyAddr string) (net.Conn, *net.UDPAddr) {
	t.Helper()
	control, err := net.DialTimeout("tcp", proxyAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		if closeErr := control.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(control, method); err != nil {
		if closeErr := control.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	if !bytes.Equal(method, []byte{0x05, 0x00}) {
		if closeErr := control.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(closeErr)
		}
		t.Fatalf("method = %v", method)
	}
	if _, err := control.Write(buildSocks5UDPAssociateRequest("0.0.0.0", 0)); err != nil {
		if closeErr := control.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	relayHost, relayPort, err := readSocks5ReplyEndpoint(control)
	if err != nil {
		if closeErr := control.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(relayHost, strconv.Itoa(int(relayPort))))
	if err != nil {
		if closeErr := control.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	return control, relayAddr
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

func TestRunProxyConvertsHTTPConnectUpstreamToSocks5(t *testing.T) {
	targetAddr := reserveTCPAddr(t)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})

	upstreamTargets := make(chan string, 1)
	upstreamErr := make(chan error, 1)
	go socks5ConnectUpstream(upstream, upstreamTargets, upstreamErr, 1)

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

	client := dialHTTPConnect(t, listenAddr, targetAddr)
	if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target := <-upstreamTargets; target != targetAddr {
		t.Fatalf("upstream target = %q, want %s", target, targetAddr)
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
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

func TestRunProxyConvertsHTTPProxyRequestUpstreamToSocks5(t *testing.T) {
	targetAddr := reserveTCPAddr(t)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})

	upstreamTarget := make(chan string, 1)
	upstreamLine := make(chan string, 1)
	upstreamErr := make(chan error, 1)
	go socks5HTTPUpstream(upstream, upstreamTarget, upstreamLine, upstreamErr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := reserveTCPAddr(t)
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
			DialTimeout: 100 * time.Millisecond,
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
	request := "GET http://" + targetAddr + "/path?q=1 HTTP/1.1\r\nHost: " + targetAddr + "\r\n\r\n"
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("HTTP/1.1 204"))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "HTTP/1.1 204" {
		t.Fatalf("response prefix = %q, want HTTP/1.1 204", response)
	}

	if target := <-upstreamTarget; target != targetAddr {
		t.Fatalf("upstream target = %q, want %s", target, targetAddr)
	}
	if line := <-upstreamLine; line != "GET /path?q=1 HTTP/1.1\r\n" {
		t.Fatalf("upstream http line = %q", line)
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
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

func TestRunProxyClientServerHTTPConnectTunnel(t *testing.T) {
	for _, transport := range []string{tunnelTransportRaw, tunnelTransportWS, tunnelTransportH2} {
		t.Run(transport, func(t *testing.T) {
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

			serverAddr := reserveTCPAddr(t)
			serverCtx, serverCancel := context.WithCancel(context.Background())
			defer serverCancel()
			serverErr := make(chan error, 1)
			go func() {
				serverErr <- runProxy(serverCtx, config{
					Mode:            proxyModeServer,
					ListenAddr:      serverAddr,
					Token:           "secret",
					TunnelTransport: transport,
					TunnelPath:      "/tunnel",
					TunnelMux:       true,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, io.Discard)
			}()
			waitForTCP(t, serverAddr)

			configPath := filepath.Join(t.TempDir(), "config.json")
			configBody := []byte(`{
				"force_upstream": {
					"ip_cidrs": ["127.0.0.1/32"]
				}
			}`)
			if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
				t.Fatal(err)
			}

			clientAddr := reserveTCPAddr(t)
			clientCtx, clientCancel := context.WithCancel(context.Background())
			defer clientCancel()
			clientErr := make(chan error, 1)
			go func() {
				clientErr <- runProxy(clientCtx, config{
					Mode:            proxyModeClient,
					ListenAddr:      clientAddr,
					ServerAddr:      serverAddr,
					Token:           "secret",
					TunnelTransport: transport,
					TunnelPath:      "/tunnel",
					TunnelMux:       true,
					ConfigPath:      configPath,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, io.Discard)
			}()
			waitForTCP(t, clientAddr)

			client := dialHTTPConnect(t, clientAddr, direct.Addr().String())
			if _, err := client.Write([]byte("hi")); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, 2)
			if _, err := io.ReadFull(client, reply); err != nil {
				t.Fatal(err)
			}
			if string(reply) != "OK" {
				t.Fatalf("reply = %q, want OK", reply)
			}
			if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatal(err)
			}
			select {
			case err := <-directErr:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("direct target did not receive tunneled TCP")
			}

			stopProxy(t, clientCancel, clientErr)
			stopProxy(t, serverCancel, serverErr)
		})
	}
}

func TestRunProxyClientServerSOCKS5UDPTunnel(t *testing.T) {
	for _, transport := range []string{tunnelTransportRaw, tunnelTransportWS, tunnelTransportH2} {
		t.Run(transport, func(t *testing.T) {
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

			serverAddr := reserveTCPAddr(t)
			serverCtx, serverCancel := context.WithCancel(context.Background())
			defer serverCancel()
			serverErr := make(chan error, 1)
			go func() {
				serverErr <- runProxy(serverCtx, config{
					Mode:            proxyModeServer,
					ListenAddr:      serverAddr,
					Token:           "secret",
					TunnelTransport: transport,
					TunnelPath:      "/tunnel",
					TunnelMux:       true,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, io.Discard)
			}()
			waitForTCP(t, serverAddr)

			configPath := filepath.Join(t.TempDir(), "config.json")
			configBody := []byte(`{
				"force_upstream": {
					"ip_cidrs": ["127.0.0.1/32"]
				}
			}`)
			if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
				t.Fatal(err)
			}

			clientAddr := reserveTCPAddr(t)
			clientCtx, clientCancel := context.WithCancel(context.Background())
			defer clientCancel()
			clientErr := make(chan error, 1)
			go func() {
				clientErr <- runProxy(clientCtx, config{
					Mode:            proxyModeClient,
					ListenAddr:      clientAddr,
					ServerAddr:      serverAddr,
					Token:           "secret",
					TunnelTransport: transport,
					TunnelPath:      "/tunnel",
					TunnelMux:       true,
					ConfigPath:      configPath,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, io.Discard)
			}()
			waitForTCP(t, clientAddr)

			control, relayAddr := dialSocks5UDPAssociate(t, clientAddr)
			defer func() {
				if err := control.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					t.Errorf("close socks control: %v", err)
				}
			}()

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

			stopProxy(t, clientCancel, clientErr)
			stopProxy(t, serverCancel, serverErr)
		})
	}
}

func TestBuildTunnelURLTransports(t *testing.T) {
	tests := []struct {
		name      string
		cfg       config
		transport string
		want      string
		wantErr   bool
	}{
		{
			name: "websocket cleartext",
			cfg: config{
				ServerAddr: "example.com:443",
				TunnelPath: "/ws",
			},
			transport: tunnelTransportWS,
			want:      "ws://example.com:443/ws",
		},
		{
			name: "websocket tls",
			cfg: config{
				ServerAddr: "example.com:443",
				TunnelPath: "ws",
				TunnelTLS:  true,
			},
			transport: tunnelTransportWS,
			want:      "wss://example.com:443/ws",
		},
		{
			name: "h2 cleartext",
			cfg: config{
				ServerAddr: "example.com:80",
				TunnelPath: "/h2",
			},
			transport: tunnelTransportH2,
			want:      "http://example.com:80/h2",
		},
		{
			name: "h2 tls",
			cfg: config{
				ServerAddr: "example.com:443",
				TunnelPath: "/h2",
				TunnelTLS:  true,
			},
			transport: tunnelTransportH2,
			want:      "https://example.com:443/h2",
		},
		{
			name: "h3 always https",
			cfg: config{
				ServerAddr: "example.com:443",
				TunnelPath: "/h3",
			},
			transport: tunnelTransportH3,
			want:      "https://example.com:443/h3",
		},
		{
			name: "h3 rejects http URL",
			cfg: config{
				ServerAddr: "http://example.com:443/h3",
			},
			transport: tunnelTransportH3,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildTunnelURL(tt.cfg, tt.transport)
			if tt.wantErr {
				if err == nil {
					t.Fatal("buildTunnelURL succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.want {
				t.Fatalf("url = %q, want %q", got.String(), tt.want)
			}
		})
	}
}
