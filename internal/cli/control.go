package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

// controlClient is a sequential JSON-lines client, owned by its caller.
type controlClient struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	id   uint64
}

type controlResponse map[string]json.RawMessage

// err returns the reply's error code, or nil when ok is true.
func (r controlResponse) err() error {
	var ok bool
	_ = json.Unmarshal(r["ok"], &ok)
	if ok {
		return nil
	}
	var code string
	if err := json.Unmarshal(r["error"], &code); err != nil || code == "" {
		return errors.New("control request failed")
	}
	return errors.New(code)
}

// dialControl resolves the control target (-s value, then environment) and
// connects to it.
func dialControl(env func(string) string, target string) (*controlClient, error) {
	path, err := resolveControl(target, env, func() ([]sessiondir.Meta, error) {
		if env("XDG_RUNTIME_DIR") == "" {
			return nil, nil
		}
		return sessiondir.List(env("XDG_RUNTIME_DIR"))
	})
	if err != nil {
		return nil, err
	}
	return dialPath(path)
}

// dialPath connects to the control socket at path.
func dialPath(path string) (*controlClient, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket %s: %w", path, err)
	}
	return &controlClient{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}, nil
}

// closeInto closes the connection, joining a close failure into *err.
func (c *controlClient) closeInto(err *error) {
	*err = errors.Join(*err, c.conn.Close())
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
	return reply.err()
}

// printControlState queries state and prints it bare, in the state file's
// shape; query state keeps the reply envelope.
func printControlState(ctx context.Context, env Env, c *controlClient) error {
	reply, err := c.call(ctx, map[string]any{"op": "query", "what": "state"})
	if err != nil {
		return err
	}
	if err := reply.err(); err != nil {
		return err
	}
	state := reply["state"]
	if len(state) == 0 {
		return errors.New("control reply has no state")
	}
	_, err = env.Stdout.Write(append(append([]byte{}, state...), '\n'))
	return err
}

var doCmd = Command{
	Name: "do", Args: "<action> [arg...] [flags]", Summary: "Run a compositor action over the control socket.",
	Setup: func(fs *flag.FlagSet) Runner {
		target := addTargetFlag(fs)
		wf := addWaitFlags(fs)
		return func(ctx context.Context, env Env, args []string) (runErr error) {
			// flag stops at the first positional argument; accept options after
			// action arguments too.
			action, err := injectArguments(fs, args)
			if err != nil {
				return err
			}
			if len(action) == 0 {
				return usagef("expected an action")
			}
			req := map[string]any{"op": "do", "action": strings.Join(action, " ")}
			if err := wf.apply(req); err != nil {
				return err
			}
			c, err := dialControl(os.Getenv, *target)
			if err != nil {
				return err
			}
			defer c.closeInto(&runErr)
			return printControl(ctx, env, c, req)
		}
	},
}

var queryCmd = Command{
	Name: "query", Args: "state", Summary: "Query the compositor over the control socket.",
	Setup: func(fs *flag.FlagSet) Runner {
		target := addTargetFlag(fs)
		return func(ctx context.Context, env Env, args []string) (runErr error) {
			if len(args) != 1 || args[0] != "state" {
				return usagef("expected state")
			}
			c, err := dialControl(os.Getenv, *target)
			if err != nil {
				return err
			}
			defer c.closeInto(&runErr)
			return printControl(ctx, env, c, map[string]any{"op": "query", "what": "state"})
		}
	},
}
