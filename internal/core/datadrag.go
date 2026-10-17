package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Data drag: a client drag and drop (wl_data_device.start_drag). Wayland
// validated the request against the client's press and tells core; core
// owns the pointer for the length of the drag, as for a window drag
// (drag.go):
//
//   - the origin client gets the pointer leave and no pointer event after;
//     nobody else gets one: the surface under the pointer is the drag
//     target, told to wayland as DataDragFocus and DataDragMotion;
//   - releasing the drag button drops (DataDragDrop), Escape cancels
//     (DataDragCancel); neither reaches a client;
//   - scrolling and touchpad gestures go nowhere; the keyboard keeps its
//     focus, clients read the modifiers that choose the action from it;
//   - the drag icon, a client surface wayland measures (DataDragIcon), is
//     drawn at the cursor over everything.
//
// Layout changes under a still cursor, like an unmapped target, are
// resolved at the next motion.

// dataDragState is the data drag in progress.
type dataDragState struct {
	// button is the pointer button that holds the drag.
	button uint32
	// target is the surface the drag was last routed to (0: none).
	target WindowID
	icon   dataDragIcon
}

// dataDragIcon is the icon surface: its top left corner relative to the
// cursor and its size, logical px. Hidden while W or H is 0.
type dataDragIcon struct {
	id         WindowID
	x, y, w, h int
}

// startDataDrag answers the start_drag wayland validated: it takes the
// pointer if a client holds a button, else cancels the drag. A drag
// during a window drag or a locked session is refused.
func (c *Core) startDataDrag(ctx context.Context, v ports.DataDragStarted) error {
	button := c.lastButton
	if c.dataDrag != nil || c.drag != nil || c.security.Protected || c.grab == 0 || c.held() == 0 || !c.buttons[button] || c.swallow[button] {
		return c.command(ctx, ports.DataDragCancel{})
	}
	c.dataDrag = &dataDragState{button: button, icon: dataDragIcon{id: v.Icon}}
	// Other held buttons release without a client to tell.
	for b := range c.buttons {
		if b != button {
			c.swallow[b] = true
		}
	}
	c.grab = 0
	// The origin gets its pointer leave, as the drag takes the pointer.
	if c.pointer != 0 {
		c.pointer = 0
		if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
			return err
		}
	}
	// The surface under the pointer, the origin's usually, is the first
	// target.
	return c.dataDragRoute(ctx, c.motionTime)
}

// dataDragRoute sends the surface under the cursor as the drag target: a
// focus when it changed, else a motion.
func (c *Core) dataDragRoute(ctx context.Context, at time.Duration) error {
	d := c.dataDrag
	id, x, y := c.hit(c.cursorX, c.cursorY)
	switch {
	case id != d.target:
		d.target = id
		return c.command(ctx, ports.DataDragFocus{ID: id, X: x, Y: y, Time: at})
	case id != 0:
		return c.command(ctx, ports.DataDragMotion{ID: id, X: x, Y: y, Time: at})
	}
	return nil
}

// dataDragMotion follows the cursor: the target gets the motion and the
// icon moves with it.
func (c *Core) dataDragMotion(ctx context.Context, at time.Duration) error {
	if err := c.dataDragRoute(ctx, at); err != nil {
		return err
	}
	if c.dataDrag.icon.visible() {
		return c.publish(ctx)
	}
	return nil
}

// dataDragButton handles a button event of a data drag: the release of
// the drag button drops; the other buttons do nothing, their releases
// neither.
func (c *Core) dataDragButton(ctx context.Context, v ports.PointerButton) error {
	if !v.Pressed {
		if v.Button != c.dataDrag.button {
			return nil
		}
		delete(c.buttons, v.Button)
		c.dataDrag = nil
		if err := c.command(ctx, ports.DataDragDrop{}); err != nil {
			return err
		}
		return c.finishDataDrag(ctx)
	}
	c.buttons[v.Button] = true
	c.swallow[v.Button] = true
	return nil
}

// cancelDataDrag ends the drag on Escape.
func (c *Core) cancelDataDrag(ctx context.Context) error {
	c.abortDataDrag()
	if err := c.command(ctx, ports.DataDragCancel{}); err != nil {
		return err
	}
	return c.finishDataDrag(ctx)
}

// abortDataDrag ends the drag where it is; the held button's release is
// dropped.
func (c *Core) abortDataDrag() {
	if d := c.dataDrag; d != nil {
		c.swallow[d.button] = true
		c.dataDrag = nil
	}
}

// finishDataDrag redraws without the icon and points the pointer at what
// lies under the cursor.
func (c *Core) finishDataDrag(ctx context.Context) error {
	if err := c.publish(ctx); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// dataDragGone drops the target that was unmapped: it gets its leave.
func (c *Core) dataDragGone(ctx context.Context, id WindowID) error {
	if d := c.dataDrag; d != nil && d.target == id {
		d.target = 0
		return c.command(ctx, ports.DataDragFocus{Time: c.motionTime})
	}
	return nil
}

// setDataDragIcon records the icon geometry wayland measured. It reports
// whether the scene changes.
func (c *Core) setDataDragIcon(v ports.DataDragIcon) bool {
	d := c.dataDrag
	if d == nil || d.icon.id == 0 || v.ID != d.icon.id {
		return false
	}
	icon := dataDragIcon{id: v.ID, x: v.X, y: v.Y, w: max(v.W, 0), h: max(v.H, 0)}
	if icon == d.icon {
		return false
	}
	d.icon = icon
	return true
}

func (i dataDragIcon) visible() bool { return i.id != 0 && i.w > 0 && i.h > 0 }

// dataDragIconOn is the icon as a window of screen sc (index i) when the
// cursor is on it.
func (c *Core) dataDragIconOn(i int, sc *screen) (ports.SceneWindow, bool) {
	d := c.dataDrag
	if d == nil || !d.icon.visible() || c.screenAt(c.cursorX, c.cursorY) != i {
		return ports.SceneWindow{}, false
	}
	x, y := int(c.cursorX)-sc.x+d.icon.x, int(c.cursorY)-sc.y+d.icon.y
	return ports.SceneWindow{ID: d.icon.id, Rect: Rect{X: x, Y: y, W: d.icon.w, H: d.icon.h}, Popup: true, OverLayers: true}, true
}
