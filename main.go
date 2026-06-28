package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"pkg.gostartkit.com/cmd"
)

const version = "v0.1.0"

type config struct {
	ListenAddr  string
	GatewayIP   string
	GatewayPort int
	DialTimeout time.Duration
	BufferSize  int
	Verbose     bool
}

func defaultConfig() config {
	return config{
		ListenAddr:  "127.0.0.1:1080",
		GatewayPort: 1080,
		DialTimeout: 5 * time.Second,
		BufferSize:  32 * 1024,
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
			f.DurationVar(&cfg.DialTimeout, "dial-timeout", cfg.DialTimeout, "upstream dial timeout", "")
			f.IntVar(&cfg.BufferSize, "buffer-size", cfg.BufferSize, "per-direction copy buffer size in bytes", "")
			f.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "enable connection logs", "v")
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
			fmt.Fprintln(os.Stdout, version)
			return nil
		},
	})

	return app
}

type proxyServer struct {
	cfg        config
	target     string
	dialer     net.Dialer
	bufferPool sync.Pool
	log        io.Writer
}

func runProxy(ctx context.Context, cfg config, log io.Writer) error {
	if cfg.GatewayPort <= 0 || cfg.GatewayPort > 65535 {
		return fmt.Errorf("invalid gateway port: %d", cfg.GatewayPort)
	}
	if cfg.BufferSize < 4096 {
		cfg.BufferSize = 4096
	}

	gatewayIP := net.ParseIP(cfg.GatewayIP)
	if gatewayIP == nil {
		ip, err := discoverDefaultGateway()
		if err != nil {
			return fmt.Errorf("discover gateway IP: %w", err)
		}
		gatewayIP = ip
	}

	target := net.JoinHostPort(gatewayIP.String(), strconv.Itoa(cfg.GatewayPort))
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &proxyServer{
		cfg:    cfg,
		target: target,
		dialer: net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		},
		log: log,
	}
	server.bufferPool.New = func() any {
		buf := make([]byte, cfg.BufferSize)
		return &buf
	}

	fmt.Fprintf(log, "listening on %s, forwarding mixed traffic to %s\n", listener.Addr(), target)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var tempDelay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
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

func (s *proxyServer) handle(ctx context.Context, client net.Conn) {
	defer client.Close()
	tuneTCP(client)

	upstream, err := s.dialer.DialContext(ctx, "tcp", s.target)
	if err != nil {
		if s.cfg.Verbose {
			fmt.Fprintf(s.log, "dial %s failed for %s: %v\n", s.target, client.RemoteAddr(), err)
		}
		return
	}
	defer upstream.Close()
	tuneTCP(upstream)

	if s.cfg.Verbose {
		fmt.Fprintf(s.log, "proxy %s -> %s\n", client.RemoteAddr(), s.target)
	}

	done := make(chan struct{}, 2)
	go s.copyAndClose(upstream, client, done)
	go s.copyAndClose(client, upstream, done)
	<-done
}

func (s *proxyServer) copyAndClose(dst net.Conn, src net.Conn, done chan<- struct{}) {
	bufPtr := s.bufferPool.Get().(*[]byte)
	_, _ = io.CopyBuffer(dst, src, *bufPtr)
	s.bufferPool.Put(bufPtr)
	closeWrite(dst)
	done <- struct{}{}
}

func tuneTCP(conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetNoDelay(true)
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(30 * time.Second)
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = conn.Close()
}
