package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// threeTileDrag starts a tile drag of 1 on a core with tiles 1-3.
func threeTileDrag(t *testing.T, c *Core) *Workspace {
	t.Helper()
	w := c.cur().mon.Current()
	w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}, {Windows: []WindowID{3}}}
	if err := c.beginDrag(context.Background(), 1, btnLeft, false, 0); err != nil {
		t.Fatal(err)
	}
	return w
}

// A release on a hover edge drops nothing: no flash, no move.
func TestReleaseOnHoverEdgeDropsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		c.cfg.Animations.On, c.cfg.Focus.Flash = true, true
		w := threeTileDrag(t, c)
		c.cursorX, c.cursorY = 995, 300
		c.retarget(w)
		if a := c.drag.target.a; !hoverEdge(a) {
			t.Fatalf("not on a hover edge: %+v", a)
		}
		if err := c.endDrag(context.Background()); err != nil {
			t.Fatal(err)
		}
		if c.flash.pending != nil {
			t.Fatalf("flash requested: %+v", c.flash.pending)
		}
		if got := w.ColumnOf(1); got != 0 || len(w.Columns) != 3 {
			t.Fatalf("1 moved to column %d of %d", got, len(w.Columns))
		}
	})
}

// The per-motion target lookup does not allocate, with output entries in
// the drop policy.
func TestDropAtAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		var o domain.OutputDrop
		o.Output = "OUT"
		o.SetEdge(domain.DropSideRight, domain.DropColumn)
		c.cfg.Drop.Outputs = []domain.OutputDrop{{Output: "other"}, o}
		c.cur().mon.Name = "OUT"
		w := threeTileDrag(t, c)
		r, _ := modelPlaced(w, 2)
		pts := [][2]float64{{float64(r.X + r.W/2), float64(r.Y + r.H/2)}, {float64(r.X + 5), float64(r.Y + r.H/2)}, {995, 300}, {5, 300}}
		for _, p := range pts {
			c.cursorX, c.cursorY = p[0], p[1]
			c.retarget(w)
		}
		if a := c.dropAt(w, 1, 995, 300).a; a.Kind != AnchorEdge || a.Action != domain.DropColumn {
			t.Fatalf("output entry not applied: %+v", a)
		}
		i := 0
		if n := testing.AllocsPerRun(1000, func() {
			p := pts[i%len(pts)]
			i++
			src, ok := c.dragAlive(1)
			if !ok {
				panic("drag not alive")
			}
			c.dropAt(src, 1, p[0], p[1])
		}); n != 0 {
			t.Fatalf("dragAlive + dropAt: %g allocations, want 0", n)
		}
	})
}
