package workspace

import (
	"testing"
	"testing/synctest"
)

func TestScrollBy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		if last != w.Usable.X+w.Usable.W-w.Gap() {
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

	})
}

func TestDropInHiddenColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		if tgt := w.DropIn(2, 900, 400, 4); tgt.Kind != DropNone {
			t.Fatalf("%+v", tgt)
		}
		// Dragging 1 (hidden) over 2: top band stacks above 2.
		if tgt := w.DropIn(1, 500, 30, 4); tgt.Kind != DropAbove || tgt.Anchor != 2 {
			t.Fatalf("%+v", tgt)
		}

	})
}
