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
	saved  []Column
}

func newBorderResize(c *Core, s *screen, w *Workspace, b Border, button uint32) *borderResize {
	g := &borderResize{core: c, screen: s, ws: w, border: b, btn: button, saved: slices.Clone(w.Columns)}
	for i := range g.saved {
		g.saved[i].Windows = slices.Clone(g.saved[i].Windows)
		g.saved[i].Shares = slices.Clone(g.saved[i].Shares)
	}
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
	if !slices.Contains(c.screens, s) || s.mon.Current() != w || s.mon.previewing() || s.mon.ov.open || w.Pinned() || w.Fullscreen != 0 || len(w.Columns) != len(g.saved) {
		return false
	}
	for i, col := range w.Columns {
		if !slices.Equal(col.Windows, g.saved[i].Windows) {
			return false
		}
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
	for i, saved := range g.saved {
		col := &g.ws.Columns[i]
		col.Span, col.SpanStamp, col.Shares = saved.Span, saved.SpanStamp, slices.Clone(saved.Shares)
	}
}
