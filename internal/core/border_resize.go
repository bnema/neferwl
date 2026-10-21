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
	border border
	btn    uint32
	saved  []Column
	start  [2]float64
}

func newBorderResize(c *Core, s *screen, w *Workspace, b border, button uint32) *borderResize {
	g := &borderResize{core: c, screen: s, ws: w, border: b, btn: button, start: [2]float64{c.cursorX, c.cursorY}, saved: slices.Clone(w.Columns)}
	for i := range g.saved {
		g.saved[i].Windows = slices.Clone(g.saved[i].Windows)
		g.saved[i].Shares = slices.Clone(g.saved[i].Shares)
	}
	c.focusScreen = c.screenIndex(s.name())
	return g
}

func (g *borderResize) button() uint32 { return g.btn }
func (g *borderResize) cursor() string {
	if g.border.kind == borderColumn {
		return "ew-resize"
	}
	return "ns-resize"
}

func (g *borderResize) valid() bool {
	c, s, w := g.core, g.screen, g.ws
	if !slices.Contains(c.screens, s) || s.mon.Current() != w || s.mon.previewing() || s.mon.ov.open || w.pinned() || w.fullscreen != 0 || len(w.Columns) != len(g.saved) {
		return false
	}
	for i, col := range w.Columns {
		if !slices.Equal(col.Windows, g.saved[i].Windows) {
			return false
		}
	}
	b := g.border
	if b.col < 0 || b.col >= len(w.Columns) {
		return false
	}
	if b.kind == borderRow {
		return b.row >= 0 && b.row+1 < len(w.Columns[b.col].Windows)
	}
	return b.col+1 < len(w.Columns) && w.laneOf(b.col) == w.laneOf(b.col+1)
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
	if g.border.kind == borderRow {
		pos = int(c.cursorY) - g.screen.y
	}
	g.ws.setBorder(g.border, pos)
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
		col.Span, col.spanLane, col.Shares = saved.Span, saved.spanLane, slices.Clone(saved.Shares)
	}
}
