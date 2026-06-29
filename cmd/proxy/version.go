package main

import (
	"context"
	"fmt"
	"os"

	proxypkg "sskycn/proxy"

	"pkg.gostartkit.com/cmd"
)

func buildVersionCommand() *cmd.Command {
	return &cmd.Command{
		Name:      "version",
		Aliases:   []string{"v", "ver"},
		UsageLine: "proxy version",
		Short:     "print version",
		Run: func(ctx context.Context, c *cmd.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			_, err := fmt.Fprintln(os.Stdout, proxypkg.Version)
			return err
		},
	}
}
