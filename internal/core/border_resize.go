package core

import (
	"context"
	"slices"
	"time"
)

// borderResize retains the original border identity, never the hit zone that
// moves as its neighbouring tiles resize. saved owns its slices.
type borderResize struct {
	core   *Core
	screen *screen
	ws     *Workspace
	border Border
	btn    uint32
	saved  Sizes
}

func newBorderResize(c *Core, s *screen, w *Workspace, b Border, button uint32) *borderResize {
	g := &borderResize{core: c, screen: s, ws: w, border: b, btn: button, saved: w.SaveSizes()}
	c.focusScreen = c.screenIndex(s.name())
	return g
}

func (g *borderResize) button() uint32 { return g.btn }
func (g *borderResize) cursor() string {
	if g.border.Kind == BorderColumn {
		return "ew-resize"
	}
	return "ns-resize"
}

func (g *borderResize) valid() bool {
	c, s, w := g.core, g.screen, g.ws
	if !slices.Contains(c.screens, s) || s.mon.Current() != w || s.mon.previewing() || s.mon.ov.open || w.Pinned() || w.Fullscreen != 0 || !w.SizesValid(g.saved) {
		return false
	}
	b := g.border
	if b.Col < 0 || b.Col >= len(w.Columns) {
		return false
	}
	if b.Kind == BorderRow {
		return b.Row >= 0 && b.Row+1 < len(w.Columns[b.Col].Windows)
	}
	return b.Col+1 < len(w.Columns) && w.LaneOf(b.Col) == w.LaneOf(b.Col+1)
}

func (g *borderResize) motion(ctx context.Context, _ time.Duration) error {
	c := g.core
	if !g.valid() {
		if err := c.abortPointerGrab(ctx); err != nil {
			return err
		}
		return c.publish(ctx)
	}
	pos := int(c.cursorX) - g.screen.x
	if g.border.Kind == BorderRow {
		pos = int(c.cursorY) - g.screen.y
	}
	if !g.ws.MoveBorder(g.border, pos) {
		return nil
	}
	return c.publish(ctx)
}

func (g *borderResize) release(ctx context.Context) error {
	if err := g.core.publish(ctx); err != nil {
		return err
	}
	return g.core.rehit(ctx)
}

func (g *borderResize) cancel() {
	if !g.valid() {
		return
	}
	g.ws.RestoreSizes(g.saved)
}
