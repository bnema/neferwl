package core

import (
	"context"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Pointer drag: cmd + left button moves the window under the pointer,
// cmd + right button resizes a float; xdg_toplevel.move/resize start the
// same drag with the held button. A float follows the pointer. A tile
// stays in place until the button is released, and NeferWL draws where
// it lands (Scene.DropHints):
//
//   - top or bottom quarter of a window: stack above or below it;
//   - middle of a window: swap the columns (a stacked window becomes a
//     new column there);
//   - scroll overflow, the gap between two columns: a new column there.
//
// The client gets no pointer events during the drag; Escape cancels it.

const (
	btnLeft  = 0x110
	btnRight = 0x111
	// dropBand is the share of a window's height, top and bottom, that
	// stacks above or below it.
	dropBandDiv = 4
	// gapPad is the least half-width of a gap's insert zone, logical px.
	gapPad = 12
	// edgeScroll is the distance from the usable edge, logical px, that
	// scrolls a scroll-overflow workspace during a tile drag, at most
	// once per edgeScrollInterval.
	edgeScroll         = 32
	edgeScrollInterval = 250 * time.Millisecond
)

type dropKind int

const (
	dropNone dropKind = iota
	dropAbove
	dropBelow
	dropSwap
	dropInsert
)

// dropTarget is where a dragged tile lands. anchor is the hovered window
// (above, below, swap) or a window of the column the new one goes before
// (insert; 0 is the end).
type dropTarget struct {
	kind   dropKind
	screen *screen
	ws     *Workspace
	anchor WindowID
	hints  []Rect
}

type dragState struct {
	id     WindowID
	button uint32
	resize bool
	edges  ports.ResizeEdges
	// start is the cursor at the press, global; from is the float as it
	// was, restored by Escape; rect is its place then.
	start  [2]float64
	from   Float
	rect   Rect
	float  bool
	target dropTarget
	// scrolled is the time of the last edge scroll.
	scrolled time.Duration
}

// draggable returns the screen and workspace of a window a drag may
// take: a tile or a native or free float of the workspace on screen,
// not fullscreen, outside the overview.
func (c *Core) draggable(id WindowID) (*screen, *Workspace, bool) {
	s, w := c.screenOf(id)
	if s == nil || w != s.mon.Current() || s.mon.ov.open || id == w.fullscreen || w.cover() != 0 {
		return nil, nil, false
	}
	if w.floatIndex(id) < 0 && w.columnOf(id) < 0 {
		return nil, nil, false
	}
	return s, w, true
}

// startDrag starts a cmd + button drag on the window under the pointer.
// It reports whether it did: the press is then not forwarded.
func (c *Core) startDrag(ctx context.Context, v ports.PointerButton) (bool, error) {
	if !v.Pressed || c.drag != nil || c.held() != 0 || c.grab != 0 || c.cmdMod == 0 || c.mods&c.cmdMod == 0 {
		return false, nil
	}
	if v.Button != btnLeft && v.Button != btnRight {
		return false, nil
	}
	id := c.pointer
	if id == 0 || c.keyboard.inhibited(c.keyboardFocus()) {
		return false, nil
	}
	_, w, ok := c.draggable(id)
	// Tiles only move; resizing them with the pointer is not supported.
	if !ok || v.Button == btnRight && w.floatIndex(id) < 0 {
		return false, nil
	}
	var edges ports.ResizeEdges
	if v.Button == btnRight {
		edges = ports.ResizeRight | ports.ResizeBottom
	}
	return true, c.beginDrag(ctx, id, v.Button, v.Button == btnRight, edges)
}

// held is the number of held buttons a client saw pressed: swallowed
// ones stay in c.buttons only for the security gate.
func (c *Core) held() int {
	n := 0
	for b := range c.buttons {
		if !c.swallow[b] {
			n++
		}
	}
	return n
}

// clientDrag starts a drag for xdg_toplevel.move/resize: the window must
// hold the pointer grab of a held button.
func (c *Core) clientDrag(ctx context.Context, v ports.WindowMoveRequest) error {
	// The adapter checked the serial of the last press: that button drags.
	button := c.lastButton
	if c.drag != nil || c.grab != v.ID || !c.buttons[button] || c.swallow[button] {
		return nil
	}
	_, w, ok := c.draggable(v.ID)
	if !ok || v.Resize && (w.floatIndex(v.ID) < 0 || v.Edges == 0) {
		return nil
	}
	// Other held buttons release without a client to tell.
	for b := range c.buttons {
		if b != button {
			c.swallow[b] = true
		}
	}
	return c.beginDrag(ctx, v.ID, button, v.Resize, v.Edges)
}

func (c *Core) beginDrag(ctx context.Context, id WindowID, button uint32, resize bool, edges ports.ResizeEdges) error {
	if err := c.dismissGrabs(ctx, id); err != nil {
		return err
	}
	s, w, ok := c.draggable(id)
	if !ok {
		return nil
	}
	// Click raises a float above the columns: Escape keeps that.
	w.Click(id)
	d := &dragState{id: id, button: button, resize: resize, edges: edges, start: [2]float64{c.cursorX, c.cursorY}}
	if i := w.floatIndex(id); i >= 0 {
		d.float, d.from, d.rect = true, w.Floats[i], w.floatRect(w.Floats[i])
	}
	c.drag = d
	c.buttons[button] = true
	c.focusScreen = c.screenIndex(s.name())
	c.grab = 0
	if c.pointer != 0 {
		c.pointer = 0
		if err := c.command(ctx, ports.PointerFocus{}); err != nil {
			return err
		}
	}
	return c.publish(ctx)
}

// dragMotion follows the cursor: a float moves or resizes, a tile shows
// where it would land.
func (c *Core) dragMotion(ctx context.Context, at time.Duration) error {
	d := c.drag
	_, w, ok := c.draggable(d.id)
	if !ok {
		c.abortDrag()
		return c.publish(ctx)
	}
	dx, dy := int(c.cursorX-d.start[0]), int(c.cursorY-d.start[1])
	if d.float {
		i := w.floatIndex(d.id)
		if i < 0 {
			c.abortDrag()
			return c.publish(ctx)
		}
		f := &w.Floats[i]
		w.makeFree(f)
		if d.resize {
			r := resized(d.rect, d.edges, dx, dy, 2*w.border+freeMinSize, w.Usable)
			f.W, f.H = r.W-2*w.border, r.H-2*w.border
			w.moveFree(f, r, 0, 0)
		} else {
			w.moveFree(f, d.rect, dx, dy)
		}
		return c.publish(ctx)
	}
	scrolled := c.edgeScroll(at)
	t := c.dropAt(d.id, c.cursorX, c.cursorY)
	if !scrolled && t.kind == d.target.kind && t.anchor == d.target.anchor && t.ws == d.target.ws && slices.Equal(t.hints, d.target.hints) {
		return nil
	}
	d.target = t
	return c.publish(ctx)
}

// resized is r with the edges moved by dx, dy, at least min wide and
// high, inside area u.
func resized(r Rect, edges ports.ResizeEdges, dx, dy, least int, u Rect) Rect {
	left, top, right, bottom := r.X, r.Y, r.X+r.W, r.Y+r.H
	if edges&ports.ResizeLeft != 0 {
		left = min(max(left+dx, u.X), right-least)
	}
	if edges&ports.ResizeRight != 0 {
		right = max(min(right+dx, u.X+u.W), left+least)
	}
	if edges&ports.ResizeTop != 0 {
		top = min(max(top+dy, u.Y), bottom-least)
	}
	if edges&ports.ResizeBottom != 0 {
		bottom = max(min(bottom+dy, u.Y+u.H), top+least)
	}
	return Rect{X: left, Y: top, W: right - left, H: bottom - top}
}

// endDrag drops the window on release: a tile lands on its target.
//
// The drop is a transition like a bind's: the snapshot before it is what
// the screens draw, the transition after it slides the dropped tile and
// re-flows its neighbours. A tile does not follow the pointer during the
// drag (dragMotion): the scene draws it in its slot with the drop hints, so
// the slot is where the user last saw it and where it slides from. A float
// follows the pointer and is already where it lands: no transition.
func (c *Core) endDrag(ctx context.Context) error {
	d := c.drag
	c.drag = nil
	if !d.float {
		if t := c.dropAt(d.id, c.cursorX, c.cursorY); t.kind != dropNone {
			now := c.now()
			shots := c.snapshot(now)
			c.drop(d.id, t)
			c.transition(shots, now)
			// Slots and guests follow the windows, as after a bind.
			if err := c.workspaceVisible(ctx, false); err != nil {
				return err
			}
		}
	}
	if err := c.publish(ctx); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// cancelDrag puts a dragged float back; a tile never moved.
func (c *Core) cancelDrag() {
	d := c.drag
	if d.float {
		if _, w := c.screenOf(d.id); w != nil {
			if i := w.floatIndex(d.id); i >= 0 {
				w.Floats[i] = d.from
			}
		}
	}
	c.abortDrag()
}

// abortDrag ends the drag where it is; the held button's release is
// dropped.
func (c *Core) abortDrag() {
	if c.drag != nil {
		c.swallow[c.drag.button] = true
		c.drag = nil
	}
}

// dropAt is the drop target under the global point x, y.
func (c *Core) dropAt(id WindowID, x, y float64) dropTarget {
	i := c.screenAt(x, y)
	if i < 0 {
		return dropTarget{}
	}
	sc := c.screens[i]
	m := sc.mon
	lx, ly := int(x)-sc.x, int(y)-sc.y
	w := m.Current()
	if m.ov.open || w.pinned() || !m.frameHas(float64(lx), float64(ly)) {
		return dropTarget{}
	}
	t := w.dropIn(id, lx, ly, max(4*c.cfg.Border.Width, 4))
	t.screen, t.ws = sc, w
	return t
}

// visibleColumn is a column on screen and the bounds of its windows.
type visibleColumn struct {
	index int
	rect  Rect
}

// dropIn resolves the drop of window id at the output-local point x, y,
// hints t logical px thick.
func (w *Workspace) dropIn(id WindowID, x, y, t int) dropTarget {
	layout := w.Layout()
	for _, p := range slices.Backward(layout) {
		// Floats and the stash are over the tiles: nothing lands there.
		if p.Floating && !p.Hidden && !p.Below && p.ID != id && contains(p.Rect, x, y) {
			return dropTarget{}
		}
	}
	src, srcRow := w.columnOf(id), -1
	if src >= 0 {
		srcRow = slices.Index(w.Columns[src].Windows, id)
	}
	lone := src >= 0 && len(w.Columns[src].Windows) == 1
	var cols []visibleColumn
	for _, p := range layout {
		if p.Floating || p.Hidden || !p.Rect.Overlaps(w.Output) {
			continue
		}
		ci := w.columnOf(p.ID)
		if ci < 0 {
			continue
		}
		if n := len(cols); n > 0 && cols[n-1].index == ci {
			cols[n-1].rect = union(cols[n-1].rect, p.Rect)
		} else {
			cols = append(cols, visibleColumn{index: ci, rect: p.Rect})
		}
	}
	if len(w.Columns) == 0 || src < 0 && len(cols) == 0 {
		if !contains(w.Usable, x, y) {
			return dropTarget{}
		}
		g := w.gap()
		r := Rect{X: w.Usable.X + g, Y: w.Usable.Y + g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
		return dropTarget{kind: dropInsert, hints: outline(r, t)}
	}
	if w.Overflow != OverflowFixed {
		if tgt, ok := w.gapDrop(cols, id, src, lone, x, y, t); ok {
			return tgt
		}
	}
	for _, p := range layout {
		if p.Floating || p.Hidden || !contains(p.Rect, x, y) {
			continue
		}
		ci := w.columnOf(p.ID)
		if ci < 0 {
			continue
		}
		r := p.Rect
		band := max(r.H/dropBandDiv, 1)
		row := slices.Index(w.Columns[ci].Windows, p.ID)
		switch {
		case y < r.Y+band:
			// Above the window just below it, or above itself: no move.
			if p.ID == id || ci == src && row == srcRow+1 {
				return dropTarget{}
			}
			return dropTarget{kind: dropAbove, anchor: p.ID, hints: []Rect{{X: r.X, Y: r.Y, W: r.W, H: min(t, r.H)}}}
		case y >= r.Y+r.H-band:
			if p.ID == id || ci == src && row == srcRow-1 {
				return dropTarget{}
			}
			return dropTarget{kind: dropBelow, anchor: p.ID, hints: []Rect{{X: r.X, Y: r.Y + r.H - min(t, r.H), W: r.W, H: min(t, r.H)}}}
		default:
			if ci == src {
				return dropTarget{}
			}
			for _, vc := range cols {
				if vc.index == ci {
					return dropTarget{kind: dropSwap, anchor: p.ID, hints: outline(vc.rect, t)}
				}
			}
			return dropTarget{}
		}
	}
	return dropTarget{}
}

// gapDrop is the insert zone of scroll overflow: the gaps between the
// columns on screen and beside the outer ones.
func (w *Workspace) gapDrop(cols []visibleColumn, id WindowID, src int, lone bool, x, y, t int) (dropTarget, bool) {
	pad := max(w.gap()/2, gapPad)
	for i := 0; i <= len(cols); i++ {
		var mid, at int
		var ref Rect
		switch {
		case i == 0:
			ref = cols[0].rect
			mid, at = ref.X-w.gap()/2, cols[0].index
		case i == len(cols):
			ref = cols[i-1].rect
			mid, at = ref.X+ref.W+w.gap()/2, cols[i-1].index+1
		default:
			a, b := cols[i-1].rect, cols[i].rect
			ref = union(a, b)
			mid, at = (a.X+a.W+b.X)/2, cols[i].index
		}
		if x < mid-pad || x >= mid+pad || y < ref.Y || y >= ref.Y+ref.H {
			continue
		}
		// A lone column next to its own place does not move.
		if lone && (at == src || at == src+1) {
			return dropTarget{}, true
		}
		var anchor WindowID
		if at < len(w.Columns) {
			anchor = w.Columns[at].Windows[0]
			if anchor == id {
				anchor = w.Columns[at].Windows[1]
			}
		}
		hint := Rect{X: mid - t/2, Y: ref.Y, W: t, H: ref.H}
		return dropTarget{kind: dropInsert, anchor: anchor, hints: []Rect{hint}}, true
	}
	return dropTarget{}, false
}

// contains reports whether r holds the point x, y.
func contains(r Rect, x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// union is the smallest rect holding a and b.
func union(a, b Rect) Rect {
	x, y := min(a.X, b.X), min(a.Y, b.Y)
	return Rect{X: x, Y: y, W: max(a.X+a.W, b.X+b.W) - x, H: max(a.Y+a.H, b.Y+b.H) - y}
}

// outline is the four sides of r, t thick, inside it.
func outline(r Rect, t int) []Rect {
	t = min(t, r.W/2, r.H/2)
	if t <= 0 {
		return nil
	}
	return []Rect{
		{X: r.X, Y: r.Y, W: r.W, H: t},
		{X: r.X, Y: r.Y + r.H - t, W: r.W, H: t},
		{X: r.X, Y: r.Y + t, W: t, H: r.H - 2*t},
		{X: r.X + r.W - t, Y: r.Y + t, W: t, H: r.H - 2*t},
	}
}

// drop moves tile id to target t, possibly on another workspace or
// screen; focus follows it.
func (c *Core) drop(id WindowID, t dropTarget) {
	from, src := c.screenOf(id)
	if from == nil || t.ws == nil || src.columnOf(id) < 0 {
		return
	}
	w := t.ws
	ci := src.columnOf(id)
	lone := len(src.Columns[ci].Windows) == 1
	if t.kind == dropSwap && w == src && lone {
		to := w.columnOf(t.anchor)
		// Their fields move with them, as MoveColumn.
		w.Columns[ci], w.Columns[to] = w.Columns[to], w.Columns[ci]
		w.FocusID(id)
	} else {
		width := Width{}
		if lone {
			width = src.Columns[ci].Width
		}
		srcIndex := ci
		src.dropSlotOf(id)
		src.RemoveWindow(id)
		col := Column{Windows: []WindowID{id}, Width: width}
		switch t.kind {
		case dropAbove, dropBelow:
			ai := w.columnOf(t.anchor)
			if ai < 0 {
				w.receive(col, -1)
				break
			}
			tc := &w.Columns[ai]
			row := slices.Index(tc.Windows, t.anchor)
			if t.kind == dropBelow {
				row++
			}
			if row == 0 {
				// The slot follows the first window.
				tc.Slot = 0
			}
			tc.Windows = slices.Insert(tc.Windows, row, id)
			tc.Shares = nil
			w.Activate(id)
		default:
			at, anchorCol := len(w.Columns), -1
			if t.anchor != 0 {
				if ai := w.columnOf(t.anchor); ai >= 0 {
					at, anchorCol = ai, ai
					// A stacked window dropped on a column right of its
					// own lands after it.
					if t.kind == dropSwap && w == src && ai >= srcIndex {
						at = ai + 1
					}
				}
			}
			if w.full() {
				// No room for a column: it joins the outlined one.
				i := min(at, len(w.Columns)-1)
				if anchorCol >= 0 {
					i = anchorCol
				}
				w.Columns[i].Windows = append(w.Columns[i].Windows, id)
				w.Columns[i].Shares = nil
				w.Activate(id)
			} else {
				w.receive(col, at)
			}
		}
	}
	from.mon.normalize()
	t.screen.mon.normalize()
	c.focusScreen = c.screenIndex(t.screen.name())
}

// edgeScroll scrolls a scroll-overflow workspace by one column when a
// tile drag moves the pointer against its usable edge. It reports whether
// the view moved.
func (c *Core) edgeScroll(t time.Duration) bool {
	i := c.screenAt(c.cursorX, c.cursorY)
	if i < 0 || t-c.drag.scrolled < edgeScrollInterval && c.drag.scrolled != 0 {
		return false
	}
	sc := c.screens[i]
	w := sc.mon.Current()
	if w.Overflow == OverflowFixed || len(w.Columns) == 0 || w.pinned() {
		return false
	}
	lx := int(c.cursorX) - sc.x
	dir := 0
	switch {
	case lx < w.Usable.X+edgeScroll:
		dir = -1
	case lx >= w.Usable.X+w.Usable.W-edgeScroll:
		dir = 1
	default:
		return false
	}
	if !w.scrollBy(dir) {
		return false
	}
	c.drag.scrolled = max(t, 1)
	return true
}

// scrollBy moves the view one column toward dir, within the row; the
// focus stays. It reports whether the view moved.
func (w *Workspace) scrollBy(dir int) bool {
	g := w.gap()
	n := len(w.Columns)
	maxView := max(w.columnX(n-1)+w.columnWidth(n-1)+g-(w.Usable.X+w.Usable.W), 0)
	view := w.ViewX
	if dir < 0 {
		// The first column not fully on screen at the left.
		for i := n - 1; i >= 0; i-- {
			if w.columnX(i)-view < w.Usable.X+g {
				view = w.columnX(i) - w.Usable.X - g
				break
			}
		}
	} else {
		for i := range n {
			if w.columnX(i)+w.columnWidth(i)-view > w.Usable.X+w.Usable.W-g {
				view = w.columnX(i) + w.columnWidth(i) + g - w.Usable.X - w.Usable.W
				break
			}
		}
	}
	view = min(max(view, 0), maxView)
	if view == w.ViewX {
		return false
	}
	w.stopSlide()
	w.ViewX = view
	return true
}

// makeFree turns native float f into a free float where it is now.
func (w *Workspace) makeFree(f *Float) {
	if f.free {
		return
	}
	r := w.floatRect(*f)
	if f.W <= 0 || f.H <= 0 {
		f.W, f.H = max(r.W-2*w.border, 1), max(r.H-2*w.border, 1)
	}
	f.free = true
	w.moveFree(f, r, 0, 0)
}
