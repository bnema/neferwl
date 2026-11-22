package workspace

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
)

func TestToggleTabbedOnAndOff(t *testing.T) {
	w := cols(OverflowScroll, 2, 0, []WindowID{1, 2})
	w.Columns[0].Shares = []int{3000, 7000}
	w.Apply(ActionToggleColumnTabbed)
	if c := w.Columns[0]; !c.Tabbed || c.Shares != nil {
		t.Fatalf("on: tabbed %v shares %v", c.Tabbed, c.Shares)
	}
	w.Apply(ActionToggleColumnTabbed)
	if c := w.Columns[0]; c.Tabbed || c.Shares != nil {
		t.Fatalf("off: tabbed %v shares %v", c.Tabbed, c.Shares)
	}
}

func TestToggleTabbedNoOp(t *testing.T) {
	w := cols(OverflowScroll, 2, 0, []WindowID{1})
	w.Apply(ActionToggleColumnTabbed)
	if w.Columns[0].Tabbed {
		t.Fatal("one-window column became tabbed")
	}

	w = cols(OverflowScroll, 2, 0, []WindowID{1, 2})
	w.AddFloating(3, 50, 50)
	if !w.OnFloat() {
		t.Fatal("float not focused")
	}
	w.Apply(ActionToggleColumnTabbed)
	if w.Columns[0].Tabbed {
		t.Fatal("toggle on a float changed the column")
	}
}

func TestRemoveWindowClearsTabbed(t *testing.T) {
	w := cols(OverflowScroll, 2, 0, []WindowID{1, 2, 3})
	w.Apply(ActionToggleColumnTabbed)
	w.RemoveWindow(3)
	if !w.Columns[0].Tabbed {
		t.Fatal("two windows left: still tabbed")
	}
	w.RemoveWindow(2)
	if w.Columns[0].Tabbed {
		t.Fatal("one window left: still tabbed")
	}
}

func TestTabbedMoveWindowReorders(t *testing.T) {
	w := cols(OverflowScroll, 2, 0, []WindowID{1, 2, 3})
	w.Columns[0].Focus = 0
	w.Apply(ActionToggleColumnTabbed)
	w.Apply(ActionMoveWindowDown)
	c := w.Columns[0]
	if !slices.Equal(c.Windows, []WindowID{2, 1, 3}) || c.Selected() != 1 || !c.Tabbed {
		t.Fatalf("windows %v selected %d tabbed %v", c.Windows, c.Selected(), c.Tabbed)
	}
}

func TestTabbedResizeRowNoOp(t *testing.T) {
	w := cols(OverflowScroll, 2, 0, []WindowID{1, 2})
	w.Apply(ActionToggleColumnTabbed)
	w.ResizeRow(10)
	if w.Columns[0].Shares != nil {
		t.Fatalf("shares %v", w.Columns[0].Shares)
	}
}

// tabbedWorkspace is one 3-window tabbed column, the second tab visible,
// next to a plain column.
func tabbedWorkspace(o Overflow) *Workspace {
	w := cols(o, 2, 0, []WindowID{1, 2, 3}, []WindowID{4})
	w.TabBar = 20
	w.Columns[0].Focus = 1
	w.Apply(ActionToggleColumnTabbed)
	return w
}

func tabPlacement(t *testing.T, ps []Placement, id WindowID) Placement {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no placement for %d", id)
	return Placement{}
}

func TestTabbedPlacements(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowCascade, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			w := tabbedWorkspace(o)
			col := w.columnRects()[0]
			ps := w.Layout()
			want := Rect{X: col.X, Y: col.Y + 20, W: col.W, H: col.H - 20}
			if p := tabPlacement(t, ps, 2); p.Hidden || p.Rect != want || !p.Focused {
				t.Fatalf("visible tab %+v, want rect %+v", p, want)
			}
			for _, id := range []WindowID{1, 3} {
				if p := tabPlacement(t, ps, id); !p.Hidden || p.Rect != (Rect{}) {
					t.Fatalf("hidden tab %d: %+v", id, p)
				}
			}
			if p := tabPlacement(t, ps, 4); p.Hidden {
				t.Fatalf("plain column hidden: %+v", p)
			}
		})
	}
}

func TestTabbedFullscreenTab(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			plain := cols(o, 2, 0, []WindowID{1, 2, 3}, []WindowID{4})
			plain.Columns[0].Focus = 1
			plain.ToggleFullscreen()
			want := tabPlacement(t, plain.Layout(), 2)

			w := tabbedWorkspace(o)
			w.ToggleFullscreen()
			if !w.Columns[0].Tabbed {
				t.Fatal("fullscreen untabbed the column")
			}
			if p := tabPlacement(t, w.Layout(), 2); !p.Fullscreen || p.Rect != want.Rect {
				t.Fatalf("fullscreen tab %+v, want %+v", p, want.Rect)
			}
		})
	}
}

// Cascade gives a fullscreen window a band of its own: the expel leaves the
// column with one window, no longer tabbed.
func TestTabbedCascadeFullscreenExpelNormalizes(t *testing.T) {
	w := cols(OverflowCascade, 2, 0, []WindowID{1, 2})
	w.Apply(ActionToggleColumnTabbed)
	w.ToggleFullscreen()
	for _, c := range w.Columns {
		if len(c.Windows) < 2 && c.Tabbed {
			t.Fatalf("lone tabbed column %+v", c)
		}
	}
	if len(w.Columns) != 2 {
		t.Fatalf("columns %v", ids(w))
	}
}

func TestTabbedOverviewShowsActiveTab(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowCascade, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			w := tabbedWorkspace(o)
			tiles, _, _ := w.Engine().PreviewTiles(w.Snapshot(), nil, nil)
			var got []WindowID
			for _, p := range tiles {
				got = append(got, p.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, []WindowID{2, 4}) {
				t.Fatalf("overview tiles %v", got)
			}
		})
	}
}

func TestTabbedNoRowBorder(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowCascade, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			plain := cols(o, 2, 0, []WindowID{1, 2}, []WindowID{3})
			rows := layout.RowRects(plain.columnRects()[0], plain.Columns[0], plain.Gap())
			x, y := rows[0].X+rows[0].W/2, rows[0].Y+rows[0].H
			if b, ok := plain.BorderAt(x, y, 4); !ok || b.Kind != BorderRow {
				t.Fatalf("plain: no row border at %d,%d", x, y)
			}
			plain.Apply(ActionToggleColumnTabbed)
			if b, ok := plain.BorderAt(x, y, 4); ok && b.Kind == BorderRow {
				t.Fatalf("tabbed: row border %+v", b)
			}
			if plain.MoveBorder(Border{Kind: BorderRow, Col: 0, Row: 0}, y+10) {
				t.Fatal("tabbed: row border moved")
			}
		})
	}
}

func TestTabbedAppendLayoutAllocs(t *testing.T) {
	w := tabbedWorkspace(OverflowScroll)
	dst := w.AppendLayout(nil)
	if n := testing.AllocsPerRun(100, func() { dst = w.AppendLayout(dst[:0]) }); n != 0 {
		t.Fatalf("AppendLayout allocates %v per run", n)
	}
}

// With gaps, a tile gap separates the bar from the visible tab.
func TestTabbedPlacementWithGaps(t *testing.T) {
	w := tabbedWorkspace(OverflowScroll)
	w.SetGaps(8)
	col := w.columnRects()[0]
	want := Rect{X: col.X, Y: col.Y + 20 + 8, W: col.W, H: col.H - 28}
	if p := tabPlacement(t, w.Layout(), 2); p.Rect != want {
		t.Fatalf("visible tab %+v, want %+v", p.Rect, want)
	}
}
