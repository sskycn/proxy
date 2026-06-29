package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	proxypkg "sskycn/proxy"

	"pkg.gostartkit.com/cmd"
)

func buildClientCommand(cfg *proxypkg.Config) *cmd.Command {
	serverAddrFlag := ""
	tokenFlag := ""
	protocolFlag := ""
	transportFlag := ""
	securityFlag := ""
	flowFlag := ""
	pathFlag := ""
	tlsServerNameFlag := ""
	return &cmd.Command{
		Name:      "client",
		Aliases:   []string{"c", "cli"},
		UsageLine: "proxy client [flags]",
		Short:     "run local mixed proxy and forward upstream traffic through a tunnel server",
		Examples: []string{
			"proxy client --server-addr 203.0.113.10:9443 --token change-me",
			"proxy client --listen 127.0.0.1:1081 --server-addr 203.0.113.10:9443",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&serverAddrFlag, "server-addr", serverAddrFlag, "custom tunnel server address", "")
			f.StringVar(&tokenFlag, "token", tokenFlag, "shared token, VLESS/VMess UUID, or Trojan password", "")
			f.StringVar(&protocolFlag, "tunnel-protocol", protocolFlag, "tunnel protocol: custom, vless, vmess, or trojan [default: custom]", "")
			f.StringVar(&transportFlag, "transport", transportFlag, "tunnel transport: raw, ws, h2, or h3 [default: raw]", "")
			f.StringVar(&securityFlag, "tunnel-security", securityFlag, "tunnel security: none or reality [default: none]", "")
			f.StringVar(&flowFlag, "flow", flowFlag, "VLESS flow, for example xtls-rprx-vision", "")
			f.StringVar(&pathFlag, "tunnel-path", pathFlag, "HTTP/WebSocket tunnel path", "")
			f.BoolVar(&cfg.TunnelTLS, "tls", cfg.TunnelTLS, "use TLS for ws/h2 transport", "")
			f.StringVar(&tlsServerNameFlag, "tls-server-name", tlsServerNameFlag, "TLS server name override", "")
			f.BoolVar(&cfg.TunnelTLSInsecure, "tls-insecure", cfg.TunnelTLSInsecure, "skip TLS certificate verification", "")
			f.StringVar(&cfg.RealityServerName, "reality-server-name", cfg.RealityServerName, "REALITY serverName", "")
			f.StringVar(&cfg.RealityFingerprint, "reality-fingerprint", cfg.RealityFingerprint, "REALITY uTLS fingerprint, for example chrome", "")
			f.StringVar(&cfg.RealityPublicKey, "reality-public-key", cfg.RealityPublicKey, "REALITY publicKey/password", "")
			f.StringVar(&cfg.RealityShortID, "reality-short-id", cfg.RealityShortID, "REALITY shortId hex", "")
			f.StringVar(&cfg.RealitySpiderX, "reality-spider-x", cfg.RealitySpiderX, "REALITY spiderX path", "")
			f.BoolVar(&cfg.TunnelMux, "mux", cfg.TunnelMux, "enable tunnel multiplexing", "")
		},
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			cfg.Mode = proxypkg.ProxyModeClient
			if strings.TrimSpace(serverAddrFlag) != "" {
				cfg.ServerAddr = serverAddrFlag
			} else {
				cfg.ServerAddr = ""
			}
			if strings.TrimSpace(tokenFlag) != "" {
				cfg.Token = tokenFlag
			} else {
				cfg.Token = ""
			}
			if strings.TrimSpace(protocolFlag) != "" {
				cfg.TunnelProtocol = protocolFlag
			} else {
				cfg.TunnelProtocol = ""
			}
			if strings.TrimSpace(transportFlag) != "" {
				cfg.TunnelTransport = transportFlag
			} else {
				cfg.TunnelTransport = ""
			}
			if strings.TrimSpace(securityFlag) != "" {
				cfg.TunnelSecurity = securityFlag
			} else {
				cfg.TunnelSecurity = ""
			}
			if strings.TrimSpace(flowFlag) != "" {
				cfg.TunnelFlow = flowFlag
			} else {
				cfg.TunnelFlow = ""
			}
			if strings.TrimSpace(pathFlag) != "" {
				cfg.TunnelPath = pathFlag
			} else {
				cfg.TunnelPath = ""
			}
			if strings.TrimSpace(tlsServerNameFlag) != "" {
				cfg.TunnelTLSServerName = tlsServerNameFlag
			} else {
				cfg.TunnelTLSServerName = ""
			}
			return proxypkg.RunProxy(ctx, *cfg, os.Stderr)
		},
	}
}
