package cli

import (
	"context"
	"flag"
	"os"
	"time"
)

var shotCmd = Command{
	Name: "shot", Args: "[flags]", Summary: "Capture presented outputs to PNG files.",
	Setup: func(fs *flag.FlagSet) Runner {
		target := addTargetFlag(fs)
		outputs := []string{}
		fs.Func("output", "Output name (repeatable; default: all outputs).", func(value string) error {
			if value == "" {
				return usagef("output must not be empty")
			}
			outputs = append(outputs, value)
			return nil
		})
		inline := fs.Bool("inline", false, "Include PNG bytes as base64 in the reply.")
		timeout := fs.Duration("timeout", 5*time.Second, "Maximum completion wait.")
		return func(ctx context.Context, env Env, args []string) error {
			if len(args) != 0 {
				return usagef("shot takes no positional arguments")
			}
			if *timeout <= 0 {
				return usagef("timeout must be positive")
			}
			c, err := dialControl(os.Getenv, *target)
			if err != nil {
				return err
			}
			defer c.conn.Close()
			return printControl(ctx, env, c, map[string]any{"op": "shot", "outputs": outputs, "inline": *inline, "raw": false, "timeout": timeout.String()})
		}
	},
}
