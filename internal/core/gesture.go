package core

import (
	"math"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A three-finger swipe follows the fingers: sideways it scrolls the columns,
// up or down it slides between workspaces. Each swipe moves one step at
// most (snapswipe.go): the next column edge or the next workspace. When
// the fingers lift, a quick swipe goes on to that step, a slow one settles
// on the closest, and a spring takes the view there (motion.go). Sideways
// over the shown stash the swipe slides the stash the same way, one window
// at most. Where the view cannot slide (fixed overflow, a float, a
// fullscreen window), the swipe runs one focus action instead.
// In the overview, three fingers use distance-based selection during movement.

const (
	// swipeDecide is the touchpad distance before the swipe picks its axis
	// (GNOME Shell's threshold).
	swipeDecide = 16.0
	// viewSwipeMovement is the touchpad distance that scrolls the columns by
	// one usable width.
	viewSwipeMovement = 1200.0
	// workspaceSwipeMovement is the touchpad distance that slides one
	// workspace.
	workspaceSwipeMovement = 300.0
	// stashSwipeMovement is the touchpad distance that slides the stash by
	// one window.
	stashSwipeMovement = 300.0
)

type swipeMode uint8

const (
	swipeUndecided swipeMode = iota
	swipeColumns
	swipeWorkspaces
	swipeDiscrete
	// swipeNavigation navigates overview cards during a three-finger gesture.
	swipeNavigation
	// swipeOverview is a vertical four-finger swipe: up opens the
	// overview, down closes it on the selection (niri's gesture). With
	// the overview closed, down shows or hides the stash, and up hides
	// it when it is shown, else opens the overview.
	swipeOverview
	// swipeStash is a sideways swipe over the shown stash: its view
	// follows the fingers, with a stop on each window, and lands on a
	// neighbor of the selection at most.
	swipeStash
	// swipeDropped ignores the rest of a swipe whose workspace changed.
	swipeDropped
)

// swipeGesture is the swipe in progress, on the screen focused when it began.
type swipeGesture struct {
	screen     *screen
	fingers    int
	mode       swipeMode
	horizontal bool
	cx, cy     float64
	// snap follows the movement along the chosen axis, natural scroll
	// applied: positive scrolls right or down. Its view is absolute: the
	// columns' x in pixels or the monitor's fractional workspace index,
	// so a focus change during the swipe does not move the fingers' view.
	// A discrete or overview swipe counts steps of -1, 0 or 1.
	snap snapSwipe
	// ws is the workspace whose columns scroll, or the active one when a
	// workspace slide began.
	ws *Workspace
	// points are ws's column snap points when a column swipe began: the
	// swipe ends if the layout no longer has them.
	points []float64
	// list is the numbered workspaces when a workspace slide began: the
	// slide indexes it, so it ends if the list changes.
	list []*Workspace
	// ids and home are the stash windows and the selection when a stash
	// swipe began: it ends if either changes.
	ids  []WindowID
	home int
	// opens is the monitor's overviewOpens when the swipe picked its
	// mode: a slide the overview opened over is dropped, even once the
	// overview closed again.
	opens int
}

// listChanged reports whether the numbered workspaces or the active one
// changed since a workspace slide began, or the overview opened over it.
func (g *swipeGesture) listChanged(m *Monitor) bool {
	return m.overviewOpens != g.opens || m.shown != nil || m.Workspaces[m.Active] != g.ws || g.horizontal != (g.ws.policy().workspace == horizontalAxis) || !slices.Equal(m.Workspaces, g.list)
}

// columnsChanged reports whether a column swipe lost its workspace: it is
// no longer shown or slidable, its snap points moved (an output resize, a
// panel, a window mapped or resized, other gaps), or the overview opened
// over it.
func (g *swipeGesture) columnsChanged(m *Monitor) bool {
	if m.overviewOpens != g.opens || m.Current() != g.ws || !g.ws.slidable() || g.horizontal != (g.ws.policy().content == horizontalAxis) {
		return true
	}
	if g.ws.policy().wraps {
		n := 1
		if len(g.ws.Columns) > 0 && g.ws.Usable.H > 0 {
			n = g.ws.band(len(g.ws.Columns)-1) + 1
		}
		if len(g.points) != n {
			return true
		}
		for i, p := range g.points {
			if p != float64(i*g.ws.Usable.H) {
				return true
			}
		}
		return false
	}
	return !slices.Equal(g.ws.snapPoints(), g.points)
}

// stashChanged reports whether a stash swipe lost its stash: it is no
// longer shown or focused, its windows or selection changed, or the
// overview opened over it.
func (g *swipeGesture) stashChanged(m *Monitor) bool {
	w := g.ws
	return m.overviewOpens != g.opens || m.Current() != w || !w.stashFocused() || w.floatFocus || !w.stashShown() || w.stashAt != g.home ||
		!slices.EqualFunc(w.Stash, g.ids, func(f Float, id WindowID) bool { return f.ID == id })
}

// swipeSign turns finger movement into view movement: natural scroll moves
// the content with the fingers.
func (c *Core) swipeSign() float64 {
	if c.cfg.Touchpad.NaturalScroll {
		return -1
	}
	return 1
}

// swipeBegin starts a swipe. Input streams one swipe at a time; one still
// running (its end was lost) ends cancelled first: its slide settles back.
func (c *Core) swipeBegin(b ports.SwipeBegin) bool {
	changed := false
	if c.swipe != nil {
		c.swipeEnd(ports.SwipeEnd{Cancelled: true, Time: b.Time})
		changed = true
	}
	c.swipe = &swipeGesture{screen: c.cur(), fingers: b.Fingers}
	if b.Fingers == 3 && c.cur().mon.ov.open && !c.overviewKeyboardTaken() {
		c.scrollStop(ports.PointerAxis{Horizontal: ports.ScrollAxis{Stop: true}})
		c.swipe.mode = swipeNavigation
	}
	return changed
}

// swipeUpdate moves the swipe; it reports whether the scene changed.
func (c *Core) swipeUpdate(u ports.SwipeUpdate) bool {
	g := c.swipe
	if g == nil {
		return false
	}
	if !c.hasScreen(g.screen) {
		// Its screen went: let go of what it moved, or that stays shifted.
		c.dropSwipe()
		c.swipe = nil
		return false
	}
	if g.mode == swipeNavigation {
		m := g.screen.mon
		if !m.ov.open || c.cur() != g.screen || c.overviewKeyboardTaken() {
			g.mode = swipeDropped
			return false
		}
		now := c.now()
		shots := c.snapshot(now)
		changed := m.overviewScroll(ports.PointerAxis{Source: ports.AxisFinger,
			Horizontal: ports.ScrollAxis{Set: true, Value: u.DX * c.swipeSign()},
			Vertical:   ports.ScrollAxis{Set: true, Value: u.DY * c.swipeSign()}})
		if changed {
			c.keyboard.takeBack()
			c.transition(shots, now)
		}
		return changed
	}
	if g.mode == swipeUndecided {
		g.cx += u.DX
		g.cy += u.DY
		if g.cx*g.cx+g.cy*g.cy < swipeDecide*swipeDecide {
			return false
		}
		c.decide(g)
	}
	d := u.DY
	if g.horizontal {
		d = u.DX
	}
	if g.mode == swipeOverview {
		// Like niri, the overview gesture ignores natural scroll: the
		// fingers going up open it.
		g.snap.push(d, u.Time)
		return false
	}
	g.snap.push(d*c.swipeSign(), u.Time)
	m := g.screen.mon
	switch g.mode {
	case swipeColumns:
		if g.columnsChanged(m) {
			g.ws.view.stop()
			g.mode = swipeDropped
			return true
		}
		g.ws.view.off = g.snap.pos() - float64(g.ws.View)
		return true
	case swipeWorkspaces:
		if g.listChanged(m) {
			m.stopSwitch()
			g.mode = swipeDropped
			return true
		}
		m.switchView.off = g.snap.pos() - float64(m.Active)
		return true
	case swipeStash:
		if g.stashChanged(m) {
			g.ws.stashView.stop()
			g.mode = swipeDropped
			return true
		}
		g.ws.stashView.off = g.snap.pos() - float64(g.ws.stashAt)
		return true
	}
	return false
}

// decide picks the swipe's axis and what it moves. A running slide stops
// where it is: the fingers take it from there.
func (c *Core) decide(g *swipeGesture) {
	g.horizontal = math.Abs(g.cx) > math.Abs(g.cy)
	m := g.screen.mon
	g.opens = m.overviewOpens
	w := m.Current()
	switch {
	case g.fingers == 4 && !g.horizontal:
		g.mode, g.snap = swipeOverview, newStepSwipe()
	case g.fingers == 4:
		// Four fingers sideways do nothing.
		g.mode = swipeDropped
	case m.ov.open:
		// Overview navigation is chosen at begin, never on release.
		g.mode = swipeDropped
	case !c.animOn():
		// With animations off nothing slides: the swipe runs a
		// focus action when the fingers lift.
		g.mode, g.snap = swipeDiscrete, newStepSwipe()
	case g.horizontal && w.stashFocused() && !w.floatFocus && w.stashShown():
		g.mode, g.ws, g.home = swipeStash, w, w.stashAt
		g.ids = make([]WindowID, 0, len(w.Stash))
		for _, f := range w.Stash {
			g.ids = append(g.ids, f.ID)
		}
		w.stashView.motion = motion{}
		g.snap = newSnapSwipe(float64(w.stashAt)+w.stashView.off, float64(w.stashAt), 1/stashSwipeMovement, indexPoints(len(w.Stash)), workspaceBand)
	case g.horizontal == (w.policy().content == horizontalAxis) && w.slidable():
		g.mode, g.ws, g.points = swipeColumns, w, w.snapPoints()
		w.view.motion = motion{}
		g.snap = newSnapSwipe(float64(w.View)+w.view.off, float64(w.View), w.swipeScale(), g.points, workspaceBand.scaled(float64(w.policy().content.span(w.Usable))))
	case g.horizontal == (w.policy().workspace == horizontalAxis) && m.shown == nil:
		// A landing slide measured in an older list lands at once first.
		if m.switchList != nil && !slices.Equal(m.switchList, m.Workspaces) {
			m.stopSwitch()
		}
		g.mode, g.ws = swipeWorkspaces, m.Workspaces[m.Active]
		axis := w.policy().workspace
		if m.switchView.busy() && m.switchAxis != axis {
			m.stopSwitch()
		}
		m.switchAxis = axis
		g.list = slices.Clone(m.Workspaces)
		m.switchView.motion, m.switchList = motion{}, nil
		g.snap = newSnapSwipe(float64(m.Active)+m.switchView.off, float64(m.Active), 1/workspaceSwipeMovement, indexPoints(len(m.Workspaces)), workspaceBand)
	default:
		g.mode, g.snap = swipeDiscrete, newStepSwipe()
	}
}

// swipeEnd lands the swipe. shown reports that another workspace came on
// screen.
func (c *Core) swipeEnd(e ports.SwipeEnd) (shown bool) {
	g := c.swipe
	if g != nil && !c.hasScreen(g.screen) {
		c.dropSwipe()
	}
	c.swipe = nil
	if g == nil || !c.hasScreen(g.screen) {
		return false
	}
	now := c.now()
	m := g.screen.mon
	switch g.mode {
	case swipeNavigation:
		c.scrollStop(ports.PointerAxis{Horizontal: ports.ScrollAxis{Stop: true}})
		return false
	case swipeColumns:
		w := g.ws
		if g.columnsChanged(m) {
			w.view.stop()
			return false
		}
		shown := float64(w.View) + w.view.off
		target, velocity := g.snap.end(e.Cancelled, e.Time)
		if !e.Cancelled {
			view := int(math.Round(target))
			focus := w.snapFocus(view, target >= shown)
			if focus != w.Focus && w.policy().equalCells {
				w.unmaximize()
			}
			w.Focus = focus
			w.View = view
			// A column wider than the view aligns as focus moves align it.
			w.scroll()
			c.focusScreen = c.screenIndex(g.screen.name())
			c.keyboard.takeBack()
		}
		w.view.off = shown - float64(w.View)
		if c.animOn() {
			w.view.motion = c.spring(viewSpring(w.view.off, velocity), now)
		} else {
			w.view.stop()
		}
	case swipeWorkspaces:
		if g.listChanged(m) {
			m.stopSwitch()
			return false
		}
		cur := float64(m.Active) + m.switchView.off
		target, velocity := g.snap.end(e.Cancelled, e.Time)
		idx := m.Active
		if !e.Cancelled {
			idx = int(math.Round(target))
		}
		// Focus may drop the empty workspace left behind and renumber the
		// list: the slide keeps measuring in the list it began on.
		off := cur - float64(idx)
		if idx != m.Active {
			m.Focus(idx)
			shown = true
		}
		if !e.Cancelled {
			c.focusScreen = c.screenIndex(g.screen.name())
			c.keyboard.takeBack()
		}
		m.switchView.off, m.switchList = off, g.list
		if c.animOn() {
			m.switchView.motion = c.spring(m.switchSpring(off, velocity), now)
		} else {
			m.stopSwitch()
		}
	case swipeStash:
		w := g.ws
		if g.stashChanged(m) {
			w.stashView.stop()
			return false
		}
		drawn := float64(w.stashAt) + w.stashView.off
		target, velocity := g.snap.end(e.Cancelled, e.Time)
		dir := 0
		if !e.Cancelled {
			dir = int(math.Round(target)) - w.stashAt
		}
		if c.cur() == g.screen && dir != 0 {
			// The landing is the focus action's: the selection, the focus
			// and the veils move as a bind moves them.
			a := ActionFocusColumnRight
			if dir < 0 {
				a = ActionFocusColumnLeft
			}
			c.keyboard.takeBack()
			c.applyAction(a)
		}
		w.stashView.off = drawn - float64(w.stashAt)
		if c.animOn() {
			w.stashView.motion = c.spring(w.stashSpring(w.stashView.off, velocity), now)
		} else {
			w.stashView.stop()
		}
		// The stash never brings another workspace on screen.
		return false
	case swipeOverview:
		step := g.snap.step(e.Cancelled, e.Time)
		if step == 0 {
			return false
		}
		if !m.ov.open && (step > 0 || m.Current().stashShown()) {
			return c.swipeStash(g, now)
		}
		if (step < 0) == m.ov.open {
			return false
		}
		before := m.Current()
		shots := c.snapshot(now)
		m.ToggleOverview()
		c.focusScreen = c.screenIndex(g.screen.name())
		c.keyboard.takeBack()
		c.transition(shots, now)
		return m.Current() != before
	case swipeDiscrete:
		step := g.snap.step(e.Cancelled, e.Time)
		if step == 0 {
			return false
		}
		w := g.screen.mon.Current()
		p := w.policy()
		workspace := g.horizontal == (p.workspace == horizontalAxis)
		a := ActionFocusColumnRight
		if step < 0 {
			a = ActionFocusColumnLeft
		}
		if workspace {
			a = ActionFocusWorkspaceDown
			if step < 0 {
				a = ActionFocusWorkspaceUp
			}
		} else if p.wraps {
			// A content swipe cannot escape the last band to another workspace.
			if w.onFloat() || w.fullscreen != 0 || w.bandNeighbor(step) < 0 {
				return false
			}
			a = ActionFocusWindowDown
			if step < 0 {
				a = ActionFocusWindowUp
			}
		}
		// Focus actions act on the focused screen: the swipe's, unless the
		// pointer took the focus to another output meanwhile.
		if c.cur() != g.screen {
			return false
		}
		if c.cur().mon.ov.open {
			return false
		}
		before := c.cur().mon.Current()
		shots := c.snapshot(now)
		c.keyboard.takeBack()
		c.applyAction(a)
		c.transition(shots, now)
		return c.cur().mon.Current() != before
	}
	return shown
}

// swipeStash runs toggle-stash-visible for a four-finger swipe down, or up
// while the stash is shown, with the overview closed, on the swipe's screen
// unless the pointer took the focus to another output meanwhile. It reports
// whether the stash showed or hid: an empty stash stays as it is.
func (c *Core) swipeStash(g *swipeGesture, now time.Time) bool {
	if c.cur() != g.screen {
		return false
	}
	w := g.screen.mon.Current()
	hidden, over := w.stashHidden, w.stashOver
	shots := c.snapshot(now)
	c.keyboard.takeBack()
	c.applyAction(ActionToggleStashVisible)
	c.transition(shots, now)
	return w.stashHidden != hidden || w.stashOver != over
}

// swipedWorkspace is the workspace on the swipe's screen, or nil without
// a swipe.
func (c *Core) swipedWorkspace() *Workspace {
	if c.swipe == nil {
		return nil
	}
	return c.swipe.screen.mon.Current()
}

// dropSwipe lets go of the swipe in progress: a bind changed the workspace
// on its screen. Its slide stops where the bind left the view; the rest of
// the swipe is ignored, even if a later bind shows its workspace again.
func (c *Core) dropSwipe() {
	g := c.swipe
	if g == nil {
		return
	}
	switch g.mode {
	case swipeColumns:
		g.ws.view.stop()
	case swipeWorkspaces:
		g.screen.mon.stopSwitch()
	case swipeStash:
		g.ws.stashView.stop()
	}
	g.mode = swipeDropped
}

func (c *Core) hasScreen(s *screen) bool {
	for _, v := range c.screens {
		if v == s {
			return true
		}
	}
	return false
}

// animOn reports whether transitions run (animations = on).
func (c *Core) animOn() bool { return c.cfg.Animations.On }

// spring starts s at now, stretched by animations.speed (Slowdown). Every motion
// core creates goes through it, except a retarget, which keeps the slowdown
// of the motion it replaces.
func (c *Core) spring(s spring, now time.Time) motion {
	return newMotion(s, now, c.cfg.Animations.Slowdown)
}

// stopAnimations settles every running spring where it is going and stops
// the frame timer: a session lock or a switch to animations = off. A swipe
// in progress keeps its fingers' view: only running springs stop. Per-window
// rect motions stop too.
func (c *Core) stopAnimations() {
	for _, sc := range c.screens {
		sc.stopAnimations()
	}
	c.stopFrame()
}

// stopAnimations settles the screen's running springs where they are going.
func (s *screen) stopAnimations() {
	if s.mon.switchView.motion.on {
		s.mon.stopSwitch()
	}
	for w := range s.mon.all() {
		if w.view.motion.on {
			w.view.stop()
		}
		if w.stashView.motion.on {
			w.stashView.stop()
		}
	}
	s.stopRects()
}

// animate moves the running springs to now; settled ones stop. A non-nil only
// limits it to that screen (its own page flip); nil moves every screen.
func (c *Core) animate(now time.Time, only *screen) {
	for _, sc := range c.screens {
		if only != nil && sc != only {
			continue
		}
		m := sc.mon
		if m.switchView.motion.on {
			m.switchView.step(now)
			if !m.switchView.motion.on {
				m.switchList = nil
			}
		}
		for w := range m.all() {
			w.view.step(now)
			w.stashView.step(now)
		}
		for id, rm := range sc.rects {
			rm.advance(now)
			if rm.on() {
				sc.rects[id] = rm
			} else {
				delete(sc.rects, id)
			}
		}
	}
}

// sliding reports whether a spring runs on the named output.
func (c *Core) sliding(output string) bool {
	i := c.screenIndex(output)
	return i >= 0 && (c.screens[i].springing() || c.pulsing(c.screens[i]))
}

// animating reports whether a spring or the focus pulse runs on any output.
func (c *Core) animating() bool {
	if c.pulse.id != 0 {
		return true
	}
	for _, sc := range c.screens {
		if sc.springing() {
			return true
		}
	}
	return false
}

func (m *Monitor) springing() bool {
	if m.switchView.motion.on {
		return true
	}
	for w := range m.all() {
		if w.view.motion.on || w.stashView.motion.on {
			return true
		}
	}
	return false
}

// frameFallback is how long core waits for a page flip before it moves a
// running slide on its own (an output that stopped flipping).
func (c *Core) frameFallback() time.Duration {
	refresh := 60000
	for _, sc := range c.screens {
		if r := sc.info.RefreshMilli; r > 0 && (sc.springing() || c.pulsing(sc)) {
			refresh = min(refresh, r)
		}
	}
	return 2 * time.Second * 1000 / time.Duration(refresh)
}

// armFrame starts the fallback timer of a running slide. The timer is made
// on the first arm and reset afterwards (Go 1.23 timers deliver nothing
// stale after a Stop or Reset), so a frame costs no allocation.
func (c *Core) armFrame() {
	d := c.frameFallback()
	if c.frameTimer == nil {
		c.frameTimer = newPortTimer(c.opts.Clock, d)
	} else {
		c.frameTimer.Stop()
		c.frameTimer.Reset(d)
	}
	c.frameC = c.frameTimer.C()
}

func (c *Core) stopFrame() {
	if c.frameC != nil {
		c.frameTimer.Stop()
	}
	c.frameC = nil
}
