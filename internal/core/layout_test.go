package core

import (
	"math"
	"testing"
)

func workspace() *Workspace {
	w := &Workspace{}
	w.SetOutput(100, 81)
	w.SetGaps(5)
	w.SetDefaultWidth(Width{Num: 1, Den: 2})
	return w
}
func TestWidths(t *testing.T) {
	for _, tc := range []struct {
		s      string
		ok     bool
		pixels int
	}{{"1", true, 0}, {"1/2", true, 0}, {"25px", true, 25}, {"2/1", false, 0}, {"0px", false, 0}, {"1/0", false, 0}, {"abc", false, 0}} {
		v, e := ParseWidth(tc.s)
		if (e == nil) != tc.ok || (e == nil && v.Pixels != tc.pixels) {
			t.Errorf("%q: %v %v", tc.s, v, e)
		}
	}
	if n := (Width{Num: 1, Den: 2}).Resolve(100, 5); n != 43 {
		t.Fatal(n)
	}
	if n := (Width{Pixels: 200}).Resolve(100, 5); n != 90 {
		t.Fatal(n)
	}
}
func TestOperations(t *testing.T) {
	w := workspace()
	w.FocusColumn(1)
	w.FocusWindow(-1)
	w.MoveColumn(1)
	w.CycleWidth()
	w.ToggleFullscreen()
	w.RemoveWindow(1)
	if _, ok := w.Focused(); ok {
		t.Fatal("empty focus")
	}
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddWindow(3)
	if w.Focus != 2 || w.ViewX != 49 {
		t.Fatalf("scroll: %+v", w)
	}
	w.FocusColumn(-1)
	if w.ViewX != 48 {
		t.Fatal(w.ViewX)
	}
	w.FocusColumn(-1)
	if w.ViewX != 0 {
		t.Fatal(w.ViewX)
	}
	w.MoveColumn(1)
	if w.Focus != 1 || w.Columns[1].Windows[0] != 1 {
		t.Fatal(w.Columns)
	}
	w.RemoveWindow(1)
	if id, _ := w.Focused(); id != 2 {
		t.Fatal(id)
	}
	w.RemoveWindow(3)
	w.RemoveWindow(2)
	if len(w.Columns) != 0 {
		t.Fatal(w.Columns)
	}
}
func TestStackAndLayout(t *testing.T) {
	w := workspace()
	w.Columns = []Column{{Windows: []WindowID{1, 2, 3}, Width: Width{Num: 1, Den: 2}, Focus: 1}}
	w.RemoveWindow(2)
	if id, _ := w.Focused(); id != 1 {
		t.Fatal(id)
	}
	w.FocusWindow(1)
	if id, _ := w.Focused(); id != 3 {
		t.Fatal(id)
	}
	w.FocusWindow(-1)
	p := w.Layout()
	if p[0].Rect != (Rect{5, 5, 43, 33}) || p[1].Rect != (Rect{5, 43, 43, 33}) {
		t.Fatal(p)
	}
	w.ToggleFullscreen()
	if p = w.Layout(); !p[0].Fullscreen || p[0].Rect != (Rect{X: 0, Y: 0, W: 100, H: 81}) || !p[1].Hidden || p[1].Rect != (Rect{}) {
		t.Fatal(p)
	}
	w.ToggleFullscreen()
	if w.Layout()[0].Fullscreen {
		t.Fatal("toggle")
	}
	w.SetUsable(Rect{10, 10, 60, 50})
	w.SetGaps(2)
	if w.Layout()[0].Rect.X != 12 {
		t.Fatal(w.Layout())
	}
	w.SetOutput(120, 90)
	if w.Usable.W != 60 {
		t.Fatal(w.Usable)
	}
}
func TestPresetsAndActions(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	p := []Width{{Pixels: 30}, {Pixels: 40}}
	w.SetPresets(p)
	p[0].Pixels = 99
	w.Apply(ActionCycleColumnWidth)
	if w.Columns[0].Width.Pixels != 30 {
		t.Fatal(w.Columns)
	}
	w.CycleWidth()
	if w.Columns[0].Width.Pixels != 40 {
		t.Fatal(w.Columns)
	}
	w.CycleWidth()
	if w.Columns[0].Width.Pixels != 30 {
		t.Fatal(w.Columns)
	}
	for _, tc := range []struct {
		a    Action
		want Effect
	}{{ActionSpawnTerminal, Effect{Spawn: true}}, {ActionCloseWindow, Effect{Close: 1}}, {ActionQuit, Effect{Quit: true}}, {ActionFocusColumnLeft, Effect{}}, {ActionFocusColumnRight, Effect{}}, {ActionFocusWindowUp, Effect{}}, {ActionFocusWindowDown, Effect{}}, {ActionMoveColumnLeft, Effect{}}, {ActionMoveColumnRight, Effect{}}, {ActionToggleFullscreen, Effect{}}, {ActionCycleColumnWidth, Effect{}}} {
		if got := w.Apply(tc.a); got != tc.want {
			t.Errorf("%s: %+v", tc.a, got)
		}
	}
	w.SetPresets(nil)
	old := w.Columns[0].Width
	w.CycleWidth()
	if w.Columns[0].Width != old {
		t.Fatal("empty presets")
	}
}
func FuzzWorkspaceOps(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Fuzz(func(t *testing.T, ops []byte) {
		w := workspace()
		w.SetPresets([]Width{{Pixels: 25}, {Pixels: 50}})
		next := WindowID(1)
		for _, op := range ops {
			switch op % 18 {
			case 0:
				w.AddWindow(next)
				next++
			case 1:
				if id, ok := w.Focused(); ok {
					w.RemoveWindow(id)
				}
			case 2:
				w.FocusColumn(-1)
			case 3:
				w.FocusColumn(1)
			case 4:
				w.FocusWindow(-1)
			case 5:
				w.FocusWindow(1)
			case 6:
				w.MoveColumn(-1)
			case 7:
				w.MoveColumn(1)
			case 8:
				w.CycleWidth()
			case 9:
				w.ToggleFullscreen()
			case 10:
				w.SetGaps(int(op) % 10)
			case 11:
				w.SetOutput(int(op)-50, int(op)-80)
			case 12:
				w.SetUsable(Rect{X: int(op) % 20, Y: int(op) % 20, W: int(op) - 50, H: int(op) - 80})
			case 13:
				w.SetGaps(int(op) * 2)
			case 14:
				if len(w.Columns) > 1 {
					stackFocused(w)
				}
			case 15:
				w.SetDefaultWidth(Width{Num: math.MaxInt, Den: math.MaxInt})
				if len(w.Columns) > 0 {
					w.Columns[w.Focus].Width = w.DefaultWidth
					w.scroll()
				}
			case 16:
				w.SetOutput(0, 0)
			case 17:
				w.SetUsable(Rect{W: math.MaxInt, H: math.MaxInt})
			}
			seen := map[WindowID]bool{}
			for _, c := range w.Columns {
				if len(c.Windows) == 0 || c.Focus < 0 || c.Focus >= len(c.Windows) {
					t.Fatal("invalid column")
				}
				for _, id := range c.Windows {
					if seen[id] {
						t.Fatal("duplicate")
					}
					seen[id] = true
				}
			}
			if len(w.Columns) > 0 {
				if w.Focus < 0 || w.Focus >= len(w.Columns) {
					t.Fatal("focus")
				}
				left := w.columnX(w.Focus) - w.ViewX
				width := w.columnWidth(w.Focus)
				minX, maxX := w.Usable.X+w.gap(), w.Usable.X+w.Usable.W-w.gap()
				if w.fullscreenColumn(w.Focus) {
					minX, maxX = 0, w.Output.W
				}
				if width <= maxX-minX && (left < minX || left+width > maxX) {
					t.Fatal("scroll")
				}
			}
			placements := w.Layout()
			if len(placements) != len(seen) {
				t.Fatal("count")
			}
			fullCount := 0
			for _, p := range placements {
				if p.Fullscreen {
					fullCount++
				}
				r := p.Rect
				if r.W < 0 || r.H < 0 || r.Y < 0 || r.Y > w.Output.H || r.H > w.Output.H-r.Y {
					t.Fatalf("bounds: %+v output %+v", p, w.Output)
				}
				if p.Hidden && r != (Rect{}) {
					t.Fatal("hidden rect")
				}
				if !p.Hidden && !p.Fullscreen && (r.Y < w.Usable.Y || r.Y > w.Usable.Y+w.Usable.H || r.H > w.Usable.Y+w.Usable.H-r.Y) {
					t.Fatalf("usable vertical bounds: %+v usable %+v", p, w.Usable)
				}
				if !seen[p.ID] {
					t.Fatal("missing")
				}
				delete(seen, p.ID)
			}
			if fullCount > 1 {
				t.Fatal("multiple fullscreen")
			}
		}
	})
}

// stackFocused moves the next column's window into the focused column.
func stackFocused(w *Workspace) {
	i := w.Focus
	j := (i + 1) % len(w.Columns)
	id := w.Columns[j].Windows[0]
	w.Columns[i].Windows = append(w.Columns[i].Windows, id)
	w.Columns[j].Windows = w.Columns[j].Windows[1:]
	if len(w.Columns[j].Windows) == 0 {
		w.Columns = append(w.Columns[:j], w.Columns[j+1:]...)
		if j < i {
			w.Focus--
		}
	} else if w.Columns[j].Focus >= len(w.Columns[j].Windows) {
		w.Columns[j].Focus = len(w.Columns[j].Windows) - 1
	}
	w.scroll()
}

func TestLargeWidthAndBounds(t *testing.T) {
	for _, tc := range []struct {
		width              Width
		usable, gaps, want int
	}{
		{Width{Num: 1, Den: 1}, math.MaxInt, 0, math.MaxInt},
		{Width{Num: math.MaxInt, Den: math.MaxInt}, math.MaxInt, 0, math.MaxInt},
		{Width{Num: math.MaxInt, Den: math.MaxInt}, math.MaxInt, 5, math.MaxInt - 10},
		{Width{Num: 1, Den: 2}, math.MaxInt, 0, (math.MaxInt + 1) / 2},
		{Width{Num: math.MaxInt, Den: math.MaxInt}, 100, 5, 90},
	} {
		if got := tc.width.Resolve(tc.usable, tc.gaps); got != tc.want {
			t.Errorf("Resolve(%+v, %d, %d) = %d; want %d", tc.width, tc.usable, tc.gaps, got, tc.want)
		}
	}
	w := workspace()
	w.AddWindow(1)
	w.SetUsable(Rect{X: -1, Y: -2, W: -10, H: math.MaxInt})
	w.SetGaps(math.MaxInt)
	for _, p := range w.Layout() {
		if p.Rect.W != 0 || p.Rect.H < 0 {
			t.Fatal(p)
		}
	}
	w.SetOutput(-1, -1)
	if w.Output.W != 0 || w.Output.H != 0 {
		t.Fatal(w.Output)
	}
}
func TestUnchangedUsablePreservesViewX(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddWindow(3)
	before := w.ViewX
	if before == 0 {
		t.Fatal("expected a scrolled workspace")
	}
	w.SetUsable(w.Usable)
	if w.ViewX != before {
		t.Fatalf("ViewX changed from %d to %d", before, w.ViewX)
	}
}

func TestFullscreenPersistsAndScrolls(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.FocusColumn(-1)
	w.ToggleFullscreen()
	if p := w.Layout()[0]; !p.Fullscreen || p.Rect.X != 0 || p.Rect.W != 100 {
		t.Fatal(p)
	}
	w.FocusColumn(1)
	if p := w.Layout()[0]; !p.Fullscreen || p.Rect.X >= 0 {
		t.Fatal(p)
	}
	w.ToggleFullscreen()
	if w.Layout()[0].Fullscreen || !w.Layout()[1].Fullscreen {
		t.Fatal(w.Layout())
	}
	w.RemoveWindow(2)
	if w.Layout()[0].Fullscreen {
		t.Fatal(w.Layout())
	}
}
