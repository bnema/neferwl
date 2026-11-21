package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// modeRig is a running Core with five windows in a row, the last focused,
// cmd+x bound to a chain, cmd+h to focus-column-left and a resize mode.
type modeRig struct {
	cfg      domain.Config
	input    chan ports.InputEvent
	commands chan ports.ClientCommand
	state    chan domain.State
	reload   chan domain.ConfigChanged
	client   chan ports.ClientEvent
	control  chan ports.ControlRequest
	epoch    *atomic.Value
}

func runModeRig(t *testing.T, chain string) modeRig {
	t.Helper()
	var epoch atomic.Value
	epoch.Store(domain.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) }).Maybe()
	r := modeRig{
		input:    make(chan ports.InputEvent, 4),
		commands: make(chan ports.ClientCommand, 256),
		state:    make(chan domain.State, 1),
		reload:   make(chan domain.ConfigChanged, 1),
		control:  make(chan ports.ControlRequest, 1),
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
		"resize": {"h": "set-column-width +1"}, // columns are one cell: -1 is a no-op
		"esc":    {"Escape": "focus-column-left"},
	}
	r.cfg = cfg
	c, err := core.New(cfg, core.Channels{Input: r.input, Client: client, Output: output, Control: r.control, Config: r.reload, ConfigErrors: make(chan error, 1), Commands: r.commands, Scenes: make(chan []domain.Scene, 1), State: r.state}, testOptions(t, core.Options{Security: gate}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); synctest.Wait() })
	output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 500, Height: 100}}
	for id := domain.WindowID(1); id <= 5; id++ {
		client <- ports.WindowMapped{ID: id}
	}
	synctest.Wait()
	for len(r.commands) > 0 {
		<-r.commands
	}
	for len(r.state) > 0 {
		<-r.state
	}
	return r
}

// stamp marks ev with the rig's current security epoch.
func (r modeRig) stamp(ev ports.InputEvent) ports.InputEvent {
	return ports.SecurityInput{State: r.epoch.Load().(domain.SecurityState), Event: ev}
}

// focused returns the windows focused since the last call, in order,
// unwrapping security envelopes.
func (r modeRig) focused() []domain.WindowID {
	var ids []domain.WindowID
	for len(r.commands) > 0 {
		cmd := <-r.commands
		if s, ok := cmd.(ports.SecurityCommand); ok {
			cmd = s.Command
		}
		if f, ok := cmd.(ports.FocusWindow); ok {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// mode returns the mode of the snapshot published since the last call.
func (r modeRig) mode(t *testing.T) string {
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

func TestBindModeSetsMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.input <- r.stamp(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
	})
}

func TestBindChainEndsInMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "focus-column-left; mode resize")
		r.input <- r.stamp(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
		if got := r.focused(); len(got) != 1 || got[0] != 4 {
			t.Fatalf("focus %v, want [4]", got)
		}
	})
}

func TestBindModeResetOnReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.input <- r.stamp(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
		cfg := r.cfg
		cfg.Binds = map[string]string{"Cmd+x": "focus-column-left"}
		cfg.Modes = nil
		r.reload <- domain.ConfigChanged{Config: cfg}
		if got := r.mode(t); got != "" {
			t.Fatalf("mode after reload %q, want default", got)
		}
	})
}

func TestBindModeResetOnSecurityEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.input <- r.stamp(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
		r.epoch.Store(domain.SecurityState{Generation: 1})
		r.input <- r.stamp(ports.KeyEvent{Keysym: "x", Keycode: 53, Mods: domain.ModSuper})
		if got := r.mode(t); got != "" {
			t.Fatalf("mode after epoch change %q, want default", got)
		}
	})
}

func TestBindModeStopsRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.input <- r.stamp(ports.KeyEvent{Keysym: "h", Keycode: 43, Mods: domain.ModSuper, Pressed: true})
		synctest.Wait()
		if got := r.focused(); len(got) != 1 {
			t.Fatalf("press: focus %v, want one change", got)
		}
		reply := make(chan ports.ControlReply, 1)
		r.control <- ports.ControlDo{Caps: domain.CapAct, Action: "mode resize", Reply: reply}
		synctest.Wait()
		if rep := <-reply; rep.Err != "" || rep.State.Mode != "resize" {
			t.Fatalf("do mode: %+v", rep)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("held: focus %v, want repeat stopped", got)
		}
	})
}

// key is an unmodified key event.
func key(sym string, code uint32, pressed bool) ports.KeyEvent {
	return ports.KeyEvent{Keysym: sym, Base: sym, Keycode: code, Pressed: pressed}
}

// enter switches the rig into its resize mode with cmd+x.
func (r modeRig) enter(t *testing.T) {
	t.Helper()
	r.input <- r.stamp(cmdX)
	r.input <- r.stamp(ports.KeyEvent{Keysym: "x", Keycode: 53, Mods: domain.ModSuper})
	if got := r.mode(t); got != "resize" {
		t.Fatalf("mode %q, want resize", got)
	}
	drained(r.commands)
}

func drained(ch chan ports.ClientCommand) {
	for len(ch) > 0 {
		<-ch
	}
}

// forwarded returns the keys forwarded since the last call.
func (r modeRig) forwarded() []ports.KeyEvent {
	var keys []ports.KeyEvent
	for len(r.commands) > 0 {
		cmd := <-r.commands
		if s, ok := cmd.(ports.SecurityCommand); ok {
			cmd = s.Command
		}
		if f, ok := cmd.(ports.ForwardKey); ok {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

// widths returns the column widths of the published snapshot, if any.
func (r modeRig) widths(t *testing.T) []int {
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

func TestModeRunsItsBinds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.enter(t)
		r.input <- r.stamp(key("h", 43, true))
		r.input <- r.stamp(key("h", 43, false))
		w := r.widths(t)
		if len(w) != 5 || w[4] <= w[0] {
			t.Fatalf("widths %v, want the focused column wider", w)
		}
		if got := r.forwarded(); len(got) != 0 {
			t.Fatalf("forwarded %v", got)
		}
	})
}

func TestModeSwallowsUnboundKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.enter(t)
		for _, sym := range []struct {
			name string
			code uint32
		}{{"x", 53}, {"Shift_L", 50}} {
			r.input <- r.stamp(key(sym.name, sym.code, true))
			r.input <- r.stamp(key(sym.name, sym.code, false))
		}
		synctest.Wait()
		if got := r.forwarded(); len(got) != 0 {
			t.Fatalf("forwarded %v, want none", got)
		}
		r.noState(t)
	})
}

func (r modeRig) noState(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case st := <-r.state:
		t.Fatalf("state published: mode %q", st.Mode)
	default:
	}
}

func TestModeIgnoresDefaultBinds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.enter(t)
		r.input <- r.stamp(ports.KeyEvent{Keysym: "h", Keycode: 43, Mods: domain.ModSuper, Pressed: true})
		synctest.Wait()
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("default bind ran: focus %v", got)
		}
		r.noState(t)
	})
}

func TestModeForwardsReleaseOfEarlierPress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.input <- r.stamp(key("a", 38, true))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 1 {
			t.Fatalf("press forwarded %v", got)
		}
		r.enter(t)
		r.input <- r.stamp(key("a", 38, false))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 1 || got[0].Pressed || got[0].Keysym != "a" {
			t.Fatalf("release forwarded %v", got)
		}
	})
}

func TestModeEscapeReturnsToDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.enter(t)
		r.input <- r.stamp(key("Escape", 9, true))
		if got := r.mode(t); got != "" {
			t.Fatalf("mode %q, want default", got)
		}
		r.input <- r.stamp(key("Escape", 9, false))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 0 {
			t.Fatalf("forwarded %v", got)
		}
	})
}

func TestModeBoundEscapeStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.input <- r.stamp(ports.KeyEvent{Keysym: "e", Keycode: 26, Mods: domain.ModSuper, Pressed: true})
		r.input <- r.stamp(ports.KeyEvent{Keysym: "e", Keycode: 26, Mods: domain.ModSuper})
		if got := r.mode(t); got != "esc" {
			t.Fatalf("mode %q, want esc", got)
		}
		r.input <- r.stamp(key("Escape", 9, true))
		synctest.Wait()
		if got := r.focused(); len(got) != 1 || got[0] != 4 {
			t.Fatalf("focus %v, want [4]", got)
		}
		if got := r.mode(t); got != "esc" {
			t.Fatalf("mode %q, want esc", got)
		}
	})
}

func TestModeInhibitorTakesKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runModeRig(t, "mode resize")
		r.enter(t)
		r.client <- ports.ShortcutsInhibit{Window: 5, Active: true}
		synctest.Wait()
		drained(r.commands)
		for len(r.state) > 0 {
			<-r.state
		}
		r.input <- r.stamp(key("h", 43, true))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 1 || got[0].Keysym != "h" {
			t.Fatalf("forwarded %v, want h", got)
		}
		r.noState(t)
	})
}

func TestModeHintKeepsPriority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHintConfig(t, core.Options{}, func(c *domain.Config) {
			c.Binds["Cmd+m"] = "mode pick"
			c.Modes = map[string]map[string]string{"pick": {"Cmd+t": "hint"}}
		})
		h.press("m", domain.ModSuper)
		h.release("m", domain.ModSuper)
		h.press("t", domain.ModSuper)
		req := receive(t, h.scans)
		h.release("t", 0)
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{h.inside(0)}}
		h.labelled(1, 0)
		// Escape goes to hint mode, which closes; the bind mode stays.
		h.press("Escape", 0)
		h.release("Escape", 0)
		synctest.Wait()
		h.press("t", domain.ModSuper)
		receive(t, h.scans)
	})
}
