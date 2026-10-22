package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// unmanagedState is a mapped override-redirect X11 window: an absolute layout
// rect chosen by the client, no parent, never keyboard-focused. rect is in
// global logical coordinates.
type unmanagedState struct {
	id      WindowID
	rect    Rect
	noInput bool
}

// unmanagedEvent applies an unmanaged window event. changed is true when the
// scene or the pointer target may differ.
func (c *Core) unmanagedEvent(ctx context.Context, ev ports.ClientEvent) (changed bool, err error) {
	switch v := ev.(type) {
	case ports.UnmanagedMapped:
		r := Rect{X: v.X, Y: v.Y, W: v.Width, H: v.Height}
		if u := c.unmanaged[v.ID]; u != nil {
			if u.rect == r && u.noInput == v.NoInput {
				return false, nil
			}
			u.rect, u.noInput = r, v.NoInput
		} else {
			c.unmanaged[v.ID] = &unmanagedState{id: v.ID, rect: r, noInput: v.NoInput}
			c.unmanagedOrder = append(c.unmanagedOrder, v.ID)
		}
	case ports.UnmanagedMoved:
		u := c.unmanaged[v.ID]
		r := Rect{X: v.X, Y: v.Y, W: v.Width, H: v.Height}
		if u == nil || u.rect == r {
			return false, nil
		}
		u.rect = r
	case ports.UnmanagedUnmapped:
		if c.unmanaged[v.ID] == nil {
			return false, nil
		}
		delete(c.unmanaged, v.ID)
		c.unmanagedOrder = slices.DeleteFunc(c.unmanagedOrder, func(id WindowID) bool { return id == v.ID })
		c.windows.drop(v.ID)
		if err := c.dataDragGone(ctx, v.ID); err != nil {
			return false, err
		}
		// rehit does nothing while a button grab or drag is held: do not
		// leave the pointer and grab on the dead window.
		if c.grab == v.ID {
			c.grab = 0
		}
		if c.pointer == v.ID {
			c.pointer = 0
			if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
				return false, err
			}
		}
	}
	// The window under a still pointer may have changed.
	return true, c.rehit(ctx)
}

// unmanagedScreen is the screen showing u: the one containing its origin, or
// the one the pointer clamp would pick when the origin is in a hole.
func (c *Core) unmanagedScreen(u *unmanagedState) *screen {
	x, y := float64(u.rect.X), float64(u.rect.Y)
	i := c.screenAt(x, y)
	if i < 0 {
		cx, cy := c.clampPointer(x, y, x, y)
		i = c.screenAt(cx, cy)
	}
	if i < 0 {
		return nil
	}
	return c.screens[i]
}

// sceneUnmanaged appends the unmanaged windows shown on sc, bottom to top,
// drawn as popups over the layers. They are hidden while sc previews.
func (c *Core) sceneUnmanaged(out []ports.SceneWindow, sc *screen) []ports.SceneWindow {
	if len(c.unmanagedOrder) == 0 || sc.mon.previewing() {
		return out
	}
	for _, id := range c.unmanagedOrder {
		u := c.unmanaged[id]
		if u == nil || c.unmanagedScreen(u) != sc {
			continue
		}
		r := u.rect
		r.X, r.Y = r.X-sc.x, r.Y-sc.y
		out = append(out, ports.SceneWindow{ID: id, Rect: r, Popup: true, OverLayers: true})
	}
	return out
}

// unmanagedAt is the topmost unmanaged window taking the pointer at the
// screen-local point. None while the pointer is locked or the screen previews.
func (c *Core) unmanagedAt(sc *screen, lx, ly float64) (WindowID, float64, float64) {
	if len(c.unmanagedOrder) == 0 || c.constraint.Mode == ports.ConstraintLock || sc.mon.previewing() {
		return 0, 0, 0
	}
	for i := len(c.unmanagedOrder) - 1; i >= 0; i-- {
		u := c.unmanaged[c.unmanagedOrder[i]]
		if u == nil || u.noInput || c.unmanagedScreen(u) != sc {
			continue
		}
		x, y := lx-float64(u.rect.X-sc.x), ly-float64(u.rect.Y-sc.y)
		if x >= 0 && x < float64(u.rect.W) && y >= 0 && y < float64(u.rect.H) && c.acceptsInput(u.id, x, y) {
			return u.id, x, y
		}
	}
	return 0, 0, 0
}
