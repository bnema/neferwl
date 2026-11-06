package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/domain"
)

var validateConfigCmd = Command{
	Name:    "validate-config",
	Args:    "[path]",
	Summary: "Check a config file without starting the compositor.",
	Setup: func(*flag.FlagSet) Runner {
		return func(_ context.Context, env Env, args []string) error {
			if len(args) > 1 {
				return usagef("expected at most one path, got %d", len(args))
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			path, _, warnings, err := loadConfig(path)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				if _, err := fmt.Fprintf(env.Stderr, "%s:%s\n", path, w); err != nil {
					return err
				}
			}
			if len(warnings) > 0 {
				return fmt.Errorf("%d warning(s)", len(warnings))
			}
			_, err = fmt.Fprintf(env.Stdout, "ok: %s\n", path)
			return err
		}
	},
}

// loadConfig reads the config at path, or the XDG default when path is
// empty; a missing default file gives the defaults. It returns the path read.
func loadConfig(path string) (string, domain.Config, []config.Warning, error) {
	if path == "" {
		cfg, warnings, err := config.LoadDefault()
		return config.DefaultPath(), cfg, warnings, err
	}
	cfg, warnings, err := config.Load(path)
	return path, cfg, warnings, err
}

// stateCmd prefers control for plain state queries (printed bare, in the state
// file shape), falling back to the file
// named by $NEFERWL_STATE, else the one of $WAYLAND_DISPLAY. output-of
// retains its statefile contract.
var stateCmd = Command{
	Name:    "state",
	Args:    "[output-of <pid>]",
	Summary: "Print the session state, or the output showing a process's window, as JSON.",
	Setup: func(fs *flag.FlagSet) Runner {
		target := addTargetFlag(fs)
		return func(ctx context.Context, env Env, args []string) (runErr error) {
			pid := 0
			switch {
			case len(args) == 0:
			case len(args) == 2 && args[0] == "output-of":
				n, err := strconv.Atoi(args[1])
				if err != nil || n <= 0 {
					return usagef("invalid pid %q", args[1])
				}
				pid = n
			default:
				return usagef("unexpected arguments: %v", args)
			}
			if pid == 0 {
				if c, err := dialControl(os.Getenv, *target); err == nil {
					defer c.closeInto(&runErr)
					return printControlState(ctx, env, c)
				} else if *target != "" || os.Getenv("NEFERWL_SESSION") != "" || os.Getenv("NEFERWL_CONTROL") != "" {
					return err
				}
			}
			path := os.Getenv(statefile.Env)
			if path == "" {
				var err error
				if path, err = statefile.Path(os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("WAYLAND_DISPLAY")); err != nil {
					return err
				}
			}
			st, err := statefile.Read(path)
			if err != nil {
				return err
			}
			var out any = st
			if pid != 0 {
				if out, err = statefile.OutputOf(st, pid); err != nil {
					return err
				}
			}
			data, err := json.Marshal(out)
			if err != nil {
				return err
			}
			_, err = env.Stdout.Write(append(data, '\n'))
			return err
		}
	},
}

var versionCmd = Command{
	Name:    "version",
	Summary: "Print the neferwl version.",
	Setup: func(*flag.FlagSet) Runner {
		return func(_ context.Context, env Env, args []string) error {
			if len(args) != 0 {
				return usagef("unexpected arguments: %v", args)
			}
			_, err := fmt.Fprintln(env.Stdout, buildVersion(env.Version))
			return err
		}
	},
}

// buildVersion prefers the packaged version, then the module version.
func buildVersion(packaged string) string {
	if packaged != "" {
		return packaged
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}
