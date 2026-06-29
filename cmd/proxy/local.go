package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	proxypkg "sskycn/proxy"

	"pkg.gostartkit.com/cmd"
)

func buildLocalCommand(cfg *proxypkg.Config, upstreamProtocolFlag *string) *cmd.Command {
	return &cmd.Command{
		Name:      "local",
		Aliases:   []string{"l", "loc"},
		UsageLine: "proxy local [flags]",
		Short:     "run local mixed proxy through the gateway proxy",
		Examples: []string{
			"proxy local",
			"proxy local --listen 127.0.0.1:1081 --gateway-port 1080",
			"proxy local --gateway-ip 192.168.1.1",
			"proxy local --upstream-protocol mixed",
		},
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			cfg.Mode = proxypkg.ProxyModeLocal
			if upstreamProtocolFlag != nil && strings.TrimSpace(*upstreamProtocolFlag) != "" {
				cfg.UpstreamProtocol = *upstreamProtocolFlag
			} else {
				cfg.UpstreamProtocol = ""
			}
			return proxypkg.RunProxy(ctx, *cfg, os.Stderr)
		},
	}
}
