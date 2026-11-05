// Package control serves the v1 JSON-lines control protocol on a Unix socket.
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/statewire"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Server owns a control listener. Requests on each connection are processed in
// order: the next line is read only after the preceding core reply is sent.
type Server struct {
	listener *net.UnixListener
	ids      *workspaceid.IDs
	done     chan struct{}
	once     sync.Once
	err      error
}

// Listen creates a private socket using the same workspace IDs as statefile.
// An existing socket is removed only when connecting proves it has no listener.
func Listen(path string, ids *workspaceid.IDs) (*Server, error) {
	if ids == nil {
		return nil, errors.New("workspace IDs are required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("control path is not a socket: %s", path)
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			conn.Close()
			return nil, fmt.Errorf("control socket already listening: %s", path)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return &Server{listener: listener, ids: ids, done: make(chan struct{})}, nil
}

// Close stops acceptance and all active connections. It is safe to call twice.
func (s *Server) Close() error {
	s.once.Do(func() { close(s.done); s.err = s.listener.Close() })
	return s.err
}

// Run accepts connections until cancellation or Close, waiting for handlers to exit.
func (s *Server) Run(ctx context.Context, out chan<- ports.ControlRequest) error {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	log := logging.For(ctx, "control")
	log.Info().Str("path", s.listener.Addr().String()).Msg("listening")
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer conn.Close()
			connCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				select {
				case <-s.done:
					cancel()
				case <-connCtx.Done():
				}
			}()
			stop := context.AfterFunc(connCtx, func() { _ = conn.Close() })
			defer stop()
			if !sameUID(conn) {
				log.Warn().Msg("peer rejected")
				return
			}
			s.serve(connCtx, conn, out)
		}()
	}
}

func sameUID(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) { cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return false
	}
	return credErr == nil && cred != nil && cred.Uid == uint32(os.Getuid())
}

type request struct {
	ID      json.RawMessage `json:"id"`
	Op      string          `json:"op"`
	Action  string          `json:"action"`
	What    string          `json:"what"`
	Wait    *string         `json:"wait"`
	Until   *string         `json:"until"`
	Timeout *string         `json:"timeout"`
}

type diff struct {
	Mapped       []domain.WindowID `json:"mapped"`
	Unmapped     []domain.WindowID `json:"unmapped"`
	FocusFrom    domain.WindowID   `json:"focus_from"`
	FocusTo      domain.WindowID   `json:"focus_to"`
	FocusChanged bool              `json:"focus_changed"`
	Workspaces   []string          `json:"workspaces"`
}

type reply struct {
	ID    json.RawMessage   `json:"id"`
	OK    bool              `json:"ok"`
	Done  string            `json:"done"`
	Diff  diff              `json:"diff"`
	State statewire.State   `json:"state"`
	Late  []domain.WindowID `json:"late"`
	Error string            `json:"error,omitempty"`
}

type command struct {
	Name    string   `json:"name"`
	Arg     string   `json:"arg"`
	Kind    string   `json:"kind"`
	Caps    []string `json:"caps"`
	Summary string   `json:"summary"`
}

func capNames(c domain.Caps) []string {
	names := []string{}
	for _, item := range []struct {
		bit  domain.Caps
		name string
	}{{domain.CapRead, "read"}, {domain.CapAct, "act"}, {domain.CapInput, "input"}, {domain.CapCapture, "capture"}, {domain.CapAdmin, "admin"}} {
		if c&item.bit != 0 {
			names = append(names, item.name)
		}
	}
	return names
}

func (s *Server) serve(ctx context.Context, conn *net.UnixConn, out chan<- ports.ControlRequest) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	encoder := json.NewEncoder(conn)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || len(req.ID) == 0 || string(req.ID) == "null" {
			if encoder.Encode(reply{Error: ports.ControlErrBadRequest}) != nil {
				return
			}
			continue
		}
		if req.Op == "hello" {
			commands := []command{}
			for _, spec := range domain.Commands() {
				commands = append(commands, command{spec.Name, spec.Arg, spec.Kind.String(), capNames(spec.Caps), spec.Summary})
			}
			if encoder.Encode(struct {
				ID       json.RawMessage `json:"id"`
				OK       bool            `json:"ok"`
				V        int             `json:"v"`
				Caps     []string        `json:"caps"`
				Commands []command       `json:"commands"`
			}{req.ID, true, 1, capNames(domain.CapsAll), commands}) != nil {
				return
			}
			continue
		}
		response := s.dispatch(ctx, req, out)
		if ctx.Err() != nil {
			return
		}
		if encoder.Encode(response) != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req request, out chan<- ports.ControlRequest) reply {
	response := reply{ID: req.ID, Error: ports.ControlErrBadRequest}
	ch := make(chan ports.ControlReply, 1)
	var message ports.ControlRequest
	switch req.Op {
	case "do":
		wait := domain.DefaultWait
		if req.Wait != nil {
			var err error
			wait, err = domain.ParseWaitLevel(*req.Wait)
			if err != nil {
				return response
			}
		}
		var until *domain.Until
		if req.Until != nil {
			parsed, err := domain.ParseUntil(*req.Until)
			if err != nil {
				return response
			}
			until = &parsed
		}
		timeout := 10 * time.Second
		if req.Timeout != nil {
			var err error
			timeout, err = time.ParseDuration(*req.Timeout)
			if err != nil || timeout <= 0 {
				return response
			}
		}
		if req.Action == "" {
			return response
		}
		message = ports.ControlDo{Action: req.Action, Wait: wait, Until: until, Timeout: timeout, Caps: domain.CapsAll, Reply: ch}
	case "query":
		if req.What == "" {
			return response
		}
		message = ports.ControlQuery{What: req.What, Caps: domain.CapsAll, Reply: ch}
	default:
		return response
	}
	select {
	case out <- message:
	case <-ctx.Done():
		return response
	}
	var result ports.ControlReply
	select {
	case result = <-ch:
	case <-ctx.Done():
		return response
	}
	code := result.Err
	if result.TimedOut {
		code = "timeout"
	}
	d := result.Diff
	return reply{ID: req.ID, OK: code == "", Done: result.Done.String(), Diff: diff{d.Mapped, d.Unmapped, d.FocusFrom, d.FocusTo, d.FocusChanged, d.Workspaces}, State: statewire.FromDomain(result.State, s.ids), Late: result.Late, Error: code}
}
