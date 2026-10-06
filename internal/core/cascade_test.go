package core

import (
	"slices"
	"testing"
)

func cascadeWorkspace(n int) *Workspace {
	w := &Workspace{Overflow: OverflowCascade, MaxColumns: 3}
	w.SetOutput(900, 600)
	for i := 1; i <= n; i++ {
		w.AddWindow(WindowID(i))
	}
	return w
}

func TestCascadeBandsAndReveal(t *testing.T) {
	for _, n := range []int{0, 1, 3, 4, 7} {
		w := cascadeWorkspace(n)
		ps := w.Layout()
		if len(ps) != n {
			t.Fatalf("n=%d placements=%d", n, len(ps))
		}
		for i, p := range ps {
			want := Rect{X: i % 3 * 300, Y: (i/3)*600 - w.View, W: 300, H: 600}
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

func TestCascadeGeometryReusesBuffer(t *testing.T) {
	w := cascadeWorkspace(7)
	buf := w.columnRectsInto(nil, false)
	if n := testing.AllocsPerRun(100, func() { buf = w.columnRectsInto(buf, false) }); n != 0 {
		t.Fatalf("allocations=%v", n)
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
