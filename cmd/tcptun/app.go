package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"sskycn/tcptun"

	"pkg.gostartkit.com/cmd"
)

func buildApp() *cmd.App {
	cfg := tcptun.DefaultConfig()
	upstreamProtocolFlag := ""

	app := cmd.NewApp("tcptun")
	app.Short = "Local mixed tcptun forwarder"
	app.Root = &cmd.Command{
		UsageLine: "tcptun [flags]",
		Short:     "forward local mixed tcptun traffic through the gateway tcptun port",
		Long: "Starts a local TCP listener for mixed tcptun clients and forwards each connection " +
			"through the default gateway's tcptun port. Upstream protocol defaults to SOCKS5.",
		Examples: []string{
			"tcptun",
			"tcptun local",
			"tcptun --listen 127.0.0.1:1081 --gateway-port 1080",
			"tcptun --gateway-ip 192.168.1.1",
			"tcptun client --server-addr 203.0.113.10:9443",
			"tcptun server --listen 0.0.0.0:9443",
			"tcptun --upstream-protocol mixed",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "local listen address", "l")
			f.StringVar(&cfg.GatewayIP, "gateway-ip", cfg.GatewayIP, "gateway IP; empty means auto-detect", "")
			f.IntVar(&cfg.GatewayPort, "gateway-port", cfg.GatewayPort, "gateway tcptun port", "p")
			f.StringVar(&upstreamProtocolFlag, "upstream-protocol", upstreamProtocolFlag, "upstream protocol: socks5 or mixed [default: socks5]", "")
			f.StringVar(&cfg.SOCKS5Username, "socks5-username", cfg.SOCKS5Username, "local SOCKS5 username; enables username/password auth when set with username or password", "")
			f.StringVar(&cfg.SOCKS5Password, "socks5-password", cfg.SOCKS5Password, "local SOCKS5 password", "")
			f.StringVar(&cfg.UpstreamSOCKS5Username, "upstream-socks5-username", cfg.UpstreamSOCKS5Username, "upstream SOCKS5 username", "")
			f.StringVar(&cfg.UpstreamSOCKS5Password, "upstream-socks5-password", cfg.UpstreamSOCKS5Password, "upstream SOCKS5 password", "")
			f.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "JSON runtime config path; defaults: config.json, client.json, or server.json by mode; empty disables runtime config loading", "c")
			f.StringVar(&cfg.RouteConfigPath, "route-config", cfg.RouteConfigPath, "JSON route config path; empty disables route loading and write-back", "")
			f.DurationVar(&cfg.DialTimeout, "dial-timeout", cfg.DialTimeout, "upstream dial timeout", "")
			f.DurationVar(&cfg.DirectProbeTimeout, "direct-probe-timeout", cfg.DirectProbeTimeout, "timeout waiting for direct target response before falling back upstream", "")
			f.DurationVar(&cfg.RefreshInterval, "refresh-interval", cfg.RefreshInterval, "interval for checking local IPv4 changes; 0 disables refresh", "")
			f.DurationVar(&cfg.ScanTimeout, "scan-timeout", cfg.ScanTimeout, "per-IP timeout when scanning local IPv4 networks", "")
			f.DurationVar(&cfg.ScanRetryInterval, "scan-retry-interval", cfg.ScanRetryInterval, "pause before retrying local IPv4 network scanning after no tcptun is found", "")
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
			return tcptun.RunProxy(ctx, cfg, os.Stderr)
		},
	}
	app.AddCommands(buildLocalCommand(&cfg, &upstreamProtocolFlag), buildClientCommand(&cfg), buildServerCommand(&cfg), buildConfigCommand(), buildVersionCommand())

	return app
}

func applyModeConfigPathDefault(cfg *tcptun.Config, defaultPath string) {
	if cfg == nil {
		return
	}
	if hasExplicitConfigPathFlag(os.Args[1:]) {
		return
	}
	if strings.TrimSpace(cfg.ConfigPath) == tcptun.DefaultConfig().ConfigPath {
		cfg.ConfigPath = defaultPath
	}
}

func hasExplicitConfigPathFlag(args []string) bool {
	for _, arg := range args {
		name, ok := configFlagName(arg)
		if !ok {
			continue
		}
		if name == "config" || name == "c" {
			return true
		}
	}
	return false
}
