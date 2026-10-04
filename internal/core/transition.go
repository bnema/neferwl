package core

import (
	"slices"
	"time"
)

// Transitions start after a user action (a bind, a click that focuses, a
// discrete swipe step): a snapshot of what each screen shows is taken before
// the action and diffed after it. The state the action leaves is the
// settled one; the transition only adds presentation springs over it
// (Workspace.shift, Monitor.switchOff), starting from what was on screen.
// Nothing here runs for events that come from clients or outputs.

// viewShot is what one screen showed before an action.
type viewShot struct {
	sc *screen
	ws *Workspace
	// viewX is ws's settled view and view where its columns were on screen
	// (viewX plus shift).
	viewX int
	view  float64
	// list and pos are the numbered workspaces and the monitor's fractional
	// position in them (index of the current one plus switchOff); ok is
	// false when there is none (a shown stash workspace).
	list []*Workspace
	pos  float64
	ok   bool
}

// snapshot records what every screen without an open overview shows. The
// returned slice and the lists in it belong to Core and are reused by the
// next snapshot: use it before the next action only.
func (c *Core) snapshot() []viewShot {
	full := c.shots[:cap(c.shots)]
	out := c.shots[:0]
	for _, sc := range c.screens {
		m := sc.mon
		var s viewShot
		if len(out) < len(full) {
			s.list = full[len(out)].list[:0]
		}
		s.sc = sc
		if !m.ov.open {
			w := m.Current()
			s.ws, s.viewX, s.view = w, w.ViewX, float64(w.ViewX)+w.shift
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
		v := w.motion.velocity()
		w.shift = b.view - float64(w.ViewX)
		w.motion = c.spring(viewSpring(w.shift, v), now)
	case b.ok && m.shown == nil && !c.movedAway(b.sc, b.ws):
		j := indexOf(b.list, w)
		if j < 0 {
			return
		}
		off := b.pos - float64(j)
		v := m.switchMotion.velocity()
		// The snapshot's list is reused by the next action: the slide keeps
		// its own copy.
		m.switchOff, m.switchList = off, slices.Clone(b.list)
		if off == 0 || m.framedSwitch() {
			m.stopSwitch()
			return
		}
		m.switchMotion = c.spring(workspaceSpring(off, v), now)
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
