package core

import (
	"context"
	"math"
	"slices"
	"time"
)

// The column switcher is an Alt+Tab for the tiled columns of the workspace on
// screen. Each workspace keeps the columns it focused, most recent first
// (recent); the switcher steps through them without moving the focus, and
// commits the selected one when the command key is released.

// switcherRecentMax caps a workspace's recent list.
const switcherRecentMax = 64

// switcherDelay is how long the command key must stay held before the cards
// show: a quick tap swaps at once and flashes nothing.
const switcherDelay = 150 * time.Millisecond

// switcherState is the switcher of a monitor. A window stands for each
// column in order, the focused column first. Nothing moves until the commit;
// shown is set once the cards are drawn.
type switcherState struct {
	open, shown bool
	ws          *Workspace
	order       []WindowID
	at          int
	// sizes are the client sizes the cards are scaled from.
	sizes map[WindowID]Rect
}

// switcherShown reports whether the switcher's cards are drawn. They belong
// to the workspace that was on screen when they showed: if another one is on
// screen now (an activation, the taskbar, an output coming back) the cards
// are stale and the monitor draws its workspace again.
func (m *Monitor) switcherShown() bool { return m.sw.shown && m.sw.ws == m.Current() }

// previewing reports whether the monitor draws previews instead of its
// workspace: the overview, or the switcher's cards.
func (m *Monitor) previewing() bool { return m.ov.open || m.switcherShown() }

// noteFocus records the focused column as the most recently used. It does
// nothing while a float or the stash has the focus, and allocates nothing
// when the focused column is already the latest.
func (w *Workspace) noteFocus() {
	if w.floatFocus || w.stashFocused() || w.Focus < 0 || w.Focus >= len(w.Columns) {
		return
	}
	col := w.Columns[w.Focus]
	id := col.Windows[col.Focus]
	if len(w.recent) > 0 && w.columnOf(w.recent[0]) == w.Focus {
		w.recent[0] = id
		return
	}
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return v == id || w.columnOf(v) == w.Focus })
	w.recent = slices.Insert(w.recent, 0, id)
	if len(w.recent) > switcherRecentMax {
		w.recent = w.recent[:switcherRecentMax]
	}
}

// switchOrder lists one window per column, the focused window of each: the
// focused column first, then the others by recent use, then those never
// used by position. It is nil with fewer than two columns.
func (w *Workspace) switchOrder() []WindowID {
	n := len(w.Columns)
	if n < 2 || w.Focus < 0 || w.Focus >= n {
		return nil
	}
	seen := make([]bool, n)
	order := make([]WindowID, 0, n)
	add := func(i int) {
		if seen[i] {
			return
		}
		seen[i] = true
		c := w.Columns[i]
		order = append(order, c.Windows[c.Focus])
	}
	add(w.Focus)
	for _, id := range w.recent {
		if i := w.columnOf(id); i >= 0 {
			add(i)
		}
	}
	for i := range n {
		add(i)
	}
	return order
}

// heir is the window of id's column that is not id, or 0: call it before id
// is removed, then hand it to remove.
func (s *switcherState) heir(id WindowID) WindowID {
	if !s.open || s.ws == nil || !slices.Contains(s.order, id) {
		return 0
	}
	if i := s.ws.columnOf(id); i >= 0 {
		for _, v := range s.ws.Columns[i].Windows {
			if v != id {
				return v
			}
		}
	}
	return 0
}

// remove drops a closed window from the open switcher. When its column
// still has windows (heir, from before the removal) the card stands for the
// column's new focused window and the selection stays; otherwise the card
// goes, and the selection stays on the same card (or the one that took its
// place). With fewer than two columns left there is nothing to switch.
func (s *switcherState) remove(id, heir WindowID) {
	if !s.open {
		return
	}
	i := slices.Index(s.order, id)
	if i < 0 {
		return
	}
	if j := s.ws.columnOf(heir); heir != 0 && j >= 0 {
		col := s.ws.Columns[j]
		next := col.Windows[col.Focus]
		if size, ok := s.sizes[id]; ok {
			if _, has := s.sizes[next]; !has {
				s.sizes[next] = size
			}
		}
		s.order[i] = next
		return
	}
	s.order = slices.Delete(s.order, i, i+1)
	if i < s.at {
		s.at--
	}
	s.at = max(min(s.at, len(s.order)-1), 0)
	if len(s.order) < 2 {
		*s = switcherState{}
	}
}

// commitSwitcher closes the switcher and focuses the selected column. The
// column left hands its maximization to the target. It does nothing when the
// workspace is not on screen any more, the window went, or the selection is
// the tile already focused (a float or the stash holding the focus does not
// count: the tile takes it back). It reports whether the selection was still
// valid on the workspace on screen.
func (m *Monitor) commitSwitcher() bool {
	s := m.sw
	m.sw = switcherState{}
	if !s.open || s.ws == nil || s.at < 0 || s.at >= len(s.order) {
		return false
	}
	id, w := s.order[s.at], s.ws
	if w != m.Current() {
		return false
	}
	i := w.columnOf(id)
	if i < 0 {
		return false
	}
	if i == w.Focus && !w.floatFocus && !w.stashFocused() {
		return true
	}
	if i != w.Focus && w.Focus >= 0 && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth {
		w.transferMaximization(i)
	}
	// Activate leaves another window's fullscreen and handles dialogs.
	w.Activate(id)
	return true
}

// switchDir is the direction of a switch action, 0 for any other action.
func switchDir(a Action) int {
	switch a {
	case ActionSwitchColumnNext:
		return 1
	case ActionSwitchColumnPrev:
		return -1
	}
	return 0
}

// switching reports whether a switcher is open.
func (c *Core) switching() bool {
	return c.switcher.sc != nil && c.switcher.sc.mon.sw.open
}

// switcherStale reports whether the open switcher belongs to a workspace
// that is not on its screen any more.
func (c *Core) switcherStale() bool {
	m := c.switcher.sc.mon
	return m.sw.open && m.sw.ws != m.Current()
}

func (c *Core) stopSwitcherTimer() {
	if c.switcher.timerStop != nil {
		c.switcher.timerStop()
	}
	c.switcher.timerC, c.switcher.timerStop = nil, nil
}

// cancelSwitcher closes the switcher with the focus unchanged. It only
// resets state: callers publish.
func (c *Core) cancelSwitcher() {
	c.stopSwitcherTimer()
	if sc := c.switcher.sc; sc != nil {
		sc.mon.sw = switcherState{}
	}
	c.switcher.sc = nil
}

// commitSwitcher commits the switcher on its own screen and ends it. The
// keyboard follows the committed window, even if the pointer moved the focus
// to another output meanwhile.
func (c *Core) commitSwitcher() {
	c.stopSwitcherTimer()
	if sc := c.switcher.sc; sc != nil {
		if sc.mon.commitSwitcher() {
			if i := slices.Index(c.screens, sc); i >= 0 {
				c.focusScreen = i
			}
		}
	}
	c.switcher.sc = nil
}

// switchStep steps the switcher by dir (+1 next, -1 previous), opening it
// on the focused screen first. With the command key held the cards show after
// switcherDelay and the commit waits for the release; otherwise it commits
// at once.
func (c *Core) switchStep(dir int) {
	sc := c.cur()
	if c.switching() && (c.switcher.sc != sc || c.switcherStale()) {
		// The focus moved to another output, or another workspace came on
		// screen: start over.
		c.cancelSwitcher()
	}
	if sc.mon.ov.open || c.drag != nil {
		return
	}
	m := sc.mon
	opened := false
	if !c.switching() {
		c.cancelSwitcher()
		w := m.Current()
		order := w.switchOrder()
		if order == nil {
			return
		}
		sizes := make(map[WindowID]Rect, len(order))
		for _, id := range order {
			if v, ok := c.configures.sent[id]; ok && v.Width > 0 && v.Height > 0 {
				sizes[id] = Rect{W: v.Width, H: v.Height}
			}
		}
		at := 1
		if dir < 0 {
			at = len(order) - 1
		}
		m.sw = switcherState{open: true, ws: w, order: order, at: at, sizes: sizes}
		c.switcher.sc = sc
		opened = true
	} else {
		n := len(m.sw.order)
		m.sw.at = (m.sw.at + dir + n) % n
	}
	if held := c.cmdMod != 0 && c.mods&c.cmdMod != 0; !held {
		c.commitSwitcher()
		return
	}
	if opened {
		c.switcher.timerC, c.switcher.timerStop = newTimer(c.opts.Clock, switcherDelay)
	}
}

// switchBind runs a switch-column bind.
func (c *Core) switchBind(ctx context.Context, dir int) error {
	now := c.now()
	shots := c.snapshot(now)
	c.keyboard.takeBack() // a bind acts on the windows
	c.switchStep(dir)
	c.transition(shots, now)
	if err := c.publish(ctx); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// releaseSwitcher commits the selection once the command key is up.
func (c *Core) releaseSwitcher(ctx context.Context) error {
	now := c.now()
	shots := c.snapshot(now)
	c.commitSwitcher()
	c.transition(shots, now)
	if err := c.publish(ctx); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// abortSwitcher cancels the switcher and publishes the workspace again.
func (c *Core) abortSwitcher(ctx context.Context) error {
	now := c.now()
	shots := c.snapshot(now)
	c.cancelSwitcher()
	c.transition(shots, now)
	if err := c.publish(ctx); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// switcherTick runs when the delay passed with the command key held: the
// cards show. It reports whether the scene changed.
func (c *Core) switcherTick() bool {
	c.switcher.timerC, c.switcher.timerStop = nil, nil
	sc := c.switcher.sc
	if !c.switching() {
		c.switcher.sc = nil
		return false
	}
	m := sc.mon
	if !c.hasScreen(sc) || c.security.Protected || m.sw.ws != m.Current() {
		c.cancelSwitcher()
		return true
	}
	if m.sw.shown {
		return false
	}
	now := c.now()
	shots := c.snapshot(now)
	m.sw.shown = true
	c.transition(shots, now)
	return true
}

// switcherClick takes a press while the switcher is open. Before the cards
// show it cancels and lets the press through (handled false). On the cards
// a press on one commits it, anywhere else cancels; the press and its
// release are consumed either way.
func (c *Core) switcherClick(ctx context.Context, button uint32) (handled bool, err error) {
	sc := c.switcher.sc
	if !c.switching() {
		return false, nil
	}
	if !sc.mon.sw.shown {
		c.cancelSwitcher()
		return false, nil
	}
	now := c.now()
	shots := c.snapshot(now)
	id := overviewIn(sc.shownLayout(), c.cursorX-float64(sc.x), c.cursorY-float64(sc.y))
	if i := slices.Index(sc.mon.sw.order, id); id != 0 && i >= 0 {
		sc.mon.sw.at = i
		c.commitSwitcher()
	} else {
		c.cancelSwitcher()
	}
	c.buttons[button], c.swallow[button] = true, true
	c.transition(shots, now)
	if err := c.publish(ctx); err != nil {
		return true, err
	}
	return true, c.rehit(ctx)
}

// switcherLayout lays out the shown switcher: one card per column in
// switchOrder, equal boxes in a row centered in the overview area, every
// window scaled into its box (never up). Like the overview, the selected
// card gets Focused (so its configure is Activated) while the keyboard stays
// on the real focus. Every other window is hidden.
func (m *Monitor) switcherLayout(dst []Placement) []Placement {
	sw := &m.sw
	w := sw.ws
	area := w.overviewArea()
	n := len(sw.order)
	for ws := range m.all() {
		start := len(dst)
		dst = ws.appendHidden(dst, true)
		kept := slices.DeleteFunc(dst[start:], func(p Placement) bool { return slices.Contains(sw.order, p.ID) })
		dst = dst[:start+len(kept)]
	}
	if n == 0 {
		return dst
	}
	gap := max(1, area.W*2/100)
	boxW := max(min(area.W/5, (area.W*9/10-(n-1)*gap)/n), 1)
	boxH := max(boxW*area.H/max(area.W, 1), 1)
	x0 := area.X + (area.W-(n*boxW+(n-1)*gap))/2
	y0 := area.Y + (area.H-boxH)/2
	for k, id := range sw.order {
		s := sw.sizes[id]
		if s.W <= 0 || s.H <= 0 {
			s = Rect{W: w.Usable.W, H: w.Usable.H}
		}
		z := 1.0
		if s.W > 0 && s.H > 0 {
			z = math.Min(1, math.Min(float64(boxW)/float64(s.W), float64(boxH)/float64(s.H)))
		}
		rw, rh := int(math.Round(float64(s.W)*z)), int(math.Round(float64(s.H)*z))
		bx := x0 + k*(boxW+gap)
		dst = append(dst, Placement{
			ID:      id,
			Rect:    Rect{X: bx + (boxW-rw)/2, Y: y0 + (boxH-rh)/2, W: rw, H: rh},
			Preview: z,
			Focused: k == sw.at,
		})
	}
	return dst
}
