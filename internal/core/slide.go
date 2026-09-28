package core

import (
	"math"
	"time"
)

// Slides move what is on screen smoothly: a workspace's columns (shift) and
// the monitor between workspaces (switchOff). Both are pure presentation
// over the settled state (ViewX, Focus, Active): the layout ends where the
// state says as soon as a slide stops.

// slidable reports whether a swipe can scroll the columns: only scroll
// overflow, with the focus on the columns and no fullscreen window.
func (w *Workspace) slidable() bool {
	return w.Overflow != OverflowFixed && len(w.Columns) > 0 && w.fullscreen == 0 && !w.onFloat()
}

func (w *Workspace) stopSlide() { w.shift, w.motion = 0, nil }

// retarget keeps the view where it is on screen when ViewX moved from
// before during a landing slide: the slide heads for the new view.
// Without a landing slide the view moves at once, as it always has.
func (w *Workspace) retarget(before int) {
	if w.ViewX == before || w.motion == nil {
		return
	}
	w.shift += float64(before - w.ViewX)
	w.motion = newMotion(viewSpring(w.shift, 0), time.Time{})
}

func (w *Workspace) shiftPixels() int { return int(math.Round(w.shift)) }

// swipeScale turns touchpad distance into pixels: viewSwipeMovement
// scrolls one usable width.
func (w *Workspace) swipeScale() float64 {
	return float64(w.Usable.W) / viewSwipeMovement
}

// snap returns the view closest to target that aligns a column edge with
// the usable area, never past the first or last column, and the column to
// focus there: the current one while it stays fully shown, else the
// furthest fully shown column toward the swipe (forward is rightward).
func (w *Workspace) snap(target float64, forward bool) (view, focus int) {
	g := w.gap()
	minX, maxX := w.Usable.X+g, w.Usable.X+w.Usable.W-g
	last := len(w.Columns) - 1
	lo := w.columnX(0) - minX
	hi := max(w.columnX(last)+w.columnWidth(last)-maxX, lo)
	view, best := lo, math.Inf(1)
	try := func(v int) {
		v = min(max(v, lo), hi)
		if d := math.Abs(float64(v) - target); d < best {
			view, best = v, d
		}
	}
	for i := range w.Columns {
		try(w.columnX(i) - minX)
		try(w.columnX(i) + w.columnWidth(i) - maxX)
	}
	full := func(i int) bool {
		x := w.columnX(i) - view
		return x >= minX && x+w.columnWidth(i) <= maxX
	}
	if full(w.Focus) && view == w.ViewX {
		return view, w.Focus
	}
	focus = -1
	for i := range w.Columns {
		if full(i) && (focus < 0 || forward) {
			focus = i
		}
	}
	if focus < 0 {
		// Columns wider than the view: the one at the left edge.
		focus = 0
		for i := range w.Columns {
			if w.columnX(i)-view <= minX {
				focus = i
			}
		}
	}
	return view, focus
}

func (m *Monitor) stopSwitch() { m.switchOff, m.switchMotion, m.switchList = 0, nil, nil }

// slideLayout adds the workspaces a workspace slide shows next to the
// current one, moved by their distance, to the current layout (moved too).
// A landing slide places them as the list was when the swipe began
// (switchList): an empty workspace dropped since shows as background.
func (m *Monitor) slideLayout(cur []Placement) []Placement {
	if m.switchOff == 0 || m.shown != nil {
		return cur
	}
	list := m.Workspaces
	if m.switchList != nil {
		list = m.switchList
	}
	base := indexOf(list, m.Current())
	if base < 0 {
		return cur
	}
	h := float64(m.template.Output.H)
	pos := float64(base) + m.switchOff
	offset := func(p []Placement, k int) []Placement {
		dy := int(math.Round((float64(k) - pos) * h))
		for i := range p {
			if !p[i].Hidden {
				p[i].Rect.Y += dy
			}
		}
		return p
	}
	cur = offset(cur, base)
	for _, k := range []int{int(math.Floor(pos)), int(math.Ceil(pos))} {
		if k == base || k < 0 || k >= len(list) || !m.has(list[k]) {
			continue
		}
		next := list[k].Layout()
		for i := range next {
			next[i].Focused = false
		}
		// Windows of the neighbor are already listed hidden: replace them.
		shown := map[WindowID]Placement{}
		for _, p := range offset(next, k) {
			shown[p.ID] = p
		}
		for i, p := range cur {
			if v, ok := shown[p.ID]; ok {
				cur[i] = v
			}
		}
	}
	return cur
}
