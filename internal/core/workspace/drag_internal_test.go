package workspace

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
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

func TestAnchorHiddenColumns(t *testing.T) {
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
		p := domain.DefaultDropPolicy()
		// Columns 1 and 3 are hidden by the maximized one: the point where
		// column 3 was hits 2 now, a drop on itself: nothing.
		if a := w.AnchorAt(w, 2, 900, 400, Anchor{}, p); a.Kind != AnchorNone {
			t.Fatalf("%+v", a)
		}
		// Dragging 1 (hidden) over 2: the top band stacks above 2.
		if a := w.AnchorAt(w, 1, 500, 30, Anchor{}, p); a.Action != domain.DropStack || a.Side != domain.DropSideTop || a.Window != 2 {
			t.Fatalf("%+v", a)
		}
	})
}
