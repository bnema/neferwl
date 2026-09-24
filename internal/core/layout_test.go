package core

import (
	"math"
	"reflect"
	"testing"
)

func workspace() *Workspace {
	w := &Workspace{}
	w.SetOutput(100, 81)
	w.SetGaps(5)
	w.SetMaxColumns(2)
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
	// Three auto columns, two visible: each (100-3*5)/2 = 42 wide.
	if w.Focus != 2 || w.ViewX != 46 || w.columnWidth(0) != 42 {
		t.Fatalf("scroll: %+v", w)
	}
	w.FocusColumn(-1)
	if w.ViewX != 46 {
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
	w.Columns = []Column{{Windows: []WindowID{1, 2, 3}, Focus: 1}}
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
	if p[0].Rect != (Rect{X: 5, Y: 5, W: 90, H: 33}) || p[1].Rect != (Rect{X: 5, Y: 43, W: 90, H: 33}) || !p[0].Borderless {
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
	w.SetUsable(Rect{X: 10, Y: 10, W: 60, H: 50})
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
	w.AddWindow(2)
	w.Apply(ActionCycleColumnWidth)
	if w.Columns[1].Width.Pixels != 30 {
		t.Fatal(w.Columns)
	}
	w.CycleWidth()
	if w.Columns[1].Width.Pixels != 40 {
		t.Fatal(w.Columns)
	}
	// After the last preset, back to the automatic share.
	w.CycleWidth()
	if w.Columns[1].Width != (Width{}) {
		t.Fatal(w.Columns)
	}
	w.RemoveWindow(2)
	for _, tc := range []struct {
		a    Action
		want Effect
	}{{ActionSpawnTerminal, Effect{Spawn: true}}, {ActionCloseWindow, Effect{Close: 1}}, {ActionQuit, Effect{Quit: true}}, {ActionFocusColumnLeft, Effect{}}, {ActionFocusColumnRight, Effect{}}, {ActionFocusWindowUp, Effect{}}, {ActionFocusWindowDown, Effect{}}, {ActionMoveColumnLeft, Effect{}}, {ActionMoveColumnRight, Effect{}}, {ActionToggleFullscreen, Effect{}}, {ActionCycleColumnWidth, Effect{}}, {"spawn fuzzel --prompt x", Effect{Spawn: true, Argv: []string{"fuzzel", "--prompt", "x"}}}, {"spawn   ", Effect{}}} {
		if got := w.Apply(tc.a); !reflect.DeepEqual(got, tc.want) {
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
				if len(w.Columns) > 0 {
					w.Columns[w.Focus].Width = Width{Num: math.MaxInt, Den: math.MaxInt}
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

func TestSetFullscreenStackDeactivation(t *testing.T) {
	w := workspace()
	w.Columns = []Column{{Windows: []WindowID{1, 2}, Focus: 0}}
	// A client request does not take focus; window 1 keeps it but is covered.
	w.SetFullscreen(2, true)
	p := w.Layout()
	if !p[0].Hidden || !p[1].Fullscreen || p[1].Focused || !p[0].Focused {
		t.Fatal(p)
	}
	w.SetFullscreen(2, false)
	if w.Layout()[0].Hidden {
		t.Fatal(w.Layout())
	}
}

// Fixed overflow keeps every window on screen: past max-columns, new columns
// split the newest one, alternating top/bottom and left/right.
func TestFixedOverflowSpiral(t *testing.T) {
	w := &Workspace{MaxColumns: 2, Overflow: OverflowFixed}
	w.SetOutput(100, 80)
	for id := WindowID(1); id <= 5; id++ {
		w.AddWindow(id)
	}
	got := map[WindowID]Rect{}
	for _, p := range w.Layout() {
		got[p.ID] = p.Rect
	}
	want := map[WindowID]Rect{
		1: {X: 0, Y: 0, W: 50, H: 80},
		2: {X: 50, Y: 0, W: 50, H: 40},  // top half of slot 2
		3: {X: 50, Y: 40, W: 25, H: 40}, // left of the bottom half
		4: {X: 75, Y: 40, W: 25, H: 20},
		5: {X: 75, Y: 60, W: 25, H: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if w.ViewX != 0 {
		t.Fatal("fixed overflow scrolled", w.ViewX)
	}
	// Up to max-columns it is the plain equal split.
	w2 := &Workspace{MaxColumns: 2, Overflow: OverflowFixed}
	w2.SetOutput(100, 80)
	w2.AddWindow(1)
	w2.AddWindow(2)
	if p := w2.Layout(); p[0].Rect.W != 50 || p[1].Rect != (Rect{X: 50, Y: 0, W: 50, H: 80}) {
		t.Fatal(p)
	}
}

func fixedWorkspace(maxCols, gaps, n int) *Workspace {
	w := &Workspace{MaxColumns: maxCols, Overflow: OverflowFixed, Gaps: gaps}
	w.SetOutput(100, 80)
	for id := 1; id <= n; id++ {
		w.AddWindow(WindowID(id))
	}
	return w
}

func placements(w *Workspace) map[WindowID]Placement {
	got := map[WindowID]Placement{}
	for _, p := range w.Layout() {
		got[p.ID] = p
	}
	return got
}

func TestFixedOverflowEdgeCases(t *testing.T) {
	t.Run("new windows go last whatever the focus", func(t *testing.T) {
		w := fixedWorkspace(2, 0, 3)
		w.FocusID(1)
		w.AddWindow(4)
		if got := w.Columns[len(w.Columns)-1].Windows[0]; got != 4 {
			t.Fatal(got)
		}
	})
	for _, col := range []WindowID{1, 4} { // a plain column and a spiral slot
		t.Run("fullscreen covers the output", func(t *testing.T) {
			w := fixedWorkspace(2, 4, 4)
			w.FocusID(col)
			w.ToggleFullscreen()
			for id, p := range placements(w) {
				if id == col && (p.Rect != Rect{W: 100, H: 80} || !p.Fullscreen) {
					t.Fatal(p)
				}
				if id != col && !p.Hidden {
					t.Fatalf("window %d visible over fullscreen: %+v", id, p)
				}
			}
		})
	}
	t.Run("presets ignored", func(t *testing.T) {
		w := fixedWorkspace(2, 0, 2)
		w.SetPresets([]Width{{Num: 2, Den: 3}})
		w.CycleWidth()
		w.FocusColumn(-1)
		w.CycleWidth()
		for id, p := range placements(w) {
			if p.Rect.X+p.Rect.W > 100 || p.Rect.W != 50 {
				t.Fatal(id, p)
			}
		}
	})
	t.Run("gaps between spiral halves", func(t *testing.T) {
		got := placements(fixedWorkspace(1, 4, 2))
		if got[1].Rect != (Rect{X: 4, Y: 4, W: 92, H: 34}) || got[2].Rect != (Rect{X: 4, Y: 42, W: 92, H: 34}) {
			t.Fatal(got[1].Rect, got[2].Rect)
		}
		if got[1].Borderless || got[2].Borderless {
			t.Fatal("spiral windows must keep borders to show focus")
		}
	})
}
