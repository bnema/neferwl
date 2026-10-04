package core

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// The focus pulse is the pulse animation of the focus indicator
// (focus.animation): it briefly shows the indicator's effect (focus.effect, a
// light screen blend visible on black backgrounds too) on a window that just
// got the keyboard focus. Only the last focus counts: it must hold for
// pulseSettle before the pulse starts, a focus change stops a running one, and
// the window that pulsed last does not pulse again within pulseCooldown
// (switching back after another window held the focus is a new change).
// Fast switching therefore shows nothing. Fullscreen windows, a window alone
// on screen (e.g. a maximized column), overview previews, a window focused as
// it maps and a protected session never pulse.

const (
	pulseSettle   = 150 * time.Millisecond
	pulseRise     = 90 * time.Millisecond
	pulseFall     = 230 * time.Millisecond
	pulseCooldown = time.Second
	// pulseRecheck is how often a pulse waiting for a slide to land checks again.
	pulseRecheck = pulseSettle / 3
)

// focusPulse is the pulse state. target is the window focus last seen and
// since when; id is the window pulsing from start (0: none). last and lastAt
// are the previous pulse, for the cooldown. drawable is whether the last
// scene could show a pulse on target.
type focusPulse struct {
	target   WindowID
	since    time.Time
	id       WindowID
	start    time.Time
	last     WindowID
	lastAt   time.Time
	drawable bool
	// value is the effect of the last sample, kept for scenes published
	// between the focused output's own flips.
	value     float64
	timerC    <-chan time.Time
	timerStop func() bool
}

func (c *Core) pulseOn() bool {
	return c.animOn() && c.cfg.Focus.Animation == ports.FocusAnimationPulse
}

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
	if c.justMapped(window) {
		return
	}
	p.timerC, p.timerStop = newTimer(c.ch.Clock, pulseSettle)
}

// justMapped reports whether the window got the focus as it mapped: opening
// an app is not a focus change, and its first frames may still be on their
// way. Its focus counts from the next change.
func (c *Core) justMapped(id WindowID) bool {
	p := &c.pulse
	return p.target == id && p.since.Sub(c.windows.lookup(id).mappedAt) < pulseSettle
}

// pulseTick runs when the settle timer fired. It reports whether a pulse
// started.
func (c *Core) pulseTick() bool {
	p := &c.pulse
	p.timerC, p.timerStop = nil, nil
	if p.target == 0 || !c.pulseOn() || !p.drawable || c.justMapped(p.target) {
		return false
	}
	now := c.now()
	if wait := pulseSettle - now.Sub(p.since); wait > 0 {
		p.timerC, p.timerStop = newTimer(c.ch.Clock, wait)
		return false
	}
	// A swipe or its landing slide still shows the neighbours: decide on
	// the settled layout, or a window left alone would start a pulse cut
	// short.
	if !c.cur().settled() {
		p.timerC, p.timerStop = newTimer(c.ch.Clock, pulseRecheck)
		return false
	}
	if p.target == p.last && now.Sub(p.lastAt) < pulseCooldown {
		return false
	}
	p.id, p.start, p.last, p.lastAt = p.target, now, p.target, now
	return true
}

// advancePulse is the effect of the running pulse at now: a quick sine rise
// to focus.strength, then a smooth fall. It ends the pulse once over.
func (c *Core) advancePulse(now time.Time) float64 {
	p := &c.pulse
	if p.id == 0 {
		return 0
	}
	peak := c.cfg.Focus.Strength
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

// settled reports whether no swipe or slide moves the monitor's layout
// (screen.settled adds the rect motions).
func (m *Monitor) settled() bool {
	if m.switchMotion.on || m.switchOff != 0 {
		return false
	}
	for w := range m.all() {
		if w.motion.on || w.shift != 0 {
			return false
		}
	}
	return true
}

// visibleCount is the number of windows of layout shown in frame.
func visibleCount(layout []Placement, frame Rect) int {
	n := 0
	for _, p := range layout {
		if p.Preview == 0 && onScreen(p, frame) {
			n++
		}
	}
	return n
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
	c.pulse.id, c.pulse.drawable = 0, false
}
