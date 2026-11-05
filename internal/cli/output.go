package cli

import (
	"context"
	"flag"
	"os"
)

var outputCmd = Command{Name: "output", Args: "add MODE [--name NAME] | rm NAME", Summary: "Manage runtime virtual outputs.", Setup: func(fs *flag.FlagSet) Runner {
	target := addTargetFlag(fs)
	name := fs.String("name", "", "Name for a new output.")
	return func(ctx context.Context, env Env, args []string) error {
		positional, err := injectArguments(fs, args)
		if err != nil {
			return err
		}
		if len(positional) != 2 || (positional[0] != "add" && positional[0] != "rm") {
			return usagef("expected output add MODE or output rm NAME")
		}
		req := map[string]any{}
		if positional[0] == "add" {
			req["op"], req["mode"], req["name"] = "output-add", positional[1], *name
		} else {
			req["op"], req["name"] = "output-rm", positional[1]
		}
		c, err := dialControl(os.Getenv, *target)
		if err != nil {
			return err
		}
		defer c.conn.Close()
		return printControl(ctx, env, c, req)
	}
}}
