package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

// controlClient is a sequential JSON-lines client, owned by its caller.
type controlClient struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	id   uint64
}

type controlResponse map[string]json.RawMessage

func dialControl(env func(string) string) (*controlClient, error) {
	path := env("NEFERWL_CONTROL")
	if path == "" {
		runtime, display := env("XDG_RUNTIME_DIR"), env("WAYLAND_DISPLAY")
		if runtime == "" || display == "" {
			return nil, errors.New("NEFERWL_CONTROL or XDG_RUNTIME_DIR and WAYLAND_DISPLAY must be set")
		}
		path = filepath.Join(runtime, "neferwl", display+".control.sock")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket %s: %w", path, err)
	}
	return &controlClient{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}, nil
}

func (c *controlClient) call(ctx context.Context, req map[string]any) (controlResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Cancellation interrupts real socket I/O, including contexts without a deadline.
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetDeadline(time.Now()); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
		_ = c.conn.SetDeadline(time.Time{})
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	c.id++
	request := make(map[string]any, len(req)+1)
	for k, v := range req {
		request[k] = v
	}
	request["id"] = c.id
	if err := c.enc.Encode(request); err != nil {
		return nil, err
	}
	var reply controlResponse
	if err := c.dec.Decode(&reply); err != nil {
		return nil, err
	}
	var id uint64
	if err := json.Unmarshal(reply["id"], &id); err != nil || id != c.id {
		return nil, errors.New("control reply id mismatch")
	}
	return reply, nil
}

func printControl(ctx context.Context, env Env, c *controlClient, req map[string]any) error {
	reply, err := c.call(ctx, req)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(reply); err != nil {
		return err
	}
	var ok bool
	_ = json.Unmarshal(reply["ok"], &ok)
	if !ok {
		var code string
		if err := json.Unmarshal(reply["error"], &code); err != nil || code == "" {
			return errors.New("control request failed")
		}
		return errors.New(code)
	}
	return nil
}

var doCmd = Command{
	Name: "do", Args: "<action> [arg...] [flags]", Summary: "Run a compositor action over the control socket.",
	Setup: func(fs *flag.FlagSet) Runner {
		wait := fs.String("wait", "settled", "Completion level: committed, settled or presented.")
		until := fs.String("until", "", "State predicate to wait for.")
		timeout := fs.Duration("timeout", 10*time.Second, "Maximum completion wait.")
		return func(ctx context.Context, env Env, args []string) error {
			// flag stops at the first positional argument; accept options after action
			// arguments too, without changing parsing for existing commands.
			var action []string
			for i := 0; i < len(args); i++ {
				arg := args[i]
				if arg == "--" {
					action = append(action, args[i+1:]...)
					break
				}
				if !strings.HasPrefix(arg, "--") {
					action = append(action, arg)
					continue
				}
				name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
				if fs.Lookup(name) == nil {
					return usagef("unknown flag --%s", name)
				}
				if !hasValue {
					i++
					if i == len(args) {
						return usagef("--%s needs a value", name)
					}
					value = args[i]
				}
				if err := fs.Set(name, value); err != nil {
					return usagef("%v", err)
				}
			}
			if len(action) == 0 {
				return usagef("expected an action")
			}
			if _, err := domain.ParseWaitLevel(*wait); err != nil {
				return usagef("%v", err)
			}
			if *until != "" {
				if _, err := domain.ParseUntil(*until); err != nil {
					return usagef("%v", err)
				}
			}
			if *timeout <= 0 {
				return usagef("timeout must be positive")
			}
			c, err := dialControl(os.Getenv)
			if err != nil {
				return err
			}
			defer c.conn.Close()
			req := map[string]any{"op": "do", "action": strings.Join(action, " "), "wait": *wait, "timeout": timeout.String()}
			if *until != "" {
				req["until"] = *until
			}
			return printControl(ctx, env, c, req)
		}
	},
}

var queryCmd = Command{
	Name: "query", Args: "state", Summary: "Query the compositor over the control socket.",
	Setup: func(*flag.FlagSet) Runner {
		return func(ctx context.Context, env Env, args []string) error {
			if len(args) != 1 || args[0] != "state" {
				return usagef("expected state")
			}
			c, err := dialControl(os.Getenv)
			if err != nil {
				return err
			}
			defer c.conn.Close()
			return printControl(ctx, env, c, map[string]any{"op": "query", "what": "state"})
		}
	},
}
