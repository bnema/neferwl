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
	output := make(chan ports.OutputEvent, 1)
	cfg := scrollDefaults()
	cfg.Layout.MaxColumns = 5
	cfg.Binds["Cmd+x"] = chain
	cfg.Binds["Cmd+h"] = "focus-column-left"
	cfg.Modes = map[string]map[string]string{"resize": {"h": "set-column-width -1"}}
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
