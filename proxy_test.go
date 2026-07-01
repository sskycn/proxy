package tcptun

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type channelLogWriter struct {
	ch chan<- string
}

func (w channelLogWriter) Write(p []byte) (int, error) {
	w.ch <- string(append([]byte(nil), p...))
	return len(p), nil
}

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
		t.Fatal("tcptun did not stop")
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
		t.Fatal("tcptun did not stop")
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
		t.Fatal("tcptun did not stop")
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
			ListenAddr:      listenAddr,
			GatewayIP:       "127.0.0.1",
			GatewayPort:     upstreamPort,
			RouteConfigPath: configPath,
			DialTimeout:     time.Second,
			BufferSize:      4096,
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
		t.Fatal("tcptun did not stop")
	}
}

func TestLoadRouteRulesForceUpstreamMatchers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"force_upstream": {
			"domains": ["exact.example.com"],
			"domain_regexes": ["^api\\.", "^assets-[0-9]+\\.example\\.net$"],
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
		"assets-12.example.net",
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
		"assets-x.example.net",
		"notx.com",
		"203.0.114.1",
		"2001:db8::2",
	} {
		if rules.shouldForceUpstream(host) {
			t.Fatalf("host %s should not force upstream", host)
		}
	}
}

func TestLoadRouteRulesRejectsInvalidDomainRegex(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"force_upstream": {
			"domain_regexes": ["["]
		}
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRouteRules(configPath); err == nil {
		t.Fatal("load route rules with invalid domain regex succeeded")
	}
}

func TestPersistDirectFailuresRemovesObsoleteDomainStartsWithField(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody, err := json.Marshal(map[string]any{
		"force_upstream": map[string]any{
			"domains":                          []string{"exact.example.com"},
			obsoleteRouteDomainStartsWithField: []string{"api."},
			"domain_regexes":                   []string{"^cdn\\."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := persistDirectFailures(configPath, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["force_upstream"][obsoleteRouteDomainStartsWithField]; ok {
		t.Fatal("obsolete domain starts-with field was not removed")
	}
	cfg, err := readRouteConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.ForceUpstream.DomainRegexes, ","), "^cdn\\."; got != want {
		t.Fatalf("domain regexes = %q, want %q", got, want)
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

func TestPersistDirectFailuresDedupesAndSortsDomainSuffixes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"force_upstream": {
			"domains": ["api.x.com", "keep.example.net"],
			"domain_suffixes": ["twitter.com", ".X.COM", "x.com", "apple.com"]
		}
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := persistDirectFailures(configPath, nil); err != nil {
		t.Fatal(err)
	}

	cfg, err := readRouteConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.ForceUpstream.DomainSuffixes, ","), "apple.com,twitter.com,x.com"; got != want {
		t.Fatalf("domain suffixes = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.ForceUpstream.Domains, ","), "keep.example.net"; got != want {
		t.Fatalf("domains = %q, want %q", got, want)
	}
}

func TestPersistDirectFailuresPromotesBusyDomainSuffix(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	hosts := []string{
		"example.co.uk",
		"api.example.co.uk",
		"cdn.example.co.uk",
		"img.example.co.uk",
		"static.example.co.uk",
		"other.example.net",
	}

	if err := persistDirectFailures(configPath, hosts); err != nil {
		t.Fatal(err)
	}

	cfg, err := readRouteConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.ForceUpstream.DomainSuffixes, ","), "example.co.uk"; got != want {
		t.Fatalf("domain suffixes = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.ForceUpstream.Domains, ","), "other.example.net"; got != want {
		t.Fatalf("domains = %q, want %q", got, want)
	}
}

func TestPersistDirectFailuresCompactsExistingBusyDomainSuffix(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"force_upstream": {
			"domains": [
				"api.example.com",
				"cdn.example.com",
				"img.example.com",
				"static.example.com",
				"other.example.net"
			]
		}
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := persistDirectFailures(configPath, nil); err != nil {
		t.Fatal(err)
	}

	cfg, err := readRouteConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.ForceUpstream.DomainSuffixes, ","), "example.com"; got != want {
		t.Fatalf("domain suffixes = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.ForceUpstream.Domains, ","), "other.example.net"; got != want {
		t.Fatalf("domains = %q, want %q", got, want)
	}
}

func TestApplyRuntimeConfigDefaultsLoadsModeOnlyWhenEmpty(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configBody := []byte(`{
		"mode": "client",
		"listen_addr": "127.0.0.1:19080",
		"server_addr": "203.0.113.10:9443",
		"token": "secret",
		"tunnel_protocol": "vless",
		"tunnel_transport": "ws",
		"tunnel_path": "/ws",
		"direct_probe_timeout": "25ms",
		"scan_retry_interval": "75ms"
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config{ConfigPath: configPath}
	if err := applyRuntimeConfigDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != proxyModeClient {
		t.Fatalf("mode = %q, want %q", cfg.Mode, proxyModeClient)
	}
	if cfg.ListenAddr != "127.0.0.1:19080" {
		t.Fatalf("listen addr = %q", cfg.ListenAddr)
	}
	if cfg.ServerAddr != "203.0.113.10:9443" {
		t.Fatalf("server addr = %q", cfg.ServerAddr)
	}
	if cfg.Token != "secret" {
		t.Fatalf("token = %q", cfg.Token)
	}
	if cfg.TunnelProtocol != tunnelProtocolVLESS {
		t.Fatalf("tunnel protocol = %q", cfg.TunnelProtocol)
	}
	if cfg.TunnelTransport != tunnelTransportWS {
		t.Fatalf("tunnel transport = %q", cfg.TunnelTransport)
	}
	if cfg.TunnelPath != "/ws" {
		t.Fatalf("tunnel path = %q", cfg.TunnelPath)
	}
	if cfg.DirectProbeTimeout != 25*time.Millisecond {
		t.Fatalf("direct probe timeout = %s, want 25ms", cfg.DirectProbeTimeout)
	}
	if cfg.ScanRetryInterval != 75*time.Millisecond {
		t.Fatalf("scan retry interval = %s, want 75ms", cfg.ScanRetryInterval)
	}

	cfg = config{ConfigPath: configPath, Mode: proxyModeServer}
	if err := applyRuntimeConfigDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != proxyModeServer {
		t.Fatalf("explicit mode = %q, want %q", cfg.Mode, proxyModeServer)
	}
}

func TestApplyRuntimeConfigDefaultsLoadsServerListenAddrs(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "server.json")
	configBody := []byte(`{
		"mode": "server",
		"listen_addr": "127.0.0.1:19080",
		"listen_addrs": ["127.0.0.1:19081", "127.0.0.1:19082"]
	}`)
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config{ConfigPath: configPath}
	if err := applyRuntimeConfigDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "" {
		t.Fatalf("listen addr = %q, want empty when listen_addrs is set", cfg.ListenAddr)
	}
	if got, want := strings.Join(cfg.ListenAddrs, ","), "127.0.0.1:19081,127.0.0.1:19082"; got != want {
		t.Fatalf("listen addrs = %q, want %q", got, want)
	}

	cfg = config{ConfigPath: configPath, ListenAddr: "127.0.0.1:19100"}
	if err := applyRuntimeConfigDefaults(&cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.ListenAddrs) != 0 {
		t.Fatalf("listen addrs = %#v, want command listen override", cfg.ListenAddrs)
	}
	if cfg.ListenAddr != "127.0.0.1:19100" {
		t.Fatalf("listen addr = %q, want command override", cfg.ListenAddr)
	}
}

func TestScanLocalIPv4WithRetryRetriesAfterNotFound(t *testing.T) {
	cfg := defaultConfig()
	cfg.GatewayPort = 1080
	cfg.ScanTimeout = time.Millisecond
	cfg.ScanRetryInterval = time.Millisecond
	cfg.ScanWorkers = 1

	attempts := 0
	wantIP := net.ParseIP("192.168.1.20")
	got, err := scanLocalIPv4WithRetry(context.Background(), cfg, nil, io.Discard, func(ctx context.Context, port int, timeout time.Duration, workers int, gatewayHint net.IP) ([]reachableTCPTun, error) {
		if ctx == nil {
			return nil, errors.New("context is nil")
		}
		if port != cfg.GatewayPort {
			return nil, fmt.Errorf("port = %d, want %d", port, cfg.GatewayPort)
		}
		attempts++
		if attempts == 1 {
			return nil, errReachableProxyNotFound
		}
		return []reachableTCPTun{{ip: wantIP, latency: time.Millisecond}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].ip.Equal(wantIP) {
		t.Fatalf("reachable = %#v, want %s", got, wantIP)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestScanLocalIPv4WithRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := defaultConfig()
	cfg.ScanRetryInterval = time.Hour

	attempts := 0
	_, err := scanLocalIPv4WithRetry(ctx, cfg, nil, io.Discard, func(ctx context.Context, port int, timeout time.Duration, workers int, gatewayHint net.IP) ([]reachableTCPTun, error) {
		attempts++
		cancel()
		return nil, errReachableProxyNotFound
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestScanLocalIPv4WithRetryDoesNotRetryOtherErrors(t *testing.T) {
	cfg := defaultConfig()
	cfg.ScanRetryInterval = time.Millisecond
	wantErr := errors.New("interfaces unavailable")

	attempts := 0
	_, err := scanLocalIPv4WithRetry(context.Background(), cfg, nil, io.Discard, func(ctx context.Context, port int, timeout time.Duration, workers int, gatewayHint net.IP) ([]reachableTCPTun, error) {
		attempts++
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestApplyModeListenDefault(t *testing.T) {
	cfg := config{Mode: proxyModeServer}
	applyModeListenDefault(&cfg)
	if cfg.ListenAddr != "0.0.0.0:9443" {
		t.Fatalf("server listen addr = %q", cfg.ListenAddr)
	}

	cfg = config{Mode: proxyModeClient}
	applyModeListenDefault(&cfg)
	if cfg.ListenAddr != defaultConfig().ListenAddr {
		t.Fatalf("client listen addr = %q", cfg.ListenAddr)
	}

	cfg = config{Mode: proxyModeServer, ListenAddr: "127.0.0.1:19090"}
	applyModeListenDefault(&cfg)
	if cfg.ListenAddr != "127.0.0.1:19090" {
		t.Fatalf("explicit listen addr = %q", cfg.ListenAddr)
	}

	cfg = config{Mode: proxyModeServer, ListenAddrs: []string{"127.0.0.1:19091"}}
	applyModeListenDefault(&cfg)
	if cfg.ListenAddr != "" {
		t.Fatalf("listen addr = %q, want empty with listen addrs", cfg.ListenAddr)
	}
}

func TestServerListenAddrsNormalizesList(t *testing.T) {
	addrs, err := serverListenAddrs(config{
		ListenAddrs: []string{" 127.0.0.1:19081 ", "", "127.0.0.1:19081", "[::1]:19082"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(addrs, ","), "127.0.0.1:19081,[::1]:19082"; got != want {
		t.Fatalf("listen addrs = %q, want %q", got, want)
	}

	addrs, err = serverListenAddrs(config{ListenAddr: "127.0.0.1:19081, [::1]:19082"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(addrs, ","), "127.0.0.1:19081,[::1]:19082"; got != want {
		t.Fatalf("comma listen addrs = %q, want %q", got, want)
	}
}

func TestRunProxyServerListensOnMultipleRawAddresses(t *testing.T) {
	listenAddrs := []string{reserveTCPAddr(t), reserveTCPAddr(t)}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runProxy(ctx, config{
			Mode:            proxyModeServer,
			ListenAddrs:     listenAddrs,
			Token:           "secret",
			TunnelTransport: tunnelTransportRaw,
			DialTimeout:     time.Second,
			BufferSize:      4096,
		}, io.Discard)
	}()
	for _, listenAddr := range listenAddrs {
		waitForTCP(t, listenAddr)
		conn, err := net.DialTimeout("tcp", listenAddr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stopProxy(t, cancel, errCh)
}

func TestRunProxyServerKeepsRunningWhenOneListenAddrFails(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := occupied.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close occupied listener: %v", err)
		}
	})

	goodAddr := reserveTCPAddr(t)
	listenAddrs := []string{occupied.Addr().String(), goodAddr}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	logCh := make(chan string, 8)
	go func() {
		errCh <- runProxy(ctx, config{
			Mode:            proxyModeServer,
			ListenAddrs:     listenAddrs,
			Token:           "secret",
			TunnelTransport: tunnelTransportRaw,
			DialTimeout:     time.Second,
			BufferSize:      4096,
		}, channelLogWriter{ch: logCh})
	}()
	waitForTCP(t, goodAddr)

	conn, err := net.DialTimeout("tcp", goodAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	wantLog := "tunnel server listener " + occupied.Addr().String() + " stopped"
	deadline := time.After(time.Second)
	var logs strings.Builder
	for !strings.Contains(logs.String(), wantLog) {
		select {
		case line := <-logCh:
			logs.WriteString(line)
		case <-deadline:
			t.Fatalf("log = %q, want %q", logs.String(), wantLog)
		}
	}
	stopProxy(t, cancel, errCh)
}

func TestResolveConfigPathSearchOrder(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	missingName := "tcptun-missing-config-for-test.json"
	got, err := resolveConfigPath(missingName)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(executable), missingName)
	if got != want {
		t.Fatalf("resolved config path = %q, want %q", got, want)
	}

	workDir := t.TempDir()
	t.Chdir(workDir)
	workName := "tcptun-work-config-for-test.json"
	workPath := filepath.Join(workDir, workName)
	if err := os.WriteFile(workPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveConfigPath(workName)
	if err != nil {
		t.Fatal(err)
	}
	if got != workPath {
		t.Fatalf("working directory config path = %q, want %q", got, workPath)
	}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	homeConfigDir := filepath.Join(homeDir, ".config", "tcptun")
	if err := os.MkdirAll(homeConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	homeName := "tcptun-home-config-for-test.json"
	homePath := filepath.Join(homeConfigDir, homeName)
	if err := os.WriteFile(homePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveConfigPath(homeName)
	if err != nil {
		t.Fatal(err)
	}
	if got != homePath {
		t.Fatalf("home config path = %q, want %q", got, homePath)
	}

	absolute := filepath.Join(t.TempDir(), "explicit.json")
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
		log: &log,
		targets: map[string]upstreamTargetState{
			upstream.Addr().String(): {target: upstream.Addr().String()},
		},
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
		log: io.Discard,
		targets: map[string]upstreamTargetState{
			"192.0.2.1:1080": {target: "192.0.2.1:1080"},
		},
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

func TestConnectUpstreamRawKeepsSourceStickyUntilFailure(t *testing.T) {
	upstreamA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstreamA.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream A: %v", err)
		}
	})
	upstreamB, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstreamB.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream B: %v", err)
		}
	})

	hits := make(chan string, 4)
	go acceptLabels(upstreamA, "A", hits)
	go acceptLabels(upstreamB, "B", hits)

	targetA := upstreamA.Addr().String()
	targetB := upstreamB.Addr().String()
	server := &proxyServer{
		cfg: config{Mode: proxyModeLocal, DialTimeout: 100 * time.Millisecond},
		resolver: &upstreamResolver{
			cfg: config{DialTimeout: 100 * time.Millisecond},
			targets: map[string]upstreamTargetState{
				targetA: {target: targetA, latency: 10 * time.Millisecond},
				targetB: {target: targetB, latency: time.Millisecond},
			},
		},
		sticky: newUpstreamSticky(),
	}
	source := "192.168.1.50"

	conn, target, err := server.connectUpstreamRaw(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target != targetB {
		t.Fatalf("first target = %s, want %s", target, targetB)
	}
	if hit := waitForLabel(t, hits); hit != "B" {
		t.Fatalf("first hit = %s, want B", hit)
	}

	server.resolver.mu.Lock()
	stateA := server.resolver.targets[targetA]
	stateA.latency = time.Nanosecond
	server.resolver.targets[targetA] = stateA
	stateB := server.resolver.targets[targetB]
	stateB.latency = time.Second
	server.resolver.targets[targetB] = stateB
	server.resolver.mu.Unlock()

	conn, target, err = server.connectUpstreamRaw(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target != targetB {
		t.Fatalf("sticky target = %s, want %s", target, targetB)
	}
	if hit := waitForLabel(t, hits); hit != "B" {
		t.Fatalf("sticky hit = %s, want B", hit)
	}

	if err := upstreamB.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	conn, target, err = server.connectUpstreamRaw(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target != targetA {
		t.Fatalf("fallback target = %s, want %s", target, targetA)
	}
	if hit := waitForLabel(t, hits); hit != "A" {
		t.Fatalf("fallback hit = %s, want A", hit)
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
	if err := accessLog(&buf, accessSource("httpc", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1237}), "-", accessTarget("Api.Service.Example.COM", "443"), "ok"); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	for _, want := range []string{
		"http/localhost:1234 -> x.com:443 ok",
		"httpc/localhost:1235 -> 10.0.0.1:1080 -> example.com:443 connect failed",
		"socks5/localhost:1236 -> 10.0.0.1:1080 -> example.com:443 connect failed",
		"httpc/localhost:1237 -> Api.Service.Example.COM:443 ok",
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
	configBody := []byte(`{"upstream_protocol":"mixed"}`)
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
		t.Fatal("tcptun did not stop")
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
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxyHTTPDirectNoResponseFallsBackQuickly(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directHits := make(chan struct{}, 2)
	releaseDirect := make(chan struct{})
	t.Cleanup(func() {
		close(releaseDirect)
	})
	directErr := make(chan error, 1)
	go blackholeHTTPDirect(direct, directHits, releaseDirect, directErr)

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close upstream listener: %v", err)
		}
	})
	upstreamLine := make(chan string, 2)
	upstreamErr := make(chan error, 1)
	go rawHTTPUpstreamN(upstream, 2, upstreamLine, upstreamErr)

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
			ListenAddr:         listenAddr,
			GatewayIP:          "127.0.0.1",
			GatewayPort:        port,
			UpstreamProtocol:   upstreamProtocolMixed,
			DialTimeout:        time.Second,
			DirectProbeTimeout: 30 * time.Millisecond,
			BufferSize:         4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	start := time.Now()
	readHTTP204Prefix(t, listenAddr, direct.Addr().String())
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("fallback took %s, want under 500ms", elapsed)
	}
	select {
	case <-directHits:
	case <-time.After(time.Second):
		t.Fatal("direct target was not tried")
	}
	if line := <-upstreamLine; line != "GET http://"+direct.Addr().String()+"/path?q=1 HTTP/1.1\r\n" {
		t.Fatalf("first upstream http line = %q", line)
	}

	readHTTP204Prefix(t, listenAddr, direct.Addr().String())
	if line := <-upstreamLine; line != "GET http://"+direct.Addr().String()+"/path?q=1 HTTP/1.1\r\n" {
		t.Fatalf("second upstream http line = %q", line)
	}
	select {
	case <-directHits:
		t.Fatal("direct target was retried after response probe failure")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case err := <-directErr:
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
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxyHTTPConnectDirectNoResponseFallsBackQuickly(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directHits := make(chan struct{}, 2)
	directPayloads := make(chan []byte, 1)
	releaseDirect := make(chan struct{})
	t.Cleanup(func() {
		close(releaseDirect)
	})
	directErr := make(chan error, 1)
	go blackholePayloadDirect(direct, 2, directHits, directPayloads, releaseDirect, directErr)

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
	upstreamPayloads := make(chan []byte, 2)
	upstreamErr := make(chan error, 1)
	go socks5PayloadUpstream(upstream, upstreamTargets, upstreamPayloads, upstreamErr, 2, 2)

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
			ListenAddr:         listenAddr,
			GatewayIP:          "127.0.0.1",
			GatewayPort:        port,
			DialTimeout:        time.Second,
			DirectProbeTimeout: 30 * time.Millisecond,
			BufferSize:         4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	start := time.Now()
	connectClient := dialHTTPConnect(t, listenAddr, direct.Addr().String())
	if _, err := connectClient.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(connectClient, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "OK" {
		t.Fatalf("reply = %q, want OK", reply)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("fallback took %s, want under 500ms", elapsed)
	}
	if err := connectClient.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}

	select {
	case <-directHits:
	case <-time.After(time.Second):
		t.Fatal("direct target was not tried")
	}
	if got := <-directPayloads; string(got) != "hi" {
		t.Fatalf("direct payload = %q, want hi", got)
	}
	if target := <-upstreamTargets; target != direct.Addr().String() {
		t.Fatalf("first upstream target = %q, want %s", target, direct.Addr())
	}
	if got := <-upstreamPayloads; string(got) != "hi" {
		t.Fatalf("first upstream payload = %q, want hi", got)
	}

	second := dialHTTPConnect(t, listenAddr, direct.Addr().String())
	if _, err := second.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(second, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "OK" {
		t.Fatalf("second reply = %q, want OK", reply)
	}
	if err := second.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target := <-upstreamTargets; target != direct.Addr().String() {
		t.Fatalf("second upstream target = %q, want %s", target, direct.Addr())
	}
	if got := <-upstreamPayloads; string(got) != "hi" {
		t.Fatalf("second upstream payload = %q, want hi", got)
	}
	select {
	case <-directHits:
		t.Fatal("direct target was retried after CONNECT probe failure")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case err := <-directErr:
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
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxySocks5DirectNoResponseFallsBackQuickly(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directHits := make(chan struct{}, 2)
	directPayloads := make(chan []byte, 1)
	releaseDirect := make(chan struct{})
	t.Cleanup(func() {
		close(releaseDirect)
	})
	directErr := make(chan error, 1)
	go blackholePayloadDirect(direct, 2, directHits, directPayloads, releaseDirect, directErr)

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
	upstreamPayloads := make(chan []byte, 2)
	upstreamErr := make(chan error, 1)
	go socks5PayloadUpstream(upstream, upstreamTargets, upstreamPayloads, upstreamErr, 2, 2)

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
			ListenAddr:         listenAddr,
			GatewayIP:          "127.0.0.1",
			GatewayPort:        port,
			DialTimeout:        time.Second,
			DirectProbeTimeout: 30 * time.Millisecond,
			BufferSize:         4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	start := time.Now()
	client := dialSocks5ConnectTCP(t, listenAddr, direct.Addr().String())
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
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("fallback took %s, want under 500ms", elapsed)
	}
	if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	select {
	case <-directHits:
	case <-time.After(time.Second):
		t.Fatal("direct target was not tried")
	}
	if got := <-directPayloads; string(got) != "hi" {
		t.Fatalf("direct payload = %q, want hi", got)
	}
	if target := <-upstreamTargets; target != direct.Addr().String() {
		t.Fatalf("first upstream target = %q, want %s", target, direct.Addr())
	}
	if got := <-upstreamPayloads; string(got) != "hi" {
		t.Fatalf("first upstream payload = %q, want hi", got)
	}

	second := dialSocks5ConnectTCP(t, listenAddr, direct.Addr().String())
	if _, err := second.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(second, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "OK" {
		t.Fatalf("second reply = %q, want OK", reply)
	}
	if err := second.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if target := <-upstreamTargets; target != direct.Addr().String() {
		t.Fatalf("second upstream target = %q, want %s", target, direct.Addr())
	}
	if got := <-upstreamPayloads; string(got) != "hi" {
		t.Fatalf("second upstream payload = %q, want hi", got)
	}
	select {
	case <-directHits:
		t.Fatal("direct target was retried after SOCKS5 probe failure")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case err := <-directErr:
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
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxyHTTPConnectDirectNoResponseFallsBackToMixedUpstream(t *testing.T) {
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close direct listener: %v", err)
		}
	})
	directHits := make(chan struct{}, 1)
	directPayloads := make(chan []byte, 1)
	releaseDirect := make(chan struct{})
	t.Cleanup(func() {
		close(releaseDirect)
	})
	directErr := make(chan error, 1)
	go blackholePayloadDirect(direct, 2, directHits, directPayloads, releaseDirect, directErr)

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
	upstreamPayload := make(chan []byte, 1)
	upstreamErr := make(chan error, 1)
	go rawHTTPConnectPayloadUpstream(upstream, upstreamLine, upstreamPayload, upstreamErr)

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
			ListenAddr:         listenAddr,
			GatewayIP:          "127.0.0.1",
			GatewayPort:        port,
			UpstreamProtocol:   upstreamProtocolMixed,
			DialTimeout:        time.Second,
			DirectProbeTimeout: 30 * time.Millisecond,
			BufferSize:         4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	client := dialHTTPConnect(t, listenAddr, direct.Addr().String())
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
	if line := <-upstreamLine; line != "CONNECT "+direct.Addr().String()+" HTTP/1.1\r\n" {
		t.Fatalf("upstream connect line = %q", line)
	}
	if got := <-upstreamPayload; string(got) != "hi" {
		t.Fatalf("upstream payload = %q, want hi", got)
	}
	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	select {
	case err := <-directErr:
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
		t.Fatal("tcptun did not stop")
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

func assertHTTPConnectRejected(t *testing.T, proxyAddr string, targetAddr string) {
	t.Helper()
	client, err := net.DialTimeout("tcp", proxyAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close rejected connect client: %v", err)
		}
	}()
	request := "CONNECT " + targetAddr + " HTTP/1.1\r\nHost: " + targetAddr + "\r\n\r\n"
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, " 200 ") {
		t.Fatalf("CONNECT response line = %q, want non-200", line)
	}
}

func assertHTTPConnectUnusable(t *testing.T, proxyAddr string, targetAddr string) {
	t.Helper()
	client, err := net.DialTimeout("tcp", proxyAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close unusable connect client: %v", err)
		}
	}()
	request := "CONNECT " + targetAddr + " HTTP/1.1\r\nHost: " + targetAddr + "\r\n\r\n"
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, " 200 ") {
		return
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if header == "\r\n" || header == "\n" {
			break
		}
	}
	if _, err := client.Write([]byte("hi")); err != nil {
		return
	}
	if err := client.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if n, err := client.Read(buf); err == nil {
		t.Fatalf("CONNECT tunnel read %d byte(s), want rejected or closed tunnel", n)
	} else if !isTimeoutError(err) && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("CONNECT tunnel read error = %v, want timeout or close", err)
		}
	}
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func dialSocks5ConnectTCP(t *testing.T, proxyAddr string, targetAddr string) net.Conn {
	t.Helper()
	client, err := net.DialTimeout("tcp", proxyAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte{socksVersion5, 0x01, socksMethodNoAuth}); err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	if !bytes.Equal(method, []byte{socksVersion5, socksMethodNoAuth}) {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(closeErr)
		}
		t.Fatalf("socks method = %v", method)
	}
	host, portText, err := net.SplitHostPort(targetAddr)
	if err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	if _, err := client.Write(buildSocks5ConnectRequest(socksRequest{cmd: socksCmdConnect, host: host, port: uint16(port)})); err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(errors.Join(err, closeErr))
		}
		t.Fatal(err)
	}
	if reply[1] != socksReplySucceeded {
		if closeErr := client.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Fatal(closeErr)
		}
		t.Fatalf("socks reply = %v", reply)
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

func socks5ConnectUpstreamWithAuth(listener net.Listener, targetCh chan<- string, username string, password string, errCh chan<- error) {
	defer close(targetCh)
	conn, err := listener.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
		return
	}
	reader := bufio.NewReader(conn)
	target, err := readSocks5ConnectTargetWithAuth(reader, conn, username, password)
	if err != nil {
		errCh <- err
		return
	}
	targetCh <- target
	if err := writeSocks5Reply(conn, socksReplySucceeded); err != nil {
		errCh <- errors.Join(err, conn.Close())
		return
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errCh <- err
		return
	}
	errCh <- nil
}

func readSocks5ConnectTargetWithAuth(reader *bufio.Reader, conn net.Conn, username string, password string) (string, error) {
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
	hasUserPass := false
	for _, method := range methods {
		if method == socksMethodUserPass {
			hasUserPass = true
			break
		}
	}
	if !hasUserPass {
		return "", errors.Join(fmt.Errorf("socks methods = %v, want username/password", methods), conn.Close())
	}
	if err := writeAll(conn, []byte{socksVersion5, socksMethodUserPass}); err != nil {
		return "", errors.Join(err, conn.Close())
	}
	ok, err := readSocks5UserPassAuth(reader, username, password)
	if err != nil {
		return "", errors.Join(err, conn.Close())
	}
	if !ok {
		return "", errors.Join(errors.New("invalid upstream socks username/password"), conn.Close())
	}
	if err := writeAll(conn, []byte{socksAuthVersion, 0x00}); err != nil {
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
	rawHTTPUpstreamN(listener, 1, lineCh, errCh)
}

func rawHTTPUpstreamN(listener net.Listener, count int, lineCh chan<- string, errCh chan<- error) {
	defer close(lineCh)
	for i := 0; i < count; i++ {
		if err := serveRawHTTPUpstreamOnce(listener, lineCh); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			errCh <- err
			return
		}
	}
	errCh <- nil
}

func serveRawHTTPUpstreamOnce(listener net.Listener, lineCh chan<- string) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return errors.Join(err, conn.Close())
	}
	lineCh <- line
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return errors.Join(err, conn.Close())
		}
		if header == "\r\n" || header == "\n" {
			break
		}
	}
	if _, err := conn.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")); err != nil {
		return errors.Join(err, conn.Close())
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func rawHTTPConnectPayloadUpstream(listener net.Listener, lineCh chan<- string, payloadCh chan<- []byte, errCh chan<- error) {
	defer close(lineCh)
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
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		errCh <- err
		return
	}
	payload := make([]byte, 2)
	if _, err := io.ReadFull(reader, payload); err != nil {
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

func blackholeHTTPDirect(listener net.Listener, hits chan<- struct{}, release <-chan struct{}, errCh chan<- error) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				errCh <- err
			}
			return
		}
		select {
		case hits <- struct{}{}:
		default:
		}
		go drainHTTPHeadersThenHold(conn, release, errCh)
	}
}

func drainHTTPHeadersThenHold(conn net.Conn, release <-chan struct{}, errCh chan<- error) {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	<-release
}

func blackholePayloadDirect(listener net.Listener, size int, hits chan<- struct{}, payloads chan<- []byte, release <-chan struct{}, errCh chan<- error) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				errCh <- err
			}
			return
		}
		select {
		case hits <- struct{}{}:
		default:
		}
		go drainPayloadThenHold(conn, size, payloads, release, errCh)
	}
}

func drainPayloadThenHold(conn net.Conn, size int, payloads chan<- []byte, release <-chan struct{}, errCh chan<- error) {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	payload := make([]byte, size)
	if _, err := io.ReadFull(conn, payload); err != nil {
		select {
		case errCh <- err:
		default:
		}
		return
	}
	select {
	case payloads <- payload:
	default:
	}
	<-release
}

func socks5PayloadUpstream(listener net.Listener, targets chan<- string, payloads chan<- []byte, errCh chan<- error, count int, payloadSize int) {
	defer close(targets)
	defer close(payloads)
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
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(reader, payload); err != nil {
			errCh <- errors.Join(err, conn.Close())
			return
		}
		payloads <- payload
		if _, err := conn.Write([]byte("OK")); err != nil {
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

func readHTTP204Prefix(t *testing.T, proxyAddr string, targetAddr string) {
	t.Helper()
	client, err := net.DialTimeout("tcp", proxyAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close client: %v", err)
		}
	}()
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
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxyAcceptsLocalSOCKS5UsernamePassword(t *testing.T) {
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
			ListenAddr:     listenAddr,
			GatewayIP:      "127.0.0.1",
			GatewayPort:    upstreamPort,
			SOCKS5Username: "user",
			SOCKS5Password: "pass",
			DialTimeout:    time.Second,
			BufferSize:     4096,
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
	if _, err := client.Write([]byte{socksVersion5, 0x01, socksMethodUserPass}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(method, []byte{socksVersion5, socksMethodUserPass}) {
		t.Fatalf("method = %v", method)
	}
	if err := writeSocks5UserPassAuth(client, "user", "pass"); err != nil {
		t.Fatal(err)
	}
	_, directPortText, err := net.SplitHostPort(direct.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	directPort, err := strconv.Atoi(directPortText)
	if err != nil {
		t.Fatal(err)
	}
	req := []byte{socksVersion5, socksCmdConnect, 0x00, socksAtypIPv4, 127, 0, 0, 1, byte(directPort >> 8), byte(directPort)}
	if _, err := client.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != socksReplySucceeded {
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

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxyUsesUpstreamSOCKS5UsernamePassword(t *testing.T) {
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
	go socks5ConnectUpstreamWithAuth(upstream, upstreamTargets, "upuser", "uppass", upstreamErr)

	configPath := filepath.Join(t.TempDir(), "route.json")
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
			ListenAddr:             listenAddr,
			GatewayIP:              "127.0.0.1",
			GatewayPort:            upstreamPort,
			UpstreamSOCKS5Username: "upuser",
			UpstreamSOCKS5Password: "uppass",
			RouteConfigPath:        configPath,
			DialTimeout:            time.Second,
			BufferSize:             4096,
		}, io.Discard)
	}()
	waitForTCP(t, listenAddr)

	targetAddr := net.JoinHostPort("127.0.0.1", "80")
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
		t.Fatal("tcptun did not stop")
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
		t.Fatal("tcptun did not stop")
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
		t.Fatal("tcptun did not stop")
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

func acceptLabels(listener net.Listener, label string, hits chan<- string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return
		}
		hits <- label
	}
}

func waitForLabel(t *testing.T, hits <-chan string) string {
	t.Helper()
	select {
	case hit := <-hits:
		return hit
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream hit")
		return ""
	}
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
		t.Fatal("tcptun did not stop")
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
		t.Fatal("tcptun did not stop")
	}
}

func TestRunProxyClientServerRejectsPrivateHTTPConnectTunnel(t *testing.T) {
	for _, transport := range []string{tunnelTransportRaw, tunnelTransportWS, tunnelTransportH2} {
		t.Run(transport, func(t *testing.T) {
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
					RouteConfigPath: configPath,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, io.Discard)
			}()
			waitForTCP(t, clientAddr)

			assertHTTPConnectRejected(t, clientAddr, "127.0.0.1:80")

			stopProxy(t, clientCancel, clientErr)
			stopProxy(t, serverCancel, serverErr)
		})
	}
}

func TestRunProxyClientServerRejectsPrivateProtocolTunnel(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		token    string
	}{
		{
			name:     "vless",
			protocol: tunnelProtocolVLESS,
			token:    "11111111-1111-4111-8111-111111111111",
		},
		{
			name:     "vmess",
			protocol: tunnelProtocolVMess,
			token:    "22222222-2222-4222-8222-222222222222",
		},
		{
			name:     "trojan",
			protocol: tunnelProtocolTrojan,
			token:    "secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverAddr := reserveTCPAddr(t)
			serverCtx, serverCancel := context.WithCancel(context.Background())
			defer serverCancel()
			serverErr := make(chan error, 1)
			go func() {
				serverErr <- runProxy(serverCtx, config{
					Mode:            proxyModeServer,
					ListenAddr:      serverAddr,
					Token:           tt.token,
					TunnelProtocol:  tt.protocol,
					TunnelTransport: tunnelTransportRaw,
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
					Token:           tt.token,
					TunnelProtocol:  tt.protocol,
					TunnelTransport: tunnelTransportRaw,
					RouteConfigPath: configPath,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, io.Discard)
			}()
			waitForTCP(t, clientAddr)

			assertHTTPConnectUnusable(t, clientAddr, "127.0.0.1:80")

			stopProxy(t, clientCancel, clientErr)
			stopProxy(t, serverCancel, serverErr)
		})
	}
}

func TestRunProxyClientServerRejectsPrivateTrojanRawTLS(t *testing.T) {
	certFile, keyFile := writeTestCertificateFiles(t)
	serverAddr := reserveTCPAddr(t)
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- runProxy(serverCtx, config{
			Mode:            proxyModeServer,
			ListenAddr:      serverAddr,
			Token:           "secret",
			TunnelProtocol:  tunnelProtocolTrojan,
			TunnelTransport: tunnelTransportRaw,
			TunnelTLSCert:   certFile,
			TunnelTLSKey:    keyFile,
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
			Mode:                proxyModeClient,
			ListenAddr:          clientAddr,
			ServerAddr:          serverAddr,
			Token:               "secret",
			TunnelProtocol:      tunnelProtocolTrojan,
			TunnelTransport:     tunnelTransportRaw,
			TunnelTLS:           true,
			TunnelTLSInsecure:   true,
			TunnelTLSServerName: "localhost",
			RouteConfigPath:     configPath,
			DialTimeout:         time.Second,
			BufferSize:          4096,
		}, io.Discard)
	}()
	waitForTCP(t, clientAddr)

	assertHTTPConnectUnusable(t, clientAddr, "127.0.0.1:80")

	stopProxy(t, clientCancel, clientErr)
	stopProxy(t, serverCancel, serverErr)
}

func TestRunProxyClientServerDropsPrivateSOCKS5UDPTunnel(t *testing.T) {
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
					RouteConfigPath: configPath,
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
			if err := udpClient.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, udpBufferSize)
			if _, _, err := udpClient.ReadFromUDP(buf); err == nil || !isTimeoutError(err) {
				t.Fatalf("udp client read error = %v, want timeout", err)
			}
			if err := direct.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := direct.ReadFromUDP(buf); err == nil || !isTimeoutError(err) {
				t.Fatalf("direct udp read error = %v, want timeout", err)
			}

			stopProxy(t, clientCancel, clientErr)
			stopProxy(t, serverCancel, serverErr)
		})
	}
}

func TestRunProxyClientServerDropsPrivateProtocolSOCKS5UDP(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		token    string
	}{
		{
			name:     "vless",
			protocol: tunnelProtocolVLESS,
			token:    "11111111-1111-4111-8111-111111111111",
		},
		{
			name:     "vmess",
			protocol: tunnelProtocolVMess,
			token:    "22222222-2222-4222-8222-222222222222",
		},
		{
			name:     "trojan",
			protocol: tunnelProtocolTrojan,
			token:    "secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			direct, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := direct.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					t.Errorf("close direct udp listener: %v", err)
				}
			})

			serverAddr := reserveTCPAddr(t)
			serverCtx, serverCancel := context.WithCancel(context.Background())
			defer serverCancel()
			serverErr := make(chan error, 1)
			var serverLog bytes.Buffer
			go func() {
				serverErr <- runProxy(serverCtx, config{
					Mode:            proxyModeServer,
					ListenAddr:      serverAddr,
					Token:           tt.token,
					TunnelProtocol:  tt.protocol,
					TunnelTransport: tunnelTransportRaw,
					Verbose:         true,
					DialTimeout:     time.Second,
					BufferSize:      4096,
				}, &serverLog)
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
					Token:           tt.token,
					TunnelProtocol:  tt.protocol,
					TunnelTransport: tunnelTransportRaw,
					RouteConfigPath: configPath,
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
			if err := udpClient.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, udpBufferSize)
			if _, _, err := udpClient.ReadFromUDP(buf); err == nil || !isTimeoutError(err) {
				t.Fatalf("udp client read error = %v, want timeout", err)
			}
			if err := direct.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := direct.ReadFromUDP(buf); err == nil || !isTimeoutError(err) {
				t.Fatalf("direct udp read error = %v, want timeout", err)
			}

			stopProxy(t, clientCancel, clientErr)
			stopProxy(t, serverCancel, serverErr)

			wantLog := "drop " + tt.protocol + " udp " + net.JoinHostPort("127.0.0.1", strconv.Itoa(int(targetPort)))
			if got := serverLog.String(); !strings.Contains(got, wantLog) {
				t.Fatalf("server log = %q, missing %q", got, wantLog)
			}
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

func TestVLESSHeaderAddonsVisionFlow(t *testing.T) {
	got, err := vlessHeaderAddons("xtls-rprx-vision")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x0a, 0x10}
	want = append(want, []byte("xtls-rprx-vision")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("addons = %x, want %x", got, want)
	}
}

func TestReadVLESSTCPRequestReturnsVisionFlow(t *testing.T) {
	var buf bytes.Buffer
	const token = "11111111-1111-4111-8111-111111111111"
	if err := writeVLESSTCPRequest(&buf, token, "xtls-rprx-vision", "example.com", 443); err != nil {
		t.Fatal(err)
	}
	req, err := readVLESSTCPRequest(&buf, token)
	if err != nil {
		t.Fatal(err)
	}
	if req.flow != "xtls-rprx-vision" {
		t.Fatalf("flow = %q, want xtls-rprx-vision", req.flow)
	}
	if req.host != "example.com" || req.port != 443 {
		t.Fatalf("target = %s:%d, want example.com:443", req.host, req.port)
	}
}

func TestBuildRealityServerConfig(t *testing.T) {
	cfg, err := buildRealityServerConfig(config{
		RealityPrivateKey:  "cMmYlsTT1jdi-LbnzNxsewNbu-NSlFl3CS277gubak8",
		RealityServerNames: []string{"Get.GoStartKit.com"},
		RealityShortIDs:    []string{"", "0a1b"},
		DialTimeout:        time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dest != "get.gostartkit.com:443" {
		t.Fatalf("dest = %q, want get.gostartkit.com:443", cfg.Dest)
	}
	if !cfg.ServerNames["get.gostartkit.com"] {
		t.Fatalf("server names = %#v, want get.gostartkit.com", cfg.ServerNames)
	}
	if !cfg.ShortIds[[8]byte{}] {
		t.Fatalf("empty shortId was not allowed: %#v", cfg.ShortIds)
	}
	var shortID [8]byte
	copy(shortID[:], []byte{0x0a, 0x1b})
	if !cfg.ShortIds[shortID] {
		t.Fatalf("shortId 0a1b was not allowed: %#v", cfg.ShortIds)
	}
}

func TestVisionConnRoundTrip(t *testing.T) {
	left, right := net.Pipe()
	defer func() {
		if err := left.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close left: %v", err)
		}
	}()
	defer func() {
		if err := right.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close right: %v", err)
		}
	}()

	uuid, err := parseUUIDToken("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	writer := newVisionConn(left, uuid, nil)
	reader := newVisionConn(right, uuid, nil)
	writeErr := make(chan error, 1)
	go func() {
		if err := writer.WriteInitialPadding(); err != nil {
			writeErr <- err
			return
		}
		if _, err := writer.Write([]byte("hello")); err != nil {
			writeErr <- err
			return
		}
		writeErr <- nil
	}()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("decoded = %q, want hello", buf)
	}
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("vision writer did not finish")
	}
}

func TestParseRealityShortID(t *testing.T) {
	got, err := parseRealityShortID("0a1b")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0x0a, 0x1b}) {
		t.Fatalf("shortId = %x, want 0a1b", got)
	}
	if _, err := parseRealityShortID("abc"); err == nil {
		t.Fatal("parseRealityShortID accepted odd-length hex")
	}
}

func writeTestCertificateFiles(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
