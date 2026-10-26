package core

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

const (
	osdHoldDelay    = 400 * time.Millisecond
	osdTapMax       = 250 * time.Millisecond
	osdDoubleTapGap = 300 * time.Millisecond
)

// osdKeyResult is an owner-goroutine OSD gesture, not a rendering action.
type osdKeyResult uint8

const (
	osdNone osdKeyResult = iota
	osdShow
	osdHide
	osdTogglePin
)

// osdKey recognizes cmd gestures on its owner's goroutine. The owner selects
// on holdC and calls holdFired; no timer callback mutates gesture state.
// Feed every key transition so keys held before cmd also inhibit a gesture.
type osdKey struct {
	clock      ports.Clock
	timer      ports.Timer
	holdC      <-chan time.Time
	held       map[string]bool
	cmdHeld    string
	pressed    time.Time
	lastTap    time.Time
	tapPending bool
	second     bool
	eligible   bool
	shown      bool
	pin        bool
}

// pinned reports the owner's current pin state.
func (k *osdKey) pinned() bool { return k.pin }

// setPinned records the pin state chosen by the owner after a toggle.
func (k *osdKey) setPinned(pinned bool) { k.pin = pinned }

// stopHold disarms the timer and removes it from the owner's select.
func (k *osdKey) stopHold() {
	if k.timer != nil {
		k.timer.Stop()
	}
	k.timer, k.holdC = nil, nil
}

// osdCmdKey recognizes either physical side of the configured modifier.
func osdCmdKey(sym string, cmd ports.Mods) bool {
	switch cmd {
	case ports.ModSuper:
		return sym == "Super_L" || sym == "Super_R"
	case ports.ModAlt:
		return sym == "Alt_L" || sym == "Alt_R"
	case ports.ModCtrl:
		return sym == "Control_L" || sym == "Control_R"
	}
	return false
}

// key consumes a transition at the owner's clock time. XKB Mods is the state
// after the transition: cmd is normally absent on its own release.
func (k *osdKey) key(ev ports.KeyEvent, id string, cmd ports.Mods, now time.Time) osdKeyResult {
	if k.held == nil {
		k.held = make(map[string]bool)
	}
	if ev.Pressed {
		if k.held[id] {
			return osdNone
		}
		alone := len(k.held) == 0
		k.held[id] = true
		if !osdCmdKey(ev.Keysym, cmd) || !alone || ev.Mods&^cmd != 0 {
			k.stopHold()
			k.eligible, k.tapPending, k.second = false, false, false
			return osdNone
		}
		k.cmdHeld, k.pressed, k.eligible = id, now, true
		gap := now.Sub(k.lastTap)
		k.second = k.tapPending && gap >= 0 && gap <= osdDoubleTapGap
		k.tapPending = false
		if !k.pin {
			k.timer = newPortTimer(k.clock, osdHoldDelay)
			k.holdC = k.timer.C()
		}
		return osdNone
	}
	delete(k.held, id)
	if id != k.cmdHeld {
		return osdNone
	}
	k.stopHold()
	k.cmdHeld = ""
	if k.shown {
		k.shown = false
		if !k.pin {
			return osdHide
		}
	}
	elapsed := now.Sub(k.pressed)
	tap := k.eligible && elapsed >= 0 && elapsed <= osdTapMax
	k.eligible = false
	if !tap {
		k.tapPending, k.second = false, false
		return osdNone
	}
	if k.second {
		k.second, k.tapPending = false, false
		return osdTogglePin
	}
	k.lastTap, k.tapPending = now, true
	return osdNone
}

// holdFired handles the hold channel on the owner goroutine, like repeatTick.
// A cancelled timer has no live channel and cannot produce a gesture.
func (k *osdKey) holdFired() osdKeyResult {
	if k.timer == nil {
		return osdNone
	}
	k.stopHold()
	k.eligible, k.tapPending, k.second = false, false, false
	if k.pin || k.cmdHeld == "" {
		return osdNone
	}
	k.shown = true
	return osdShow
}
