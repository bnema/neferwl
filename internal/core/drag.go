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
	if s == nil || w != s.mon.Current() || s.mon.previewing() || id == w.Fullscreen || w.cover() != 0 {
		return nil, nil, false
	}
	if w.floatIndex(id) < 0 && w.ColumnOf(id) < 0 {
		return nil, nil, false
	}
	return s, w, true
}

// startDrag starts a cmd + button drag on the window under the pointer.
// It reports whether it did: the press is then not forwarded.
func (c *Core) startDrag(ctx context.Context, v ports.PointerButton) (bool, error) {
	if c.synthetic || !v.Pressed || c.drag != nil || c.held() != 0 || c.grab != 0 || c.cmdMod == 0 || c.mods&c.cmdMod == 0 {
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
	c.focusClick(s, w, id)
	d := &dragState{id: id, button: button, resize: resize, edges: edges, start: [2]float64{c.cursorX, c.cursorY}}
	if i := w.floatIndex(id); i >= 0 {
		d.float, d.from, d.rect = true, w.Floats[i], w.floatRect(w.Floats[i])
	}
	c.drag = d
	c.buttons[button] = true
	c.grab = 0
	if c.pointer != 0 {
		c.pointer = 0
		if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
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
			c.requestFlash(flashWindow, d.id)
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
	if m.previewing() || w.pinned() || !m.frameHas(float64(lx), float64(ly)) {
		return dropTarget{}
	}
	t := w.dropIn(id, lx, ly, max(4*c.cfg.Border.Width, 4))
	return dropTarget{kind: t.kind, anchor: t.anchor, hints: t.hints, screen: sc, ws: w}
}

// drop moves tile id to target t, possibly on another workspace or
// screen; focus follows it.
func (c *Core) drop(id WindowID, t dropTarget) {
	from, src := c.screenOf(id)
	if from == nil || t.ws == nil || src.ColumnOf(id) < 0 {
		return
	}
	w := t.ws
	ci := src.ColumnOf(id)
	lone := len(src.Columns[ci].Windows) == 1
	if t.kind == dropSwap && w == src && lone {
		to := w.ColumnOf(t.anchor)
		// Their fields move with them, as MoveColumn.
		w.Columns[ci], w.Columns[to] = w.Columns[to], w.Columns[ci]
		// Layout state keyed by position stays at its index.
		w.FocusID(id)
	} else {
		cells := 0
		if lone {
			cells = src.Columns[ci].Cells
		}
		srcIndex := ci
		src.dropSlotOf(id)
		src.RemoveWindow(id)
		col := Column{Windows: []WindowID{id}, Cells: cells}
		switch t.kind {
		case dropAbove, dropBelow:
			ai := w.ColumnOf(t.anchor)
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
				if ai := w.ColumnOf(t.anchor); ai >= 0 {
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
	if !w.scrollsRow() || len(w.Columns) == 0 || w.pinned() {
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
