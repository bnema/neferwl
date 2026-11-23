package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Pointer drag: cmd + left button moves the window under the pointer,
// cmd + right button resizes a float; xdg_toplevel.move/resize start the
// same drag with the held button. A float follows the pointer. A tile
// stays in place until the button is released, and NeferWL draws the
// zone it would land in (Scene.DropPreview), resolved by the drop policy
// (workspace.AnchorAt):
//
//   - output edges: a release action (column, stash, float) or a hover
//     action (scroll, workspace) that runs while the pointer rests there;
//   - scroll overflow, the gap between two columns: a new column there;
//   - side bands of a window: by default a new column beside it (left,
//     right) or a stack above or below it (top, bottom);
//   - center of a window: by default a swap.
//
// A zone is kept while the pointer stays near it (hysteresis). The client
// gets no pointer events during the drag; Escape cancels it.

const (
	btnLeft  = 0x110
	btnRight = 0x111
	// gapPad is the least half-width of a border's resize hit zone
	// (BorderAt), logical px.
	gapPad = 12
	// dragHoverFirst is how long the pointer rests on a hover edge
	// (scroll, workspace) before it acts; it then repeats every
	// dragHoverRepeat while the zone stays the same.
	dragHoverFirst  = 400 * time.Millisecond
	dragHoverRepeat = 250 * time.Millisecond
)

// dropTarget is where a dragged tile lands: anchor a of workspace ws on
// screen. preview is its published preview (advanceDropPreview); immutable,
// replaced only when its content changes so equal frames share it.
type dropTarget struct {
	a       Anchor
	screen  *screen
	ws      *Workspace
	preview *domain.DropPreview
}

type dragState struct {
	id     WindowID
	button uint32
	resize bool
	edges  domain.ResizeEdges
	// start is the cursor at the press, global; from is the float as it
	// was, restored by Escape; rect is its place then.
	start  [2]float64
	from   Float
	rect   Rect
	float  bool
	target dropTarget
	anim   dropAnim
	// moved is set once the pointer left the press by more than
	// dragSelfSlop: a tile's own bands drop nothing before, so a click
	// near its side does not pop it out.
	moved bool
}

// dragSelfSlop is how far, in logical px, the pointer goes from the
// press before a tile's own anchors take a drop.
const dragSelfSlop = 8

// dragHover is the pending hover action of a tile drag on a hover edge,
// owned by the core event loop; a nil channel disables it.
type dragHover struct {
	timerC <-chan time.Time
	stop   func() bool
}

// draggable returns the screen and workspace of a window a drag may
// take: a tile or a native or free float of the workspace on screen,
// not fullscreen (the cover or a held member), outside the overview.
func (c *Core) draggable(id WindowID) (*screen, *Workspace, bool) {
	s, w := c.screenOf(id)
	if s == nil || w != s.mon.Current() || s.mon.previewing() || w.IsFullscreenMember(id) || w.Cover() != 0 {
		return nil, nil, false
	}
	if _, _, floating := w.FloatAt(id); !floating && w.ColumnOf(id) < 0 {
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
	if !ok {
		return false, nil
	}
	_, _, floating := w.FloatAt(id)
	if v.Button == btnRight && !floating {
		return false, nil
	}
	var edges domain.ResizeEdges
	if v.Button == btnRight {
		edges = domain.ResizeRight | domain.ResizeBottom
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
	if !ok {
		return nil
	}
	_, _, floating := w.FloatAt(v.ID)
	if v.Resize && (!floating || v.Edges == 0) {
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

func (c *Core) beginDrag(ctx context.Context, id WindowID, button uint32, resize bool, edges domain.ResizeEdges) error {
	if err := c.dismissGrabs(ctx, id); err != nil {
		return err
	}
	s, w, ok := c.draggable(id)
	if !ok {
		return nil
	}
	// Click raises a float above the columns: Escape keeps that.
	c.focusClick(s, w, id, true)
	d := &dragState{id: id, button: button, resize: resize, edges: edges, start: [2]float64{c.cursorX, c.cursorY}}
	if f, r, ok := w.FloatAt(id); ok {
		d.float, d.from, d.rect = true, f, r
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
func (c *Core) dragMotion(ctx context.Context) error {
	d := c.drag
	if d.float {
		_, w, ok := c.draggable(d.id)
		dx, dy := int(c.cursorX-d.start[0]), int(c.cursorY-d.start[1])
		if !ok || !w.DragFloat(d.id, d.rect, d.edges, dx, dy, d.resize) {
			c.abortDrag()
			return c.publish(ctx)
		}
		return c.publish(ctx)
	}
	src, ok := c.dragAlive(d.id)
	if !ok {
		c.abortDrag()
		return c.publish(ctx)
	}
	was := d.moved
	if !d.moved {
		dx, dy := c.cursorX-d.start[0], c.cursorY-d.start[1]
		d.moved = dx*dx+dy*dy > dragSelfSlop*dragSelfSlop
	}
	// A thumbnail follows the pointer; a zone only moves on a new target.
	// Crossing the slop publishes once so the thumbnail appears.
	thumb := d.target.preview != nil && d.target.preview.Thumb != 0
	if !c.retarget(src) && !thumb && (!d.moved || was) {
		return nil
	}
	return c.publish(ctx)
}

// retarget recomputes the drop target under the pointer. It reports
// whether the target changed; a new hover edge arms the hover timer.
// src is the workspace of the dragged tile.
func (c *Core) retarget(src *Workspace) bool {
	d := c.drag
	t := c.dropAt(src, d.id, c.cursorX, c.cursorY)
	if t.a == d.target.a && t.ws == d.target.ws && t.screen == d.target.screen {
		return false
	}
	t.preview = d.target.preview
	old := d.target
	same := t.a == old.a
	d.target = t
	c.aimDropPreview(old)
	if !same {
		c.stopDragHover()
		if hoverEdge(t.a) {
			c.dragHover.timerC, c.dragHover.stop = newTimer(c.opts.Clock, dragHoverFirst)
		}
	}
	return true
}

// dragAlive reports whether the tile drag of id goes on: the window is
// still a tile, not fullscreen nor covered on its own workspace, nothing
// is locked and the pointer's monitor is not previewing. Its workspace
// need not be on screen: a hover edge may have switched it away. It
// returns that workspace.
func (c *Core) dragAlive(id WindowID) (*Workspace, bool) {
	s, w := c.screenOf(id)
	if s == nil || w.ColumnOf(id) < 0 || w.IsFullscreenMember(id) || w.Cover() != 0 || c.security.Protected {
		return nil, false
	}
	if i := c.screenAt(c.cursorX, c.cursorY); i >= 0 && c.screens[i].mon.previewing() {
		return nil, false
	}
	return w, true
}

// hoverEdge reports whether a is an edge acting while the pointer rests.
func hoverEdge(a Anchor) bool { return a.Kind == AnchorEdge && a.Action.Hover() }

// retargetDrag follows a config change under a still pointer: the tile
// drag's preview never shows a zone the new policy removed.
func (c *Core) retargetDrag() {
	d := c.drag
	if d == nil || d.float {
		return
	}
	src, ok := c.dragAlive(d.id)
	if !ok {
		c.abortDrag()
		return
	}
	c.retarget(src)
}

func (c *Core) stopDragHover() {
	if c.dragHover.stop != nil {
		c.dragHover.stop()
	}
	c.dragHover = dragHover{}
}

// dragHoverTick runs the hover action of the edge under the pointer, then
// re-arms while the zone stays the same. The target is checked first: a
// reload or a layout change since the last motion may have moved it.
func (c *Core) dragHoverTick(ctx context.Context) error {
	c.dragHover = dragHover{}
	d := c.drag
	if d == nil || d.float || !hoverEdge(d.target.a) {
		return nil
	}
	src, ok := c.dragAlive(d.id)
	if !ok {
		c.abortDrag()
		return c.publish(ctx)
	}
	if t := c.dropAt(src, d.id, c.cursorX, c.cursorY); t.a != d.target.a || t.ws != d.target.ws {
		c.retarget(src)
		if c.dragHover.timerC == nil && hoverEdge(d.target.a) {
			c.dragHover.timerC, c.dragHover.stop = newTimer(c.opts.Clock, dragHoverFirst)
		}
		return c.publish(ctx)
	}
	a := d.target.a
	switch a.Action {
	case domain.DropScroll:
		// At the end of the range the timer stays stopped.
		if !d.target.ws.ScrollStep(a.Dir) {
			return nil
		}
	case domain.DropWorkspace:
		// dropAt offers no workspace edge without a workspace past it.
		m := d.target.screen.mon
		now := c.now()
		shots := c.snapshot(now)
		m.Focus(m.Active + a.Dir)
		c.transition(shots, now)
		if err := c.workspaceVisible(ctx, false); err != nil {
			return err
		}
	}
	// The view moved: the zone may have too.
	d.target = dropTarget{a: d.target.a, screen: d.target.screen, preview: d.target.preview}
	c.retarget(src)
	if c.dragHover.timerC == nil && d.target.a == a {
		c.dragHover.timerC, c.dragHover.stop = newTimer(c.opts.Clock, dragHoverRepeat)
	}
	return c.publish(ctx)
}

// endDrag drops the window on release: a tile lands on its target.
//
// The drop is a transition like a bind's: the snapshot before it is what
// the screens draw, the transition after it slides the dropped tile and
// re-flows its neighbours. A tile does not follow the pointer during the
// drag (dragMotion): the scene draws it in its slot with the drop preview, so
// the slot is where the user last saw it and where it slides from. A float
// follows the pointer and is already where it lands: no transition.
//
// The tile lands on what the preview shows: the target is resolved with
// the drag's hysteresis before the drag ends. A hover edge or an invalid
// zone drops nothing.
func (c *Core) endDrag(ctx context.Context) error {
	d := c.drag
	var t dropTarget
	from, src := c.screenOf(d.id)
	if !d.float && src != nil {
		t = c.dropAt(src, d.id, c.cursorX, c.cursorY)
	}
	c.drag = nil
	c.stopDragHover()
	if !d.float {
		if t.a.Kind != AnchorNone && !hoverEdge(t.a) && t.ws.DropValid(src, d.id, t.a) {
			now := c.now()
			shots := c.snapshot(now)
			m := c.markDrop(d.id, from, src)
			c.drop(d.id, t)
			c.traceDrop(m, t.a.Action.String())
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

// dropMark is where a dropped tile was before the drop; ok is false when
// traces are off.
type dropMark struct {
	id  WindowID
	out string
	ws  int
	ok  bool
}

// markDrop records where tile id is, on screen from in workspace src,
// before the drop: the drop may delete the emptied src.
func (c *Core) markDrop(id WindowID, from *screen, src *Workspace) dropMark {
	if c.ch.Binds == nil {
		return dropMark{}
	}
	return dropMark{id: id, out: from.name(), ws: indexOf(from.mon.Workspaces, src) + 1, ok: true}
}

// traceDrop traces the tile marked by m, dropped with action kind, to
// where it is now.
func (c *Core) traceDrop(m dropMark, kind string) {
	if !m.ok {
		return
	}
	t := domain.WindowDropped{Window: m.id, Kind: kind, Output: m.out, WorkspaceBefore: m.ws}
	if s, w := c.screenOf(m.id); s != nil {
		t.OutputAfter, t.WorkspaceAfter = s.name(), indexOf(s.mon.Workspaces, w)+1
	}
	c.traceInput(t)
}

// cancelDrag puts a dragged float back; a tile never moved.
func (c *Core) cancelDrag() {
	d := c.drag
	if d.float {
		if _, w := c.screenOf(d.id); w != nil {
			w.RestoreFloat(d.from)
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
	c.stopDragHover()
}

// dropAt is the drop target under the global point x, y of tile id,
// dragged from workspace src.
func (c *Core) dropAt(src *Workspace, id WindowID, x, y float64) dropTarget {
	i := c.screenAt(x, y)
	if i < 0 {
		return dropTarget{}
	}
	sc := c.screens[i]
	m := sc.mon
	lx, ly := int(x)-sc.x, int(y)-sc.y
	w := m.Current()
	if m.previewing() || w.Pinned() || !m.frameHas(float64(lx), float64(ly)) {
		return dropTarget{}
	}
	var prev Anchor
	if d := c.drag; d != nil && d.target.ws == w {
		prev = d.target.a
	}
	p := c.dropPolicy(m, w)
	// A workspace edge with no workspace past it (a named workspace has
	// none, the first has none before it, the last none after it) would
	// do nothing: it offers no zone.
	for side := domain.DropSideLeft; side <= domain.DropSideBottom; side++ {
		if w.EdgeAction(side, p.Edge[side]) != domain.DropWorkspace {
			continue
		}
		back := side == domain.DropSideLeft || side == domain.DropSideTop
		if m.shown != nil || back && m.Active == 0 || !back && m.Active >= len(m.Workspaces)-1 {
			p.SetEdge(side, domain.DropNone)
		}
	}
	a := w.AnchorAt(src, id, lx, ly, prev, p)
	if a.Kind == AnchorWindow && a.Window == id && (c.drag == nil || !c.drag.moved) {
		a = Anchor{}
	}
	return dropTarget{a: a, screen: sc, ws: w}
}

// drop moves tile id to target t, possibly on another workspace or
// screen; focus follows it.
func (c *Core) drop(id WindowID, t dropTarget) {
	from, src := c.screenOf(id)
	if from == nil || t.ws == nil || src.ColumnOf(id) < 0 {
		return
	}
	Drop(src, t.ws, id, t.a, int(c.cursorX)-t.screen.x, int(c.cursorY)-t.screen.y)
	from.mon.normalize()
	t.screen.mon.normalize()
	c.focusScreen = c.screenIndex(t.screen.name())
}
