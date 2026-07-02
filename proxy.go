package tcptun

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	Version                = "v0.1.0"
	ProxyModeLocal         = "local"
	ProxyModeClient        = "client"
	ProxyModeServer        = "server"
	UpstreamProtocolSOCKS5 = "socks5"
	UpstreamProtocolMixed  = "mixed"
	TunnelTransportRaw     = "raw"
	TunnelTransportWS      = "ws"
	TunnelTransportH2      = "h2"
	TunnelTransportH3      = "h3"
	TunnelProtocolNative   = "native"
	TunnelProtocolVLESS    = "vless"
	TunnelProtocolVMess    = "vmess"
	TunnelProtocolTrojan   = "trojan"
	TunnelSecurityNone     = "none"
	TunnelSecurityReality  = "reality"

	version                = Version
	proxyModeLocal         = ProxyModeLocal
	proxyModeClient        = ProxyModeClient
	proxyModeServer        = ProxyModeServer
	upstreamProtocolSOCKS5 = UpstreamProtocolSOCKS5
	upstreamProtocolMixed  = UpstreamProtocolMixed
	tunnelTransportRaw     = TunnelTransportRaw
	tunnelTransportWS      = TunnelTransportWS
	tunnelTransportH2      = TunnelTransportH2
	tunnelTransportH3      = TunnelTransportH3
	tunnelProtocolNative   = TunnelProtocolNative
	tunnelProtocolVLESS    = TunnelProtocolVLESS
	tunnelProtocolVMess    = TunnelProtocolVMess
	tunnelProtocolTrojan   = TunnelProtocolTrojan
	tunnelSecurityNone     = TunnelSecurityNone
	tunnelSecurityReality  = TunnelSecurityReality
)

var (
	errListenerClosedByContext = errors.New("listener closed after context cancellation")
	logMu                      sync.Mutex
)

type Config struct {
	ListenAddr             string
	ListenAddrs            []string
	Mode                   string
	ServerAddr             string
	Token                  string
	TunnelProtocol         string
	TunnelTransport        string
	TunnelPath             string
	TunnelTLS              bool
	TunnelTLSCert          string
	TunnelTLSKey           string
	TunnelTLSServerName    string
	TunnelTLSInsecure      bool
	TunnelSecurity         string
	TunnelFlow             string
	RealityServerName      string
	RealityServerNames     []string
	RealityFingerprint     string
	RealityPublicKey       string
	RealityPrivateKey      string
	RealityShortID         string
	RealityShortIDs        []string
	RealityDest            string
	RealitySpiderX         string
	TunnelMux              bool
	GatewayIP              string
	GatewayPort            int
	UpstreamProtocol       string
	SOCKS5Username         string
	SOCKS5Password         string
	UpstreamSOCKS5Username string
	UpstreamSOCKS5Password string
	ConfigPath             string
	RouteConfigPath        string
	DialTimeout            time.Duration
	DirectProbeTimeout     time.Duration
	RefreshInterval        time.Duration
	ScanTimeout            time.Duration
	ScanRetryInterval      time.Duration
	HeartbeatInterval      time.Duration
	ConnectionIdleTimeout  time.Duration
	UDPSessionTimeout      time.Duration
	RetryInitialInterval   time.Duration
	RetryMaxInterval       time.Duration
	ScanWorkers            int
	BufferSize             int
	Verbose                bool
}

type config = Config

func DefaultConfig() Config {
	return defaultConfig()
}

func defaultConfig() Config {
	return Config{
		ListenAddr:            "127.0.0.1:1080",
		ListenAddrs:           []string{"127.0.0.1:1080"},
		Mode:                  proxyModeLocal,
		TunnelProtocol:        tunnelProtocolNative,
		TunnelSecurity:        tunnelSecurityNone,
		TunnelTransport:       tunnelTransportRaw,
		TunnelPath:            "/proxy",
		TunnelMux:             true,
		GatewayPort:           1080,
		UpstreamProtocol:      upstreamProtocolSOCKS5,
		ConfigPath:            "config.json",
		RouteConfigPath:       "route.json",
		DialTimeout:           5 * time.Second,
		DirectProbeTimeout:    500 * time.Millisecond,
		RefreshInterval:       5 * time.Second,
		ScanTimeout:           250 * time.Millisecond,
		ScanRetryInterval:     5 * time.Second,
		HeartbeatInterval:     2 * time.Minute,
		ConnectionIdleTimeout: 10 * time.Minute,
		UDPSessionTimeout:     2 * time.Minute,
		RetryInitialInterval:  200 * time.Millisecond,
		RetryMaxInterval:      30 * time.Second,
		ScanWorkers:           max(64, runtime.GOMAXPROCS(0)*32),
		BufferSize:            32 * 1024,
	}
}

func applyModeListenDefault(cfg *config) {
	if cfg == nil {
		return
	}
	if len(normalizedListenAddrs("", cfg.ListenAddrs)) > 0 {
		return
	}
	if cfg.Mode == proxyModeServer {
		if strings.TrimSpace(cfg.ListenAddr) != "" {
			return
		}
		cfg.ListenAddr = "0.0.0.0:9443"
		cfg.ListenAddrs = []string{cfg.ListenAddr}
		return
	}
	if strings.TrimSpace(cfg.ListenAddr) != "" {
		cfg.ListenAddrs = []string{cfg.ListenAddr}
		return
	}
	cfg.ListenAddr = defaultConfig().ListenAddr
	cfg.ListenAddrs = []string{cfg.ListenAddr}
}

func normalizedListenAddrs(listenAddr string, listenAddrs []string) []string {
	candidates := listenAddrs
	if len(candidates) == 0 {
		candidates = splitCommaSeparated(listenAddr)
	}
	seen := make(map[string]struct{}, len(candidates))
	addrs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		addrs = append(addrs, trimmed)
	}
	return addrs
}

func splitCommaSeparated(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func configListenAddrs(cfg config) ([]string, error) {
	addrs := normalizedListenAddrs(cfg.ListenAddr, cfg.ListenAddrs)
	if len(addrs) == 0 {
		return nil, errors.New("listen address is required")
	}
	return addrs, nil
}

func listenConfigIsUnsetOrDefault(cfg config) bool {
	addrs := normalizedListenAddrs(cfg.ListenAddr, cfg.ListenAddrs)
	if len(addrs) == 0 {
		return true
	}
	return stringSlicesEqual(addrs, defaultConfig().ListenAddrs)
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

type proxyServer struct {
	cfg        config
	resolver   *upstreamResolver
	dialer     net.Dialer
	direct     *directCache
	sticky     *upstreamSticky
	routes     *routeRules
	local      *localSourceSet
	bufferPool sync.Pool
	log        io.Writer
	mux        tunnelMuxClient
	xrayMux    xrayMuxClient
	reality    *realityServer
}

type directCache struct {
	mu           sync.RWMutex
	upstreamOnly map[string]string
}

type upstreamSticky struct {
	mu      sync.RWMutex
	targets map[string]string
}

type localSourceSet struct {
	ips map[netip.Addr]struct{}
}

func newDirectCache() *directCache {
	return &directCache{upstreamOnly: make(map[string]string)}
}

func newUpstreamSticky() *upstreamSticky {
	return &upstreamSticky{targets: make(map[string]string)}
}

func (s *upstreamSticky) get(source string) string {
	if s == nil || strings.TrimSpace(source) == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.targets[source]
}

func (s *upstreamSticky) set(source string, target string) {
	if s == nil || strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
		return
	}
	s.mu.Lock()
	s.targets[source] = target
	s.mu.Unlock()
}

func (s *upstreamSticky) clear(source string, target string) {
	if s == nil || strings.TrimSpace(source) == "" {
		return
	}
	s.mu.Lock()
	if current := s.targets[source]; current == target || target == "" {
		delete(s.targets, source)
	}
	s.mu.Unlock()
}

func (c *directCache) shouldTry(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, blocked := c.upstreamOnly[key]
	return !blocked
}

func (c *directCache) markUpstreamOnly(key string, host string) {
	c.mu.Lock()
	c.upstreamOnly[key] = normalizeTargetHost(host)
	c.mu.Unlock()
}

func (c *directCache) upstreamOnlyHosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := make(map[string]struct{}, len(c.upstreamOnly))
	hosts := make([]string, 0, len(c.upstreamOnly))
	for _, host := range c.upstreamOnly {
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts
}

func RunProxy(ctx context.Context, cfg Config, log io.Writer) error {
	return runProxy(ctx, cfg, log)
}

func runProxy(ctx context.Context, cfg config, log io.Writer) (retErr error) {
	if cfg.GatewayPort == 0 {
		cfg.GatewayPort = defaultConfig().GatewayPort
	}
	if cfg.GatewayPort < 0 || cfg.GatewayPort > 65535 {
		return fmt.Errorf("invalid gateway port: %d", cfg.GatewayPort)
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultConfig().DialTimeout
	}
	if cfg.RefreshInterval < 0 {
		return fmt.Errorf("invalid refresh interval: %s", cfg.RefreshInterval)
	}
	if cfg.BufferSize < 4096 {
		cfg.BufferSize = 4096
	}
	if err := validateSOCKS5Credentials("socks5", cfg.SOCKS5Username, cfg.SOCKS5Password); err != nil {
		return err
	}
	if err := validateSOCKS5Credentials("upstream socks5", cfg.UpstreamSOCKS5Username, cfg.UpstreamSOCKS5Password); err != nil {
		return err
	}

	configPath, err := resolveConfigPath(cfg.ConfigPath)
	if err != nil {
		return err
	}
	cfg.ConfigPath = configPath
	routeConfigPath, err := resolveConfigPath(cfg.RouteConfigPath)
	if err != nil {
		return err
	}
	cfg.RouteConfigPath = routeConfigPath

	if err := applyRuntimeConfigDefaults(&cfg); err != nil {
		return err
	}
	if cfg.DirectProbeTimeout < 0 {
		return fmt.Errorf("invalid direct probe timeout: %s", cfg.DirectProbeTimeout)
	}
	if cfg.DirectProbeTimeout == 0 {
		cfg.DirectProbeTimeout = defaultConfig().DirectProbeTimeout
	}
	if cfg.ScanTimeout < 0 {
		return fmt.Errorf("invalid scan timeout: %s", cfg.ScanTimeout)
	}
	if cfg.ScanTimeout == 0 {
		cfg.ScanTimeout = defaultConfig().ScanTimeout
	}
	if cfg.ScanRetryInterval < 0 {
		return fmt.Errorf("invalid scan retry interval: %s", cfg.ScanRetryInterval)
	}
	if cfg.ScanRetryInterval == 0 {
		cfg.ScanRetryInterval = defaultConfig().ScanRetryInterval
	}
	if cfg.HeartbeatInterval < 0 {
		return fmt.Errorf("invalid heartbeat interval: %s", cfg.HeartbeatInterval)
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultConfig().HeartbeatInterval
	}
	if cfg.ConnectionIdleTimeout < 0 {
		return fmt.Errorf("invalid connection idle timeout: %s", cfg.ConnectionIdleTimeout)
	}
	if cfg.ConnectionIdleTimeout == 0 {
		cfg.ConnectionIdleTimeout = defaultConfig().ConnectionIdleTimeout
	}
	if cfg.UDPSessionTimeout < 0 {
		return fmt.Errorf("invalid udp session timeout: %s", cfg.UDPSessionTimeout)
	}
	if cfg.UDPSessionTimeout == 0 {
		cfg.UDPSessionTimeout = defaultConfig().UDPSessionTimeout
	}
	if cfg.RetryInitialInterval < 0 {
		return fmt.Errorf("invalid retry initial interval: %s", cfg.RetryInitialInterval)
	}
	if cfg.RetryInitialInterval == 0 {
		cfg.RetryInitialInterval = defaultConfig().RetryInitialInterval
	}
	if cfg.RetryMaxInterval < 0 {
		return fmt.Errorf("invalid retry max interval: %s", cfg.RetryMaxInterval)
	}
	if cfg.RetryMaxInterval == 0 {
		cfg.RetryMaxInterval = defaultConfig().RetryMaxInterval
	}
	if cfg.RetryInitialInterval > cfg.RetryMaxInterval {
		return fmt.Errorf("retry initial interval %s exceeds max interval %s", cfg.RetryInitialInterval, cfg.RetryMaxInterval)
	}
	if cfg.ScanWorkers <= 0 {
		cfg.ScanWorkers = defaultConfig().ScanWorkers
	}
	cfg.Mode, err = normalizeProxyMode(cfg.Mode)
	if err != nil {
		return err
	}
	applyModeListenDefault(&cfg)
	listenAddrs, err := configListenAddrs(cfg)
	if err != nil {
		return err
	}
	cfg.UpstreamProtocol, err = normalizeUpstreamProtocol(cfg.UpstreamProtocol)
	if err != nil {
		return err
	}
	cfg.TunnelTransport, err = normalizeTunnelTransport(cfg.TunnelTransport)
	if err != nil {
		return err
	}
	cfg.TunnelProtocol, err = normalizeTunnelProtocol(cfg.TunnelProtocol)
	if err != nil {
		return err
	}
	cfg.TunnelSecurity, err = normalizeTunnelSecurity(cfg.TunnelSecurity)
	if err != nil {
		return err
	}
	if cfg.TunnelSecurity == tunnelSecurityReality && (cfg.Mode != proxyModeClient && cfg.Mode != proxyModeServer || cfg.TunnelTransport != tunnelTransportRaw || cfg.TunnelProtocol != tunnelProtocolVLESS) {
		return errors.New("REALITY tunnel security requires client/server mode with raw transport and vless protocol")
	}
	if cfg.TunnelSecurity == tunnelSecurityReality && cfg.TunnelTLS {
		return errors.New("REALITY tunnel security cannot be combined with tunnel TLS")
	}
	cfg.TunnelPath = normalizeTunnelPath(cfg.TunnelPath)
	if cfg.Mode == proxyModeClient && strings.TrimSpace(cfg.ServerAddr) == "" {
		return errors.New("server address is required in client mode")
	}
	if cfg.Mode == proxyModeServer {
		return runTunnelServer(ctx, cfg, log)
	}

	routes, err := loadRouteRules(cfg.RouteConfigPath)
	if err != nil {
		return err
	}
	localSources, err := systemLocalSourceSet()
	if err != nil {
		return err
	}

	var resolver *upstreamResolver
	if cfg.Mode == proxyModeLocal {
		resolver, err = newUpstreamResolver(ctx, cfg, log)
		if err != nil {
			return err
		}
	}

	server := &proxyServer{
		cfg:      cfg,
		resolver: resolver,
		dialer: net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: cfg.HeartbeatInterval,
		},
		direct: newDirectCache(),
		sticky: newUpstreamSticky(),
		routes: routes,
		local:  localSources,
		log:    log,
	}
	server.bufferPool.New = func() any {
		buf := make([]byte, cfg.BufferSize)
		return &buf
	}
	defer func() {
		if cfg.Mode != proxyModeServer {
			if err := persistDirectFailures(cfg.RouteConfigPath, server.direct.upstreamOnlyHosts()); err != nil {
				retErr = errors.Join(retErr, err)
			}
		}
	}()

	var refreshErr <-chan error
	if resolver != nil {
		refreshErr = resolver.start(ctx)
	}
	return runProxyOnAddrs(ctx, cfg, server, listenAddrs, refreshErr, log)
}

func runProxyOnAddrs(ctx context.Context, cfg config, server *proxyServer, listenAddrs []string, refreshErr <-chan error, log io.Writer) error {
	if len(listenAddrs) == 0 {
		return errors.New("listen address is required")
	}

	type listenResult struct {
		addr string
		err  error
	}

	errCh := make(chan listenResult, len(listenAddrs))
	var wg sync.WaitGroup
	for _, listenAddr := range listenAddrs {
		addr := listenAddr
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- listenResult{addr: addr, err: runProxyOnAddr(ctx, cfg, server, addr, log)}
		}()
	}
	go func() {
		wg.Wait()
		close(errCh)
	}()

	var retErr error
	active := len(listenAddrs)
	for active > 0 {
		select {
		case err := <-refreshErr:
			if err != nil {
				return err
			}
		case result, ok := <-errCh:
			if !ok {
				active = 0
				break
			}
			active--
			if result.err == nil {
				continue
			}
			if ctx.Err() != nil {
				if logErr := logf(log, "listener %s stopped after context cancellation: %v\n", result.addr, result.err); logErr != nil {
					retErr = errors.Join(retErr, logErr)
				}
				continue
			}
			err := fmt.Errorf("listener %s stopped: %w", result.addr, result.err)
			retErr = errors.Join(retErr, err)
			if logErr := logf(log, "%v\n", err); logErr != nil {
				retErr = errors.Join(retErr, logErr)
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return retErr
}

func runProxyOnAddr(ctx context.Context, cfg config, server *proxyServer, listenAddr string, log io.Writer) error {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(log, "close listener: %v\n", err); logErr != nil {
				return
			}
		}
	}()

	if cfg.Mode == proxyModeClient {
		if err := logf(log, "listening on %s, forwarding mixed traffic to tunnel server %s\n", listener.Addr(), cfg.ServerAddr); err != nil {
			return err
		}
	} else {
		if err := logf(log, "listening on %s, forwarding mixed traffic to %s via %s\n", listener.Addr(), server.upstreamTargetSummary(), cfg.UpstreamProtocol); err != nil {
			return err
		}
	}

	closeErr := make(chan error, 1)
	var active activeConnTracker
	defer active.closeAllAndWait()
	go func() {
		<-ctx.Done()
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr <- err
			return
		}
		active.closeAll()
		closeErr <- errListenerClosedByContext
	}()

	var tempDelay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				select {
				case closeErrValue := <-closeErr:
					if closeErrValue != nil && !errors.Is(closeErrValue, errListenerClosedByContext) {
						return closeErrValue
					}
				default:
				}
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = cfg.RetryInitialInterval
				} else {
					tempDelay *= 2
				}
				if tempDelay > cfg.RetryMaxInterval {
					tempDelay = cfg.RetryMaxInterval
				}
				if err := sleepContext(ctx, tempDelay); err != nil {
					return nil
				}
				continue
			}
			return err
		}
		tempDelay = 0
		active.Go(conn, func() {
			server.handle(ctx, conn)
		})
	}
}

func validateSOCKS5Credentials(name string, username string, password string) error {
	if len(username) > 255 {
		return fmt.Errorf("%s username is too long: %d bytes", name, len(username))
	}
	if len(password) > 255 {
		return fmt.Errorf("%s password is too long: %d bytes", name, len(password))
	}
	return nil
}

func normalizeUpstreamProtocol(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", upstreamProtocolSOCKS5:
		return upstreamProtocolSOCKS5, nil
	case upstreamProtocolMixed:
		return upstreamProtocolMixed, nil
	default:
		return "", fmt.Errorf("invalid upstream protocol %q; supported values: %s, %s", value, upstreamProtocolSOCKS5, upstreamProtocolMixed)
	}
}

func normalizeProxyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", proxyModeLocal:
		return proxyModeLocal, nil
	case proxyModeClient:
		return proxyModeClient, nil
	case proxyModeServer:
		return proxyModeServer, nil
	default:
		return "", fmt.Errorf("invalid mode %q; supported values: %s, %s, %s", value, proxyModeLocal, proxyModeClient, proxyModeServer)
	}
}

func normalizeTunnelTransport(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", tunnelTransportRaw:
		return tunnelTransportRaw, nil
	case "websocket", tunnelTransportWS:
		return tunnelTransportWS, nil
	case "http2", tunnelTransportH2:
		return tunnelTransportH2, nil
	case "http3", tunnelTransportH3:
		return tunnelTransportH3, nil
	default:
		return "", fmt.Errorf("invalid tunnel transport %q; supported values: %s, %s, %s, %s", value, tunnelTransportRaw, tunnelTransportWS, tunnelTransportH2, tunnelTransportH3)
	}
}

func normalizeTunnelProtocol(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", tunnelProtocolNative:
		return tunnelProtocolNative, nil
	case tunnelProtocolVLESS:
		return tunnelProtocolVLESS, nil
	case tunnelProtocolVMess:
		return tunnelProtocolVMess, nil
	case tunnelProtocolTrojan:
		return tunnelProtocolTrojan, nil
	default:
		return "", fmt.Errorf("invalid tunnel protocol %q; supported values: %s, %s, %s, %s", value, tunnelProtocolNative, tunnelProtocolVLESS, tunnelProtocolVMess, tunnelProtocolTrojan)
	}
}

func normalizeTunnelSecurity(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", tunnelSecurityNone:
		return tunnelSecurityNone, nil
	case tunnelSecurityReality:
		return tunnelSecurityReality, nil
	default:
		return "", fmt.Errorf("invalid tunnel security %q; supported values: %s, %s", value, tunnelSecurityNone, tunnelSecurityReality)
	}
}

func normalizeTunnelPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultConfig().TunnelPath
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return "/" + trimmed
}

func (s *proxyServer) upstreamTarget() string {
	if s.cfg.Mode == proxyModeClient {
		return s.cfg.ServerAddr
	}
	if s.resolver == nil {
		return ""
	}
	return s.resolver.target()
}

func (s *proxyServer) upstreamTargetSummary() string {
	if s.cfg.Mode == proxyModeClient {
		return s.cfg.ServerAddr
	}
	if s.resolver == nil {
		return ""
	}
	return s.resolver.targetSummary()
}

func upstreamStickySource(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	switch value := addr.(type) {
	case *net.TCPAddr:
		return value.IP.String()
	case *net.UDPAddr:
		return value.IP.String()
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil && strings.TrimSpace(host) != "" {
		return trimHostBrackets(host)
	}
	return addr.String()
}

func systemLocalSourceSet() (*localSourceSet, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return &localSourceSet{ips: map[netip.Addr]struct{}{}}, nil
	}

	set := &localSourceSet{ips: make(map[netip.Addr]struct{}, len(interfaces)*2)}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := addrIP(addr)
			if ip == nil {
				continue
			}
			parsed, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}
			set.ips[parsed.Unmap()] = struct{}{}
		}
	}
	return set, nil
}

func addrIP(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPAddr:
		return value.IP
	case *net.IPNet:
		return value.IP
	case *net.TCPAddr:
		return value.IP
	case *net.UDPAddr:
		return value.IP
	default:
		return nil
	}
}

func (s *proxyServer) shouldSelectRouteForSource(addr net.Addr) bool {
	if s == nil || s.local == nil {
		return false
	}
	return s.local.contains(addr)
}

func (s *localSourceSet) contains(addr net.Addr) bool {
	if s == nil || addr == nil {
		return false
	}
	ip := addrIP(addr)
	if ip == nil {
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return false
		}
		parsed, err := netip.ParseAddr(trimHostBrackets(host))
		if err != nil {
			return false
		}
		return s.containsIP(parsed)
	}
	parsed, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return s.containsIP(parsed)
}

func (s *localSourceSet) containsIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	_, ok := s.ips[ip]
	return ok
}

type upstreamCandidate struct {
	target  string
	latency time.Duration
}

func resolveGatewayTargets(ctx context.Context, cfg config, log io.Writer) ([]upstreamCandidate, error) {
	if cfg.GatewayIP != "" {
		gatewayIP := net.ParseIP(cfg.GatewayIP)
		if gatewayIP == nil {
			return nil, fmt.Errorf("invalid gateway IP: %s", cfg.GatewayIP)
		}
		return []upstreamCandidate{{target: net.JoinHostPort(gatewayIP.String(), strconv.Itoa(cfg.GatewayPort))}}, nil
	}

	hasInternalIPv4, err := hasLocalInternalIPv4()
	if err != nil {
		return nil, err
	}
	if !hasInternalIPv4 {
		return nil, errors.New("local internal IPv4 address not found; automatic gateway proxy discovery disabled")
	}

	gatewayIP, err := discoverDefaultGateway()
	if err == nil {
		target := net.JoinHostPort(gatewayIP.String(), strconv.Itoa(cfg.GatewayPort))
		if latency, ok := canConnectTargetLatency(ctx, target, cfg.DialTimeout); ok {
			return []upstreamCandidate{{target: target, latency: latency}}, nil
		}
		if cfg.Verbose {
			if err := logf(log, "gateway %s:%d is not reachable, scanning local IPv4 networks\n", gatewayIP, cfg.GatewayPort); err != nil {
				return nil, err
			}
		}
	} else {
		if cfg.Verbose {
			if err := logf(log, "discover gateway IP failed: %v; scanning local IPv4 networks\n", err); err != nil {
				return nil, err
			}
		}
	}

	reachable, err := scanLocalIPv4WithRetry(ctx, cfg, gatewayIP, log, scanLocalIPv4All)
	if err != nil {
		if gatewayIP != nil {
			return nil, fmt.Errorf("gateway %s:%d is unreachable and scan found no reachable proxy: %w", gatewayIP, cfg.GatewayPort, err)
		}
		return nil, fmt.Errorf("scan local IPv4 networks: %w", err)
	}
	candidates := make([]upstreamCandidate, 0, len(reachable))
	for _, item := range reachable {
		if item.ip == nil {
			continue
		}
		candidates = append(candidates, upstreamCandidate{
			target:  net.JoinHostPort(item.ip.String(), strconv.Itoa(cfg.GatewayPort)),
			latency: item.latency,
		})
	}
	if len(candidates) == 0 {
		return nil, errReachableProxyNotFound
	}
	return candidates, nil
}

type localIPv4Scanner func(context.Context, int, time.Duration, int, net.IP) ([]reachableTCPTun, error)

func scanLocalIPv4WithRetry(ctx context.Context, cfg config, gatewayHint net.IP, log io.Writer, scanner localIPv4Scanner) ([]reachableTCPTun, error) {
	if scanner == nil {
		return nil, errors.New("local IPv4 scanner is nil")
	}
	retry := newRetryBackoff(cfg.ScanRetryInterval, max(cfg.ScanRetryInterval, cfg.RetryMaxInterval))
	for {
		reachable, err := scanner(ctx, cfg.GatewayPort, cfg.ScanTimeout, cfg.ScanWorkers, gatewayHint)
		if err == nil {
			return reachable, nil
		}
		if !errors.Is(err, errReachableProxyNotFound) {
			return nil, err
		}
		delay := retry.Next()
		if cfg.Verbose {
			if logErr := logf(log, "scan local IPv4 networks found no reachable proxy; retrying in %s\n", delay); logErr != nil {
				return nil, logErr
			}
		}
		if err := sleepContext(ctx, delay); err != nil {
			return nil, err
		}
	}
}

type retryBackoff struct {
	next time.Duration
	max  time.Duration
}

func newRetryBackoff(initial time.Duration, maxDelay time.Duration) *retryBackoff {
	if initial <= 0 {
		initial = defaultConfig().RetryInitialInterval
	}
	if maxDelay <= 0 {
		maxDelay = initial
	}
	if initial > maxDelay {
		initial = maxDelay
	}
	return &retryBackoff{next: initial, max: maxDelay}
}

func (b *retryBackoff) Next() time.Duration {
	if b == nil {
		return 0
	}
	delay := b.next
	if delay <= 0 {
		delay = defaultConfig().RetryInitialInterval
	}
	next := delay * 2
	if next < delay || next > b.max {
		next = b.max
	}
	b.next = next
	return delay
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type upstreamResolver struct {
	cfg              config
	log              io.Writer
	mu               sync.RWMutex
	targets          map[string]upstreamTargetState
	localIPSignature string
}

type upstreamTargetState struct {
	target   string
	latency  time.Duration
	failures int
}

type upstreamTargetSnapshot struct {
	target   string
	latency  time.Duration
	failures int
}

func newUpstreamResolver(ctx context.Context, cfg config, log io.Writer) (*upstreamResolver, error) {
	signature, err := localIPv4Signature()
	if err != nil {
		return nil, err
	}
	candidates, err := resolveGatewayTargets(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	resolver := &upstreamResolver{
		cfg:              cfg,
		log:              log,
		targets:          make(map[string]upstreamTargetState, len(candidates)),
		localIPSignature: signature,
	}
	resolver.setTargets(candidates)
	return resolver, nil
}

func (r *upstreamResolver) target() string {
	targets := r.orderedTargets()
	if len(targets) == 0 {
		return ""
	}
	return targets[0].target
}

func (r *upstreamResolver) targetSummary() string {
	targets := r.orderedTargets()
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, target.target)
	}
	return strings.Join(parts, ",")
}

func (r *upstreamResolver) orderedTargets() []upstreamTargetSnapshot {
	r.mu.RLock()
	targets := make([]upstreamTargetSnapshot, 0, len(r.targets))
	for _, target := range r.targets {
		targets = append(targets, upstreamTargetSnapshot{
			target:   target.target,
			latency:  target.latency,
			failures: target.failures,
		})
	}
	r.mu.RUnlock()

	sort.SliceStable(targets, func(i, j int) bool {
		left := r.targetScore(targets[i])
		right := r.targetScore(targets[j])
		if left != right {
			return left < right
		}
		return targets[i].target < targets[j].target
	})
	return targets
}

func (r *upstreamResolver) orderedTargetsFor(preferred string) []upstreamTargetSnapshot {
	targets := r.orderedTargets()
	if strings.TrimSpace(preferred) == "" || len(targets) < 2 {
		return targets
	}
	for i, target := range targets {
		if target.target != preferred {
			continue
		}
		if i == 0 {
			return targets
		}
		copy(targets[1:i+1], targets[0:i])
		targets[0] = target
		return targets
	}
	return targets
}

func (r *upstreamResolver) targetScore(target upstreamTargetSnapshot) time.Duration {
	base := target.latency
	if base <= 0 {
		base = r.cfg.DialTimeout
		if base <= 0 {
			base = defaultConfig().DialTimeout
		}
	}
	failures := target.failures
	if failures > 100 {
		failures = 100
	}
	penaltyBase := r.cfg.DialTimeout
	if penaltyBase <= 0 {
		penaltyBase = defaultConfig().DialTimeout
	}
	return base + time.Duration(failures)*penaltyBase
}

func (r *upstreamResolver) setTargets(candidates []upstreamCandidate) {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]upstreamTargetState, len(candidates))
	for _, candidate := range candidates {
		target := strings.TrimSpace(candidate.target)
		if target == "" {
			continue
		}
		state, ok := r.targets[target]
		if !ok {
			state = upstreamTargetState{target: target}
		}
		if candidate.latency > 0 {
			state.latency = candidate.latency
		}
		next[target] = state
	}
	r.targets = next
}

func (r *upstreamResolver) recordSuccess(target string, latency time.Duration) {
	if r == nil || strings.TrimSpace(target) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.targets[target]
	if !ok {
		state = upstreamTargetState{target: target}
	}
	state.failures = 0
	if latency > 0 {
		if state.latency <= 0 {
			state.latency = latency
		} else {
			state.latency = (state.latency*3 + latency) / 4
		}
	}
	r.targets[target] = state
}

func (r *upstreamResolver) recordFailure(target string) {
	if r == nil || strings.TrimSpace(target) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.targets[target]
	if !ok {
		state = upstreamTargetState{target: target}
	}
	state.failures++
	r.targets[target] = state
}

func (r *upstreamResolver) setLocalIPSignature(signature string) {
	r.mu.Lock()
	r.localIPSignature = signature
	r.mu.Unlock()
}

func (r *upstreamResolver) localSignature() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.localIPSignature
}

func (r *upstreamResolver) start(ctx context.Context) <-chan error {
	if r.cfg.RefreshInterval == 0 {
		return nil
	}

	errCh := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(r.cfg.RefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.refresh(ctx); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}
	}()
	return errCh
}

func (r *upstreamResolver) refresh(ctx context.Context) error {
	signature, err := localIPv4Signature()
	if err != nil {
		if r.cfg.Verbose {
			if logErr := logf(r.log, "refresh local IP check failed: %v; keeping %s\n", err, r.target()); logErr != nil {
				return logErr
			}
		}
		return nil
	}
	if signature == r.localSignature() {
		return nil
	}
	r.setLocalIPSignature(signature)

	candidates, err := resolveGatewayTargets(ctx, r.cfg, r.log)
	current := r.targetSummary()
	if err != nil {
		if r.cfg.Verbose {
			if logErr := logf(r.log, "refresh upstream failed: %v; keeping %s\n", err, current); logErr != nil {
				return logErr
			}
		}
		return nil
	}

	r.setTargets(candidates)
	next := r.targetSummary()
	if next == current {
		return nil
	}

	if err := logf(r.log, "upstreams changed from %s to %s\n", current, next); err != nil {
		return err
	}
	return nil
}

func (s *proxyServer) handle(ctx context.Context, client net.Conn) {
	if err := s.handleConn(ctx, client); err != nil && s.cfg.Verbose {
		if logErr := logf(s.log, "connection error for %s: %v\n", client.RemoteAddr(), err); logErr != nil {
			return
		}
	}
}

func (s *proxyServer) handleConn(ctx context.Context, client net.Conn) error {
	defer func() {
		if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(s.log, "close client %s: %v\n", client.RemoteAddr(), err); logErr != nil {
				return
			}
		}
	}()
	if err := tuneTCP(client, s.cfg.HeartbeatInterval); err != nil {
		return fmt.Errorf("tune client tcp: %w", err)
	}

	reader := bufio.NewReader(client)
	return s.routeMixed(ctx, client, reader)
}

func (s *proxyServer) connectUpstreamRaw(ctx context.Context, source string) (net.Conn, string, error) {
	if s.cfg.Mode == proxyModeClient {
		return nil, s.cfg.ServerAddr, errors.New("raw upstream is unsupported in client mode")
	}
	if s.resolver == nil {
		return nil, "", errors.New("upstream resolver is nil")
	}
	preferred := s.sticky.get(source)
	targets := s.resolver.orderedTargetsFor(preferred)
	if len(targets) == 0 {
		return nil, "", errors.New("no upstream targets available")
	}

	var errs []error
	for _, candidate := range targets {
		upstream, err := s.connectUpstreamRawTarget(ctx, candidate.target)
		if err != nil {
			s.sticky.clear(source, candidate.target)
			errs = append(errs, err)
			if ctx.Err() != nil {
				return nil, candidate.target, errors.Join(errs...)
			}
			continue
		}
		s.sticky.set(source, candidate.target)
		return upstream, candidate.target, nil
	}
	return nil, targets[0].target, errors.Join(errs...)
}

func (s *proxyServer) connectUpstreamRawTarget(ctx context.Context, target string) (net.Conn, error) {
	start := time.Now()
	upstream, err := s.dialer.DialContext(ctx, "tcp", target)
	latency := time.Since(start)
	if err != nil {
		s.resolver.recordFailure(target)
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	if err := tuneTCP(upstream, s.cfg.HeartbeatInterval); err != nil {
		s.resolver.recordFailure(target)
		if closeErr := upstream.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			return nil, fmt.Errorf("%s: %w", target, errors.Join(fmt.Errorf("tune upstream tcp: %w", err), fmt.Errorf("close upstream after tune failure: %w", closeErr)))
		}
		return nil, fmt.Errorf("%s: tune upstream tcp: %w", target, err)
	}
	s.resolver.recordSuccess(target, latency)
	return upstream, nil
}

func (s *proxyServer) bridge(upstream net.Conn, client net.Conn, clientReader io.Reader) error {
	return s.bridgeWithReaders(upstream, client, upstream, clientReader)
}

func (s *proxyServer) bridgeWithReaders(upstream net.Conn, client net.Conn, upstreamReader io.Reader, clientReader io.Reader) error {
	done := make(chan error, 2)
	go s.copyAndClose(upstream, idleReader(client, clientReader, s.cfg.ConnectionIdleTimeout), done)
	go s.copyAndClose(client, idleReader(upstream, upstreamReader, s.cfg.ConnectionIdleTimeout), done)
	if err := <-done; err != nil && !isExpectedNetworkClose(err) {
		return err
	}
	return nil
}

func (s *proxyServer) copyAndClose(dst net.Conn, src io.Reader, done chan<- error) {
	bufPtr := s.bufferPool.Get().(*[]byte)
	_, copyErr := io.CopyBuffer(dst, src, *bufPtr)
	s.bufferPool.Put(bufPtr)
	closeErr := closeWrite(dst)
	done <- errors.Join(copyErr, closeErr)
}

func tuneTCP(conn net.Conn, keepAliveInterval time.Duration) error {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return nil
	}
	if keepAliveInterval <= 0 {
		return tcp.SetNoDelay(true)
	}
	return errors.Join(tcp.SetNoDelay(true), tcp.SetKeepAlive(true), tcp.SetKeepAlivePeriod(keepAliveInterval))
}

type readDeadlineReader struct {
	conn    net.Conn
	reader  io.Reader
	timeout time.Duration
}

type activeConnTracker struct {
	mu    sync.Mutex
	wg    sync.WaitGroup
	conns map[net.Conn]struct{}
}

func (t *activeConnTracker) Go(conn net.Conn, fn func()) {
	t.mu.Lock()
	if t.conns == nil {
		t.conns = make(map[net.Conn]struct{})
	}
	t.conns[conn] = struct{}{}
	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer func() {
			t.mu.Lock()
			delete(t.conns, conn)
			t.mu.Unlock()
			t.wg.Done()
		}()
		fn()
	}()
}

func (t *activeConnTracker) closeAll() {
	t.mu.Lock()
	conns := make([]net.Conn, 0, len(t.conns))
	for conn := range t.conns {
		conns = append(conns, conn)
	}
	t.mu.Unlock()
	for _, conn := range conns {
		closeConnQuiet(conn)
	}
}

func (t *activeConnTracker) closeAllAndWait() {
	t.closeAll()
	t.wg.Wait()
}

func idleReader(conn net.Conn, reader io.Reader, timeout time.Duration) io.Reader {
	if conn == nil || reader == nil || timeout <= 0 {
		return reader
	}
	return readDeadlineReader{conn: conn, reader: reader, timeout: timeout}
}

func (r readDeadlineReader) Read(p []byte) (int, error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(r.timeout)); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func refreshUDPReadDeadline(conn *net.UDPConn, timeout time.Duration) error {
	if conn == nil || timeout <= 0 {
		return nil
	}
	return conn.SetReadDeadline(time.Now().Add(timeout))
}

func waitUDPProxyDone(ctx context.Context, conn net.Conn, udpConn *net.UDPConn, done <-chan error) error {
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	closeConnQuiet(udpConn)
	closeConnQuiet(conn)

	timer := time.NewTimer(time.Second)
	select {
	case secondErr := <-done:
		err = errors.Join(err, secondErr)
	case <-timer.C:
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	return err
}

func closeWrite(conn net.Conn) error {
	if tcp, ok := conn.(*net.TCPConn); ok {
		return tcp.CloseWrite()
	}
	return conn.Close()
}

func closeConnQuiet(conn net.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Close()
}

func logf(w io.Writer, format string, args ...any) error {
	logMu.Lock()
	defer logMu.Unlock()
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func accessLog(w io.Writer, source string, tcptun string, target string, status string) error {
	status = strings.ReplaceAll(status, "\n", " ")
	status = strings.ReplaceAll(status, "\r", " ")
	target = strings.TrimSpace(target)
	if target == "" {
		target = "unknown"
	}
	if tcptun == "" {
		tcptun = "-"
	}
	if tcptun == "-" {
		_, err := fmt.Fprintf(w, "%s -> %s %s\n", source, target, status)
		return err
	}
	_, err := fmt.Fprintf(w, "%s -> %s -> %s %s\n", source, tcptun, target, status)
	return err
}

func accessTarget(host string, port string) string {
	host = trimHostBrackets(strings.TrimSpace(host))
	port = strings.TrimSpace(port)
	if host == "" {
		if port == "" {
			return "unknown"
		}
		return ":" + port
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func accessSource(protocol string, addr net.Addr) string {
	if addr == nil {
		return protocol + "/unknown"
	}
	return protocol + "/" + friendlyAddr(addr.String())
}

func friendlyAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	host = trimHostBrackets(host)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

func isExpectedNetworkClose(err error) bool {
	if err == nil {
		return true
	}
	if isNetworkTimeout(err) {
		return true
	}
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}

func isNetworkTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
