package core

import (
	"math"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
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
	// overview is set when the screen showed its overview: ws is then the
	// row on screen, the camera fields stay zero, and rects are the cards.
	overview bool
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
	// rects are the windows the screen drew (cards in the overview),
	// hidden windows left out. The slice is reused like list.
	rects []rectShot
	// stash holds the settled placements of the stash windows the screen
	// drew, for the ones an action hides (transitionStash). Reused too.
	stash []Placement
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
	// fade and fadeV are the fade offset of a running fade motion and its
	// speed, so an overview toggle carries it over (transitionOverview).
	fade, fadeV float64
	// dim and dimV are the dim offset of a running dim motion and its
	// speed; veil is the peek veil the window's settled placement drew
	// (Stash.Dim, 0 for another window). A stash navigation takes the
	// veil from what was on screen to the new one (transitionRects).
	dim, dimV, veil float64
}

// rectMotion takes a window's drawn rect to its settled one: each component
// is an offset from the settled value, sprung to 0 (dx, dy, dw, dh hold the
// current offsets, updated by advance). fade and dim are offsets too, from
// the settled Fade (0, or 1 for a leaving window) and the settled Dim.
//
// With scale, the window's content follows its drawn size: the shown
// placement gets a Zoom of the settled zoom (its Preview, or 1) times
// shown.W / settled.W (refreshShown), which the scene carries as Preview.
// The walk shrinks content toward the content rect's top-left, so this
// suits aspect-preserving changes: the zoom follows the smaller of the width
// and height ratios, so content never exceeds its frame when the aspect
// differs a little (rounding) or a lot (the frame is then letterboxed). An overview card's settled zoom is
// its Preview: a window that opens into a card goes from 1 to Preview with
// its width, and back.
//
// A leaving motion draws a window that closed or hid while it fades out:
// left is its last settled placement, which refreshShown appends (Hidden
// and Leaving) to the settled and shown layouts, keeping them aligned.
type rectMotion struct {
	x, y, w, h     motion
	dx, dy, dw, dh float64
	fade, dim      motion
	df, ddim       float64
	scale          bool
	leaving        bool
	left           Placement
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
	m.df = stepComponent(&m.fade, m.df, now)
	m.ddim = stepComponent(&m.dim, m.ddim, now)
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
func (m *rectMotion) on() bool {
	return m.x.on || m.y.on || m.w.on || m.h.on || m.fade.on || m.dim.on
}

// settledFade is the Fade the motion springs to: a leaving window fades
// out, any other is opaque.
func (m *rectMotion) settledFade() float64 {
	if m.leaving {
		return 1
	}
	return 0
}

// show is p as drawn: its rect moved by the offsets, its fade and dim
// offsets added, and its content zoomed with its size when scale is set.
func (m *rectMotion) show(p *Placement) {
	settled := p.Rect
	p.Rect = m.apply(settled)
	p.Fade = max(0, min(1, m.settledFade()+m.df))
	// A peek's Dim is an offset from its veil (publish adds Stash.Dim and
	// clamps the sum): it may go below 0, down to cancelling the veil.
	lo := 0.0
	if p.Peek {
		lo = -1
	}
	p.Dim = max(lo, min(1, p.Dim+m.ddim))
	if m.scale && settled.W > 0 && settled.H > 0 {
		zoom := p.Preview
		if zoom == 0 {
			zoom = 1
			if m.leaving {
				// A leaving window settles shrunk, its content with it.
				zoom = appearScale
			}
		}
		zoom *= min(float64(p.Rect.W)/float64(settled.W), float64(p.Rect.H)/float64(settled.H))
		if zoom >= 0.999 {
			// Drawn at its size: content is never magnified. publish
			// emits no Zoom for a window, and Zoom 1 for a card (whose
			// Preview would shrink it).
			zoom = 1
		}
		p.Zoom = zoom
	}
}

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
// belong to the workspace they began on, or to the open overview (rectsOwner):
// any other change drops them.
//
// Leaving motions add an entry for a window the layout no longer shows:
// appended to both layouts in ID order, Hidden (no input, focus, popups or
// configure) and Leaving (drawn, fading). The two layouts stay
// index-aligned: publish reads the settled entry at the shown one's index.
func (c *Core) refreshShown() {
	for _, sc := range c.screens {
		m := sc.mon
		if len(sc.rects) > 0 && sc.rectsWS != sc.rectsOwner() {
			sc.stopRects()
		}
		sc.settledLayout = m.Layout()
		if len(sc.rects) > 0 {
			// A window that left the layout (or is hidden) keeps no motion:
			// it would ask for frames with nothing to move. A leaving one
			// is the opposite: it goes once the layout shows the window.
			for id, rm := range sc.rects {
				shown := slices.ContainsFunc(sc.settledLayout, func(p Placement) bool {
					return p.ID == id && !p.Hidden
				})
				if shown == rm.leaving {
					delete(sc.rects, id)
				}
			}
		}
		if len(sc.rects) == 0 {
			sc.shown = sc.settledLayout
			continue
		}
		sc.settledLayout = sc.withLeaving(sc.settledLayout)
		buf := append(sc.shownBuf[:0], sc.settledLayout...)
		for i := range buf {
			p := &buf[i]
			if p.Hidden && !p.Leaving {
				continue
			}
			if rm, ok := sc.rects[p.ID]; ok {
				rm.show(p)
			}
		}
		sc.shownBuf, sc.shown = buf, buf
	}
}

// withLeaving is layout with the leaving windows' last placements added,
// Hidden and Leaving, into the screen's reused buffer (the layout itself
// is left alone). A window the layout still lists, Hidden (a hidden stash),
// is replaced where it is. A leaving tile goes where the tiles are painted: before
// the first window that opens the floats (a float above the tiles, as the
// renderer orders them), so it never flashes over a float; a leaving float
// goes last, over the tiles. Within a group the absent ones go smallest ID
// first: a stable order keeps the scenes comparable frame to frame.
func (s *screen) withLeaving(layout []Placement) []Placement {
	out := append(s.settledBuf[:0], layout...)
	// Pass 1: an entry the layout still lists, Hidden (a stash that hid),
	// replaces it where it is, never twice. Where the pass goes through the
	// map does not matter: every entry has its own place. The absent ones
	// are collected for pass 2.
	ids := s.leaveIDs[:0]
	for id, rm := range s.rects {
		if !rm.leaving {
			continue
		}
		j := slices.IndexFunc(out, func(q Placement) bool { return q.ID == id && q.Hidden && !q.Leaving })
		if j < 0 {
			ids = append(ids, id)
			continue
		}
		out[j] = rm.leavingPlacement()
	}
	// Pass 2: the absent ones in ID order, each at the end of its group
	// (tiles before the first float, a leaving stash float included;
	// floats last), so the order never depends on the map's.
	floats := len(out)
	for i, p := range out {
		if p.Floating && !p.Below && p.Preview == 0 && (!p.Hidden || p.Leaving) {
			floats = i
			break
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := s.rects[id].leavingPlacement()
		if p.Floating {
			out = append(out, p)
			continue
		}
		out = slices.Insert(out, floats, p)
		floats++
	}
	s.leaveIDs, s.settledBuf = ids, out
	return out
}

// leavingPlacement is the placement a leaving motion draws: its last
// settled one, Hidden and Leaving (the scene draws it; input, focus,
// popups and configures see a hidden window, as the C0 design has it).
// Hidden stays set on purpose: the leaving entry never takes input, even
// though its stash window is still in the layout.
func (m rectMotion) leavingPlacement() Placement {
	p := m.left
	p.Hidden, p.Leaving, p.Focused, p.Fade, p.Dim, p.Zoom = true, true, false, 0, 0, 0
	return p
}

// appear starts the entrance of a window the action made visible on sc:
// it fades in from invisible and grows from 90 % of its settled rect,
// around its centre. Nothing with animations off or if the layout does not
// show it. A leaving motion of the same window is replaced.
func (c *Core) appear(sc *screen, id WindowID, now time.Time) {
	if !c.animOn() {
		return
	}
	for _, p := range sc.mon.Layout() {
		if p.ID == id && !p.Hidden {
			c.appearAt(sc, p, now)
			return
		}
	}
}

// appearAt is appear for a window whose settled placement is p. A leaving
// motion of the same window (a show during a hide) is continued from, not
// replaced: the entrance starts at the fade, rect and speeds it was drawn
// with.
func (c *Core) appearAt(sc *screen, p Placement, now time.Time) {
	if !c.animOn() || p.Hidden {
		return
	}
	if sc.rects == nil {
		sc.rects = make(map[WindowID]rectMotion)
	}
	var rm rectMotion
	rm.scale = true
	if old, ok := sc.rects[p.ID]; ok && old.leaving {
		c.appearFrom(&rm, p, &old, now)
	} else {
		c.scaleFrom(&rm, p.Rect, appearScale, now)
		c.retargetComponent(&rm.fade, &rm.df, 1, 0, now)
	}
	sc.rects[p.ID], sc.rectsWS = rm, sc.mon.Current()
}

// appearFrom starts rm, the entrance to the settled placement p, from where
// the leaving motion old draws its window at now: its rect (old.left.Rect
// plus its offsets, sampled), fade, veil and their speeds. The leaving fade
// is an offset from 1, the entrance's from 0.
func (c *Core) appearFrom(rm *rectMotion, p Placement, old *rectMotion, now time.Time) {
	settled := p.Rect
	off, vel := old.sample(now)
	base := old.left.Rect
	fade, fadeV := old.df, 0.0
	sampleComponent(old.fade, &fade, &fadeV, now)
	dim, dimV := old.ddim, 0.0
	sampleComponent(old.dim, &dim, &dimV, now)
	// The veil the leaving entry drew (its peek's, plus the offset) to the
	// one the settled placement draws.
	dim += c.peekDim(old.left) - c.peekDim(p)
	c.retargetComponent(&rm.x, &rm.dx, float64(base.X-settled.X)+off.x, vel.x, now)
	c.retargetComponent(&rm.y, &rm.dy, float64(base.Y-settled.Y)+off.y, vel.y, now)
	c.retargetComponent(&rm.w, &rm.dw, float64(base.W-settled.W)+off.w, vel.w, now)
	c.retargetComponent(&rm.h, &rm.dh, float64(base.H-settled.H)+off.h, vel.h, now)
	c.retargetComponent(&rm.fade, &rm.df, 1+fade, fadeV, now)
	c.retargetComponent(&rm.dim, &rm.ddim, dim, dimV, now)
}

// mapWindow handles a window's map: it joins its workspace, the tiles the
// new one displaces re-flow with rect motions, and a window that comes on
// screen appears (appearMapped). A map is the one client event that
// animates; the settled layout is the same with animations off, so the
// client is configured once, to its final size.
func (c *Core) mapWindow(v ports.WindowMapped) {
	now := c.now()
	c.windows.mapped(v, now)
	if s, _ := c.screenOf(v.ID); s != nil || !c.animOn() || c.security.Protected {
		// Mapped before (a re-map keeps its place), animations off or a
		// session lock: nothing to animate.
		c.placement.place(c, v)
		return
	}
	shots := c.snapshot(now)
	for i := range shots {
		// A screen with its overview open re-lays its cards at once: a map
		// is not an overview action (transition skips a shot without ws).
		if shots[i].overview {
			shots[i].ws = nil
		}
	}
	c.placement.place(c, v)
	c.transition(shots, now)
	c.appearMapped(v.ID, now)
}

// appearMapped starts the entrance of a window that just mapped when it
// shows on its screen's current workspace as a tile or an ordinary float:
// not with the overview open or a session lock, not fullscreen (it keeps
// the direct scanout path), not a float below the columns, not a stash
// peek.
func (c *Core) appearMapped(id WindowID, now time.Time) {
	sc, w := c.screenOf(id)
	if sc == nil || c.security.Protected || sc.mon.ov.open || sc.mon.Current() != w {
		return
	}
	for _, p := range sc.mon.Layout() {
		if p.ID != id {
			continue
		}
		if !p.Hidden && !p.Fullscreen && !p.Below && !p.Peek && p.Preview == 0 {
			c.appearAt(sc, p, now)
		}
		return
	}
}

// unmapWindow handles a window's unmap: it leaves its workspace, the tiles
// around it re-flow with rect motions, and a window that was drawn as a
// tile or an ordinary float keeps fading out from where it was drawn
// (leaveFrom with the snapshot's record of it: a window closed mid-motion
// leaves from its drawn rect, fade and speed, not from its settled rect).
// Like a map, an unmap is a client event that animates; the settled layout
// is the same with animations off. A fullscreen window, one under a
// session lock or with the overview open just goes.
func (c *Core) unmapWindow(v ports.WindowUnmapped) {
	// The window is destroyed: its ID may come back for another one, which
	// must be configured from nothing (a leaving entry keeps no configure).
	c.configures.forget(v.ID)
	sc, w := c.screenOf(v.ID)
	if sc == nil {
		return
	}
	if !c.animOn() || c.security.Protected || sc.mon.ov.open {
		sc.mon.RemoveWindow(v.ID)
		delete(sc.rects, v.ID)
		return
	}
	now := c.now()
	// The placement that leaves is the settled one of the last publish:
	// where the window was going.
	var left Placement
	if sc.mon.Current() == w {
		for _, p := range sc.settledLayout {
			if p.ID == v.ID {
				left = p
				break
			}
		}
	}
	shots := c.snapshot(now)
	var shot *rectShot
	for i := range shots {
		if shots[i].overview {
			shots[i].ws = nil
		}
		if shots[i].sc == sc {
			if j := slices.IndexFunc(shots[i].rects, func(r rectShot) bool { return r.id == v.ID }); j >= 0 {
				shot = &shots[i].rects[j]
			}
		}
	}
	sc.mon.RemoveWindow(v.ID)
	delete(sc.rects, v.ID)
	c.transition(shots, now)
	if left.ID == v.ID && !left.Leaving && !left.Fullscreen && !left.Below && !left.Peek {
		c.leaveFrom(sc, left, shot, now)
	}
}

// resizeFloating applies a floating window's committed size. A float maps
// before its first commit, so core first lays it out at a default size;
// when its entrance runs, the settled rect changing under it would break
// the aspect of the 90 % rect. The entrance's rect offsets (and their
// springs, which are linear in their start and speed) are scaled by the
// settled size ratio: the progress and speed carry over, and the window
// still grows from 90 % of its real size around its centre.
func (c *Core) resizeFloating(v ports.WindowResized) {
	sc, w := c.screenOf(v.ID)
	if w == nil {
		return
	}
	rm, running := sc.rects[v.ID]
	running = running && rm.scale && !rm.leaving
	var old Rect
	if running {
		old = settledRect(sc.mon, v.ID)
	}
	w.ResizeFloating(v.ID, v.Width, v.Height)
	if !running {
		return
	}
	cur := settledRect(sc.mon, v.ID)
	if old.W <= 0 || old.H <= 0 || cur.W <= 0 || cur.H <= 0 || old == cur {
		return
	}
	kw, kh := float64(cur.W)/float64(old.W), float64(cur.H)/float64(old.H)
	rm.dx, rm.dw = rm.dx*kw, rm.dw*kw
	rm.dy, rm.dh = rm.dy*kh, rm.dh*kh
	rm.x.scale(kw)
	rm.w.scale(kw)
	rm.y.scale(kh)
	rm.h.scale(kh)
	sc.rects[v.ID] = rm
}

// settledRect is where m's layout puts the visible window id, zero if it
// shows none.
func settledRect(m *Monitor, id WindowID) Rect {
	for _, p := range m.Layout() {
		if p.ID == id && !p.Hidden {
			return p.Rect
		}
	}
	return Rect{}
}

// leave keeps drawing a window the action closed or hid, from its last
// settled placement p: it fades out and shrinks to 90 % around its centre,
// then its entry goes. Nothing with animations off, or for a placement that
// was not drawn.
func (c *Core) leave(sc *screen, p Placement, now time.Time) {
	c.leaveFrom(sc, p, nil, now)
}

// leaveFrom is leave for a window that was drawn as shot says (the
// snapshot's record of it, nil for a window at rest): the exit starts at the
// rect, fade and dim the window had on screen, with their speeds, so a hide
// during an entrance or a slide does not jump.
func (c *Core) leaveFrom(sc *screen, p Placement, shot *rectShot, now time.Time) {
	if !c.animOn() || p.Hidden || p.Preview > 0 || p.Rect.W <= 0 || p.Rect.H <= 0 {
		return
	}
	if sc.rects == nil {
		sc.rects = make(map[WindowID]rectMotion)
	}
	rm := rectMotion{scale: true, leaving: true, left: p}
	rm.left.Hidden, rm.left.Leaving, rm.left.Focused = true, true, false
	var off, vel rectOffsets
	fade, fadeV, dim, dimV := 0.0, 0.0, 0.0, 0.0
	if shot != nil {
		off, vel, fade, fadeV, dim, dimV = shot.off, shot.vel, shot.fade, shot.fadeV, shot.dim, shot.dimV
	}
	// The entry settles at 90 % of the rect it left, its content zoomed
	// to match (show): the offsets run from the rect it was drawn at (the
	// full one at rest) to that.
	end := scaledRect(p.Rect, appearScale)
	c.retargetComponent(&rm.x, &rm.dx, float64(p.Rect.X-end.X)+off.x, vel.x, now)
	c.retargetComponent(&rm.y, &rm.dy, float64(p.Rect.Y-end.Y)+off.y, vel.y, now)
	c.retargetComponent(&rm.w, &rm.dw, float64(p.Rect.W-end.W)+off.w, vel.w, now)
	c.retargetComponent(&rm.h, &rm.dh, float64(p.Rect.H-end.H)+off.h, vel.h, now)
	rm.left.Rect = end
	// The leaving fade is an offset from 1: the window drawn at fade f is
	// at f-1.
	c.retargetComponent(&rm.fade, &rm.df, fade-1, fadeV, now)
	c.retargetComponent(&rm.dim, &rm.ddim, dim, dimV, now)
	sc.rects[p.ID], sc.rectsWS = rm, sc.mon.Current()
}

// appearScale is the size, relative to the settled rect, a window appears
// from and leaves to.
const appearScale = 0.9

// scaledRect is r scaled by k around its centre, in float.
func scaledRect(r Rect, k float64) Rect {
	w, h := float64(r.W)*k, float64(r.H)*k
	x := float64(r.X) + (float64(r.W)-w)/2
	y := float64(r.Y) + (float64(r.H)-h)/2
	return Rect{X: int(math.Round(x)), Y: int(math.Round(y)), W: int(math.Round(w)), H: int(math.Round(h))}
}

// scaleFrom starts rm's rect components from settled scaled by k around
// its centre, toward settled.
func (c *Core) scaleFrom(rm *rectMotion, settled Rect, k float64, now time.Time) {
	w, h := float64(settled.W)*k, float64(settled.H)*k
	c.retargetComponent(&rm.x, &rm.dx, (float64(settled.W)-w)/2, 0, now)
	c.retargetComponent(&rm.y, &rm.dy, (float64(settled.H)-h)/2, 0, now)
	c.retargetComponent(&rm.w, &rm.dw, w-float64(settled.W), 0, now)
	c.retargetComponent(&rm.h, &rm.dh, h-float64(settled.H), 0, now)
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
			s.stash = full[len(out)].stash[:0]
		}
		s.sc = sc
		w := m.Current()
		s.ws, s.overview = w, m.ov.open
		if !m.ov.open {
			// The overview does not scroll or slide: no camera.
			s.viewX, s.view = w.ViewX, float64(w.ViewX)+w.shift
		}
		if w.motion.on {
			_, s.viewV = w.motion.sampleAt(now)
		}
		if m.switchMotion.on {
			_, s.switchV = m.switchMotion.sampleAt(now)
		}
		// settledLayout and shown are index-aligned and differ only by
		// the offsets of sc.rects: the shot keeps both unrounded. The
		// overview's cards are recorded like windows.
		for _, p := range sc.settledLayout {
			if p.Hidden {
				continue
			}
			r := rectShot{id: p.ID, rect: p.Rect}
			if rm, ok := sc.rects[p.ID]; ok {
				r.off, r.vel = rm.sample(now)
				r.fade = rm.df
				sampleComponent(rm.fade, &r.fade, &r.fadeV, now)
				r.dim = rm.ddim
				sampleComponent(rm.dim, &r.dim, &r.dimV, now)
			}
			r.veil = c.peekDim(p)
			s.rects = append(s.rects, r)
			if !m.ov.open && w.stashIndex(p.ID) >= 0 {
				s.stash = append(s.stash, p)
			}
		}
		if m.shown == nil && !m.ov.open {
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
// off, or while a swipe follows the fingers on that screen. Per-window rect
// motions join the camera here; with the overview open before or after
// there is no camera, only the cards' (transitionOverview).
//
// A workspace moved to another monitor (move-workspace-to-monitor-*, or a
// named workspace pulled to the focused one by a "workspace <name>" bind
// while it shows on another) is animated on both screens: the destination's
// windows slide in from where the source drew them (no workspace slide
// there), the source slides to its next workspace. The focus-monitor-*
// actions move nothing: no window changes place, so they are not animated
// (the focus pulse marks the new focus).
func (c *Core) transition(before []viewShot, now time.Time) {
	if !c.animOn() {
		return
	}
	for i := range before {
		b := &before[i]
		if b.ws == nil || !c.hasScreen(b.sc) || c.following(b.sc) {
			continue
		}
		if b.overview || b.sc.mon.ov.open {
			c.transitionOverview(b, now)
			continue
		}
		c.transitionCamera(b, before, now)
		c.transitionRects(b, before, now)
		c.transitionStash(b, now)
	}
}

// following reports whether a swipe moves sc's view with the fingers.
func (c *Core) following(sc *screen) bool {
	g := c.swipe
	return g != nil && g.screen == sc && (g.mode == swipeColumns || g.mode == swipeWorkspaces)
}

// transitionCamera slides the columns or the monitor from what b showed.
func (c *Core) transitionCamera(b *viewShot, before []viewShot, now time.Time) {
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
	case b.ok && m.shown == nil && movedFrom(b, before) == nil:
		// A screen that received its workspace from another monitor does
		// not slide to it: a running slide's list (switchList) may still
		// hold the workspace, which would start a slide from nowhere. Only
		// its windows move (transitionRects).
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
//
// A workspace that arrived from another monitor starts from the rects its
// source screen drew (found in before), translated to this screen's
// coordinates; a source whose workspace left, or any other change of
// workspace, gets none.
func (c *Core) transitionRects(b *viewShot, before []viewShot, now time.Time) {
	sc := b.sc
	m := sc.mon
	from, dx, dy := b, 0, 0
	if m.Current() != b.ws {
		// The motions are those of the workspace this screen left.
		sc.stopRects()
		if from = movedFrom(b, before); from == nil {
			return
		}
		dx, dy = from.sc.x-sc.x, from.sc.y-sc.y
	}
	for _, p := range m.Layout() {
		if p.Hidden || p.Preview > 0 {
			continue
		}
		i := slices.IndexFunc(from.rects, func(r rectShot) bool { return r.id == p.ID })
		if i < 0 {
			continue
		}
		old := &from.rects[i]
		oldRect := Rect{X: old.rect.X + dx, Y: old.rect.Y + dy, W: old.rect.W, H: old.rect.H}
		// A window that becomes a peek or stops being one (a stash
		// navigation) changes its veil: the offset takes the one drawn to
		// the new one. Otherwise a running dim motion goes on as it is.
		dimOff := old.dim + old.veil - c.peekDim(p)
		dimMoves := old.veil != c.peekDim(p)
		if oldRect == p.Rect && !dimMoves {
			// The settled rect did not change: a running motion goes on
			// as it is, and there is none to start.
			continue
		}
		if sc.rects == nil {
			sc.rects = make(map[WindowID]rectMotion)
		}
		// Where the window was drawn, unrounded, relative to where it goes.
		rm := sc.rects[p.ID]
		if oldRect != p.Rect {
			// A re-flow may change the aspect: the content zoom (appear)
			// stops following the frame and snaps to the settled size; the
			// fade goes on.
			rm.scale = false
			c.retargetComponent(&rm.x, &rm.dx, float64(oldRect.X-p.Rect.X)+old.off.x, old.vel.x, now)
			c.retargetComponent(&rm.y, &rm.dy, float64(oldRect.Y-p.Rect.Y)+old.off.y, old.vel.y, now)
			c.retargetComponent(&rm.w, &rm.dw, float64(oldRect.W-p.Rect.W)+old.off.w, old.vel.w, now)
			c.retargetComponent(&rm.h, &rm.dh, float64(oldRect.H-p.Rect.H)+old.off.h, old.vel.h, now)
		}
		if dimMoves {
			c.retargetComponent(&rm.dim, &rm.ddim, dimOff, old.dimV, now)
		}
		if rm.on() {
			sc.rects[p.ID], sc.rectsWS = rm, m.Current()
		} else {
			delete(sc.rects, p.ID)
		}
	}
}

// transitionStash animates the stash windows an action shows or hides on
// the workspace b's screen keeps on screen (a toggle-stash-visible, a click
// that closes the stash, a navigation that brings a window to a margin or
// sends it off): one drawn after and not before appears (appearAt), one
// drawn before and hidden after leaves from its last settled placement
// (leave). A window that is gone from the layout (unstashed, moved) or
// fullscreen is not touched: its rect motion, if any, is transitionRects's.
// Nothing during a session lock or with a covering fullscreen window (it
// keeps its direct scanout path).
func (c *Core) transitionStash(b *viewShot, now time.Time) {
	sc := b.sc
	m := sc.mon
	w := m.Current()
	if w != b.ws || len(w.Stash) == 0 || c.security.Protected || w.cover() != 0 {
		return
	}
	layout := m.Layout()
	for _, p := range b.stash {
		i := slices.IndexFunc(layout, func(q Placement) bool { return q.ID == p.ID })
		if i >= 0 && layout[i].Hidden && !p.Fullscreen {
			var shot *rectShot
			if j := slices.IndexFunc(b.rects, func(r rectShot) bool { return r.id == p.ID }); j >= 0 {
				shot = &b.rects[j]
			}
			c.leaveFrom(sc, p, shot, now)
		}
	}
	for _, p := range layout {
		if p.Hidden || p.Fullscreen || w.stashIndex(p.ID) < 0 || slices.ContainsFunc(b.rects, func(r rectShot) bool { return r.id == p.ID }) {
			continue
		}
		c.appearAt(sc, p, now)
	}
}

// peekDim is the veil the settled placement p draws: the configured stash
// dim for a peek, none for another window. The placement's Dim is an
// offset from it.
func (c *Core) peekDim(p Placement) float64 {
	if p.Peek {
		return c.cfg.Stash.Dim
	}
	return 0
}

// transitionOverview starts the motions of an action that opened or closed
// the overview, or moved inside it (the selection, the rows). The camera
// springs stay stopped (ToggleOverview): every window drawn before and
// after moves from its shown rect to its settled one, its content scaled
// with it (a card's Zoom, rectMotion.show). A window drawn after and not
// before (another row, a column the overview hid) fades in with no motion
// of its own; one drawn before and not after just goes. Motions of the
// other state are dropped first: their rects are not the ones snapshot
// measured from. A fade that ran on a window drawn in both states carries
// over (picking a card that still fades in does not pop it).
func (c *Core) transitionOverview(b *viewShot, now time.Time) {
	sc := b.sc
	changed := b.overview != sc.mon.ov.open
	if changed {
		sc.stopRects()
	}
	for _, p := range sc.mon.Layout() {
		if p.Hidden {
			continue
		}
		rm := sc.rects[p.ID]
		i := slices.IndexFunc(b.rects, func(r rectShot) bool { return r.id == p.ID })
		if i >= 0 && changed {
			c.retargetComponent(&rm.fade, &rm.df, b.rects[i].fade, b.rects[i].fadeV, now)
		}
		if i < 0 {
			c.retargetComponent(&rm.fade, &rm.df, 1, 0, now)
		} else if old := &b.rects[i]; old.rect != p.Rect {
			rm.scale = true
			c.retargetComponent(&rm.x, &rm.dx, float64(old.rect.X-p.Rect.X)+old.off.x, old.vel.x, now)
			c.retargetComponent(&rm.y, &rm.dy, float64(old.rect.Y-p.Rect.Y)+old.off.y, old.vel.y, now)
			c.retargetComponent(&rm.w, &rm.dw, float64(old.rect.W-p.Rect.W)+old.off.w, old.vel.w, now)
			c.retargetComponent(&rm.h, &rm.dh, float64(old.rect.H-p.Rect.H)+old.off.h, old.vel.h, now)
		} else {
			continue
		}
		if !rm.on() {
			delete(sc.rects, p.ID)
			continue
		}
		if sc.rects == nil {
			sc.rects = make(map[WindowID]rectMotion)
		}
		sc.rects[p.ID], sc.rectsWS = rm, sc.rectsOwner()
	}
}

// movedFrom is the shot of the other screen that showed the workspace b's
// screen shows now (moved to it by an action), or nil.
func movedFrom(b *viewShot, before []viewShot) *viewShot {
	w := b.sc.mon.Current()
	for i := range before {
		if o := &before[i]; o.ws == w && o.sc != b.sc && !o.overview {
			return o
		}
	}
	return nil
}
