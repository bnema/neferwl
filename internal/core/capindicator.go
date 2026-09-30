package core

import (
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// The capture indicator: the compositor shows that something is captured,
// for every capture of every client and protocol, and no client can turn it
// off. Core owns what is shown and until when; the renderer draws it over the
// physical output only (Scene.CaptureIndicators) and never in a capture.
//
//   - a session that produced a frame (CaptureFrameTaken with a Session)
//     marks its target for as long as it lives;
//   - every captured frame marks its target for ports.CaptureFlash after it
//     (a flash), so a one-shot screenshot shows for that long and periodic
//     captures stay visible;
//   - a target drawn on screen (output, region, workspace frame) gets a
//     border; a workspace rendered for capture only has no place on screen, so
//     its output gets a pill.
//
// Marks are the union of all live sessions and flashes. Expiry follows the
// core clock: one timer, armed at the earliest flash, is the only time
// source. Only the core goroutine touches this state; nothing here runs, or
// allocates, while no session produced a frame and no flash is live.

// maxCaptureFlashes bounds the live flashes. Past it the flashes of regions
// of an output collapse into one flash of the whole output (a larger mark,
// never a missing one); targets that are not regions are bounded by the
// outputs and workspaces that exist.
const maxCaptureFlashes = 256

// capFlash is a captured frame's target and when its mark ends.
type capFlash struct {
	target capTarget
	until  time.Time
}

// captureFrame records a captured frame. It reports whether what is shown
// changed, so a scene must be published; extending a flash that shows
// already does not.
func (c *Core) captureFrame(v ports.CaptureFrameTaken) bool {
	changed := false
	if s := c.capSession(v.Session); v.Session != 0 && s != nil && !s.recording {
		s.recording, changed = true, true
	}
	t := capTarget{output: v.Output, workspace: v.Workspace, rect: v.Region}
	if _, reason := c.capResolve(t); reason != ports.CaptureReasonNone {
		// Nothing of the target is left on screen to mark.
		return changed
	}
	until := c.now().Add(ports.CaptureFlash)
	for i := range c.capFlashes {
		if c.capFlashes[i].target == t {
			c.capFlashes[i].until = until
			c.armCaptureTimer()
			return changed
		}
	}
	if len(c.capFlashes) >= maxCaptureFlashes {
		c.collapseFlashes(t)
	}
	c.capFlashes = append(c.capFlashes, capFlash{target: t, until: until})
	c.armCaptureTimer()
	return true
}

// collapseFlashes makes room: the flashes of regions of t's output become one
// whole-output flash lasting as long as the longest of them; when that is
// not enough the oldest flash goes.
func (c *Core) collapseFlashes(t capTarget) {
	if t.workspace == 0 && t.output != "" {
		var until time.Time
		n := 0
		c.capFlashes = slices.DeleteFunc(c.capFlashes, func(f capFlash) bool {
			if f.target.workspace != 0 || f.target.output != t.output || f.target.rect == (Rect{}) {
				return false
			}
			if f.until.After(until) {
				until = f.until
			}
			n++
			return true
		})
		if n > 0 {
			c.capFlashes = append(c.capFlashes, capFlash{target: capTarget{output: t.output}, until: until})
		}
	}
	if len(c.capFlashes) >= maxCaptureFlashes {
		old := 0
		for i, f := range c.capFlashes {
			if f.until.Before(c.capFlashes[old].until) {
				old = i
			}
		}
		c.capFlashes = slices.Delete(c.capFlashes, old, old+1)
	}
}

// captureExpire forgets the flashes that ended.
func (c *Core) captureExpire() {
	if len(c.capFlashes) == 0 {
		return
	}
	now := c.now()
	c.capFlashes = slices.DeleteFunc(c.capFlashes, func(f capFlash) bool { return !f.until.After(now) })
}

// captureFlashTick runs when the flash timer fired: it forgets the flashes
// that ended and re-arms for the next. It reports whether one ended.
func (c *Core) captureFlashTick() bool {
	n := len(c.capFlashes)
	c.captureExpire()
	c.armCaptureTimer()
	return len(c.capFlashes) != n
}

// armCaptureTimer arms the timer at the earliest flash when none runs. A
// flash that was extended leaves it: the timer fires early, finds nothing to
// forget and arms again. No flash stops it.
func (c *Core) armCaptureTimer() {
	if len(c.capFlashes) == 0 {
		c.stopCaptureTimer()
		return
	}
	if c.capStop != nil {
		return
	}
	first := c.capFlashes[0].until
	for _, f := range c.capFlashes[1:] {
		if f.until.Before(first) {
			first = f.until
		}
	}
	d := max(first.Sub(c.now()), time.Millisecond)
	if c.ch.Clock != nil {
		t := c.ch.Clock.NewTimer(d)
		c.capC, c.capStop = t.C(), t.Stop
		return
	}
	t := time.NewTimer(d)
	c.capC, c.capStop = t.C, t.Stop
}

func (c *Core) stopCaptureTimer() {
	if c.capStop != nil {
		c.capStop()
	}
	c.capC, c.capStop = nil, nil
}

// captureIndicators is the marks of one output's scene; nil when there are
// none. The slice is immutable once published and shared with the next scene
// of the screen while the marks are the same; it is cloned only when they
// change, so a steady recording allocates nothing.
func (c *Core) captureIndicators(sc *screen) []ports.CaptureIndicator {
	if len(c.capSessions) == 0 && len(c.capFlashes) == 0 {
		sc.capMarks = nil
		return nil
	}
	out := c.capMarks[:0]
	add := func(t capTarget) {
		r, reason := c.capResolve(t)
		if reason != ports.CaptureReasonNone || r.sc != sc {
			return
		}
		o := sc.mon.Output()
		var m ports.CaptureIndicator
		if r.hidden {
			m = ports.CaptureIndicator{Pill: true, Rect: pillRect(o)}
		} else {
			m = ports.CaptureIndicator{Rect: thickenMark(r.rect, o)}
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	for _, s := range c.capSessions {
		if s.recording {
			add(s.target())
		}
	}
	for _, f := range c.capFlashes {
		add(f.target)
	}
	c.capMarks = out
	switch {
	case len(out) == 0:
		sc.capMarks = nil
	case !slices.Equal(sc.capMarks, out):
		sc.capMarks = slices.Clone(out)
	}
	return sc.capMarks
}

// thickenMark inflates a mark to at least ports.MinCaptureMark on each side,
// around the target and clamped to the output (a target one logical pixel
// wide gets a border with something to draw; the border then covers the
// target whole). An output smaller than that is marked whole.
func thickenMark(r, out Rect) Rect {
	grow := func(pos, size, lo, span int) (int, int) {
		if size >= ports.MinCaptureMark {
			return pos, size
		}
		if span <= ports.MinCaptureMark {
			return lo, span
		}
		pos -= (ports.MinCaptureMark - size) / 2
		pos = min(max(pos, lo), lo+span-ports.MinCaptureMark)
		return pos, ports.MinCaptureMark
	}
	r.X, r.W = grow(r.X, r.W, 0, out.W)
	r.Y, r.H = grow(r.Y, r.H, 0, out.H)
	return r
}

// pillRect is the pill in the top-right corner of an output, inset, and always
// whole inside it: an output too small for the pill (clamped to the output it
// would be cut) is filled whole instead, so the mark is never empty.
func pillRect(o Rect) Rect {
	r := Rect{X: max(o.W-ports.CapturePillInset-ports.CapturePillSize, 0), Y: ports.CapturePillInset, W: ports.CapturePillSize, H: ports.CapturePillSize}
	if in := intersect(r, Rect{W: o.W, H: o.H}); in != r {
		return Rect{W: o.W, H: o.H}
	}
	return r
}
