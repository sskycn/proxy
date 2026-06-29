package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"pkg.gostartkit.com/cmd"
)

const version = "v0.1.0"

var errListenerClosedByContext = errors.New("listener closed after context cancellation")

type config struct {
	ListenAddr      string
	GatewayIP       string
	GatewayPort     int
	ConfigPath      string
	DialTimeout     time.Duration
	RefreshInterval time.Duration
	ScanTimeout     time.Duration
	ScanWorkers     int
	BufferSize      int
	Verbose         bool
}

func defaultConfig() config {
	return config{
		ListenAddr:      "127.0.0.1:1080",
		GatewayPort:     1080,
		ConfigPath:      "config.json",
		DialTimeout:     5 * time.Second,
		RefreshInterval: 5 * time.Second,
		ScanTimeout:     250 * time.Millisecond,
		ScanWorkers:     max(64, runtime.GOMAXPROCS(0)*32),
		BufferSize:      32 * 1024,
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := buildApp()
	os.Exit(app.MainDefault(ctx, os.Args[1:]))
}

func buildApp() *cmd.App {
	cfg := defaultConfig()

	app := cmd.NewApp("proxy")
	app.Short = "Local mixed proxy forwarder"
	app.Root = &cmd.Command{
		UsageLine: "proxy [flags]",
		Short:     "forward local mixed proxy traffic to the gateway mixed port",
		Long: "Starts a local TCP listener for mixed proxy clients and forwards each connection " +
			"unchanged to the default gateway's mixed proxy port.",
		Examples: []string{
			"proxy",
			"proxy --listen 127.0.0.1:1081 --gateway-port 1080",
			"proxy --gateway-ip 192.168.1.1",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "local listen address", "l")
			f.StringVar(&cfg.GatewayIP, "gateway-ip", cfg.GatewayIP, "gateway IP; empty means auto-detect", "")
			f.IntVar(&cfg.GatewayPort, "gateway-port", cfg.GatewayPort, "gateway mixed proxy port", "p")
			f.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "JSON route config path; empty disables config loading", "c")
			f.DurationVar(&cfg.DialTimeout, "dial-timeout", cfg.DialTimeout, "upstream dial timeout", "")
			f.DurationVar(&cfg.RefreshInterval, "refresh-interval", cfg.RefreshInterval, "interval for checking local IPv4 changes; 0 disables refresh", "")
			f.DurationVar(&cfg.ScanTimeout, "scan-timeout", cfg.ScanTimeout, "per-IP timeout when scanning local IPv4 networks", "")
			f.IntVar(&cfg.ScanWorkers, "scan-workers", cfg.ScanWorkers, "parallel workers used for IPv4 network scanning", "")
			f.IntVar(&cfg.BufferSize, "buffer-size", cfg.BufferSize, "per-direction copy buffer size in bytes", "")
			f.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "enable debug logs", "v")
		},
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			return runProxy(ctx, cfg, os.Stderr)
		},
	}
	app.AddCommands(&cmd.Command{
		Name:      "version",
		UsageLine: "proxy version",
		Short:     "print version",
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			_, err := fmt.Fprintln(os.Stdout, version)
			return err
		},
	})

	return app
}

type proxyServer struct {
	cfg        config
	resolver   *upstreamResolver
	dialer     net.Dialer
	direct     *directCache
	routes     *routeRules
	bufferPool sync.Pool
	log        io.Writer
}

type directCache struct {
	mu           sync.RWMutex
	upstreamOnly map[string]string
}

func newDirectCache() *directCache {
	return &directCache{upstreamOnly: make(map[string]string)}
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

func runProxy(ctx context.Context, cfg config, log io.Writer) (retErr error) {
	if cfg.GatewayPort <= 0 || cfg.GatewayPort > 65535 {
		return fmt.Errorf("invalid gateway port: %d", cfg.GatewayPort)
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultConfig().DialTimeout
	}
	if cfg.ScanTimeout <= 0 {
		cfg.ScanTimeout = defaultConfig().ScanTimeout
	}
	if cfg.ScanWorkers <= 0 {
		cfg.ScanWorkers = defaultConfig().ScanWorkers
	}
	if cfg.RefreshInterval < 0 {
		return fmt.Errorf("invalid refresh interval: %s", cfg.RefreshInterval)
	}
	if cfg.BufferSize < 4096 {
		cfg.BufferSize = 4096
	}

	configPath, err := resolveConfigPath(cfg.ConfigPath)
	if err != nil {
		return err
	}
	cfg.ConfigPath = configPath

	routes, err := loadRouteRules(cfg.ConfigPath)
	if err != nil {
		return err
	}

	resolver, err := newUpstreamResolver(ctx, cfg, log)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", cfg.ListenAddr)
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

	server := &proxyServer{
		cfg:      cfg,
		resolver: resolver,
		dialer: net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		},
		direct: newDirectCache(),
		routes: routes,
		log:    log,
	}
	server.bufferPool.New = func() any {
		buf := make([]byte, cfg.BufferSize)
		return &buf
	}
	defer func() {
		if err := persistDirectFailures(cfg.ConfigPath, server.direct.upstreamOnlyHosts()); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()

	if err := logf(log, "listening on %s, forwarding mixed traffic to %s\n", listener.Addr(), resolver.target()); err != nil {
		return err
	}

	closeErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr <- err
			return
		}
		closeErr <- errListenerClosedByContext
	}()

	refreshErr := resolver.start(ctx)

	var tempDelay time.Duration
	for {
		select {
		case err := <-refreshErr:
			if err != nil {
				return err
			}
		default:
		}
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
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := time.Second; tempDelay > max {
					tempDelay = max
				}
				time.Sleep(tempDelay)
				continue
			}
			return err
		}
		tempDelay = 0
		go server.handle(ctx, conn)
	}
}

func resolveGatewayIP(ctx context.Context, cfg config, log io.Writer) (net.IP, error) {
	if cfg.GatewayIP != "" {
		gatewayIP := net.ParseIP(cfg.GatewayIP)
		if gatewayIP == nil {
			return nil, fmt.Errorf("invalid gateway IP: %s", cfg.GatewayIP)
		}
		return gatewayIP, nil
	}

	gatewayIP, err := discoverDefaultGateway()
	if err == nil {
		if canConnect(ctx, gatewayIP, cfg.GatewayPort, cfg.DialTimeout) {
			return gatewayIP, nil
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

	ip, err := scanLocalIPv4(ctx, cfg.GatewayPort, cfg.ScanTimeout, cfg.ScanWorkers, gatewayIP)
	if err != nil {
		if gatewayIP != nil {
			return nil, fmt.Errorf("gateway %s:%d is unreachable and scan found no reachable proxy: %w", gatewayIP, cfg.GatewayPort, err)
		}
		return nil, fmt.Errorf("scan local IPv4 networks: %w", err)
	}
	return ip, nil
}

type upstreamResolver struct {
	cfg              config
	log              io.Writer
	mu               sync.RWMutex
	currentTarget    string
	localIPSignature string
}

func newUpstreamResolver(ctx context.Context, cfg config, log io.Writer) (*upstreamResolver, error) {
	signature, err := localIPv4Signature()
	if err != nil {
		return nil, err
	}
	ip, err := resolveGatewayIP(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	return &upstreamResolver{
		cfg:              cfg,
		log:              log,
		currentTarget:    net.JoinHostPort(ip.String(), strconv.Itoa(cfg.GatewayPort)),
		localIPSignature: signature,
	}, nil
}

func (r *upstreamResolver) target() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.currentTarget
}

func (r *upstreamResolver) setTarget(target string) {
	r.mu.Lock()
	r.currentTarget = target
	r.mu.Unlock()
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

	ip, err := resolveGatewayIP(ctx, r.cfg, r.log)
	current := r.target()
	if err != nil {
		if r.cfg.Verbose {
			if logErr := logf(r.log, "refresh upstream failed: %v; keeping %s\n", err, current); logErr != nil {
				return logErr
			}
		}
		return nil
	}

	next := net.JoinHostPort(ip.String(), strconv.Itoa(r.cfg.GatewayPort))
	if next == current {
		return nil
	}

	r.setTarget(next)
	if err := logf(r.log, "upstream changed from %s to %s\n", current, next); err != nil {
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
	if err := tuneTCP(client); err != nil {
		return fmt.Errorf("tune client tcp: %w", err)
	}

	reader := bufio.NewReader(client)
	return s.routeMixed(ctx, client, reader)
}

func (s *proxyServer) connectUpstreamRaw(ctx context.Context) (net.Conn, string, error) {
	target := s.resolver.target()
	upstream, err := s.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, target, err
	}
	if err := tuneTCP(upstream); err != nil {
		if closeErr := upstream.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			return nil, target, errors.Join(fmt.Errorf("tune upstream tcp: %w", err), fmt.Errorf("close upstream after tune failure: %w", closeErr))
		}
		return nil, target, fmt.Errorf("tune upstream tcp: %w", err)
	}
	return upstream, target, nil
}

func (s *proxyServer) proxyViaUpstream(ctx context.Context, client net.Conn, clientReader io.Reader, initial []byte, accessSource string, accessTarget string) error {
	upstream, target, err := s.connectUpstreamRaw(ctx)
	if err != nil {
		if s.cfg.Verbose {
			if logErr := logf(s.log, "dial %s failed for %s: %v\n", target, client.RemoteAddr(), err); logErr != nil {
				return fmt.Errorf("write dial failure log: %w", logErr)
			}
		}
		if logErr := accessLog(s.log, accessSource, target, accessTarget, err.Error()); logErr != nil {
			return logErr
		}
		return nil
	}
	defer func() {
		if err := upstream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			if logErr := logf(s.log, "close upstream %s: %v\n", target, err); logErr != nil {
				return
			}
		}
	}()
	if s.cfg.Verbose {
		if err := logf(s.log, "proxy %s -> %s\n", client.RemoteAddr(), target); err != nil {
			return err
		}
	}
	var src io.Reader = clientReader
	if len(initial) > 0 {
		src = io.MultiReader(bytes.NewReader(initial), clientReader)
	}
	if err := s.bridge(upstream, client, src); err != nil {
		if logErr := accessLog(s.log, accessSource, target, accessTarget, err.Error()); logErr != nil {
			return errors.Join(err, logErr)
		}
		return err
	}
	return accessLog(s.log, accessSource, target, accessTarget, "ok")
}

func (s *proxyServer) bridge(upstream net.Conn, client net.Conn, clientReader io.Reader) error {
	done := make(chan error, 2)
	go s.copyAndClose(upstream, clientReader, done)
	go s.copyAndClose(client, upstream, done)
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

func tuneTCP(conn net.Conn) error {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return nil
	}
	return errors.Join(
		tcp.SetNoDelay(true),
		tcp.SetKeepAlive(true),
		tcp.SetKeepAlivePeriod(30*time.Second),
	)
}

func closeWrite(conn net.Conn) error {
	if tcp, ok := conn.(*net.TCPConn); ok {
		return tcp.CloseWrite()
	}
	return conn.Close()
}

func logf(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func accessLog(w io.Writer, source string, proxy string, target string, status string) error {
	status = strings.ReplaceAll(status, "\n", " ")
	status = strings.ReplaceAll(status, "\r", " ")
	if proxy == "" {
		proxy = "-"
	}
	if proxy == "-" {
		_, err := fmt.Fprintf(w, "%s -> %s %s\n", source, target, status)
		return err
	}
	_, err := fmt.Fprintf(w, "%s -> %s -> %s %s\n", source, proxy, target, status)
	return err
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
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}
