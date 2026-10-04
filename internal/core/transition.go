package core

import (
	"math"
	"slices"
	"time"
)

// Transitions start after a user action (a bind, a click that focuses, a
// discrete swipe step): a snapshot of what each screen shows is taken before
// the action and diffed after it. The state the action leaves is the
// settled one; the transition only adds presentation springs over it
// (Workspace.shift, Monitor.switchOff), starting from what was on screen.
// Nothing here runs for events that come from clients or outputs.
//
// A window's rect transition (presentation-only rect transition) is presentation too: configures keep
// using the settled rect, so the client resizes once to its final size; what
// is drawn and hit-tested is the settled rect plus an offset that a spring
// takes to zero. The offsets are relative to the camera-shifted layout, so a
// window the view scrolls moves with the camera spring only.

// viewShot is what one screen showed before an action.
type viewShot struct {
	sc *screen
	ws *Workspace
	// viewX is ws's settled view and view where its columns were on screen
	// (viewX plus shift).
	viewX int
	view  float64
	// viewV and switchV are the speeds the running view and workspace
	// springs have at the snapshot's time (units per second, 0 without
	// one), not at their last frame. An action may stop them
	// (Monitor.Focus), so the transition reads them here.
	viewV, switchV float64
	// list and pos are the numbered workspaces and the monitor's fractional
	// position in them (index of the current one plus switchOff); ok is
	// false when there is none (a shown stash workspace).
	list []*Workspace
	pos  float64
	ok   bool
	// rects are the windows of ws the screen drew, hidden windows and
	// previews left out. The slice is reused like list.
	rects []rectShot
}

// rectOffsets are the four components of a rect motion: x, y, w, h.
type rectOffsets struct{ x, y, w, h float64 }

// rectShot is where a window was drawn: its settled rect plus the float
// offsets of its running rect motion (zero without one), which the drawn
// rect only rounds. vel is the speed of each offset component, in units per
// second. Offsets and speeds are sampled at the snapshot's time.
type rectShot struct {
	id       WindowID
	rect     Rect
	off, vel rectOffsets
}

// rectMotion takes a window's drawn rect to its settled one: each component
// is an offset from the settled value, sprung to 0 (dx, dy, dw, dh hold the
// current offsets, updated by advance).
type rectMotion struct {
	x, y, w, h     motion
	dx, dy, dw, dh float64
}

// apply is r moved by the current offsets; a size never drops below 1.
func (m *rectMotion) apply(r Rect) Rect {
	return Rect{
		X: r.X + int(math.Round(m.dx)), Y: r.Y + int(math.Round(m.dy)),
		W: max(r.W+int(math.Round(m.dw)), 1), H: max(r.H+int(math.Round(m.dh)), 1),
	}
}

// sample is where the offsets and their speeds would be at now, without
// moving the motion: a running component is sampled on a copy.
func (m *rectMotion) sample(now time.Time) (off, vel rectOffsets) {
	off = rectOffsets{m.dx, m.dy, m.dw, m.dh}
	sampleComponent(m.x, &off.x, &vel.x, now)
	sampleComponent(m.y, &off.y, &vel.y, now)
	sampleComponent(m.w, &off.w, &vel.w, now)
	sampleComponent(m.h, &off.h, &vel.h, now)
	return off, vel
}

func sampleComponent(m motion, off, vel *float64, now time.Time) {
	if m.on {
		*off, *vel = m.sampleAt(now)
	}
}

// advance moves the offsets to now; a settled component stops at 0.
func (m *rectMotion) advance(now time.Time) {
	m.dx = stepComponent(&m.x, m.dx, now)
	m.dy = stepComponent(&m.y, m.dy, now)
	m.dw = stepComponent(&m.w, m.dw, now)
	m.dh = stepComponent(&m.h, m.dh, now)
}

func stepComponent(m *motion, cur float64, now time.Time) float64 {
	if !m.on {
		return cur
	}
	v, done := m.at(now)
	if done {
		*m = motion{}
		return 0
	}
	return v
}

// on reports whether any component still runs.
func (m *rectMotion) on() bool { return m.x.on || m.y.on || m.w.on || m.h.on }

// retargetComponent starts m for an offset of off from its settled value at
// now, with the speed v it had then. A component with nothing to move and no
// speed stops.
func (c *Core) retargetComponent(m *motion, cur *float64, off, v float64, now time.Time) {
	*cur = off
	if off == 0 && v == 0 {
		*m = motion{}
		return
	}
	*m = c.spring(viewSpring(off, v), now)
}

// refreshShown builds the layouts of every screen once per publish: the
// settled one (what configures are sized from) and the shown one (the same
// with the rect motions applied: what is drawn and hit-tested). Rect motions
// belong to the workspace they began on and to a closed overview: any other
// change drops them.
func (c *Core) refreshShown() {
	for _, sc := range c.screens {
		m := sc.mon
		if len(sc.rects) > 0 && (m.ov.open || sc.rectsWS != m.Current()) {
			sc.stopRects()
		}
		sc.settledLayout = m.Layout()
		if len(sc.rects) > 0 {
			// A window that left the layout (or is hidden) keeps no motion:
			// it would ask for frames with nothing to move.
			for id := range sc.rects {
				if !slices.ContainsFunc(sc.settledLayout, func(p Placement) bool {
					return p.ID == id && !p.Hidden && p.Preview == 0
				}) {
					delete(sc.rects, id)
				}
			}
		}
		if len(sc.rects) == 0 {
			sc.shown = sc.settledLayout
			continue
		}
		buf := append(sc.shownBuf[:0], sc.settledLayout...)
		for i := range buf {
			p := &buf[i]
			if p.Hidden || p.Preview > 0 {
				continue
			}
			if rm, ok := sc.rects[p.ID]; ok {
				p.Rect = rm.apply(p.Rect)
			}
		}
		sc.shownBuf, sc.shown = buf, buf
	}
}

// snapshot records what every screen without an open overview shows at now.
// Running motions are sampled at now on a copy, so a transition that
// follows chains from their speed at the time of the action, not from the
// last frame's. The view is the one drawn (the layouts it is measured
// against are), only its speed is sampled. The returned slice and the lists
// in it belong to Core and are reused by the next snapshot: use it before
// the next action only.
func (c *Core) snapshot(now time.Time) []viewShot {
	full := c.shots[:cap(c.shots)]
	out := c.shots[:0]
	for _, sc := range c.screens {
		m := sc.mon
		var s viewShot
		if len(out) < len(full) {
			s.list = full[len(out)].list[:0]
			s.rects = full[len(out)].rects[:0]
		}
		s.sc = sc
		if !m.ov.open {
			w := m.Current()
			s.ws, s.viewX, s.view = w, w.ViewX, float64(w.ViewX)+w.shift
			if w.motion.on {
				_, s.viewV = w.motion.sampleAt(now)
			}
			if m.switchMotion.on {
				_, s.switchV = m.switchMotion.sampleAt(now)
			}
			// settledLayout and shown are index-aligned and differ only by
			// the offsets of sc.rects: the shot keeps both unrounded.
			for _, p := range sc.settledLayout {
				if p.Hidden || p.Preview != 0 {
					continue
				}
				r := rectShot{id: p.ID, rect: p.Rect}
				if rm, ok := sc.rects[p.ID]; ok {
					r.off, r.vel = rm.sample(now)
				}
				s.rects = append(s.rects, r)
			}
			if m.shown == nil {
				// A landing slide measures in the list it began on.
				list := m.Workspaces
				if m.switchList != nil {
					list = m.switchList
				}
				if i := indexOf(list, w); i >= 0 {
					s.list = append(s.list, list...)
					s.pos, s.ok = float64(i)+m.switchOff, true
				}
			}
		}
		out = append(out, s)
	}
	for i := len(out); i < len(full); i++ {
		full[i].sc, full[i].ws = nil, nil
	}
	c.shots = out
	return out
}

// transition starts the springs that take each screen from what before
// showed to what the action left, at now. It does nothing with animations
// off, with the overview open before or after, or while a swipe follows the
// fingers on that screen. Per-window rect motions join the camera here.
func (c *Core) transition(before []viewShot, now time.Time) {
	if !c.animOn() {
		return
	}
	for i := range before {
		b := &before[i]
		if b.ws == nil || !c.hasScreen(b.sc) || b.sc.mon.ov.open || c.following(b.sc) {
			continue
		}
		c.transitionCamera(b, now)
		c.transitionRects(b, now)
	}
}

// following reports whether a swipe moves sc's view with the fingers.
func (c *Core) following(sc *screen) bool {
	g := c.swipe
	return g != nil && g.screen == sc && (g.mode == swipeColumns || g.mode == swipeWorkspaces)
}

// transitionCamera slides the columns or the monitor from what b showed.
func (c *Core) transitionCamera(b *viewShot, now time.Time) {
	m := b.sc.mon
	w := m.Current()
	switch {
	case w == b.ws:
		if w.ViewX == b.viewX || !w.slidable() {
			return
		}
		// A running landing slide was retargeted by scroll(): its velocity
		// carries over, and the spring starts now like every action's.
		w.shift = b.view - float64(w.ViewX)
		w.motion = c.spring(viewSpring(w.shift, b.viewV), now)
	case b.ok && m.shown == nil && !c.movedAway(b.sc, b.ws):
		j := indexOf(b.list, w)
		if j < 0 {
			return
		}
		off := b.pos - float64(j)
		// The snapshot's list is reused by the next action: the slide keeps
		// its own copy.
		m.switchOff, m.switchList = off, slices.Clone(b.list)
		if off == 0 || m.framedSwitch() {
			m.stopSwitch()
			return
		}
		m.switchMotion = c.spring(workspaceSpring(off, b.switchV), now)
	}
}

// transitionRects starts or retargets the rect motions of the windows whose
// layout the action changed. The new layout is measured with the camera as
// transitionCamera left it (the view where it was on screen), so a window
// the view scrolls differs only by what the layout itself changed. Windows
// that were not drawn before (new, or hidden) or are hidden now get none.
func (c *Core) transitionRects(b *viewShot, now time.Time) {
	sc := b.sc
	m := sc.mon
	if m.Current() != b.ws {
		sc.stopRects()
		return
	}
	for _, p := range m.Layout() {
		if p.Hidden || p.Preview > 0 {
			continue
		}
		i := slices.IndexFunc(b.rects, func(r rectShot) bool { return r.id == p.ID })
		if i < 0 {
			continue
		}
		old := &b.rects[i]
		if old.rect == p.Rect {
			// The settled rect did not change: a running motion goes on
			// as it is, and there is none to start.
			continue
		}
		if sc.rects == nil {
			sc.rects = make(map[WindowID]rectMotion)
		}
		// Where the window was drawn, unrounded, relative to where it goes.
		rm := sc.rects[p.ID]
		c.retargetComponent(&rm.x, &rm.dx, float64(old.rect.X-p.Rect.X)+old.off.x, old.vel.x, now)
		c.retargetComponent(&rm.y, &rm.dy, float64(old.rect.Y-p.Rect.Y)+old.off.y, old.vel.y, now)
		c.retargetComponent(&rm.w, &rm.dw, float64(old.rect.W-p.Rect.W)+old.off.w, old.vel.w, now)
		c.retargetComponent(&rm.h, &rm.dh, float64(old.rect.H-p.Rect.H)+old.off.h, old.vel.h, now)
		if rm.on() {
			sc.rects[p.ID], sc.rectsWS = rm, b.ws
		} else {
			delete(sc.rects, p.ID)
		}
	}
}

// movedAway reports whether w, the workspace sc showed, went to another
// screen: it gets no motion on either side.
func (c *Core) movedAway(sc *screen, w *Workspace) bool {
	for _, o := range c.screens {
		if o != sc && o.mon.has(w) {
			return true
		}
	}
	return false
}
