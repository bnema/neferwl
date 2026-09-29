package core

import (
	"math"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A three-finger swipe follows the fingers: sideways it scrolls the columns,
// up or down it slides between workspaces. When the fingers lift, the view
// lands where the swipe's speed would carry it and a spring takes it there
// (motion.go). Where the view cannot slide (fixed overflow, the stash, a
// float, a fullscreen window), the swipe runs one focus action instead.

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
	// discreteSwipeMin is the projected distance a swipe needs to run a
	// focus action where the view cannot slide, or a four-finger swipe to
	// open or close the overview (half of niri's 300).
	discreteSwipeMin = 150.0
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
	// tracker adds up the movement along the chosen axis, natural scroll
	// applied: positive scrolls right or down.
	tracker swipeTracker
	// ws is the workspace whose columns scroll, or the active one when a
	// workspace slide began; start is where the view was when the swipe
	// took over: its x in pixels (columns) or the monitor's fractional
	// workspace index (workspaces). Both are absolute, so a focus change
	// during the swipe does not move the fingers' view.
	ws    *Workspace
	start float64
	// list is the numbered workspaces when a workspace slide began: start
	// indexes it, so the slide ends if the list changes.
	list []*Workspace
}

// listChanged reports whether the numbered workspaces or the active one
// changed since a workspace slide began, or the overview opened over it.
func (g *swipeGesture) listChanged(m *Monitor) bool {
	return m.overview || m.shown != nil || m.Workspaces[m.Active] != g.ws || !slices.Equal(m.Workspaces, g.list)
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
		g.tracker.push(d, u.Time)
		return false
	}
	g.tracker.push(d*c.swipeSign(), u.Time)
	m := g.screen.mon
	switch g.mode {
	case swipeColumns:
		if m.overview || m.Current() != g.ws || !g.ws.slidable() {
			g.ws.stopSlide()
			g.mode = swipeDropped
			return true
		}
		g.ws.shift = g.start + g.tracker.pos*g.ws.swipeScale() - float64(g.ws.ViewX)
		return true
	case swipeWorkspaces:
		if g.listChanged(m) {
			m.stopSwitch()
			g.mode = swipeDropped
			return true
		}
		f := workspaceBand.clamp(0, float64(len(m.Workspaces)-1), g.start+g.tracker.pos/workspaceSwipeMovement)
		m.switchOff = f - float64(m.Active)
		return true
	}
	return false
}

// decide picks the swipe's axis and what it moves. A running slide stops
// where it is: the fingers take it from there.
func (c *Core) decide(g *swipeGesture) {
	g.horizontal = math.Abs(g.cx) > math.Abs(g.cy)
	m := g.screen.mon
	w := m.Current()
	switch {
	case g.fingers == 4 && !g.horizontal:
		g.mode = swipeOverview
	case g.fingers == 4:
		// Four fingers sideways do nothing.
		g.mode = swipeDropped
	case m.overview:
		// The overview does not slide: the swipe moves its selection.
		g.mode = swipeDiscrete
	case g.horizontal && w.slidable():
		g.mode, g.ws = swipeColumns, w
		w.motion = nil
		g.start = float64(w.ViewX) + w.shift
	case !g.horizontal && m.shown == nil:
		// A landing slide measured in an older list lands at once first.
		if m.switchList != nil && !slices.Equal(m.switchList, m.Workspaces) {
			m.stopSwitch()
		}
		g.mode, g.ws = swipeWorkspaces, m.Workspaces[m.Active]
		g.list = slices.Clone(m.Workspaces)
		m.switchMotion, m.switchList = nil, nil
		g.start = float64(m.Active) + m.switchOff
	default:
		g.mode = swipeDiscrete
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
	// Idle time before the lift slows the swipe down.
	g.tracker.push(0, e.Time)
	now := c.now()
	m := g.screen.mon
	switch g.mode {
	case swipeColumns:
		w := g.ws
		if m.overview || m.Current() != w || !w.slidable() {
			w.stopSlide()
			return false
		}
		scale := w.swipeScale()
		shown := float64(w.ViewX) + w.shift
		if !e.Cancelled {
			target := g.start + g.tracker.projectedEnd()*scale
			w.ViewX, w.Focus = w.snap(target, target >= shown)
			// A column wider than the view aligns as focus moves align it.
			w.scroll()
			c.focusScreen = c.screenIndex(g.screen.name())
			c.layerFocus = 0
		}
		w.shift = shown - float64(w.ViewX)
		w.motion = newMotion(viewSpring(w.shift, g.tracker.velocity()*scale), now)
	case swipeWorkspaces:
		if g.listChanged(m) {
			m.stopSwitch()
			return false
		}
		last := float64(len(m.Workspaces) - 1)
		cur := float64(m.Active) + m.switchOff
		velocity := g.tracker.velocity() / workspaceSwipeMovement * workspaceBand.clampDerivative(0, last, g.start+g.tracker.pos/workspaceSwipeMovement)
		idx := m.Active
		if !e.Cancelled {
			idx = int(math.Round(min(max(g.start+g.tracker.projectedEnd()/workspaceSwipeMovement, 0), last)))
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
			c.layerFocus = 0
		}
		m.switchOff, m.switchList = off, g.list
		m.switchMotion = newMotion(workspaceSpring(off, velocity), now)
	case swipeOverview:
		p := g.tracker.projectedEnd()
		if e.Cancelled || math.Abs(p) < discreteSwipeMin || (p < 0) == m.overview {
			return false
		}
		before := m.Current()
		m.ToggleOverview()
		c.focusScreen = c.screenIndex(g.screen.name())
		c.layerFocus = 0
		return m.Current() != before
	case swipeDiscrete:
		p := g.tracker.projectedEnd()
		if e.Cancelled || math.Abs(p) < discreteSwipeMin {
			return false
		}
		a := ActionFocusWorkspaceDown
		switch {
		case g.horizontal && p < 0:
			a = ActionFocusColumnLeft
		case g.horizontal:
			a = ActionFocusColumnRight
		case p < 0:
			a = ActionFocusWorkspaceUp
		}
		// Focus actions act on the focused screen: the swipe's, unless the
		// pointer took the focus to another output meanwhile.
		if c.cur() != g.screen {
			return false
		}
		before := c.cur().mon.Current()
		c.layerFocus = 0
		if mon := c.cur().mon; mon.overview {
			mon.overviewSwipe(a)
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

// animate moves the running springs to now; settled ones stop.
func (c *Core) animate(now time.Time) {
	for _, sc := range c.screens {
		m := sc.mon
		if m.switchMotion != nil {
			v, done := m.switchMotion.at(now)
			m.switchOff = v
			if done {
				m.stopSwitch()
			}
		}
		for _, w := range m.all() {
			if w.motion != nil {
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
	return i >= 0 && c.screens[i].mon.springing()
}

// animating reports whether a spring runs on any output.
func (c *Core) animating() bool {
	for _, sc := range c.screens {
		if sc.mon.springing() {
			return true
		}
	}
	return false
}

func (m *Monitor) springing() bool {
	if m.switchMotion != nil {
		return true
	}
	for _, w := range m.all() {
		if w.motion != nil {
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
		if r := sc.info.RefreshMilli; r > 0 && sc.mon.springing() {
			refresh = min(refresh, r)
		}
	}
	return 2 * time.Second * 1000 / time.Duration(refresh)
}

// armFrame starts the fallback timer of a running slide.
func (c *Core) armFrame() {
	c.stopFrame()
	d := c.frameFallback()
	if c.ch.Clock != nil {
		t := c.ch.Clock.NewTimer(d)
		c.frameC, c.frameStop = t.C(), t.Stop
		return
	}
	t := time.NewTimer(d)
	c.frameC, c.frameStop = t.C, t.Stop
}

func (c *Core) stopFrame() {
	if c.frameStop != nil {
		c.frameStop()
	}
	c.frameC, c.frameStop = nil, nil
}
