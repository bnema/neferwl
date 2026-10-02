package core

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// The focus pulse briefly raises the contrast of a window that just got the
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
	// pulseStrength is the contrast gain at the top of the pulse.
	pulseStrength = 0.08
)

// focusPulse is the pulse state. target is the focus last seen and since
// when; id is the window pulsing from start (0: none). last and lastAt are
// the previous pulse, for the cooldown.
type focusPulse struct {
	target    WindowID
	since     time.Time
	id        WindowID
	start     time.Time
	last      WindowID
	lastAt    time.Time
	timerC    <-chan time.Time
	timerStop func() bool
}

func (c *Core) pulseOn() bool { return c.cfg.Focus.Pulse == ports.FocusPulseContrast }

// pulseFocus follows the keyboard focus: a new focus stops the running pulse
// and waits pulseSettle before one starts.
func (c *Core) pulseFocus(focus WindowID) {
	p := &c.pulse
	if !c.pulseOn() {
		c.stopPulse()
		return
	}
	if focus == p.target {
		return
	}
	p.target, p.id = focus, 0
	c.stopPulseTimer()
	if focus == 0 {
		return
	}
	p.since = c.now()
	c.armPulseTimer(pulseSettle)
}

// pulseTick runs when the settle timer fired. It reports whether a pulse
// started.
func (c *Core) pulseTick() bool {
	p := &c.pulse
	p.timerC, p.timerStop = nil, nil
	if p.target == 0 || !c.pulseOn() {
		return false
	}
	now := c.now()
	if wait := pulseSettle - now.Sub(p.since); wait > 0 {
		c.armPulseTimer(wait)
		return false
	}
	if p.target == p.last && now.Sub(p.lastAt) < pulseCooldown {
		return false
	}
	p.id, p.start, p.last, p.lastAt = p.target, now, p.target, now
	return true
}

// pulseAt is the contrast gain of the running pulse at now: a quick sine
// rise, then a smooth fall. It ends the pulse once over.
func (c *Core) pulseAt(now time.Time) float64 {
	p := &c.pulse
	if p.id == 0 {
		return 0
	}
	t := now.Sub(p.start)
	switch {
	case t < 0:
		return 0
	case t < pulseRise:
		return pulseStrength * math.Sin(float64(t)/float64(pulseRise)*math.Pi/2)
	case t < pulseRise+pulseFall:
		x := 1 - float64(t-pulseRise)/float64(pulseFall)
		return pulseStrength * x * x * (3 - 2*x)
	}
	p.id = 0
	return 0
}

// pulsing reports whether a pulse runs on the screen.
func (c *Core) pulsing(sc *screen) bool {
	return c.pulse.id != 0 && sc == c.cur()
}

func (c *Core) armPulseTimer(d time.Duration) {
	p := &c.pulse
	if c.ch.Clock != nil {
		t := c.ch.Clock.NewTimer(d)
		p.timerC, p.timerStop = t.C(), t.Stop
		return
	}
	t := time.NewTimer(d)
	p.timerC, p.timerStop = t.C, t.Stop
}

func (c *Core) stopPulseTimer() {
	p := &c.pulse
	if p.timerStop != nil {
		p.timerStop()
	}
	p.timerC, p.timerStop = nil, nil
}

// stopPulse forgets the pulse and its timer (security change, shutdown).
func (c *Core) stopPulse() {
	c.stopPulseTimer()
	c.pulse = focusPulse{}
}
