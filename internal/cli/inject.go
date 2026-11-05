package cli

import (
	"context"
	"flag"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

func injectCommand(name, args, summary string) Command {
	return Command{Name: name, Args: args + " [flags]", Summary: summary, Setup: func(fs *flag.FlagSet) Runner {
		target := addTargetFlag(fs)
		wf := addWaitFlags(fs)
		return func(ctx context.Context, env Env, args []string) error {
			positional, err := injectArguments(fs, args)
			if err != nil {
				return err
			}
			req, err := injectRequest(name, positional)
			if err != nil {
				return err
			}
			if err := wf.apply(req); err != nil {
				return err
			}
			c, err := dialControl(os.Getenv, *target)
			if err != nil {
				return err
			}
			defer c.conn.Close()
			return printControl(ctx, env, c, req)
		}
	}}
}

// waitFlags are the completion flags shared by do and the input verbs.
type waitFlags struct {
	wait, until *string
	timeout     *time.Duration
	shot        *bool
}

func addWaitFlags(fs *flag.FlagSet) waitFlags {
	return waitFlags{
		wait:    fs.String("wait", "settled", "Completion level: committed, settled or presented."),
		until:   fs.String("until", "", "State predicate to wait for, e.g. 'window-mapped app_id=foot'."),
		timeout: fs.Duration("timeout", domain.DefaultControlTimeout, "Maximum completion wait."),
		shot:    fs.Bool("shot", false, "Capture all outputs once the result is presented (implies --wait=presented)."),
	}
}

// apply validates the flags and adds them to a do or inject request.
func checkControlTimeout(d time.Duration) error {
	if d <= 0 {
		return usagef("timeout must be positive")
	}
	if d > domain.MaxControlTimeout {
		return usagef("timeout must not exceed 5m")
	}
	return nil
}

func (w waitFlags) apply(req map[string]any) error {
	if _, err := domain.ParseWaitLevel(*w.wait); err != nil {
		return usagef("%v", err)
	}
	if *w.until != "" {
		if _, err := domain.ParseUntil(*w.until); err != nil {
			return usagef("%v", err)
		}
		req["until"] = *w.until
	}
	if err := checkControlTimeout(*w.timeout); err != nil {
		return err
	}
	req["wait"], req["timeout"] = *w.wait, w.timeout.String()
	if *w.shot {
		req["shot"] = true
	}
	return nil
}

// injectArguments accepts flags after positional arguments. -- ends option
// parsing, for text or coordinates that start with a dash.
func injectArguments(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for n := 0; n < len(args); n++ {
		arg := args[n]
		if arg == "--" {
			return append(positional, args[n+1:]...), nil
		}
		if !strings.HasPrefix(arg, "--") && arg != "-s" && !strings.HasPrefix(arg, "-s=") {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if fs.Lookup(name) == nil {
			return nil, usagef("unknown flag --%s", name)
		}
		if b, ok := fs.Lookup(name).Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() && !hasValue {
			value, hasValue = "true", true
		}
		if !hasValue {
			n++
			if n == len(args) {
				return nil, usagef("--%s needs a value", name)
			}
			value = args[n]
		}
		if err := fs.Set(name, value); err != nil {
			return nil, usagef("%v", err)
		}
	}
	return positional, nil
}

func injectRequest(name string, args []string) (map[string]any, error) {
	req := map[string]any{"op": "inject", "kind": name}
	switch name {
	case "key", "press", "release":
		if len(args) != 1 || args[0] == "" {
			return nil, usagef("expected one key combination")
		}
		req["combo"] = args[0]
	case "type":
		if len(args) != 1 {
			return nil, usagef("expected one text argument")
		}
		req["text"] = args[0]
	case "click":
		if len(args) > 1 {
			return nil, usagef("expected at most one button")
		}
		if len(args) == 1 && args[0] != "left" && args[0] != "right" && args[0] != "middle" {
			return nil, usagef("unknown button")
		}
		req["kind"], req["cmd"], req["args"] = "pointer", "click", args
	case "move":
		if len(args) != 2 {
			return nil, usagef("expected x y")
		}
		for _, arg := range args {
			v, err := strconv.ParseFloat(arg, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, usagef("invalid coordinate")
			}
		}
		req["kind"], req["cmd"], req["args"] = "pointer", "move", args
	case "swipe":
		if len(args) != 2 || args[0] != "3" && args[0] != "4" {
			return nil, usagef("expected 3|4 and up|down|left|right")
		}
		switch args[1] {
		case "up", "down", "left", "right":
		default:
			return nil, usagef("invalid swipe direction")
		}
		fingers, _ := strconv.Atoi(args[0])
		req["fingers"], req["dir"] = fingers, args[1]
	}
	return req, nil
}

var (
	keyCmd     = injectCommand("key", "<combo>", "Press and release a key combination over the control socket.")
	typeCmd    = injectCommand("type", "<text>", "Type text over the control socket.")
	pressCmd   = injectCommand("press", "<combo>", "Press keys without releasing them.")
	releaseCmd = injectCommand("release", "<combo>", "Release keys in reverse order.")
	clickCmd   = injectCommand("click", "[left|right|middle]", "Click a pointer button over the control socket.")
	moveCmd    = injectCommand("move", "<x> <y>", "Move the pointer to global logical coordinates.")
	swipeCmd   = injectCommand("swipe", "<3|4> <up|down|left|right>", "Inject a touchpad swipe over the control socket.")
)
