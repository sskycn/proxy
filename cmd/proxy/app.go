package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	proxypkg "sskycn/proxy"

	"pkg.gostartkit.com/cmd"
)

func buildApp() *cmd.App {
	cfg := proxypkg.DefaultConfig()
	upstreamProtocolFlag := ""

	app := cmd.NewApp("proxy")
	app.Short = "Local mixed proxy forwarder"
	app.Root = &cmd.Command{
		UsageLine: "proxy [flags]",
		Short:     "forward local mixed proxy traffic through the gateway proxy port",
		Long: "Starts a local TCP listener for mixed proxy clients and forwards each connection " +
			"through the default gateway's proxy port. Upstream protocol defaults to SOCKS5.",
		Examples: []string{
			"proxy",
			"proxy local",
			"proxy --listen 127.0.0.1:1081 --gateway-port 1080",
			"proxy --gateway-ip 192.168.1.1",
			"proxy client --server-addr 203.0.113.10:9443",
			"proxy server --listen 0.0.0.0:9443",
			"proxy --upstream-protocol mixed",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "local listen address", "l")
			f.StringVar(&cfg.GatewayIP, "gateway-ip", cfg.GatewayIP, "gateway IP; empty means auto-detect", "")
			f.IntVar(&cfg.GatewayPort, "gateway-port", cfg.GatewayPort, "gateway proxy port", "p")
			f.StringVar(&upstreamProtocolFlag, "upstream-protocol", upstreamProtocolFlag, "upstream protocol: socks5 or mixed [default: socks5]", "")
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
			cfg.Mode = ""
			cfg.TunnelProtocol = ""
			cfg.TunnelTransport = ""
			cfg.TunnelPath = ""
			if strings.TrimSpace(upstreamProtocolFlag) != "" {
				cfg.UpstreamProtocol = upstreamProtocolFlag
			} else {
				cfg.UpstreamProtocol = ""
			}
			return proxypkg.RunProxy(ctx, cfg, os.Stderr)
		},
	}
	app.AddCommands(buildLocalCommand(&cfg, &upstreamProtocolFlag), buildClientCommand(&cfg), buildServerCommand(&cfg), buildVersionCommand())

	return app
}
