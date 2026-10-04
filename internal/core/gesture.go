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
// on the closest, and a spring takes the view there (motion.go). Where the
// view cannot slide (fixed overflow, the stash, a float, a fullscreen
// window, the overview), the swipe runs one focus action instead.

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
)

type swipeMode uint8

const (
	swipeUndecided swipeMode = iota
	swipeColumns
	swipeWorkspaces
	swipeDiscrete
	// swipeOverview is a vertical four-finger swipe: up opens the
	// overview, down closes it on the selection (niri's gesture).
	swipeOverview
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
	// opens is the monitor's overviewOpens when the swipe picked its
	// mode: a slide the overview opened over is dropped, even once the
	// overview closed again.
	opens int
}

// listChanged reports whether the numbered workspaces or the active one
// changed since a workspace slide began, or the overview opened over it.
func (g *swipeGesture) listChanged(m *Monitor) bool {
	return m.overviewOpens != g.opens || m.shown != nil || m.Workspaces[m.Active] != g.ws || !slices.Equal(m.Workspaces, g.list)
}

// columnsChanged reports whether a column swipe lost its workspace: it is
// no longer shown or slidable, its snap points moved (an output resize, a
// panel, a window mapped or resized, other gaps), or the overview opened
// over it.
func (g *swipeGesture) columnsChanged(m *Monitor) bool {
	return m.overviewOpens != g.opens || m.Current() != g.ws || !g.ws.slidable() || !slices.Equal(g.ws.snapPoints(), g.points)
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
	return changed
}

// swipeUpdate moves the swipe; it reports whether the scene changed.
func (c *Core) swipeUpdate(u ports.SwipeUpdate) bool {
	g := c.swipe
	if g == nil || !c.hasScreen(g.screen) {
		c.swipe = nil
		return false
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
			g.ws.stopSlide()
			g.mode = swipeDropped
			return true
		}
		g.ws.shift = g.snap.pos() - float64(g.ws.ViewX)
		return true
	case swipeWorkspaces:
		if g.listChanged(m) {
			m.stopSwitch()
			g.mode = swipeDropped
			return true
		}
		m.switchOff = g.snap.pos() - float64(m.Active)
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
		// The overview does not slide: the swipe moves its selection.
		g.mode, g.snap = swipeDiscrete, newStepSwipe()
	case g.horizontal && w.slidable():
		g.mode, g.ws, g.points = swipeColumns, w, w.snapPoints()
		w.motion = motion{}
		g.snap = newSnapSwipe(float64(w.ViewX)+w.shift, float64(w.ViewX), w.swipeScale(), g.points, workspaceBand.scaled(float64(w.Usable.W)))
	case !g.horizontal && m.shown == nil:
		// A landing slide measured in an older list lands at once first.
		if m.switchList != nil && !slices.Equal(m.switchList, m.Workspaces) {
			m.stopSwitch()
		}
		g.mode, g.ws = swipeWorkspaces, m.Workspaces[m.Active]
		g.list = slices.Clone(m.Workspaces)
		m.switchMotion, m.switchList = motion{}, nil
		g.snap = newSnapSwipe(float64(m.Active)+m.switchOff, float64(m.Active), 1/workspaceSwipeMovement, indexPoints(len(m.Workspaces)), workspaceBand)
	default:
		g.mode, g.snap = swipeDiscrete, newStepSwipe()
	}
}

// swipeEnd lands the swipe. shown reports that another workspace came on
// screen.
func (c *Core) swipeEnd(e ports.SwipeEnd) (shown bool) {
	g := c.swipe
	c.swipe = nil
	if g == nil || !c.hasScreen(g.screen) {
		return false
	}
	now := c.now()
	m := g.screen.mon
	switch g.mode {
	case swipeColumns:
		w := g.ws
		if g.columnsChanged(m) {
			w.stopSlide()
			return false
		}
		shown := float64(w.ViewX) + w.shift
		target, velocity := g.snap.end(e.Cancelled, e.Time)
		if !e.Cancelled {
			view := int(math.Round(target))
			w.Focus = w.snapFocus(view, target >= shown)
			w.ViewX = view
			// A column wider than the view aligns as focus moves align it.
			w.scroll()
			c.focusScreen = c.screenIndex(g.screen.name())
			c.keyboard.takeBack()
		}
		w.shift = shown - float64(w.ViewX)
		if c.animOn() {
			w.motion = c.spring(viewSpring(w.shift, velocity), now)
		} else {
			w.stopSlide()
		}
	case swipeWorkspaces:
		if g.listChanged(m) {
			m.stopSwitch()
			return false
		}
		cur := float64(m.Active) + m.switchOff
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
		m.switchOff, m.switchList = off, g.list
		if c.animOn() {
			m.switchMotion = c.spring(workspaceSpring(off, velocity), now)
		} else {
			m.stopSwitch()
		}
	case swipeOverview:
		step := g.snap.step(e.Cancelled, e.Time)
		if step == 0 || (step < 0) == m.ov.open {
			return false
		}
		before := m.Current()
		m.ToggleOverview()
		c.focusScreen = c.screenIndex(g.screen.name())
		c.keyboard.takeBack()
		return m.Current() != before
	case swipeDiscrete:
		step := g.snap.step(e.Cancelled, e.Time)
		if step == 0 {
			return false
		}
		a := ActionFocusWorkspaceDown
		switch {
		case g.horizontal && step < 0:
			a = ActionFocusColumnLeft
		case g.horizontal:
			a = ActionFocusColumnRight
		case step < 0:
			a = ActionFocusWorkspaceUp
		}
		// Focus actions act on the focused screen: the swipe's, unless the
		// pointer took the focus to another output meanwhile.
		if c.cur() != g.screen {
			return false
		}
		before := c.cur().mon.Current()
		c.keyboard.takeBack()
		if mon := c.cur().mon; mon.ov.open {
			mon.overviewFocus(a)
		} else {
			c.applyAction(a)
		}
		return c.cur().mon.Current() != before
	}
	return shown
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
		g.ws.stopSlide()
	case swipeWorkspaces:
		g.screen.mon.stopSwitch()
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

// spring starts s at now, slowed down by animations.slowdown. Every motion
// core creates goes through it, except a retarget, which keeps the slowdown
// of the motion it replaces.
func (c *Core) spring(s spring, now time.Time) motion {
	return newMotion(s, now, c.cfg.Animations.Slowdown)
}

// stopAnimations settles every running spring where it is going and stops
// the frame timer: a session lock or a switch to animations = off. A swipe
// in progress keeps its fingers' view: only running springs stop. Per-window
// rect motions stop here too once they exist.
func (c *Core) stopAnimations() {
	for _, sc := range c.screens {
		if sc.mon.switchMotion.on {
			sc.mon.stopSwitch()
		}
		for w := range sc.mon.all() {
			if w.motion.on {
				w.stopSlide()
			}
		}
	}
	c.stopFrame()
}

// animate moves the running springs to now; settled ones stop. A non-nil only
// limits it to that screen (its own page flip); nil moves every screen.
func (c *Core) animate(now time.Time, only *screen) {
	for _, sc := range c.screens {
		if only != nil && sc != only {
			continue
		}
		m := sc.mon
		if m.switchMotion.on {
			v, done := m.switchMotion.at(now)
			m.switchOff = v
			if done {
				m.stopSwitch()
			}
		}
		for w := range m.all() {
			if w.motion.on {
				v, done := w.motion.at(now)
				w.shift = v
				if done {
					w.stopSlide()
				}
			}
		}
	}
}

// sliding reports whether a spring runs on the named output.
func (c *Core) sliding(output string) bool {
	i := c.screenIndex(output)
	return i >= 0 && (c.screens[i].mon.springing() || c.pulsing(c.screens[i]))
}

// animating reports whether a spring or the focus pulse runs on any output.
func (c *Core) animating() bool {
	if c.pulse.id != 0 {
		return true
	}
	for _, sc := range c.screens {
		if sc.mon.springing() {
			return true
		}
	}
	return false
}

func (m *Monitor) springing() bool {
	if m.switchMotion.on {
		return true
	}
	for w := range m.all() {
		if w.motion.on {
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
		if r := sc.info.RefreshMilli; r > 0 && (sc.mon.springing() || c.pulsing(sc)) {
			refresh = min(refresh, r)
		}
	}
	return 2 * time.Second * 1000 / time.Duration(refresh)
}

// armFrame starts the fallback timer of a running slide.
func (c *Core) armFrame() {
	c.stopFrame()
	c.frameC, c.frameStop = newTimer(c.ch.Clock, c.frameFallback())
}

func (c *Core) stopFrame() {
	if c.frameStop != nil {
		c.frameStop()
	}
	c.frameC, c.frameStop = nil, nil
}
