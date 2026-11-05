package core

import (
	"context"

	"github.com/bnema/neferwl/internal/ports"
)

// virtualPointer routes one zwlr_virtual_pointer_v1 event as real pointer
// input: core places the pointer (relative from its cursor, absolute in an
// output or the whole layout), then the usual handlers decide hover, focus,
// grabs and delivery. A protected session or a stale epoch drops it (the
// virtual pointer never reaches a lock surface), and a release without a
// press is ignored like one from a device. Input learns the new position
// by a warp so the visible cursor follows.
func (c *Core) virtualPointer(ctx context.Context, v ports.VirtualPointerInput) error {
	if c.security.Protected || v.State.Protected || c.opts.Security != nil && v.State != c.security {
		return nil
	}
	// Keep every event associated with its bound output, not just absolute
	// motion. A paired release must still unwind held buttons and grabs if
	// the output disappeared after the press.
	r, available := c.virtualArea(v.Output)
	if v.Output != "" && !available {
		e, release := v.Event.(ports.PointerButton)
		if !release || e.Pressed || !c.buttons[e.Button] {
			return nil
		}
	}
	if err := c.userActivity(ctx); err != nil {
		return err
	}
	c.inputActive = true
	defer func() { c.inputActive = false }()
	switch e := v.Event.(type) {
	case ports.PointerMotion:
		if v.Absolute {
			if !available {
				return nil
			}
			// FX, FY may reach 1 (x == extent): clamp onto the last pixel.
			fx, fy := min(max(v.FX, 0), 1), min(max(v.FY, 0), 1)
			e.X = min(float64(r.X)+fx*float64(r.W), float64(r.X+r.W)-1)
			e.Y = min(float64(r.Y)+fy*float64(r.H), float64(r.Y+r.H)-1)
			e.DX, e.DY, e.UnaccelDX, e.UnaccelDY = 0, 0, 0, 0
		} else {
			e.X, e.Y = c.cursorX+e.DX, c.cursorY+e.DY
		}
		if c.inputBindings {
			if err := c.activateInput(ctx, e); err != nil {
				return err
			}
		}
		if err := c.pointerMotion(ctx, e); err != nil {
			return err
		}
		if c.ch.Constraints != nil {
			g := c.constraint
			g.X, g.Y, g.Warp = c.cursorX, c.cursorY, true
			latest(c.ch.Constraints, g)
		}
	case ports.PointerButton:
		// Only evdev button codes (BTN_MISC..BTN_GEAR_UP): c.buttons stays
		// bounded whatever virtual pointers send.
		if e.Button < 0x100 || e.Button > 0x151 {
			return nil
		}
		if !e.Pressed && !c.buttons[e.Button] {
			return nil
		}
		return c.pointerButton(ctx, e)
	case ports.PointerAxis:
		return c.pointerAxis(ctx, e)
	}
	return nil
}

// virtualArea is the rectangle absolute virtual motion maps into: the named
// output if reachable, or the bounds of every reachable output when unbound.
// An unavailable named output must never fall back to the live desktop.
func (c *Core) virtualArea(output string) (Rect, bool) {
	if output != "" {
		if i := c.screenIndex(output); i >= 0 && !c.screens[i].isolated {
			return c.screens[i].rect(), true
		}
		return Rect{}, false
	}
	var r Rect
	found := false
	for _, s := range c.screens {
		if s.name() == "" || s.isolated {
			continue
		}
		sr := s.rect()
		if !found {
			r, found = sr, true
			continue
		}
		x0, y0 := min(r.X, sr.X), min(r.Y, sr.Y)
		x1, y1 := max(r.X+r.W, sr.X+sr.W), max(r.Y+r.H, sr.Y+sr.H)
		r = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	}
	return r, found
}
