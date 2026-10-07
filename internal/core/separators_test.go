package core

import (
	"reflect"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// tiles lays out rects edge to edge (no gap) with focus on index f.
func tiles(f int, rs ...Rect) []Placement {
	ps := make([]Placement, len(rs))
	for i, r := range rs {
		ps[i] = Placement{ID: WindowID(i + 1), Rect: r, Focused: i == f}
	}
	setVisibleNeighbors(ps, 0, bigOutput)
	return ps
}

// bigOutput is an output wider than every test layout.
var bigOutput = Rect{W: 1000, H: 1000}

// colorAt is what the renderer shows at (x, y): the last separator over
// it wins. "" is no line.
func colorAt(seps []ports.Separator, x, y int) string {
	got := ""
	for _, s := range seps {
		r := s.Rect
		if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
			got = "gray"
			if s.Active {
				got = "lit"
			}
		}
	}
	return got
}

type px struct {
	x, y int
	want string
}

func TestSeparators(t *testing.T) {
	cols2 := []Rect{{X: 0, Y: 0, W: 16, H: 16}, {X: 16, Y: 0, W: 16, H: 16}}
	cols3 := []Rect{{X: 0, Y: 0, W: 16, H: 16}, {X: 16, Y: 0, W: 16, H: 16}, {X: 32, Y: 0, W: 16, H: 16}}
	// A full-height column and a column split in two.
	grid := []Rect{{X: 0, Y: 0, W: 16, H: 32}, {X: 16, Y: 0, W: 16, H: 16}, {X: 16, Y: 16, W: 16, H: 16}}
	for _, tc := range []struct {
		name  string
		ps    []Placement
		lit   bool
		check []px
	}{
		// The left window owns the line (x 14-15); the client of the right
		// window is not covered.
		{"two columns, left focused: top half", tiles(0, cols2...), true, []px{{14, 3, "lit"}, {15, 7, "lit"}, {14, 8, "gray"}, {14, 15, "gray"}, {16, 3, ""}, {13, 3, ""}}},
		{"two columns, right focused: bottom half", tiles(1, cols2...), true, []px{{14, 3, "gray"}, {14, 12, "lit"}}},
		{"unfocused output: all gray", tiles(0, cols2...), false, []px{{14, 3, "gray"}, {14, 12, "gray"}}},
		{"three columns, middle: both lines whole", tiles(1, cols3...), true, []px{{14, 3, "lit"}, {14, 12, "lit"}, {30, 3, "lit"}, {30, 12, "lit"}}},
		{"three columns, first: its line only", tiles(0, cols3...), true, []px{{14, 3, "lit"}, {14, 12, "lit"}, {30, 3, "gray"}, {30, 12, "gray"}}},
		{"no line at the screen edge", tiles(2, cols3...), true, []px{{47, 3, ""}, {0, 3, ""}}},
		// The left and top lines meet: the corner square is lit.
		{"bottom-right tile: corner closed", tiles(2, grid...), true, []px{{14, 30, "lit"}, {14, 20, "lit"}, {14, 14, "lit"}, {14, 15, "lit"}, {14, 5, "gray"}, {20, 14, "lit"}, {31, 15, "lit"}}},
		{"top-right tile", tiles(1, grid...), true, []px{{14, 3, "lit"}, {14, 15, "lit"}, {14, 20, "gray"}, {20, 14, "lit"}}},
		{"left column: whole line, split column untouched", tiles(0, grid...), true, []px{{14, 3, "lit"}, {14, 30, "lit"}, {20, 14, "gray"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seps := separators(tc.ps, 2, 0, bigOutput, tc.lit)
			for _, c := range tc.check {
				if got := colorAt(seps, c.x, c.y); got != c.want {
					t.Errorf("(%d,%d) = %q, want %q; %v", c.x, c.y, got, c.want, seps)
				}
			}
		})
	}
}

func TestSeparatorsSkip(t *testing.T) {
	if s := separators(tiles(0, Rect{W: 32, H: 16}), 2, 0, bigOutput, true); len(s) != 0 {
		t.Fatal("lone window:", s)
	}
	if s := separators(tiles(0, Rect{W: 16, H: 16}, Rect{X: 16, W: 16, H: 16}), 0, 0, bigOutput, true); len(s) != 0 {
		t.Fatal("width 0:", s)
	}
	full := tiles(0, Rect{W: 16, H: 16}, Rect{X: 16, W: 16, H: 16})
	full[0].Fullscreen, full[1].Hidden = true, true
	if s := separators(full, 2, 0, bigOutput, true); len(s) != 0 {
		t.Fatal("fullscreen:", s)
	}
}

func TestSeparatorsFloat(t *testing.T) {
	float := Placement{ID: 1, Rect: Rect{X: 10, Y: 10, W: 20, H: 20}, Floating: true, Focused: true, Inset: ports.SideAll}
	seps := separators([]Placement{float}, 2, 0, bigOutput, true)
	if seps[0].Window != 1 {
		t.Fatal("float border not tied to its window:", seps)
	}
	for _, c := range [][2]int{{10, 10}, {29, 29}, {20, 11}} {
		if got := colorAt(seps, c[0], c[1]); got != "lit" {
			t.Errorf("(%d,%d) = %q", c[0], c[1], got)
		}
	}
	if got := colorAt(seps, 20, 20); got != "" {
		t.Error("inside:", got)
	}
}

// Through the real layout: scrolling removes borders to off-output
// neighbors and gives the client back the reserved space.
func TestSeparatorsScroll(t *testing.T) {
	m := monitor() // 100x80, 2 columns on screen
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	insets := func() map[WindowID]ports.Sides {
		got := map[WindowID]ports.Sides{}
		for _, p := range w.Layout() {
			got[p.ID] = p.Inset
		}
		return got
	}
	before := insets()
	if before[1] != 0 || before[3]&ports.SideRight != 0 {
		t.Fatalf("scrolled tiles reserve off-output lines: %v", before)
	}
	if w.View == 0 {
		t.Fatal("expected a scrolled view")
	}
	m.Apply(ActionFocusColumnLeft)
	m.Apply(ActionFocusColumnLeft)
	if after := insets(); reflect.DeepEqual(before, after) || after[1]&ports.SideRight == 0 || after[3] != 0 {
		t.Fatalf("expected visible borders to change with the view: %v then %v", before, after)
	}
	out := Rect{W: 100, H: 80}
	seps := separators(w.Layout(), 2, 0, out, true)
	if got := colorAt(seps, 99, 40); got != "" {
		t.Fatalf("line at the output edge: %v", seps)
	}
	// Two columns on screen out of three: the half rule applies.
	if got := colorAt(seps, 48, 60); got != "gray" {
		t.Fatalf("(48,60) = %q; %v", got, seps)
	}
	if got := colorAt(seps, 48, 10); got != "lit" {
		t.Fatalf("(48,10) = %q; %v", got, seps)
	}
}

// A scroll column scrolled under a side panel is no neighbor: the column
// next to it in the usable area gets no line on that side.
// With a gap, the edge the two share lies inside the usable area: only
// the tile under the panel being out of it keeps the line away.
func TestSeparatorsScrollUnderSidePanel(t *testing.T) {
	for _, gap := range []int{0, 8} {
		ps := []Placement{
			{ID: 1, Rect: Rect{X: 0, W: 100, H: 600}},
			{ID: 2, Rect: Rect{X: 100 + gap, W: 400, H: 600}, Focused: true},
			{ID: 3, Rect: Rect{X: 500 + 2*gap, W: 380, H: 600}},
		}
		setVisibleNeighbors(ps, gap, Rect{X: 100, W: 900, H: 600})
		if ps[0].Neighbors != 0 {
			t.Fatalf("gap %d: tile under the panel: neighbors %04b, want none", gap, ps[0].Neighbors)
		}
		if ps[1].Neighbors != ports.SideRight {
			t.Fatalf("gap %d: tile at the panel: neighbors %04b, want right only", gap, ps[1].Neighbors)
		}
	}
}

// A cascade band above a bottom panel: the next band starts under the
// panel, so the focused tile gets no line along the panel, only the one it
// shares with its band neighbor.
func TestSeparatorsCascadeBottomPanel(t *testing.T) {
	w := &Workspace{Overflow: OverflowCascade, MaxColumns: 2}
	w.SetOutput(900, 600)
	w.SetUsable(Rect{W: 900, H: 580})
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	w.FocusID(1)
	ps := w.Layout()
	if p := previewOf(t, ps, 1); p.Neighbors != ports.SideRight {
		t.Fatalf("neighbors %04b, want right only", p.Neighbors)
	}
	seps := separators(ps, 2, 0, w.Output, true)
	if got := colorAt(seps, 200, 578); got != "" {
		t.Fatalf("line along the panel: %v", seps)
	}
	// Two tiles on screen: each lights its half of the shared line.
	if got := colorAt(seps, 449, 100); got != "lit" {
		t.Fatalf("shared line (449,100) = %q; %v", got, seps)
	}
	if got := colorAt(seps, 449, 400); got != "gray" {
		t.Fatalf("shared line (449,400) = %q; %v", got, seps)
	}
}

// With gaps each tile has its own line: the focused one lights it whole.
func TestSeparatorsGaps(t *testing.T) {
	ps := []Placement{{ID: 1, Rect: Rect{X: 0, W: 16, H: 16}, Focused: true}, {ID: 2, Rect: Rect{X: 20, W: 16, H: 16}}}
	setVisibleNeighbors(ps, 4, bigOutput)
	seps := separators(ps, 2, 4, bigOutput, true)
	for _, c := range []px{{14, 2, "lit"}, {14, 13, "lit"}, {20, 8, "gray"}, {17, 8, ""}} {
		if got := colorAt(seps, c.x, c.y); got != c.want {
			t.Errorf("(%d,%d) = %q, want %q", c.x, c.y, got, c.want)
		}
	}
}
