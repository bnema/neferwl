package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

var sessionCmd = Command{
	Name:    "session",
	Args:    "new|ls [flags]",
	Summary: "Create or list compositor sessions.",
	Setup: func(*flag.FlagSet) Runner {
		return func(ctx context.Context, env Env, args []string) error {
			if len(args) > 0 && args[0] == "new" {
				return sessionNew(ctx, env, args[1:])
			}
			if len(args) == 0 || args[0] != "ls" {
				return usagef("expected session ls")
			}
			fs := flag.NewFlagSet("neferwl session ls", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			asJSON := fs.Bool("json", false, "Print session metadata as JSON.")
			if err := fs.Parse(args[1:]); err != nil {
				return usagef("%v", err)
			}
			if len(fs.Args()) != 0 {
				return usagef("unexpected arguments: %v", fs.Args())
			}
			sessions, err := sessiondir.List(os.Getenv("XDG_RUNTIME_DIR"))
			if err != nil {
				return err
			}
			if *asJSON {
				if sessions == nil {
					sessions = []sessiondir.Meta{}
				}
				enc := json.NewEncoder(env.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(sessions)
			}
			w := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tPROFILE\tPID\tWAYLAND\tCREATED"); err != nil {
				return err
			}
			for _, s := range sessions {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", s.ID, s.Profile, s.PID, s.Wayland, s.Created.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return w.Flush()
		}
	},
}
