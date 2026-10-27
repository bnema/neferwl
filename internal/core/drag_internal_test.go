package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestResized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u := Rect{W: 1000, H: 800}
		r := Rect{X: 100, Y: 100, W: 300, H: 200}
		for _, tc := range []struct {
			edges  domain.ResizeEdges
			dx, dy int
			want   Rect
		}{
			{domain.ResizeRight | domain.ResizeBottom, 50, 20, Rect{X: 100, Y: 100, W: 350, H: 220}},
			{domain.ResizeLeft | domain.ResizeTop, 50, 20, Rect{X: 150, Y: 120, W: 250, H: 180}},
			{domain.ResizeLeft, -500, 0, Rect{X: 0, Y: 100, W: 400, H: 200}},
			{domain.ResizeRight, -1000, 0, Rect{X: 100, Y: 100, W: 64, H: 200}},
			{domain.ResizeBottom, 0, 2000, Rect{X: 100, Y: 100, W: 300, H: 700}},
		} {
			if got := resized(r, tc.edges, tc.dx, tc.dy, 64, u); got != tc.want {
				t.Errorf("%v %d,%d: %+v, want %+v", tc.edges, tc.dx, tc.dy, got, tc.want)
			}
		}

	})
}

// dragCore is a core with one 1000x600 output and no channels but the
// scene and command ones; tests drive its methods directly.
func dragCore(t *testing.T) *Core {
	t.Helper()
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 3
	c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1), Commands: make(chan ports.ClientCommand, 1024)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.cur().info = ports.OutputInfo{Name: "OUT", Width: 1000, Height: 600}
	c.cur().mon.SetOutput(1000, 600)
	return c
}

func TestCancelKeepsFloatRaised(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		w := c.cur().mon.Current()
		w.AddWindow(1)
		w.AddFloating(2, 1000, 600)
		w.FocusID(1)
		if !w.Inspect().Floats[0].Below {
			t.Fatal("not below")
		}
		if err := c.beginDrag(context.Background(), 2, btnLeft, false, 0); err != nil {
			t.Fatal(err)
		}
		c.cancelDrag()
		if w.Inspect().Floats[0].Below {
			t.Fatal("escape put the focused float behind the tiles")
		}

	})
}

func TestDropReleasesSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		w := c.cur().mon.Current()
		w.Columns = []Column{{Windows: []WindowID{1}, Slot: 1}, {Windows: []WindowID{2}}}
		key := slotKey{workspace: w.Name, index: 1}
		c.slots[key] = &slotState{window: 1}
		ctx := context.Background()
		if err := c.beginDrag(ctx, 1, btnLeft, false, 0); err != nil {
			t.Fatal(err)
		}
		c.drag.target = dropTarget{}
		r, _ := modelPlaced(w, 2)
		c.cursorX, c.cursorY = float64(r.X+r.W/2), float64(r.Y+5)
		if err := c.endDrag(ctx); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(w.Columns[0].Windows, []WindowID{1, 2}) || w.Columns[0].Slot != 0 {
			t.Fatal(w.Columns)
		}
		if c.slots[key].window != 0 {
			t.Fatal("slot still holds the window")
		}

	})
}
