package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// pointerGrab is a compositor-owned pointer interaction started by cmd+button.
// Clients get no pointer events while it runs. Implementations: borderResize
// (border_resize.go); planned: column move within a workspace, and edge-hold
// transfer of a dragged column to the next workspace or the adjacent monitor.
type pointerGrab interface {
	button() uint32
	motion(ctx context.Context, at time.Duration) error
	release(ctx context.Context) error
	cancel()        // Escape: restore the state from before the grab
	cursor() string // CSS cursor name; "" = the client's cursor
}

func (c *Core) pointerBusy() bool { return c.drag != nil || c.pgrab != nil }

func (c *Core) startPointerGrab(ctx context.Context, v ports.PointerButton) (bool, error) {
	if c.synthetic || !v.Pressed || c.pointerBusy() || c.held() != 0 || c.grab != 0 || c.cmdMod == 0 || c.mods&c.cmdMod == 0 || c.keyboard.inhibited(c.keyboardFocus()) {
		return false, nil
	}
	if v.Button == btnRight {
		if i := c.screenAt(c.cursorX, c.cursorY); i >= 0 {
			s := c.screens[i]
			w := s.mon.Current()
			lx, ly := int(c.cursorX)-s.x, int(c.cursorY)-s.y
			if !s.mon.previewing() && !s.mon.ov.open && !w.pinned() && s.mon.frameHas(float64(lx), float64(ly)) {
				if b, ok := w.borderAt(lx, ly, max(w.gap()/2, gapPad)); ok {
					last := c.lastBorderClick
					if last.ws == w && last.b.kind == b.kind && last.b.col == b.col && last.b.row == b.row && v.Time >= last.at && v.Time-last.at <= 400*time.Millisecond {
						c.lastBorderClick.ws = nil
						now := c.now()
						shots := c.snapshot(now)
						w.resetBorder(b)
						c.transition(shots, now)
						c.buttons[v.Button], c.swallow[v.Button] = true, true
						return true, c.publish(ctx)
					}
					c.lastBorderClick.at, c.lastBorderClick.ws, c.lastBorderClick.b = v.Time, w, b
					g := newBorderResize(c, s, w, b, v.Button)
					return true, c.beginPointerGrab(ctx, g)
				}
			}
		}
	}
	c.lastBorderClick.ws = nil
	return c.startDrag(ctx, v)
}

func (c *Core) beginPointerGrab(ctx context.Context, g pointerGrab) error {
	if err := c.dismissGrabs(ctx, 0); err != nil {
		return err
	}
	c.pgrab = g
	c.buttons[g.button()] = true
	c.grab = 0
	if c.pointer != 0 {
		c.pointer = 0
		if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
			return err
		}
	}
	if err := c.command(ctx, ports.CursorOverride{Shape: g.cursor()}); err != nil {
		return err
	}
	return c.publish(ctx)
}

func (c *Core) endPointerGrab(ctx context.Context) error {
	g := c.pgrab
	c.pgrab = nil
	if err := c.command(ctx, ports.CursorOverride{}); err != nil {
		return err
	}
	return g.release(ctx)
}

// abortPointerGrab keeps the current layout and swallows the held release.
// The caller restores state first when this is an Escape cancellation.
func (c *Core) abortPointerGrab(ctx context.Context) error {
	if c.pgrab == nil {
		return nil
	}
	c.swallow[c.pgrab.button()] = true
	c.pgrab = nil
	return c.command(ctx, ports.CursorOverride{})
}
