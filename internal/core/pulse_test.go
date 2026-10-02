package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// pulseCore is indicatorCore with the focus pulse on and two tiled windows,
// 2 focused, its pulse settled and over.
func pulseCore(t *testing.T) (*Core, *indicatorClock) {
	t.Helper()
	c, ic := indicatorCore(t)
	c.cfg.Focus.Pulse = ports.FocusPulseContrast
	c.cur().mon.AddWindow(1)
	c.cur().mon.AddWindow(2)
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	c.pulseTick()
	ic.now = ic.now.Add(pulseCooldown)
	indicatorScene(t, c)
	return c, ic
}

func pulseOf(s ports.Scene, id WindowID) float64 {
	for _, w := range s.Windows {
		if w.ID == id {
			return w.Pulse
		}
	}
	return -1
}

func TestFocusPulseAfterSettle(t *testing.T) {
	c, ic := pulseCore(t)
	c.cur().mon.Current().FocusID(1)
	if s := indicatorScene(t, c); pulseOf(s, 1) != 0 {
		t.Fatalf("pulse before settle: %v", pulseOf(s, 1))
	}
	ic.now = ic.now.Add(pulseSettle)
	if !c.pulseTick() {
		t.Fatal("no pulse after settle")
	}
	if !c.animating() {
		t.Fatal("a running pulse asks for no frames")
	}
	ic.now = ic.now.Add(pulseRise)
	s := indicatorScene(t, c)
	if p := pulseOf(s, 1); p < pulseStrength*0.99 || p > pulseStrength {
		t.Fatalf("peak %v, want %v", p, pulseStrength)
	}
	if pulseOf(s, 2) != 0 {
		t.Fatal("unfocused window pulses")
	}
	ic.now = ic.now.Add(pulseFall)
	if s := indicatorScene(t, c); pulseOf(s, 1) != 0 || c.animating() {
		t.Fatalf("pulse left after its end: %v", pulseOf(s, 1))
	}
}

// Switching faster than pulseSettle never pulses: only the last focus counts.
func TestFocusPulseDebounced(t *testing.T) {
	c, ic := pulseCore(t)
	for range 4 {
		c.cur().mon.Current().FocusID(1)
		indicatorScene(t, c)
		ic.now = ic.now.Add(pulseSettle / 3)
		c.cur().mon.Current().FocusID(2)
		indicatorScene(t, c)
		ic.now = ic.now.Add(pulseSettle / 3)
	}
	// A stale timer of an earlier focus fires: the focus is too recent.
	if c.pulseTick() {
		t.Fatal("pulse while switching")
	}
	if c.pulse.timerC == nil {
		t.Fatal("no timer for the last focus")
	}
	ic.now = ic.now.Add(pulseSettle)
	// Window 2 pulsed in pulseCore over a second ago: it may pulse again.
	if !c.pulseTick() || c.pulse.id != 2 {
		t.Fatalf("last focus did not pulse: %d", c.pulse.id)
	}
}

// A focus change stops the running pulse; coming back within the cooldown
// does not pulse the same window again.
func TestFocusPulseStopsAndCoolsDown(t *testing.T) {
	c, ic := pulseCore(t)
	c.cur().mon.Current().FocusID(1)
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	c.pulseTick()
	ic.now = ic.now.Add(pulseRise / 2)
	c.cur().mon.Current().FocusID(2)
	if s := indicatorScene(t, c); pulseOf(s, 1) != 0 || pulseOf(s, 2) != 0 {
		t.Fatal("pulse survived a focus change")
	}
	c.cur().mon.Current().FocusID(1)
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	if c.pulseTick() {
		t.Fatal("same window pulsed within the cooldown")
	}
}

func TestFocusPulseSkipsFullscreen(t *testing.T) {
	c, ic := pulseCore(t)
	c.cur().mon.Current().FocusID(1)
	c.cur().mon.ToggleFullscreen()
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	if c.pulseTick() || c.animating() {
		t.Fatal("fullscreen window asks for pulse frames")
	}
	// Fullscreen entered mid-pulse ends it.
	c.cur().mon.ToggleFullscreen()
	c.cur().mon.Current().FocusID(2)
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	if !c.pulseTick() {
		t.Fatal("no pulse on the tiled window")
	}
	c.cur().mon.ToggleFullscreen()
	if s := indicatorScene(t, c); pulseOf(s, 2) != 0 || c.animating() {
		t.Fatal("pulse survived fullscreen")
	}
}

// A popup grab or layer taking the keyboard stops the pulse; the window
// getting it back is not a new focus and does not pulse again.
func TestFocusPulseIgnoresKeyboardDetours(t *testing.T) {
	c, ic := pulseCore(t)
	c.cur().mon.Current().FocusID(1)
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	c.pulseTick()
	// The keyboard goes elsewhere (held false), then comes back.
	c.pulseFocus(1, false)
	if c.animating() {
		t.Fatal("pulse kept while the keyboard is elsewhere")
	}
	ic.now = ic.now.Add(2 * pulseCooldown)
	c.pulseFocus(1, true)
	if c.pulse.timerC != nil {
		t.Fatal("returning keyboard counts as a new focus")
	}
}

func TestFocusPulseOff(t *testing.T) {
	c, ic := pulseCore(t)
	c.cfg.Focus.Pulse = ports.FocusPulseOff
	c.cur().mon.Current().FocusID(1)
	indicatorScene(t, c)
	if c.pulse.timerC != nil {
		t.Fatal("timer armed with the pulse off")
	}
	ic.now = ic.now.Add(pulseSettle)
	if c.pulseTick() || c.animating() {
		t.Fatal("pulse with focus.pulse off")
	}
	// Turning it back on does not pulse the window already focused.
	c.cfg.Focus.Pulse = ports.FocusPulseContrast
	indicatorScene(t, c)
	if c.pulse.timerC != nil {
		t.Fatal("enabling the pulse pulses the focused window")
	}
}
