package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// chainRig is a running Core behind a security gate with five windows in a
// row on one output, the last focused: cmd+x runs chain, cmd+h is
// focus-column-left, cmd+e enters mode esc, and mode resize is declared.
type chainRig struct {
	cfg      domain.Config
	input    chan ports.InputEvent
	commands chan ports.ClientCommand
	state    chan domain.State
	reload   chan domain.ConfigChanged
	client   chan ports.ClientEvent
	control  chan ports.ControlRequest
	spawn    chan domain.SpawnRequest
	binds    chan domain.InputTrace
	epoch    *atomic.Value
	done     chan error
}

func runChainRig(t *testing.T, chain string) chainRig {
	t.Helper()
	var epoch atomic.Value
	epoch.Store(domain.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) }).Maybe()
	r := chainRig{
		input:    make(chan ports.InputEvent, 4),
		commands: make(chan ports.ClientCommand, 256),
		state:    make(chan domain.State, 1),
		reload:   make(chan domain.ConfigChanged, 1),
		control:  make(chan ports.ControlRequest, 1),
		spawn:    make(chan domain.SpawnRequest, 4),
		binds:    make(chan domain.InputTrace, 16),
		epoch:    &epoch,
	}
	client := make(chan ports.ClientEvent, 8)
	r.client = client
	output := make(chan ports.OutputEvent, 1)
	cfg := scrollDefaults()
	cfg.Layout.MaxColumns = 5
	cfg.Binds["Cmd+x"] = chain
	cfg.Binds["Cmd+h"] = "focus-column-left"
	cfg.Binds["Cmd+e"] = "mode esc"
	cfg.Modes = map[string]map[string]string{
		"resize": {"h": "set-column-width +1", "q": "mode default"}, // columns are one cell: -1 is a no-op
		"esc":    {"Escape": "focus-column-left"},
	}
	r.cfg = cfg
	c, err := core.New(cfg, core.Channels{Input: r.input, Client: client, Output: output, Control: r.control, Config: r.reload, ConfigErrors: make(chan error, 1), Commands: r.commands, Scenes: make(chan []domain.Scene, 1), State: r.state, Spawn: r.spawn, Binds: r.binds}, testOptions(t, core.Options{Security: gate}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.done = make(chan error, 1)
	go func() { r.done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); synctest.Wait() })
	output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 500, Height: 100}}
	for id := domain.WindowID(1); id <= 5; id++ {
		client <- ports.WindowMapped{ID: id}
	}
	synctest.Wait()
	r.drain()
	return r
}

// drain drops the commands, states and traces sent so far.
func (r chainRig) drain() {
	for len(r.commands) > 0 {
		<-r.commands
	}
	for len(r.state) > 0 {
		<-r.state
	}
	for len(r.binds) > 0 {
		<-r.binds
	}
}

// send gives core an input event stamped with the current epoch.
func (r chainRig) send(ev ports.InputEvent) { r.input <- r.stamp(ev) }

// sent returns the client commands sent since the last call, out of
// their security envelopes.
func (r chainRig) sent() []ports.ClientCommand {
	var cmds []ports.ClientCommand
	for len(r.commands) > 0 {
		cmd := <-r.commands
		if s, ok := cmd.(ports.SecurityCommand); ok {
			cmd = s.Command
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

var cmdX = ports.KeyEvent{Keysym: "x", Keycode: 53, Mods: domain.ModSuper, Pressed: true}

// stamp marks ev with the rig's current security epoch.
func (r chainRig) stamp(ev ports.InputEvent) ports.InputEvent {
	return ports.SecurityInput{State: r.epoch.Load().(domain.SecurityState), Event: ev}
}

// focused returns the windows focused since the last call, in order.
func (r chainRig) focused() []domain.WindowID {
	var ids []domain.WindowID
	for _, cmd := range r.sent() {
		if f, ok := cmd.(ports.FocusWindow); ok {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// mode returns the mode of the snapshot published since the last call.
func (r chainRig) mode(t *testing.T) string {
	t.Helper()
	synctest.Wait()
	select {
	case st := <-r.state:
		return st.Mode
	default:
		t.Fatal("no state published")
		return ""
	}
}

// key is an unmodified key event.
func key(sym string, code uint32, pressed bool) ports.KeyEvent {
	return ports.KeyEvent{Keysym: sym, Base: sym, Keycode: code, Pressed: pressed}
}

// enter switches the rig into its resize mode with cmd+x.
func (r chainRig) enter(t *testing.T) {
	t.Helper()
	r.send(cmdX)
	r.send(ports.KeyEvent{Keysym: "x", Keycode: 53, Mods: domain.ModSuper})
	if got := r.mode(t); got != "resize" {
		t.Fatalf("mode %q, want resize", got)
	}
	r.drain()
}

// forwarded returns the keys forwarded since the last call.
func (r chainRig) forwarded() []ports.KeyEvent {
	var keys []ports.KeyEvent
	for _, cmd := range r.sent() {
		if f, ok := cmd.(ports.ForwardKey); ok {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

// widths returns the column widths of the published snapshot, if any.
func (r chainRig) widths(t *testing.T) []int {
	t.Helper()
	synctest.Wait()
	select {
	case st := <-r.state:
		var w []int
		for _, col := range st.Workspaces[0].Columns {
			w = append(w, col.Rect.W)
		}
		return w
	default:
		return nil
	}
}

func (r chainRig) noState(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case st := <-r.state:
		t.Fatalf("state published: mode %q", st.Mode)
	default:
	}
}

// do runs a control action and returns its reply.
func (r chainRig) do(t *testing.T, action string) ports.ControlReply {
	t.Helper()
	reply := make(chan ports.ControlReply, 1)
	r.control <- ports.ControlDo{Caps: domain.CapAct, Wait: domain.WaitCommitted, Action: action, Reply: reply}
	synctest.Wait()
	select {
	case rep := <-reply:
		return rep
	default:
		t.Fatalf("do %q: no reply", action)
		return ports.ControlReply{}
	}
}

// current is the live state, read over control.
func (r chainRig) current(t *testing.T) domain.State {
	t.Helper()
	reply := make(chan ports.ControlReply, 1)
	r.control <- ports.ControlQuery{Caps: domain.CapRead, What: "state", Reply: reply}
	synctest.Wait()
	return (<-reply).State
}
