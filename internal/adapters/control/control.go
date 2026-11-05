// Package control serves the v1 JSON-lines control protocol on a Unix socket.
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/statewire"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Server owns a control listener. Requests on each connection are processed in
// order: the next line is read only after the preceding core reply is sent.
// Options supplies a dedicated control keymap, never the physical input map.
// Listen takes ownership of Keymap on success. The server takes ownership of
// each map it receives from Keymaps; producers own any maps left in the channel
// after shutdown. Security is sampled before every translated event.
type Options struct {
	Continuous chan<- ports.ContinuousCaptureRequest
	FramesDir  string
	Shooter    *Shooter
	Keymap     *xkb.Keymap
	Keymaps    <-chan *xkb.Keymap
	Security   ports.SessionSecurity
}

type Server struct {
	continuous chan<- ports.ContinuousCaptureRequest
	framesDir  string
	security   ports.SessionSecurity
	shooter    *Shooter
	injectMu   sync.Mutex
	injector   *injector
	keymaps    <-chan *xkb.Keymap
	listener   *net.UnixListener
	ids        *workspaceid.IDs
	done       chan struct{}
	once       sync.Once
	err        error
}

// Listen creates a private socket using the same workspace IDs as statefile.
// An existing socket is removed only when connecting proves it has no listener.
func Listen(path string, ids *workspaceid.IDs, options ...Options) (*Server, error) {
	if len(options) > 1 {
		return nil, errors.New("at most one control options value is accepted")
	}
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
	s := &Server{listener: listener, ids: ids, done: make(chan struct{})}
	if len(options) == 1 {
		opts := options[0]
		s.continuous, s.framesDir, s.security = opts.Continuous, opts.FramesDir, opts.Security
		s.shooter = opts.Shooter
		s.keymaps = opts.Keymaps
		if opts.Keymap != nil {
			s.injector = newInjector(opts.Keymap, opts.Security)
		}
	}
	return s, nil
}

// Close stops acceptance and all active connections. It is safe to call twice.
func (s *Server) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.err = s.listener.Close()
		s.injectMu.Lock()
		defer s.injectMu.Unlock()
		if s.injector != nil {
			s.injector.km.Close()
			s.injector = nil
		}
	})
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
	EveryFrame bool            `json:"every_frame"`
	Stop       bool            `json:"stop"`
	Name       string          `json:"name"`
	Mode       string          `json:"mode"`
	Outputs    []string        `json:"outputs"`
	Inline     bool            `json:"inline"`
	Raw        bool            `json:"raw"`
	Shot       json.RawMessage `json:"shot"`
	ID         json.RawMessage `json:"id"`
	Op         string          `json:"op"`
	Action     string          `json:"action"`
	What       string          `json:"what"`
	Wait       *string         `json:"wait"`
	Until      *string         `json:"until"`
	Timeout    *string         `json:"timeout"`
	Kind       string          `json:"kind"`
	Combo      string          `json:"combo"`
	Text       string          `json:"text"`
	Cmd        string          `json:"cmd"`
	Args       []string        `json:"args"`
	Fingers    int             `json:"fingers"`
	Dir        string          `json:"dir"`
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
	Dir    string            `json:"dir,omitempty"`
	Shots  []Shot            `json:"shots,omitempty"`
	Detail string            `json:"detail,omitempty"`
	ID     json.RawMessage   `json:"id"`
	OK     bool              `json:"ok"`
	Done   string            `json:"done"`
	Diff   diff              `json:"diff"`
	State  statewire.State   `json:"state"`
	Late   []domain.WindowID `json:"late"`
	Error  string            `json:"error,omitempty"`
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

var nullID = json.RawMessage("null")

// maxRequestTimeout caps the client-supplied timeout: core and the shooter
// hold a request at most this long.
const maxRequestTimeout = 5 * time.Minute

func (s *Server) serve(ctx context.Context, conn *net.UnixConn, out chan<- ports.ControlRequest) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	encoder := json.NewEncoder(conn)
	announced := false
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if encoder.Encode(reply{ID: nullID, Error: ports.ControlErrBadRequest}) != nil {
				return
			}
			continue
		}
		// The id is optional: a request without one gets "id": null back.
		if len(req.ID) == 0 {
			req.ID = nullID
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
		response := s.dispatch(ctx, req, out, &announced)
		if ctx.Err() != nil {
			return
		}
		if encoder.Encode(response) != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req request, out chan<- ports.ControlRequest, announced *bool) reply {
	response := reply{ID: req.ID, Error: ports.ControlErrBadRequest}
	if req.Op == "shot" && (req.EveryFrame || req.Stop) {
		return s.continuousCapture(ctx, req)
	}
	ch := make(chan ports.ControlReply, 1)
	var message ports.ControlRequest
	timeout := 10 * time.Second
	switch req.Op {
	case "output-add", "output-rm":
		mode := domain.OutputMode{}
		if req.Op == "output-add" {
			var err error
			mode, err = parseOutputMode(req.Mode)
			if err != nil {
				return response
			}
		} else if req.Name == "" {
			return response
		}
		message = ports.ControlOutput{Name: req.Name, Mode: mode, Remove: req.Op == "output-rm", Caps: domain.CapsAll, Reply: ch}

	case "do", "inject", "shot":
		shots, err := requestShots(req)
		if err != nil {
			response.Detail = err.Error()
			return response
		}
		if len(shots) > 0 && s.shooter == nil {
			response.Detail = "capture unavailable"
			return response
		}
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
		if req.Timeout != nil {
			var err error
			timeout, err = time.ParseDuration(*req.Timeout)
			if err != nil || timeout <= 0 || timeout > maxRequestTimeout {
				return response
			}
		}
		if len(shots) > 0 {
			wait = domain.WaitPresented
		}
		if req.Op == "inject" {
			events, err := s.translate(req, !*announced)
			if err != nil {
				return response
			}
			message = ports.ControlInject{Shots: shots, Events: events, Wait: wait, Until: until, Timeout: timeout, Caps: domain.CapsAll, Reply: ch}
		} else {
			if req.Op == "shot" {
				req.Action = "none"
			}
			if req.Action == "" {
				return response
			}
			message = ports.ControlDo{Shots: shots, Action: req.Action, Wait: wait, Until: until, Timeout: timeout, Caps: domain.CapsAll, Reply: ch}
		}
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
	// The device announcement went through only if core ran the batch.
	if _, inject := message.(ports.ControlInject); inject && (result.Err == "" || result.TimedOut) {
		*announced = true
	}
	code := result.Err
	if result.TimedOut {
		code = "timeout"
	}
	d := result.Diff
	response = reply{ID: req.ID, OK: code == "", Done: result.Done.String(), Diff: diff{d.Mapped, d.Unmapped, d.FocusFrom, d.FocusTo, d.FocusChanged, d.Workspaces}, State: statewire.FromDomain(result.State, s.ids), Late: result.Late, Error: code}
	if len(result.Shots) > 0 {
		if s.shooter == nil {
			response.OK = false
			response.Error = ports.ControlErrBadRequest
			response.Detail = "capture unavailable"
			return response
		}
		// The capture is part of the request: bound it by the request timeout.
		shotCtx, cancel := context.WithTimeout(ctx, timeout)
		shots, err := s.shooter.Shoot(shotCtx, result.Shots, req.Inline)
		cancel()
		response.Shots = shots
		if err != nil {
			response.OK = false
			response.Error = ports.ControlErrCaptureFailed
			response.Detail = err.Error()
		}
	}
	return response
}

func requestShots(req request) ([]domain.ShotRequest, error) {
	var outputs []string
	if req.Op == "shot" {
		if req.Raw {
			return nil, errors.New("raw shots need --every-frame")
		}
		outputs = req.Outputs
		if len(outputs) == 0 {
			outputs = []string{""}
		}
	} else if len(req.Shot) > 0 {
		var enabled bool
		if err := json.Unmarshal(req.Shot, &enabled); err == nil {
			if !enabled {
				return nil, nil
			}
			outputs = []string{""}
		} else {
			if err := json.Unmarshal(req.Shot, &outputs); err != nil || outputs == nil {
				return nil, errors.New("shot must be true, false or an output list")
			}
		}
	}
	shots := make([]domain.ShotRequest, 0, len(outputs))
	for _, output := range outputs {
		shots = append(shots, domain.ShotRequest{Output: output})
	}
	return shots, nil
}

func parseOutputMode(text string) (domain.OutputMode, error) {
	size, refresh, has := strings.Cut(text, "@")
	w, h, ok := strings.Cut(size, "x")
	if !ok {
		return domain.OutputMode{}, errors.New("invalid mode")
	}
	width, e1 := strconv.Atoi(w)
	height, e2 := strconv.Atoi(h)
	hz := 60.0
	var err error
	if has {
		hz, err = strconv.ParseFloat(refresh, 64)
	}
	if e1 != nil || e2 != nil || err != nil || width < 1 || width > 16384 || height < 1 || height > 16384 || hz <= 0 || hz > 1000 || math.IsNaN(hz) {
		return domain.OutputMode{}, errors.New("invalid mode")
	}
	return domain.OutputMode{Width: width, Height: height, RefreshMilli: int(math.Round(hz * 1000))}, nil
}

func (s *Server) continuousCapture(ctx context.Context, req request) reply {
	response := reply{ID: req.ID, Error: ports.ControlErrBadRequest}
	if s.continuous == nil || (req.EveryFrame && req.Stop) || req.Inline {
		return response
	}
	if s.security != nil && s.security.Snapshot().Protected {
		response.Error = ports.ControlErrLocked
		return response
	}
	dir := req.Dir
	if dir == "" {
		dir = s.framesDir
	}
	if dir == "" && !req.Stop {
		return response
	}
	ack := make(chan ports.ContinuousCaptureResult, 1)
	select {
	case s.continuous <- ports.ContinuousCaptureRequest{Outputs: req.Outputs, Dir: dir, Raw: req.Raw, Stop: req.Stop, Reply: ack}:
	case <-ctx.Done():
		return response
	}
	select {
	case result := <-ack:
		response.OK = result.Err == ""
		response.Error = result.Err
		response.Dir = result.Dir
	case <-ctx.Done():
	}
	return response
}
