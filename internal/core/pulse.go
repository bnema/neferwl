package core

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// The focus pulse briefly lifts the colors of a window toward white (a light
// screen blend, visible on black backgrounds too): a window that just got the
// keyboard focus (focus.pulse). Only the last focus counts: it must hold for
// pulseSettle before the pulse starts, a focus change stops a running one, and
// the same window does not pulse again within pulseCooldown. Fast switching
// therefore shows nothing. Fullscreen windows, overview previews and a
// protected session never pulse.

const (
	pulseSettle   = 150 * time.Millisecond
	pulseRise     = 90 * time.Millisecond
	pulseFall     = 230 * time.Millisecond
	pulseCooldown = time.Second
)

// focusPulse is the pulse state. target is the window focus last seen and
// since when; id is the window pulsing from start (0: none). last and lastAt
// are the previous pulse, for the cooldown. drawable is whether the last
// scene could show a pulse on target.
type focusPulse struct {
	target    WindowID
	since     time.Time
	id        WindowID
	start     time.Time
	last      WindowID
	lastAt    time.Time
	drawable  bool
	timerC    <-chan time.Time
	timerStop func() bool
}

func (c *Core) pulseOn() bool { return c.cfg.Focus.Pulse == ports.FocusPulseContrast }

// pulseFocus follows the window focus. held is false while something else
// (a popup grab, a layer, a lock surface) has the keyboard: the running pulse
// stops, and the window getting it back is not a new focus. A new window
// stops the running pulse and waits pulseSettle before one starts. With the
// pulse off the focus is still followed, so turning it on pulses nothing.
func (c *Core) pulseFocus(window WindowID, held bool) {
	p := &c.pulse
	if !held || !c.pulseOn() {
		p.id = 0
		c.stopPulseTimer()
		if held {
			p.target = window
		}
		return
	}
	if window == p.target {
		return
	}
	p.target, p.id = window, 0
	c.stopPulseTimer()
	if window == 0 {
		return
	}
	p.since = c.now()
	p.timerC, p.timerStop = newTimer(c.ch.Clock, pulseSettle)
}

// pulseTick runs when the settle timer fired. It reports whether a pulse
// started.
func (c *Core) pulseTick() bool {
	p := &c.pulse
	p.timerC, p.timerStop = nil, nil
	if p.target == 0 || !c.pulseOn() || !p.drawable {
		return false
	}
	now := c.now()
	if wait := pulseSettle - now.Sub(p.since); wait > 0 {
		p.timerC, p.timerStop = newTimer(c.ch.Clock, wait)
		return false
	}
	if p.target == p.last && now.Sub(p.lastAt) < pulseCooldown {
		return false
	}
	p.id, p.start, p.last, p.lastAt = p.target, now, p.target, now
	return true
}

// advancePulse is the lift of the running pulse at now: a quick sine rise
// to focus.pulse-strength, then a smooth fall. It ends the pulse once over.
func (c *Core) advancePulse(now time.Time) float64 {
	p := &c.pulse
	if p.id == 0 {
		return 0
	}
	peak := c.cfg.Focus.PulseStrength
	t := now.Sub(p.start)
	switch {
	case t < 0:
		return 0
	case t < pulseRise:
		return peak * math.Sin(float64(t)/float64(pulseRise)*math.Pi/2)
	case t < pulseRise+pulseFall:
		x := 1 - float64(t-pulseRise)/float64(pulseFall)
		return peak * x * x * (3 - 2*x)
	}
	p.id = 0
	return 0
}

// pulsing reports whether a pulse runs on the screen.
func (c *Core) pulsing(sc *screen) bool {
	return c.pulse.id != 0 && sc == c.cur()
}

func (c *Core) stopPulseTimer() {
	p := &c.pulse
	if p.timerStop != nil {
		p.timerStop()
	}
	p.timerC, p.timerStop = nil, nil
}

// stopPulse ends the running pulse and its timer (security change). The
// focus is kept: the window focused again after an unlock does not pulse.
func (c *Core) stopPulse() {
	c.stopPulseTimer()
	c.pulse.id = 0
}

// newTimer makes a timer on clock, or the system timer when clock is nil.
func newTimer(clock ports.Clock, d time.Duration) (<-chan time.Time, func() bool) {
	if clock != nil {
		t := clock.NewTimer(d)
		return t.C(), t.Stop
	}
	t := time.NewTimer(d)
	return t.C, t.Stop
}
