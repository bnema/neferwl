package cli

import (
	"context"
	"flag"
	"os"
	"path/filepath"

	"github.com/bnema/neferwl/internal/domain"
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
		every := fs.Bool("every-frame", false, "Write each presented frame until stopped.")
		stop := fs.Bool("stop", false, "Stop continuous capture.")
		dir := fs.String("dir", "", "Continuous capture directory.")
		raw := fs.Bool("raw", false, "Also write raw PQ frames (requires --every-frame).")
		inline := fs.Bool("inline", false, "Include PNG bytes as base64 in the reply.")
		timeout := fs.Duration("timeout", domain.DefaultControlTimeout, "Maximum completion wait.")
		return func(ctx context.Context, env Env, args []string) (runErr error) {
			if len(args) != 0 {
				return usagef("shot takes no positional arguments")
			}
			if (*raw && !*every) || (*every && *stop) || (*inline && (*every || *stop)) || (*dir != "" && !*every) {
				return usagef("invalid continuous capture flags")
			}
			if err := checkControlTimeout(*timeout); err != nil {
				return err
			}
			// The session resolves paths in its own working directory; send an
			// absolute one.
			if *dir != "" {
				abs, err := filepath.Abs(*dir)
				if err != nil {
					return err
				}
				*dir = abs
			}
			c, err := dialControl(os.Getenv, *target)
			if err != nil {
				return err
			}
			defer c.closeInto(&runErr)
			if *every || *stop {
				return printControl(ctx, env, c, map[string]any{"op": "shot", "outputs": outputs, "every_frame": *every, "stop": *stop, "dir": *dir, "raw": *raw, "timeout": timeout.String()})
			}
			return printControl(ctx, env, c, map[string]any{"op": "shot", "outputs": outputs, "inline": *inline, "raw": false, "timeout": timeout.String()})
		}
	},
}
