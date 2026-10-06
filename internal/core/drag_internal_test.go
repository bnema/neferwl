package core

import (
	"context"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestResized(t *testing.T) {
	u := Rect{W: 1000, H: 800}
	r := Rect{X: 100, Y: 100, W: 300, H: 200}
	for _, tc := range []struct {
		edges  ports.ResizeEdges
		dx, dy int
		want   Rect
	}{
		{ports.ResizeRight | ports.ResizeBottom, 50, 20, Rect{X: 100, Y: 100, W: 350, H: 220}},
		{ports.ResizeLeft | ports.ResizeTop, 50, 20, Rect{X: 150, Y: 120, W: 250, H: 180}},
		{ports.ResizeLeft, -500, 0, Rect{X: 0, Y: 100, W: 400, H: 200}},
		{ports.ResizeRight, -1000, 0, Rect{X: 100, Y: 100, W: 64, H: 200}},
		{ports.ResizeBottom, 0, 2000, Rect{X: 100, Y: 100, W: 300, H: 700}},
	} {
		if got := resized(r, tc.edges, tc.dx, tc.dy, 64, u); got != tc.want {
			t.Errorf("%v %d,%d: %+v, want %+v", tc.edges, tc.dx, tc.dy, got, tc.want)
		}
	}
}

func TestScrollBy(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	for id := WindowID(1); id <= 4; id++ {
		w.AddWindow(id)
	}
	w.FocusID(1)
	if w.View != 0 {
		t.Fatal(w.View)
	}
	if w.scrollBy(-1) {
		t.Fatal("scrolled past the start")
	}
	steps := 0
	for w.scrollBy(1) {
		steps++
		if steps > 10 {
			t.Fatal("no end")
		}
	}
	if steps != 2 {
		t.Fatal(steps)
	}
	last := w.columnX(3) + w.columnWidth(3) - w.View
	if last != w.Usable.X+w.Usable.W-w.gap() {
		t.Fatal(last)
	}
	// Focus did not change.
	if id, _ := w.Focused(); id != 1 {
		t.Fatal(id)
	}
	for w.scrollBy(-1) {
	}
	if w.View != 0 {
		t.Fatal(w.View)
	}
}

func TestDropInHiddenColumns(t *testing.T) {
	w := workspace()
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	w.Overflow = OverflowFixed
	w.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	w.FocusID(2)
	w.ToggleFullWidth()
	// Columns 1 and 3 are hidden by the maximized one: the point where
	// column 3 was hits 2 now, a swap with itself: nothing.
	if tgt := w.dropIn(2, 900, 400, 4); tgt.kind != dropNone {
		t.Fatalf("%+v", tgt)
	}
	// Dragging 1 (hidden) over 2: top band stacks above 2.
	if tgt := w.dropIn(1, 500, 30, 4); tgt.kind != dropAbove || tgt.anchor != 2 {
		t.Fatalf("%+v", tgt)
	}
}

// dragCore is a core with one 1000x600 output and no channels but the
// scene and command ones; tests drive its methods directly.
func dragCore(t *testing.T) *Core {
	t.Helper()
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 3
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: make(chan ports.ClientCommand, 1024)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.cur().info = ports.OutputInfo{Name: "OUT", Width: 1000, Height: 600}
	c.cur().mon.SetOutput(1000, 600)
	return c
}

func TestCancelKeepsFloatRaised(t *testing.T) {
	c := dragCore(t)
	w := c.cur().mon.Current()
	w.AddWindow(1)
	w.AddFloating(2, 1000, 600)
	w.FocusID(1)
	if !w.Floats[0].below {
		t.Fatal("not below")
	}
	if err := c.beginDrag(context.Background(), 2, btnLeft, false, 0); err != nil {
		t.Fatal(err)
	}
	c.cancelDrag()
	if w.Floats[0].below {
		t.Fatal("escape put the focused float behind the tiles")
	}
}

func TestDropReleasesSlot(t *testing.T) {
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
	r, _ := w.placed(2)
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
}
