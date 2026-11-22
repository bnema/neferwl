package workspace

import (
	"reflect"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
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
			var hidden []WindowID
			for _, p := range tiles {
				if p.Hidden {
					hidden = append(hidden, p.ID)
					continue
				}
				got = append(got, p.ID)
			}
			slices.Sort(got)
			slices.Sort(hidden)
			if !slices.Equal(hidden, []WindowID{1, 3}) {
				t.Fatalf("hidden tiles %v", hidden)
			}
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

// With gaps, domain.TabBarGap (not the tile gap) separates the bar from the
// visible tab.
func TestTabbedPlacementWithGaps(t *testing.T) {
	w := tabbedWorkspace(OverflowScroll)
	w.SetGaps(8)
	col := w.columnRects()[0]
	want := Rect{X: col.X, Y: col.Y + 20 + domain.TabBarGap, W: col.W, H: col.H - 20 - domain.TabBarGap}
	if p := tabPlacement(t, w.Layout(), 2); p.Rect != want {
		t.Fatalf("visible tab %+v, want %+v", p.Rect, want)
	}
}

func TestDropTab(t *testing.T) {
	center := func(id WindowID) Anchor { return win(id, domain.DropSideCenter, domain.DropTab) }

	// A lone window: a 2-tab column, the dropped one visible and focused.
	w := cols(OverflowScroll, 4, 0, []WindowID{1}, []WindowID{2})
	Drop(w, w, 2, center(1), 0, 0)
	if c := w.Columns; len(c) != 1 || !slices.Equal(c[0].Windows, []WindowID{1, 2}) || !c[0].Tabbed || c[0].Selected() != 2 {
		t.Fatalf("columns %+v", c)
	}
	if id, _ := w.Focused(); id != 2 {
		t.Fatalf("focused %d", id)
	}

	// A 2-stack: the whole column becomes tabbed, the dropped one after W.
	w = cols(OverflowScroll, 4, 0, []WindowID{1, 2}, []WindowID{3})
	w.Columns[0].Shares = []int{3000, 7000}
	other := cols(OverflowScroll, 4, 0, []WindowID{9})
	Drop(other, w, 9, center(1), 0, 0)
	if c := w.Columns[0]; !slices.Equal(c.Windows, []WindowID{1, 9, 2}) || !c.Tabbed || c.Selected() != 9 || c.Shares != nil {
		t.Fatalf("column %+v", c)
	}

	// A window already in W's column, or W itself: no zone.
	w = cols(OverflowScroll, 4, 0, []WindowID{1, 2})
	if w.DropValid(w, 2, center(1)) || w.DropValid(w, 1, center(1)) {
		t.Fatal("tab within its own column is valid")
	}
}

// Top and bottom have no zone on a tabbed column; the sides and the centre
// stay.
func TestDropTabbedColumnHasNoStackZones(t *testing.T) {
	w := cols(OverflowScroll, 4, 0, []WindowID{1, 2}, []WindowID{3})
	w.Apply(ActionToggleColumnTabbed)
	p := domain.DefaultDropPolicy()
	src := cols(OverflowScroll, 4, 0, []WindowID{9})
	on := w.sidesOn(w.dropCtx(src, 9), 2, p)
	for _, s := range []domain.DropSide{domain.DropSideTop, domain.DropSideBottom} {
		if bandOn(on, s) {
			t.Fatalf("side %v on a tabbed column", s)
		}
	}
	for _, s := range []domain.DropSide{domain.DropSideCenter, domain.DropSideLeft, domain.DropSideRight} {
		if !bandOn(on, s) {
			t.Fatalf("side %v missing", s)
		}
	}
}

// drop.window.center = swap restores the swap.
func TestDropCenterSwapPolicy(t *testing.T) {
	w := cols(OverflowScroll, 4, 0, []WindowID{1}, []WindowID{2}, []WindowID{3})
	var p domain.DropPolicy
	p.SetWindow(domain.DropSideCenter, domain.DropSwap)
	p = p.Over(domain.DefaultDropPolicy())
	Drop(w, w, 1, win(3, domain.DropSideCenter, p.Window[domain.DropSideCenter]), 0, 0)
	if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{3}, {2}, {1}}) || w.Columns[2].Tabbed {
		t.Fatalf("columns %v", got)
	}
}

// A 2-tab column that lost a tab to the stash or to floating is tabbed
// again when the window comes back next to its sibling.
func TestTabbedRoundTripRestoresTabs(t *testing.T) {
	for _, a := range []Action{ActionToggleWindowStash, ActionToggleFloating} {
		t.Run(string(a), func(t *testing.T) {
			w := cols(OverflowScroll, 2, 0, []WindowID{1, 2}, []WindowID{3})
			w.TabBar = 20
			w.Columns[0].Focus = 1
			w.Apply(ActionToggleColumnTabbed)
			w.Apply(a)
			if c := w.Columns[0]; c.Tabbed || !slices.Equal(c.Windows, []WindowID{1}) {
				t.Fatalf("after %s: windows %v tabbed %v", a, c.Windows, c.Tabbed)
			}
			w.FocusID(2)
			w.Apply(a)
			c := w.Columns[0]
			if !slices.Equal(c.Windows, []WindowID{1, 2}) || !c.Tabbed || c.Selected() != 2 {
				t.Fatalf("back: windows %v tabbed %v selected %d", c.Windows, c.Tabbed, c.Selected())
			}
		})
		t.Run(string(a)+"/user untabbed", func(t *testing.T) {
			w := cols(OverflowScroll, 2, 0, []WindowID{1, 2, 3})
			w.TabBar = 20
			w.Apply(ActionToggleColumnTabbed)
			w.Apply(a) // 3 leaves; {1 2} stays tabbed
			w.FocusID(1)
			w.Apply(ActionToggleColumnTabbed)
			if w.Columns[0].Tabbed {
				t.Fatal("still tabbed")
			}
			w.Columns[0].Shares = []int{30, 70}
			w.FocusID(3)
			w.Apply(a)
			if c := w.Columns[0]; c.Tabbed || !slices.Equal(c.Windows, []WindowID{1, 2, 3}) || c.Shares == nil {
				t.Fatalf("windows %v tabbed %v shares %v", c.Windows, c.Tabbed, c.Shares)
			}
		})
		t.Run(string(a)+"/sibling moved to a stack", func(t *testing.T) {
			w := cols(OverflowScroll, 2, 0, []WindowID{1, 2}, []WindowID{4, 5})
			w.TabBar = 20
			w.Columns[0].Focus = 1
			w.Apply(ActionToggleColumnTabbed)
			w.Apply(a) // 2 leaves
			w.Columns = []Column{{Windows: []WindowID{4, 5, 1}, Focus: 2, Shares: []int{20, 30, 50}}}
			w.FocusID(2)
			w.Apply(a)
			if c := w.Columns[0]; c.Tabbed || len(c.Windows) != 4 || c.Shares == nil {
				t.Fatalf("windows %v tabbed %v shares %v", c.Windows, c.Tabbed, c.Shares)
			}
		})
	}
}
