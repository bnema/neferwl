package core_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestBindModeSetsMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.send(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
	})
}

func TestBindChainEndsInMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "focus-column-left; mode resize")
		r.send(cmdX)
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
		r := runChainRig(t, "mode resize")
		r.send(cmdX)
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
		r := runChainRig(t, "mode resize")
		r.send(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
		r.epoch.Store(domain.SecurityState{Generation: 1})
		r.send(ports.KeyEvent{Keysym: "x", Keycode: 53, Mods: domain.ModSuper})
		if got := r.mode(t); got != "" {
			t.Fatalf("mode after epoch change %q, want default", got)
		}
	})
}

func TestBindModeStopsRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.send(ports.KeyEvent{Keysym: "h", Keycode: 43, Mods: domain.ModSuper, Pressed: true})
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

func TestModeRunsItsBinds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.send(key("h", 43, true))
		r.send(key("h", 43, false))
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
		r := runChainRig(t, "mode resize")
		r.enter(t)
		for _, sym := range []struct {
			name string
			code uint32
		}{{"x", 53}, {"Shift_L", 50}} {
			r.send(key(sym.name, sym.code, true))
			r.send(key(sym.name, sym.code, false))
		}
		synctest.Wait()
		if got := r.forwarded(); len(got) != 0 {
			t.Fatalf("forwarded %v, want none", got)
		}
		r.noState(t)
	})
}

func TestModeIgnoresDefaultBinds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.send(ports.KeyEvent{Keysym: "h", Keycode: 43, Mods: domain.ModSuper, Pressed: true})
		synctest.Wait()
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("default bind ran: focus %v", got)
		}
		r.noState(t)
	})
}

func TestModeForwardsReleaseOfEarlierPress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.send(key("a", 38, true))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 1 {
			t.Fatalf("press forwarded %v", got)
		}
		r.enter(t)
		r.send(key("a", 38, false))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 1 || got[0].Pressed || got[0].Keysym != "a" {
			t.Fatalf("release forwarded %v", got)
		}
	})
}

func TestModeEscapeReturnsToDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.send(key("Escape", 9, true))
		if got := r.mode(t); got != "" {
			t.Fatalf("mode %q, want default", got)
		}
		r.send(key("Escape", 9, false))
		synctest.Wait()
		if got := r.forwarded(); len(got) != 0 {
			t.Fatalf("forwarded %v", got)
		}
	})
}

func TestModeBoundEscapeStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.send(ports.KeyEvent{Keysym: "e", Keycode: 26, Mods: domain.ModSuper, Pressed: true})
		r.send(ports.KeyEvent{Keysym: "e", Keycode: 26, Mods: domain.ModSuper})
		if got := r.mode(t); got != "esc" {
			t.Fatalf("mode %q, want esc", got)
		}
		r.send(key("Escape", 9, true))
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
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.client <- ports.ShortcutsInhibit{Window: 5, Active: true}
		synctest.Wait()
		r.drain()
		r.send(key("h", 43, true))
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

func TestControlDoMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		// The rig never commits configures: do waits for committed only.
		if rep := r.do(t, "mode ghost"); rep.Err != ports.ControlErrBadRequest {
			t.Fatalf("do mode ghost: %+v", rep.Err)
		}
		if rep := r.do(t, "focus-column-left; mode ghost"); rep.Err != ports.ControlErrBadRequest {
			t.Fatalf("chain with ghost: %+v", rep.Err)
		}
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("rejected chain ran: focus %v", got)
		}
		r.noState(t)
		if rep := r.do(t, "mode resize"); rep.Err != "" || rep.Done != domain.WaitCommitted || rep.State.Mode != "resize" {
			t.Fatalf("do mode resize: err %q done %q mode %q", rep.Err, rep.Done, rep.State.Mode)
		}
	})
}

func TestControlLockedChainWithQuit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.epoch.Store(domain.SecurityState{Generation: 1, Protected: true})
		if rep := r.do(t, "focus-column-left; quit"); rep.Err != ports.ControlErrLocked {
			t.Fatalf("locked chain: %+v", rep.Err)
		}
		// The lock clears focus (window 0); the chain moves it nowhere.
		if got := r.focused(); len(got) != 1 || got[0] != 0 {
			t.Fatalf("locked chain ran: focus %v", got)
		}
		select {
		case err := <-r.done:
			t.Fatalf("core stopped: %v", err)
		default:
		}
	})
}

func TestControlRejectsInvalidChains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		for _, action := range []string{"none; focus-column-left", "switch-column-next; focus-column-left", "mode a b", "focus-column-left; mode a b"} {
			if rep := r.do(t, action); rep.Err != ports.ControlErrBadRequest {
				t.Fatalf("%q: %q, want bad-request", action, rep.Err)
			}
		}
		// An unknown command wins over a bad argument anywhere in the chain.
		if rep := r.do(t, "mode a b; bogus"); rep.Err != ports.ControlErrUnknownCommand {
			t.Fatalf("unknown after bad: %q", rep.Err)
		}
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("rejected chain ran: focus %v", got)
		}
	})
}

func TestApplyRejectsInvalidChains(t *testing.T) {
	for _, chain := range []string{"none; focus-column-left", "focus-column-left; none", "switch-column-prev; focus-column-left"} {
		cfg := scrollDefaults()
		cfg.Binds["Cmd+x"] = chain
		if _, err := core.New(cfg, core.Channels{Commands: make(chan ports.ClientCommand, 1), Scenes: make(chan []domain.Scene, 1)}, testOptions(t, core.Options{})); err == nil {
			t.Fatalf("%q accepted", chain)
		}
	}
	cfg := scrollDefaults()
	cfg.Binds["Cmd+x"] = "focus-column-left; switch-column-next"
	if _, err := core.New(cfg, core.Channels{Commands: make(chan ports.ClientCommand, 1), Scenes: make(chan []domain.Scene, 1)}, testOptions(t, core.Options{})); err != nil {
		t.Fatalf("switch last: %v", err)
	}
}

func TestBindChainStartsWithMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize; focus-column-left")
		r.send(cmdX)
		if got := r.mode(t); got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
		if got := r.focused(); len(got) != 1 || got[0] != 4 {
			t.Fatalf("focus %v, want [4]", got)
		}
	})
}

func TestModeDefaultAction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.send(key("q", 24, true))
		if got := r.mode(t); got != "" {
			t.Fatalf("bind mode default: %q", got)
		}
		r.send(key("q", 24, false))
		if rep := r.do(t, "mode resize"); rep.State.Mode != "resize" {
			t.Fatalf("do mode resize: %q", rep.State.Mode)
		}
		if rep := r.do(t, "mode default"); rep.Err != "" || rep.State.Mode != "" {
			t.Fatalf("do mode default: %+v", rep.Err)
		}
	})
}

func TestModeShiftEscapeLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.send(ports.KeyEvent{Keysym: "Escape", Keycode: 9, Mods: domain.ModShift, Pressed: true})
		if got := r.mode(t); got != "" {
			t.Fatalf("mode %q, want default", got)
		}
	})
}

func TestModeBindRepeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.send(key("h", 43, true))
		pressed := r.widths(t)
		time.Sleep(time.Second)
		held := r.widths(t)
		if len(pressed) != 5 || len(held) != 5 || held[4] <= pressed[4] {
			t.Fatalf("widths %v then %v, want the held bind to repeat", pressed, held)
		}
	})
}

func TestModeOverviewTakesItsKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		if rep := r.do(t, "toggle-overview"); rep.Err != "" {
			t.Fatalf("overview: %q", rep.Err)
		}
		// Escape closes the overview; the mode stays.
		r.send(key("Escape", 9, true))
		r.send(key("Escape", 9, false))
		synctest.Wait()
		if got := r.current(t).Mode; got != "resize" {
			t.Fatalf("mode %q, want resize", got)
		}
	})
}

func TestReloadKeepsDeclaredMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		r.enter(t)
		r.reload <- domain.ConfigChanged{Config: r.cfg}
		synctest.Wait()
		if got := r.current(t).Mode; got != "resize" {
			t.Fatalf("same config: mode %q", got)
		}
		cfg := r.cfg
		cfg.Modes = map[string]map[string]string{"resize": {"l": "set-column-width +1"}}
		r.reload <- domain.ConfigChanged{Config: cfg}
		synctest.Wait()
		if got := r.current(t).Mode; got != "resize" {
			t.Fatalf("changed table: mode %q", got)
		}
	})
}

func TestControlDoRunsTrimmedText(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mode resize")
		if rep := r.do(t, "focus-column-left "); rep.Err != "" {
			t.Fatalf("do: %q", rep.Err)
		}
		if got := r.focused(); len(got) != 1 || got[0] != 4 {
			t.Fatalf("focus %v, want [4]", got)
		}
		if rep := r.do(t, "quit "); rep.Err != "" {
			t.Fatalf("quit: %q", rep.Err)
		}
		if err := <-r.done; !errors.Is(err, core.ErrQuit) {
			t.Fatalf("run: %v, want ErrQuit", err)
		}
	})
}

func TestBindModeTraced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "focus-column-left; mode resize")
		r.send(cmdX)
		synctest.Wait()
		var traces []domain.BindDispatched
		for len(r.binds) > 0 {
			if b, ok := (<-r.binds).(domain.BindDispatched); ok {
				traces = append(traces, b)
			}
		}
		if len(traces) != 2 || traces[1].Action != "mode resize" || traces[1].Combo == "" || traces[1].Combo != traces[0].Combo || traces[1].Source != domain.BindSourceKey {
			t.Fatalf("traces %+v", traces)
		}
	})
}
