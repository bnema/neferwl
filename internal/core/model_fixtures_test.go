package core

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/layout/layoutmocks"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/mock"
)

func workspace() *Workspace {
	w := &Workspace{}
	w.SetOutput(100, 81)
	w.SetGaps(5)
	w.SetMaxColumns(2)
	return w
}

func order(w *Workspace) []WindowID {
	ids := []WindowID{}
	for _, p := range w.Layout() {
		if !p.Hidden {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func wantOrder(t *testing.T, w *Workspace, want ...WindowID) {
	t.Helper()
	if got := order(w); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func registeredLayouts() []layout.Layout {
	var all []layout.Layout
	for _, name := range layout.LayoutNames() {
		l, _ := layout.Lookup(Overflow(name))
		all = append(all, l)
	}
	return all
}

func columnOrder(w *Workspace) []WindowID {
	var ids []WindowID
	for _, c := range w.Columns {
		ids = append(ids, c.Windows[0])
	}
	return ids
}

func wantColumns(t *testing.T, w *Workspace, want ...WindowID) {
	t.Helper()
	if got := columnOrder(w); !slices.Equal(got, want) {
		t.Fatalf("columns %v, want %v", got, want)
	}
}

func wantBands(t *testing.T, w *Workspace, want ...[]WindowID) {
	t.Helper()
	var got [][]WindowID
	w.WalkBands(func(_, first, last int) bool {
		got = append(got, columnOrder(&Workspace{Lanes: layout.Lanes{Columns: w.Columns[first : last+1]}}))
		return true
	})
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("bands %v, want %v", got, want)
	}
}

func bandIDs(w *Workspace) [][]WindowID {
	var out [][]WindowID
	w.WalkBands(func(_, first, last int) bool {
		var b []WindowID
		for _, c := range w.Columns[first : last+1] {
			b = append(b, c.Windows[0])
		}
		out = append(out, slices.Clone(b))
		return true
	})
	return out
}

func cellsOf(w *Workspace) []int {
	got := make([]int, len(w.Columns))
	for i, c := range w.Columns {
		got[i] = c.Cells
	}
	return got
}

func placement(w *Workspace, id WindowID) Placement {
	for _, p := range w.Layout() {
		if p.ID == id {
			return p
		}
	}
	return Placement{}
}

func shownCards(t *testing.T, w *Workspace, ps []Placement) []Placement {
	t.Helper()
	var out []Placement
	for _, e := range w.Stash {
		if p := previewOf(t, ps, e.ID); !p.Hidden {
			out = append(out, p)
		}
	}
	return out
}

func stashIDs(w *Workspace) []WindowID {
	var ids []WindowID
	for _, f := range w.Stash {
		ids = append(ids, f.ID)
	}
	return ids
}

func dispatchLayout(t *testing.T, cols int) (*Workspace, *layoutmocks.MockLayout) {
	return dispatchLayoutWith(t, cols, scrollTraits)
}

func dispatchLayoutWith(t *testing.T, cols int, tr dispatchTraits) (*Workspace, *layoutmocks.MockLayout) {
	t.Helper()
	l := layoutmocks.NewMockLayout(t)
	l.EXPECT().NewState().Return(nil).Maybe()
	l.EXPECT().OverridesWidth().Return(true).Maybe()
	l.EXPECT().Name().Return(OverflowScroll).Maybe()
	l.EXPECT().Axes().Return(tr.content, tr.workspace).Maybe()
	l.EXPECT().EqualCells().Return(false).Maybe()
	l.EXPECT().MaximizeHides().Return(false).Maybe()
	l.EXPECT().ResetsMaximize().Return(false).Maybe()
	l.EXPECT().Scrolls().Return(tr.scrolls).Maybe()
	l.EXPECT().Spiral().Return(tr.spiral).Maybe()
	l.EXPECT().Bands().Return(false).Maybe()
	l.EXPECT().PinsFullscreen().Return(false).Maybe()
	l.EXPECT().Isolate(mock.Anything, mock.Anything).Return(false).Maybe()
	l.EXPECT().LaneRow(mock.Anything).Return(tr.laneRow).Maybe()
	w := &Workspace{Lanes: layout.Lanes{Override: l, MaxColumns: 3, Usable: Rect{W: 900, H: 600}, Output: Rect{W: 900, H: 600}}}
	for i := 0; i < cols; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	return w, l
}

func cacheAgrees(t *testing.T, step string, w *Workspace) {
	t.Helper()
	want, ok := layout.Lookup(w.Overflow)
	if !ok {
		want, _ = layout.Lookup(layout.Default)
	}
	if got := w.Engine(); got != want {
		t.Fatalf("%s: engine %v, want %v for %q", step, got, want, w.Overflow)
	}

}

func heights(w *Workspace) map[WindowID]int {
	out := map[WindowID]int{}
	for _, p := range w.Layout() {
		if !p.Floating {
			out[p.ID] = p.Rect.H
		}
	}
	return out
}

func spanWorkspace(mode Overflow, n int) *Workspace {
	w := workspace()
	w.Overflow = mode
	w.MaxColumns = 3
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	for i := 0; i < n; i++ {
		w.AddWindow(WindowID(i + 1))
	}
	w.View = 0
	w.StopColumns()
	return w
}

func stampSpans(w *Workspace, first, last int) {
	stamp := w.NewStamp()
	for i := first; i <= last; i++ {
		w.Columns[i].Span = 10000 / (last - first + 1)
		w.Columns[i].SpanStamp = stamp
	}
	w.Columns[last].Span += 10000 % (last - first + 1)
}

func setVisibleNeighbors(tiles []Placement, gap int, area Rect) {
	visible := func(p Placement) bool {
		return !p.Hidden && !p.Fullscreen && p.Rect.Overlaps(area)
	}
	overlap := func(a0, a1, b0, b1 int) bool { return a0 < b1 && b0 < a1 }
	for i := range tiles {
		a := &tiles[i]
		a.Neighbors, a.Inset = 0, 0
		if !visible(*a) {
			continue
		}
		for j := range tiles {
			if i == j || !visible(tiles[j]) {
				continue
			}
			b, r := tiles[j].Rect, a.Rect
			// Only count a shared edge strictly within the area.
			switch {
			case r.X+r.W+gap == b.X && overlap(r.Y, r.Y+r.H, b.Y, b.Y+b.H) && r.X+r.W > area.X && r.X+r.W < area.X+area.W:
				a.Neighbors |= domain.SideRight
			case b.X+b.W+gap == r.X && overlap(r.Y, r.Y+r.H, b.Y, b.Y+b.H) && r.X > area.X && r.X < area.X+area.W:
				a.Neighbors |= domain.SideLeft
			case r.Y+r.H+gap == b.Y && overlap(r.X, r.X+r.W, b.X, b.X+b.W) && r.Y+r.H > area.Y && r.Y+r.H < area.Y+area.H:
				a.Neighbors |= domain.SideBottom
			case b.Y+b.H+gap == r.Y && overlap(r.X, r.X+r.W, b.X, b.X+b.W) && r.Y > area.Y && r.Y < area.Y+area.H:
				a.Neighbors |= domain.SideTop
			}
		}
		a.Inset = a.Neighbors
		if gap == 0 {
			a.Inset &= domain.SideRight | domain.SideBottom
		} else {
			a.Inset = domain.SideAll
		}
	}
}

func resized(r Rect, edges domain.ResizeEdges, dx, dy, least int, u Rect) Rect {
	left, top, right, bottom := r.X, r.Y, r.X+r.W, r.Y+r.H
	if edges&domain.ResizeLeft != 0 {
		left = min(max(left+dx, u.X), right-least)
	}
	if edges&domain.ResizeRight != 0 {
		right = max(min(right+dx, u.X+u.W), left+least)
	}
	if edges&domain.ResizeTop != 0 {
		top = min(max(top+dy, u.Y), bottom-least)
	}
	if edges&domain.ResizeBottom != 0 {
		bottom = max(min(bottom+dy, u.Y+u.H), top+least)
	}
	return Rect{X: left, Y: top, W: right - left, H: bottom - top}
}
