package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

// parseSubFlags parses a session subverb's flags. -h/--help prints the
// subverb usage and its flags to stdout and reports help=true.
func parseSubFlags(env Env, fs *flag.FlagSet, args, summary string, argv []string) (help bool, err error) {
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Not printUsage: it lists commands, which include sessionCmd.
			if _, err := fmt.Fprintf(env.Stdout, "usage: neferwl session %s\n\n%s\n\nFlags:\n", args, summary); err != nil {
				return true, err
			}
			fs.SetOutput(env.Stdout)
			fs.PrintDefaults()
			return true, nil
		}
		return false, usagef("%v", err)
	}
	return false, nil
}

var sessionCmd = Command{
	Name:    "session",
	Args:    "new|ls|rm [flags]",
	Summary: "Create, list or remove compositor sessions.",
	Setup: func(*flag.FlagSet) Runner {
		return func(ctx context.Context, env Env, args []string) error {
			if len(args) > 0 && args[0] == "rm" {
				return sessionRemove(ctx, env, args[1:])
			}
			if len(args) > 0 && args[0] == "new" {
				return sessionNew(ctx, env, args[1:])
			}
			if len(args) == 0 || args[0] != "ls" {
				return usagef("expected session ls")
			}
			fs := flag.NewFlagSet("neferwl session ls", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			asJSON := fs.Bool("json", false, "Print session metadata as JSON.")
			if help, err := parseSubFlags(env, fs, "ls [flags]", "List live sessions.", args[1:]); help || err != nil {
				return err
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
