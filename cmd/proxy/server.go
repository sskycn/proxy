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
	protocolFlag := ""
	transportFlag := ""
	securityFlag := ""
	flowFlag := ""
	pathFlag := ""
	serverNamesFlag := ""
	shortIDsFlag := ""
	return &cmd.Command{
		Name:      "server",
		Aliases:   []string{"s", "srv"},
		UsageLine: "proxy server [flags]",
		Short:     "run a custom tunnel server",
		Examples: []string{
			"proxy server --listen 0.0.0.0:9443 --token change-me",
		},
		SetFlags: func(f *cmd.FlagSet) {
			f.StringVar(&tokenFlag, "token", tokenFlag, "shared token, VLESS/VMess UUID, or Trojan password", "")
			f.StringVar(&protocolFlag, "tunnel-protocol", protocolFlag, "tunnel protocol: custom, vless, vmess, or trojan [default: custom]", "")
			f.StringVar(&transportFlag, "transport", transportFlag, "tunnel transport: raw, ws, h2, or h3 [default: raw]", "")
			f.StringVar(&securityFlag, "tunnel-security", securityFlag, "tunnel security: none or reality [default: none]", "")
			f.StringVar(&flowFlag, "flow", flowFlag, "VLESS flow, for example xtls-rprx-vision", "")
			f.StringVar(&pathFlag, "tunnel-path", pathFlag, "HTTP/WebSocket tunnel path", "")
			f.StringVar(&cfg.TunnelTLSCert, "tls-cert", cfg.TunnelTLSCert, "TLS certificate file for raw/ws/h2/h3 server", "")
			f.StringVar(&cfg.TunnelTLSKey, "tls-key", cfg.TunnelTLSKey, "TLS private key file for raw/ws/h2/h3 server", "")
			f.StringVar(&cfg.RealityPrivateKey, "reality-private-key", cfg.RealityPrivateKey, "REALITY privateKey", "")
			f.StringVar(&serverNamesFlag, "reality-server-names", serverNamesFlag, "comma-separated REALITY serverNames", "")
			f.StringVar(&shortIDsFlag, "reality-short-ids", shortIDsFlag, "comma-separated REALITY shortIds in hex", "")
			f.StringVar(&cfg.RealityDest, "reality-dest", cfg.RealityDest, "REALITY fallback destination host:port", "")
			f.BoolVar(&cfg.TunnelMux, "mux", cfg.TunnelMux, "enable tunnel multiplexing", "")
		},
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			cfg.Mode = proxypkg.ProxyModeServer
			applyModeConfigPathDefault(cfg, "server.json")
			if cfg.ListenAddr == proxypkg.DefaultConfig().ListenAddr {
				cfg.ListenAddr = ""
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
			if strings.TrimSpace(serverNamesFlag) != "" {
				cfg.RealityServerNames = splitCommaList(serverNamesFlag)
			}
			if strings.TrimSpace(shortIDsFlag) != "" {
				cfg.RealityShortIDs = splitCommaList(shortIDsFlag)
			}
			return proxypkg.RunProxy(ctx, *cfg, os.Stderr)
		},
	}
}

func splitCommaList(value string) []string {
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
