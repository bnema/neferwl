package core

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func cascadeWorkspace(n int) *Workspace {
	w := &Workspace{Overflow: OverflowCascade, MaxColumns: 3}
	w.SetOutput(900, 600)
	for i := 1; i <= n; i++ {
		w.AddWindow(WindowID(i))
	}
	return w
}

// With gaps and a width that does not divide evenly, the columns of a
// partial band share what the gaps leave, and the configured width follows
// the band as it fills.
func TestCascadeBandWidthWithGaps(t *testing.T) {
	w := &Workspace{Overflow: OverflowCascade, MaxColumns: 3}
	w.SetOutput(1000, 600)
	w.SetGaps(8)
	w.AddWindow(1)
	if got := w.columnWidth(0); got != 1000-2*8 {
		t.Fatalf("lone column width %d, want %d", got, 1000-2*8)
	}
	w.AddWindow(2)
	cell := (1000 - 3*8) / 2
	for i, x := range []int{8, 8 + cell + 8} {
		r := w.columnRectsFor(false)[i]
		if r.X != x || r.W != cell || w.columnWidth(i) != cell {
			t.Fatalf("column %d rect %v width %d, want X %d W %d", i, r, w.columnWidth(i), x, cell)
		}
	}
}

// A partial last band after a full one shares the band width with gaps:
// each band divides by its own column count.
func TestCascadePartialLastBandWithGaps(t *testing.T) {
	w := &Workspace{Overflow: OverflowCascade, MaxColumns: 3}
	w.SetOutput(1000, 600)
	w.SetGaps(8)
	for id := WindowID(1); id <= 5; id++ {
		w.AddWindow(id)
	}
	third, half := (1000-4*8)/3, (1000-3*8)/2
	rects := w.columnRectsFor(false)
	for i, want := range []struct{ x, w int }{
		{8, third}, {8 + third + 8, third}, {8 + 2*(third+8), third},
		{8, half}, {8 + half + 8, half},
	} {
		if r := rects[i]; r.X != want.x || r.W != want.w {
			t.Fatalf("column %d rect %v, want X %d W %d", i, r, want.x, want.w)
		}
	}
	if rects[3].Y-rects[0].Y != 600 {
		t.Fatalf("band spacing %d, want 600", rects[3].Y-rects[0].Y)
	}
}

// A side panel moves and narrows every band: cells start at the usable
// area's X and share its width.
func TestCascadeSidePanel(t *testing.T) {
	w := &Workspace{Overflow: OverflowCascade, MaxColumns: 2}
	w.SetOutput(1000, 600)
	w.SetUsable(Rect{X: 100, W: 900, H: 600})
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	rects := w.columnRectsFor(false)
	for i, want := range []Rect{
		{X: 100, Y: 0, W: 450, H: 600},
		{X: 550, Y: 0, W: 450, H: 600},
		{X: 100, Y: 600, W: 900, H: 600},
	} {
		if rects[i].X != want.X || rects[i].W != want.W || rects[i].Y-rects[0].Y != want.Y {
			t.Fatalf("column %d rect %v, want %v", i, rects[i], want)
		}
	}
	// The panel edge at x=100 is no shared line: column 1 has only its
	// right neighbor.
	w.FocusID(1)
	if p := previewOf(t, w.Layout(), 1); p.Neighbors != ports.SideRight {
		t.Fatalf("neighbors %04b, want right only", p.Neighbors)
	}
}

// Each band's columns share its width equally, as in fixed overflow: a
// lone column fills it, a full band of three has thirds.
func TestCascadeBandsAndReveal(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 5, 7} {
		w := cascadeWorkspace(n)
		ps := w.Layout()
		if len(ps) != n {
			t.Fatalf("n=%d placements=%d", n, len(ps))
		}
		for i, p := range ps {
			inBand := min(n-i/3*3, 3)
			cell := 900 / inBand
			want := Rect{X: i % 3 * cell, Y: (i/3)*600 - w.View, W: cell, H: 600}
			if p.Rect != want {
				t.Fatalf("n=%d window=%d rect=%v want=%v", n, p.ID, p.Rect, want)
			}
		}
		if n > 0 && w.View != ((n-1)/3)*600 {
			t.Fatalf("n=%d view=%d", n, w.View)
		}
	}
}

func TestCascadeCompactionAndNavigation(t *testing.T) {
	w := cascadeWorkspace(7)
	w.FocusID(2)
	if !w.FocusWindow(1) {
		t.Fatal("no lower band")
	}
	if id, _ := w.Focused(); id != 5 {
		t.Fatalf("focus=%d", id)
	}
	w.RemoveWindow(1)
	if w.View != 600 {
		t.Fatalf("compacted view=%d", w.View)
	}
	w.SetMaxColumns(2)
	if w.View != 600 {
		t.Fatalf("changed max view=%d", w.View)
	}
	w.FocusID(2)
	w.AddWindow(8)
	if w.Columns[len(w.Columns)-1].Windows[0] != 8 {
		t.Fatal("new column did not append")
	}
	w.SetUsable(Rect{X: 0, Y: 20, W: 900, H: 580})
	if w.View != w.band(w.Focus)*580 {
		t.Fatal("resize did not reveal band")
	}
	if !slices.Equal(w.snapPoints(), []float64{0, 580, 1160, 1740}) {
		t.Fatalf("points=%v", w.snapPoints())
	}
}

func TestCascadeCovers(t *testing.T) {
	w := cascadeWorkspace(4)
	w.FocusID(2)
	w.ToggleFullWidth()
	ps := w.Layout()
	for _, p := range ps {
		if p.ID == 2 && p.Rect.W != 900 {
			t.Fatalf("full width=%v", p.Rect)
		}
		if (p.ID == 1 || p.ID == 3) && !p.Hidden {
			t.Fatalf("sibling %d visible", p.ID)
		}
	}
	w.ToggleFullWidth()
	w.ToggleFullscreen()
	for _, p := range w.Layout() {
		if p.ID == 2 && p.Rect != w.Output {
			t.Fatalf("fullscreen=%v", p.Rect)
		}
		if p.ID != 2 && !p.Hidden {
			t.Fatalf("fullscreen sibling %d visible", p.ID)
		}
	}
	w.ToggleFullscreen()
	w.FocusID(4)
	if w.View != 600 {
		t.Fatal("fullscreen restore lost band reveal")
	}
}

func TestCascadeClearsMaximizationOnBandChanges(t *testing.T) {
	for _, move := range []struct {
		name string
		run  func(*Workspace)
	}{
		{"focus", func(w *Workspace) { w.FocusWindow(1) }},
		{"append", func(w *Workspace) { w.AddWindow(5) }},
		{"insert", func(w *Workspace) { w.insertColumn(4, Column{Windows: []WindowID{5}}) }},
	} {
		t.Run(move.name, func(t *testing.T) {
			w := cascadeWorkspace(4)
			w.FocusID(1)
			w.ToggleFullWidth()
			move.run(w)
			for _, col := range w.Columns {
				if col.FullWidth {
					t.Fatal("stale maximization")
				}
			}
		})
	}
}

func TestCascadeReloadAndStashRestoreClearMaximization(t *testing.T) {
	w := cascadeWorkspace(3)
	w.FocusID(1)
	w.ToggleFullWidth()
	w.restore(4, &origPlace{stacked: []WindowID{2}, col: 1})
	for _, c := range w.Columns {
		if c.FullWidth {
			t.Fatal("restore retained maximization")
		}
	}
	m := NewMonitor()
	m.SetOutput(900, 600)
	for i := WindowID(1); i <= 3; i++ {
		m.AddWindow(i)
	}
	m.Current().FocusID(1)
	m.Current().ToggleFullWidth()
	m.Current().FocusID(3)
	m.SetOverflow(OverflowCascade)
	for _, c := range m.Current().Columns {
		if c.FullWidth {
			t.Fatal("reload retained maximization")
		}
	}
}

func TestCascadeGestureValidityDoesNotAllocate(t *testing.T) {
	m := NewMonitor()
	m.SetOutput(900, 600)
	m.SetOverflow(OverflowCascade)
	for i := WindowID(1); i <= 7; i++ {
		m.AddWindow(i)
	}
	g := &swipeGesture{ws: m.Current(), points: m.Current().snapPoints(), horizontal: false, opens: m.overviewOpens}
	if n := testing.AllocsPerRun(100, func() {
		if g.columnsChanged(m) {
			t.Fatal("unchanged gesture invalidated")
		}
	}); n != 0 {
		t.Fatalf("allocations=%v", n)
	}
}

func TestCascadeGeometryReusesBuffer(t *testing.T) {
	w := cascadeWorkspace(7)
	buf := w.columnRectsInto(nil, false)
	if n := testing.AllocsPerRun(100, func() { buf = w.columnRectsInto(buf, false) }); n != 0 {
		t.Fatalf("allocations=%v", n)
	}
	// The overview's lane-encoded tiles reuse their buffer too.
	tiles, _, _ := w.previewTiles()
	if n := testing.AllocsPerRun(100, func() { tiles, _, _ = w.previewTilesInto(tiles[:0]) }); n != 0 {
		t.Fatalf("preview tile allocations=%v", n)
	}
}

// Lanes are the bands of a cascade workspace and the whole row of any other.
func TestLaneGeometry(t *testing.T) {
	w := cascadeWorkspace(7) // bands {0,1,2} {3,4,5} {6}
	for _, c := range []struct{ i, lane int }{{0, 0}, {2, 0}, {3, 1}, {6, 2}} {
		if got := w.laneOf(c.i); got != c.lane {
			t.Fatalf("cascade laneOf(%d)=%d, want %d", c.i, got, c.lane)
		}
	}
	for lane, want := range [][2]int{{0, 2}, {3, 5}, {6, 6}} {
		if first, last := w.laneBounds(lane); first != want[0] || last != want[1] {
			t.Fatalf("cascade laneBounds(%d)=%d,%d, want %v", lane, first, last, want)
		}
	}
	for _, lane := range []int{-1, 3} {
		if first, last := w.laneBounds(lane); first <= last {
			t.Fatalf("cascade laneBounds(%d)=%d,%d exists", lane, first, last)
		}
	}
	// The closest column of the adjacent lane keeps its offset, clamped.
	for _, c := range []struct{ focus, d, want int }{{1, 1, 4}, {5, 1, 6}, {4, -1, 1}, {2, -1, -1}, {6, 1, -1}, {6, -1, 3}} {
		w.Focus = c.focus
		if got := w.laneNeighbor(c.d); got != c.want {
			t.Fatalf("cascade laneNeighbor from %d by %d = %d, want %d", c.focus, c.d, got, c.want)
		}
	}

	s := &Workspace{Overflow: OverflowScroll, MaxColumns: 3}
	s.SetOutput(900, 600)
	for i := 1; i <= 5; i++ {
		s.AddWindow(WindowID(i))
	}
	for i := range s.Columns {
		if s.laneOf(i) != 0 {
			t.Fatalf("scroll laneOf(%d)=%d", i, s.laneOf(i))
		}
	}
	if first, last := s.laneBounds(0); first != 0 || last != 4 {
		t.Fatalf("scroll laneBounds(0)=%d,%d", first, last)
	}
	for _, lane := range []int{-1, 1} {
		if first, last := s.laneBounds(lane); first <= last {
			t.Fatalf("scroll laneBounds(%d)=%d,%d exists", lane, first, last)
		}
	}
	for _, d := range []int{-1, 1} {
		if got := s.laneNeighbor(d); got != -1 {
			t.Fatalf("scroll laneNeighbor(%d)=%d", d, got)
		}
	}
}

func TestMixedLayoutSwitchKeepsSourceAxis(t *testing.T) {
	m := NewMonitor()
	m.SetOutput(900, 600)
	m.Current().Overflow = OverflowCascade
	m.AddWindow(1)
	m.Focus(1)
	m.AddWindow(2)
	m.Current().Overflow = OverflowScroll
	m.Focus(0)
	m.switchAxis = m.Current().policy().workspace
	m.switchView.off = .5
	ps := m.layoutInto(nil)
	for _, p := range ps {
		if p.ID == 1 && (p.Rect.X != -450 || p.Rect.Y != 0) {
			t.Fatalf("cascade source offset=%v", p.Rect)
		}
	}
	m.stopSwitch()
	m.Focus(1)
	m.switchAxis = m.Current().policy().workspace
	m.switchView.off = -.5
	ps = m.layoutInto(nil)
	for _, p := range ps {
		if p.ID == 2 && (p.Rect.X != 0 || p.Rect.Y != 300) {
			t.Fatalf("scroll source offset=%v", p.Rect)
		}
	}
}

func TestCascadeViewportSnap(t *testing.T) {
	w := cascadeWorkspace(7)
	w.FocusID(3)
	if w.snapFocus(600, true) != 5 {
		t.Fatalf("nearest cell=%d", w.snapFocus(600, true))
	}
	if w.snapFocus(1200, true) != 6 {
		t.Fatal("short band neighbour")
	}
	w.SetUsable(Rect{})
	if got := w.snapPoints(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("zero area=%v", got)
	}
}
