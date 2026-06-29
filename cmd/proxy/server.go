package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	proxypkg "sskycn/proxy"

	"pkg.gostartkit.com/cmd"
)

func buildServerCommand(cfg *proxypkg.Config) *cmd.Command {
	tokenFlag := ""
	transportFlag := ""
	pathFlag := ""
	return &cmd.Command{
		Name:      "server",
		Aliases:   []string{"s", "srv"},
		UsageLine: "proxy server [flags]",
		Short:     "run a custom tunnel server",
		Examples: []string{
			"proxy server --listen 0.0.0.0:9443 --token change-me",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&tokenFlag, "token", tokenFlag, "shared token for custom tunnel auth", "")
			f.StringVar(&transportFlag, "transport", transportFlag, "tunnel transport: raw, ws, h2, or h3 [default: raw]", "")
			f.StringVar(&pathFlag, "tunnel-path", pathFlag, "HTTP/WebSocket tunnel path", "")
			f.StringVar(&cfg.TunnelTLSCert, "tls-cert", cfg.TunnelTLSCert, "TLS certificate file for h2/h3 server", "")
			f.StringVar(&cfg.TunnelTLSKey, "tls-key", cfg.TunnelTLSKey, "TLS private key file for h2/h3 server", "")
			f.BoolVar(&cfg.TunnelMux, "mux", cfg.TunnelMux, "enable tunnel multiplexing", "")
		},
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			cfg.Mode = proxypkg.ProxyModeServer
			if cfg.ListenAddr == proxypkg.DefaultConfig().ListenAddr {
				cfg.ListenAddr = "0.0.0.0:9443"
			}
			if strings.TrimSpace(tokenFlag) != "" {
				cfg.Token = tokenFlag
			} else {
				cfg.Token = ""
			}
			if strings.TrimSpace(transportFlag) != "" {
				cfg.TunnelTransport = transportFlag
			} else {
				cfg.TunnelTransport = ""
			}
			if strings.TrimSpace(pathFlag) != "" {
				cfg.TunnelPath = pathFlag
			} else {
				cfg.TunnelPath = ""
			}
			return proxypkg.RunProxy(ctx, *cfg, os.Stderr)
		},
	}
}
