package core

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

// tiles lays out rects edge to edge (no gap) with focus on index f.
func tiles(f int, rs ...Rect) []Placement {
	ps := make([]Placement, len(rs))
	for i, r := range rs {
		ps[i] = Placement{ID: WindowID(i + 1), Rect: r, Focused: i == f}
	}
	setNeighbors(ps, 0, Rect{W: 1000, H: 1000})
	return ps
}

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

func TestSeparators(t *testing.T) {
	type px struct {
		x, y int
		want string
	}
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
			seps := separators(tc.ps, 2, tc.lit)
			for _, c := range tc.check {
				if got := colorAt(seps, c.x, c.y); got != c.want {
					t.Errorf("(%d,%d) = %q, want %q; %v", c.x, c.y, got, c.want, seps)
				}
			}
		})
	}
}

func TestSeparatorsSkip(t *testing.T) {
	if s := separators(tiles(0, Rect{W: 32, H: 16}), 2, true); len(s) != 0 {
		t.Fatal("lone window:", s)
	}
	if s := separators(tiles(0, Rect{W: 16, H: 16}, Rect{X: 16, W: 16, H: 16}), 0, true); len(s) != 0 {
		t.Fatal("width 0:", s)
	}
	full := tiles(0, Rect{W: 16, H: 16}, Rect{X: 16, W: 16, H: 16})
	full[0].Fullscreen, full[1].Hidden = true, true
	if s := separators(full, 2, true); len(s) != 0 {
		t.Fatal("fullscreen:", s)
	}
}

func TestSeparatorsFloat(t *testing.T) {
	float := Placement{ID: 1, Rect: Rect{X: 10, Y: 10, W: 20, H: 20}, Floating: true, Focused: true, Inset: ports.SideAll}
	seps := separators([]Placement{float}, 2, true)
	for _, c := range [][2]int{{10, 10}, {29, 29}, {20, 11}} {
		if got := colorAt(seps, c[0], c[1]); got != "lit" {
			t.Errorf("(%d,%d) = %q", c[0], c[1], got)
		}
	}
	if got := colorAt(seps, 20, 20); got != "" {
		t.Error("inside:", got)
	}
}
