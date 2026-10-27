package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

const (
	keyH     = 43
	keyLeft  = 113
	keyF     = 41
	keyShift = 50
)

// repeatCore is a Core on a test clock with four columns, the last one
// focused, and a few move and toggle binds.
func repeatCore(t *testing.T) (*Core, *indicatorClock) {
	t.Helper()
	c, ic := indicatorCore(t)
	cfg := c.cfg
	cfg.Layout.MaxColumns = 4
	cfg.Binds = map[string]string{
		"Cmd+h":         string(ActionFocusColumnLeft),
		"Cmd+l":         string(ActionFocusColumnRight),
		"Cmd+Left":      string(ActionFocusColumnLeft),
		"Cmd+f":         string(ActionToggleFullscreen),
		"Cmd+o":         string(ActionToggleOverview),
		"Cmd+Shift+h":   string(ActionMoveColumnLeft),
		"Cmd+Ctrl+Left": string(ActionFocusWorkspacePrev),
	}
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	m := c.cur().mon
	for id := WindowID(1); id <= 4; id++ {
		m.AddWindow(id)
	}
	m.Current().FocusID(4)
	indicatorScene(t, c)
	return c, ic
}

func key(sym string, code uint32, mods domain.Mods, pressed bool) ports.KeyEvent {
	return ports.KeyEvent{Keysym: sym, Keycode: code, Mods: mods, Pressed: pressed}
}

// tick fires the repeat timer once, as Run does on its channel.
func tick(t *testing.T, c *Core) {
	t.Helper()
	if c.repeatC == nil {
		t.Fatal("no repeat armed")
	}
	if err := c.repeatTick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A held move bind repeats: the first repeat after repeatDelay, then one
// per repeatInterval, until the key is released.
func TestHeldBindRepeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic := repeatCore(t)
		before := len(ic.timers)
		sendKeys(t, c, key("h", keyH, domain.ModSuper, true))
		if focusedID(c.cur().mon) != 3 {
			t.Fatalf("press: focus %d, want 3", focusedID(c.cur().mon))
		}
		if len(ic.timers) != before+1 || ic.timers[len(ic.timers)-1] != repeatDelay {
			t.Fatalf("repeat timer %v, want %v", ic.timers[before:], repeatDelay)
		}
		tick(t, c)
		tick(t, c)
		if focusedID(c.cur().mon) != 1 {
			t.Fatalf("two repeats: focus %d, want 1", focusedID(c.cur().mon))
		}
		if r := ic.resets[len(ic.resets)-1]; r != repeatInterval {
			t.Fatalf("next repeat in %v, want %v", r, repeatInterval)
		}
		sendKeys(t, c, key("h", keyH, domain.ModSuper, false))
		if c.repeatC != nil || c.repeat.timer != nil {
			t.Fatal("release did not stop the repeat")
		}

	})
}

// Only moves repeat: a held toggle runs once.
func TestHeldToggleDoesNotRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := repeatCore(t)
		sendKeys(t, c, key("f", keyF, domain.ModSuper, true))
		if c.repeatC != nil {
			t.Fatal("toggle-fullscreen repeats")
		}
		for _, a := range []Action{ActionToggleFullscreen, ActionToggleOverview, ActionCloseWindow, ActionSpawnTerminal, ActionQuit, ActionToggleWindowStash, ActionMaximizeColumn} {
			if repeats(a) {
				t.Errorf("%s repeats", a)
			}
		}
		for _, a := range []Action{ActionFocusColumnLeft, ActionMoveColumnRight, ActionFocusWorkspaceNext, ActionMoveWindowToWorkspacePrev, "set-column-width +1"} {
			if !repeats(a) {
				t.Errorf("%s does not repeat", a)
			}
		}

	})
}

// Another key pressed or the modifiers changed end the repeat: releasing
// Cmd first does not leave the move running.
func TestRepeatStopsOnKeyOrModifierChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := repeatCore(t)
		sendKeys(t, c, key("h", keyH, domain.ModSuper, true))
		sendKeys(t, c, key("Super_L", keySuper, 0, false))
		if c.repeatC != nil {
			t.Fatal("releasing Cmd did not stop the repeat")
		}
		sendKeys(t, c, key("h", keyH, 0, false))

		sendKeys(t, c, key("h", keyH, domain.ModSuper, true))
		sendKeys(t, c, key("Shift_L", keyShift, domain.ModSuper|domain.ModShift, true))
		if c.repeatC != nil {
			t.Fatal("pressing Shift did not stop the repeat")
		}

	})
}

// In the overview, held h/j/k/l or arrows repeat without Cmd, and so do
// the Cmd binds: holding Left walks the unfolded stash card by card.
func TestOverviewHeldKeyRepeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := repeatCore(t)
		m := c.cur().mon
		w := m.Current()
		w.Overflow = OverflowCascade
		for id := WindowID(40); id < 44; id++ {
			w.AddWindow(id)
			w.FocusID(id)
			w.ToggleWindowStash()
		}
		m.ToggleOverview()
		m.selectCard(w, 3)
		sendKeys(t, c, key("Left", keyLeft, 0, true))
		if m.cardAt(w) != 2 {
			t.Fatalf("press: card %d, want 2", m.cardAt(w))
		}
		tick(t, c)
		tick(t, c)
		if m.cardAt(w) != 0 {
			t.Fatalf("two repeats: card %d, want 0", m.cardAt(w))
		}
		sendKeys(t, c, key("Left", keyLeft, 0, false))
		if c.repeatC != nil {
			t.Fatal("release did not stop the repeat")
		}

		// Cmd+Left, a bind, repeats in the overview too.
		m.selectCard(w, 3)
		sendKeys(t, c, key("Left", keyLeft, domain.ModSuper, true))
		tick(t, c)
		if m.cardAt(w) != 1 {
			t.Fatalf("Cmd+Left held: card %d, want 1", m.cardAt(w))
		}
		sendKeys(t, c, key("Left", keyLeft, domain.ModSuper, false))

		// The overview closing ends a repeat, of an overview key or a bind:
		// Cmd+Left would go on in the layout.
		for _, mods := range []domain.Mods{0, domain.ModSuper} {
			if !m.ov.open {
				m.ToggleOverview()
			}
			sendKeys(t, c, key("Left", keyLeft, mods, true))
			m.CancelOverview()
			focus := focusedID(m)
			tick(t, c)
			if c.repeatC != nil || focusedID(m) != focus {
				t.Fatalf("mods %v: the repeat outlived the overview", mods)
			}
			sendKeys(t, c, key("Left", keyLeft, mods, false))
		}

	})
}
