package core

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// cascadeOverview is overviewMonitor with workspace 2 (current) a cascade of
// seven columns, 4..10, in three bands of three (the last holds 10 alone),
// the focus on column 8 (band 2, middle). Workspace 1 (columns 1..3) is a
// scroll workspace on its left, workspace 3 (column 30) on its right.
func cascadeOverview() *Monitor {
	m := overviewMonitor()
	m.SetBorder(2)
	m.Focus(1)
	w := m.Current()
	w.Overflow = OverflowCascade
	for id := WindowID(5); id <= 10; id++ {
		w.AddWindow(id)
	}
	m.Focus(2)
	m.AddWindow(30)
	m.Focus(1)
	w.FocusID(8)
	return m
}

// withStash stashes windows 40..40+n-1 of the current workspace, with the
// focus back on column 8.
func withStash(m *Monitor, n int) {
	w := m.Current()
	for id := WindowID(40); id < WindowID(40+n); id++ {
		w.AddWindow(id)
		w.FocusID(id)
		w.ToggleWindowStash()
	}
	w.FocusID(8)
}

// cascadeCore is a Core on one output with workspace 0 holding window 1,
// workspace 1 (current) a cascade of windows 2..6 in bands of two ({2,3}
// {4,5} {6}), workspace 2 window 30, and window 2 focused.
func cascadeCore(t *testing.T) (*Core, *Monitor) {
	t.Helper()
	c, _ := indicatorCore(t)
	m := c.cur().mon
	m.AddWindow(1)
	m.Focus(1)
	w := m.Current()
	w.Overflow = OverflowCascade
	for id := WindowID(2); id <= 6; id++ {
		w.AddWindow(id)
	}
	m.normalize()
	m.Focus(2)
	m.AddWindow(30)
	m.Focus(1)
	w.FocusID(2)
	return c, m
}

func centreY(r Rect) int { return r.Y + r.H/2 }

func within1(a, b int) bool { return a-b <= 1 && b-a <= 1 }

func hiddenOff(p Placement) bool { return p.Hidden && p.Preview == 0 && !p.Focused }

// The zoom does not depend on the number of bands.
func TestOverviewCascadeZoomIgnoresBandCount(t *testing.T) {
	if one, three := cascadeWorkspace(3).overviewZoom(), cascadeWorkspace(7).overviewZoom(); one != three || one != overviewMaxZoom {
		t.Fatalf("zoom with one band %v, with three %v", one, three)
	}
}

// A maximized column fills its band in the preview, inset for the columns
// it hides: they are dimmed cards at their cell size behind it, on their
// side, the nearest one step out, the farther ones two (the band's edge).
// Other bands keep their cells.
func TestOverviewCascadeMaximizedColumnCards(t *testing.T) {
	// Four columns, three per band: band 0 holds 0..2, band 1 holds 3.
	for _, tc := range []struct {
		n, max int
		cards  []int // per column: 0 not a card, -k/+k k steps left/right
		order  []int // paint order of band 0's columns
	}{
		{n: 1, max: 0, cards: []int{0}, order: []int{0}},
		{n: 4, max: 0, cards: []int{0, 1, 2, 0}, order: []int{2, 1, 0}},
		{n: 4, max: 1, cards: []int{-1, 0, 1, 0}, order: []int{0, 2, 1}},
		{n: 4, max: 3, cards: []int{0, 0, 0, 0}},
		// Partial bands: the card has its band's half width.
		{n: 2, max: 0, cards: []int{0, 1}, order: []int{1, 0}},
		{n: 5, max: 4, cards: []int{0, 0, 0, -1, 0}},
	} {
		w := cascadeWorkspace(tc.n)
		w.Focus = tc.max
		w.maximize(tc.max)
		tiles, _, sel := w.previewTiles()
		g, h, s := w.gap(), w.Usable.H-2*w.gap(), w.bandCardStep()
		off := 0
		if slices.ContainsFunc(tc.cards, func(k int) bool { return k != 0 }) {
			off = 2 * s
		}
		var order []int
		for _, p := range tiles {
			i := int(p.ID) - 1
			if i/3 == 0 && tc.order != nil {
				order = append(order, i)
			}
			band := i / 3
			want := Rect{X: off + w.cellX(i), Y: g + band*w.Usable.H, W: w.cellWidth(i), H: h}
			switch k := tc.cards[i]; {
			case i == tc.max:
				// Full size, as on screen: the workspace's ratio.
				want.X, want.W = off+g, w.Usable.W-2*g
			case k < 0:
				want.X = g + (2+k)*s
			case k > 0:
				want.X = w.Usable.W - g + (2+k)*s - want.W
			}
			if p.Hidden || p.Peek != (tc.cards[i] != 0) || p.Focused != (i == tc.max) || p.Rect != want {
				t.Fatalf("n=%d max=%d column %d: %+v, want rect %+v card %v", tc.n, tc.max, i, p, want, tc.cards[i] != 0)
			}
			// The 900-wide band split among its columns, no gaps.
			if inBand := min(tc.n-band*3, 3); i != tc.max && p.Rect.W != 900/inBand {
				t.Fatalf("n=%d max=%d column %d width %d, want %d", tc.n, tc.max, i, p.Rect.W, 900/inBand)
			}
			if i == tc.max && sel != want {
				t.Fatalf("n=%d max=%d sel %+v, want %+v", tc.n, tc.max, sel, want)
			}
		}
		if tc.order != nil && !slices.Equal(order, tc.order) {
			t.Fatalf("n=%d max=%d paint order %v, want %v", tc.n, tc.max, order, tc.order)
		}
	}
}

// Moving onto a card behind a maximized column hands it the maximization;
// Return keeps it there, Escape gives it back. Moving to another band
// unmaximizes as before.
func TestOverviewCascadeMaximizedCardSelection(t *testing.T) {
	for _, d := range cascadeDrivers {
		t.Run(d.name, func(t *testing.T) {
			m := cascadeOverview()
			w := m.Current()
			w.FocusID(9) // band 2 holds 7, 8, 9
			w.ToggleFullWidth()
			m.ToggleOverview()
			for _, id := range []WindowID{7, 8} {
				if p := previewOf(t, m.Layout(), id); p.Hidden || !p.Peek {
					t.Fatalf("card %d: %+v", id, p)
				}
			}
			d.move(m, -1, 0)
			if focusedID(m) != 8 || !w.Columns[w.Focus].FullWidth || w.Columns[w.columnOf(9)].FullWidth {
				t.Fatalf("left: focus %d, maximization not moved", focusedID(m))
			}
			if p := previewOf(t, m.Layout(), 9); !p.Peek {
				t.Fatalf("former maximized column not a card: %+v", p)
			}
			m.CancelOverview()
			if focusedID(m) != 9 || !w.Columns[w.Focus].FullWidth || w.Columns[w.columnOf(8)].FullWidth {
				t.Fatalf("escape: focus %d, maximization not restored", focusedID(m))
			}

			m.ToggleOverview()
			d.move(m, -1, 0)
			d.move(m, -1, 0)
			m.ToggleOverview() // return
			if focusedID(m) != 7 || !w.Columns[w.Focus].FullWidth || w.Columns[w.columnOf(9)].FullWidth {
				t.Fatalf("return: focus %d, maximization not on 7", focusedID(m))
			}

			m.ToggleOverview()
			d.move(m, 0, -1)
			if focusedID(m) != 4 || slices.ContainsFunc(w.Columns, func(c Column) bool { return c.FullWidth }) {
				t.Fatalf("up: focus %d, a column still maximized", focusedID(m))
			}
		})
	}
}

// A click on a window of a card's column records that window in the
// maximize history, not the column's former focus.
func TestOverviewCascadeMaximizedCardPickRecordsWindow(t *testing.T) {
	m := cascadeOverview()
	w := m.Current()
	// Column 8 stacks 8 over 50, 8 focused.
	w.Columns[w.columnOf(8)].Windows = append(w.Columns[w.columnOf(8)].Windows, 50)
	w.FocusID(7)
	w.ToggleFullWidth()
	m.ToggleOverview()
	m.OverviewPick(50)
	if len(w.maximized) == 0 || w.maximized[0] != 50 || focusedID(m) != 50 {
		t.Fatalf("history %v, focus %d; want 50 first", w.maximized, focusedID(m))
	}
}

// Only the focused maximized column is inset for cards: a FullWidth column
// elsewhere fills its band.
func TestOverviewCascadeUnfocusedMaximizedNotInset(t *testing.T) {
	w := cascadeWorkspace(6)
	w.SetGaps(8)
	w.Focus = 4
	w.maximize(4)
	w.Columns[0].FullWidth = true
	tiles, _, _ := w.previewTiles()
	// The row keeps room for the focused column's cards on its left.
	g, off := w.gap(), 2*w.bandCardStep()
	p := previewOf(t, tiles, 1)
	if p.Hidden || p.Rect.X != off+g || p.Rect.W != w.Usable.W-2*g {
		t.Fatalf("unfocused maximized column %+v, want X %d W %d", p, off+g, w.Usable.W-2*g)
	}
}

// A click on a card behind a maximized cascade column hands it the
// maximization.
func TestOverviewCascadeMaximizedCardPick(t *testing.T) {
	m := cascadeOverview()
	w := m.Current()
	w.FocusID(7)
	w.ToggleFullWidth()
	m.ToggleOverview()
	// Each card's strip right of the maximized column hits it: 8 next to
	// it, 9 at the band's edge.
	ps := m.Layout()
	front, near, far := previewOf(t, ps, 7).Rect, previewOf(t, ps, 8).Rect, previewOf(t, ps, 9).Rect
	for _, c := range []struct {
		x0, x1 int
		want   WindowID
	}{{front.X + front.W, near.X + near.W, 8}, {near.X + near.W, far.X + far.W, 9}} {
		x := (c.x0 + c.x1) / 2
		if c.x0 >= c.x1 {
			t.Fatalf("no strip for %d: front %v near %v far %v", c.want, front, near, far)
		}
		if got := overviewIn(ps, float64(x), float64(centreY(far))); got != c.want {
			t.Fatalf("strip at x=%d hits %d, want %d", x, got, c.want)
		}
	}
	m.OverviewPick(9)
	if m.ov.open || focusedID(m) != 9 || !w.Columns[w.Focus].FullWidth || w.Columns[w.columnOf(7)].FullWidth {
		t.Fatalf("pick: open %v, focus %d, maximization not moved", m.ov.open, focusedID(m))
	}
}

// The selected band is the centre lane: undimmed, centred, selected tile
// focused.
func TestOverviewCascadeCentreLane(t *testing.T) {
	m := cascadeOverview()
	m.ToggleOverview()
	ps := m.Layout()
	u := m.Current().overviewArea()
	for _, id := range []WindowID{7, 8, 9} {
		p := previewOf(t, ps, id)
		if p.Hidden || p.Peek || p.Preview != overviewMaxZoom || p.Focused != (id == 8) {
			t.Fatalf("band tile %d: %+v", id, p)
		}
		if !within1(centreY(p.Rect), u.Y+u.H/2) {
			t.Fatalf("band tile %d centre %d, area centre %d", id, centreY(p.Rect), u.Y+u.H/2)
		}
	}
}

// The other bands are dimmed lanes a screen (and a gap) away; those
// outside the area are hidden.
func TestOverviewCascadeOtherLanes(t *testing.T) {
	m := cascadeOverview()
	m.ToggleOverview()
	ps := m.Layout()
	sel := previewOf(t, ps, 8).Rect
	above, below := previewOf(t, ps, 5), previewOf(t, ps, 10)
	if above.Hidden || !above.Peek || above.Focused || below.Hidden || !below.Peek || below.Focused {
		t.Fatalf("adjacent bands: above %+v, below %+v", above, below)
	}
	// The lanes sit evenly around the selected one, apart from it (a gap),
	// and the area still shows part of each.
	up, down := sel.Y-above.Rect.Y, below.Rect.Y-sel.Y
	if !within1(up, down) {
		t.Fatalf("lanes not evenly spaced: %d above, %d below", up, down)
	}
	if gap := up - sel.H; gap <= 0 {
		t.Fatalf("lane above overlaps the selected one: %v over %v", above.Rect, sel)
	}
	u := m.Current().overviewArea()
	if above.Rect.Y+above.Rect.H <= u.Y || below.Rect.Y >= u.Y+u.H {
		t.Fatalf("adjacent lanes not visible in %v: %v, %v", u, above.Rect, below.Rect)
	}

	// Five bands (13 columns): the ones two lanes away are off the area.
	m = cascadeOverview()
	w := m.Current()
	for id := WindowID(11); id <= 16; id++ {
		w.AddWindow(id)
	}
	w.FocusID(8)
	m.ToggleOverview()
	// Columns 4..16: bands {4,5,6} {7,8,9} {10,11,12} {13,14,15} {16}.
	w.FocusID(11)
	ps = m.Layout()
	for _, id := range []WindowID{4, 5, 6, 16} {
		if p := previewOf(t, ps, id); !hiddenOff(p) {
			t.Fatalf("band two lanes away %d not hidden: %+v", id, p)
		}
	}
	for _, id := range []WindowID{7, 13} {
		if p := previewOf(t, ps, id); p.Hidden || !p.Peek {
			t.Fatalf("adjacent band %d: %+v", id, p)
		}
	}
}

// Neighbor workspaces sit left and right of the current slot, dimmed,
// their selected lane level with the centre lane.
func TestOverviewCascadeNeighbors(t *testing.T) {
	m := cascadeOverview()
	m.ToggleOverview()
	rows, _ := m.overviewRows()
	cur, left, right := rows[m.Workspaces[1]], rows[m.Workspaces[0]], rows[m.Workspaces[2]]
	if left.X+left.W > cur.X || right.X < cur.X+cur.W {
		t.Fatalf("slots overlap: %v %v %v", left, cur, right)
	}
	ps := m.Layout()
	centre := centreY(previewOf(t, ps, 8).Rect)
	shown := 0
	for _, n := range []struct {
		id   WindowID
		side int
	}{{1, -1}, {2, -1}, {3, -1}, {30, 1}} {
		id, side := n.id, n.side
		p := previewOf(t, ps, id)
		if p.Hidden {
			continue
		}
		shown++
		if !p.Peek || p.Focused || p.Preview == 0 {
			t.Fatalf("neighbor %d not dimmed: %+v", id, p)
		}
		if side < 0 && p.Rect.X+p.Rect.W > cur.X || side > 0 && p.Rect.X < cur.X+cur.W {
			t.Fatalf("neighbor %d overlaps the current slot %v: %+v", id, cur, p.Rect)
		}
		if !within1(centreY(p.Rect), centre) {
			t.Fatalf("neighbor %d centre %d, centre lane %d", id, centreY(p.Rect), centre)
		}
	}
	if shown < 2 {
		t.Fatalf("only %d neighbor tiles drawn", shown)
	}
	for _, p := range ps {
		if p.Hidden || p.Peek {
			continue
		}
		if p.Rect.X < cur.X || p.Rect.X+p.Rect.W > cur.X+cur.W {
			t.Fatalf("current tile %d outside its slot %v: %+v", p.ID, cur, p.Rect)
		}
	}
}

// Lanes along the overview axis would overlap the other workspaces: a
// cascade neighbor of a scroll workspace shows its selected band only.
func TestOverviewCascadeNeighborBelowScrollShowsOneBand(t *testing.T) {
	m := cascadeOverview()
	m.Focus(0)
	m.ToggleOverview()
	if m.overviewAxis() != verticalAxis {
		t.Fatal("setup: scroll overview is not vertical")
	}
	ps := m.Layout()
	for _, id := range []WindowID{4, 5, 6} {
		if p := previewOf(t, ps, id); !hiddenOff(p) {
			t.Fatalf("band %d of the neighbor drawn: %+v", id, p)
		}
	}
	if p := previewOf(t, ps, 8); p.Hidden {
		// The neighbor's selected column is 8.
		t.Fatalf("selected band hidden: %+v", p)
	}
	for _, id := range []WindowID{7, 9} {
		if p := previewOf(t, ps, id); p.Hidden || !p.Peek {
			t.Fatalf("selected band tile %d: %+v", id, p)
		}
	}
	if p := previewOf(t, ps, 10); !hiddenOff(p) {
		t.Fatalf("band 3 drawn: %+v", p)
	}
}

// The stash pile is inside the current slot, left of the band; the
// previous workspace stays clear of it.
func TestOverviewCascadeStashInSlot(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 2)
	m.ToggleOverview()
	rows, _ := m.overviewRows()
	cur, prev := rows[m.Current()], rows[m.Workspaces[0]]
	ps := m.Layout()
	left := cur.X + cur.W
	for _, id := range []WindowID{7, 8, 9} {
		left = min(left, previewOf(t, ps, id).Rect.X)
	}
	for _, id := range []WindowID{40, 41} {
		p := previewOf(t, ps, id)
		if p.Hidden || p.Preview != overviewCardZoom {
			t.Fatalf("pile card %d: %+v", id, p)
		}
		if p.Rect.X < cur.X || p.Rect.X+p.Rect.W > left {
			t.Fatalf("pile card %d not inside the slot, left of the band (%d..%d): %+v", id, cur.X, left, p.Rect)
		}
		// 41 is the front card of the pile.
		if id == 41 && !within1(centreY(p.Rect), centreY(previewOf(t, ps, 8).Rect)) {
			t.Fatalf("pile not level with the selected lane: %+v", p.Rect)
		}
	}
	// The pile does not shift the band: its row stays centred on the area.
	u := m.Current().overviewArea()
	lo, hi := previewOf(t, ps, 7).Rect, previewOf(t, ps, 9).Rect
	if mid := (lo.X + hi.X + hi.W) / 2; !within1(mid, u.X+u.W/2) {
		t.Fatalf("band centre %d with a stash, area centre %d", mid, u.X+u.W/2)
	}
	for _, id := range []WindowID{1, 2, 3} {
		if p := previewOf(t, ps, id); !p.Hidden && p.Rect.X+p.Rect.W > cur.X {
			t.Fatalf("previous workspace overlaps the slot with the pile: %+v", p.Rect)
		}
	}
	if prev.X+prev.W > cur.X {
		t.Fatalf("slots overlap: %v %v", prev, cur)
	}
}

type cascadeDriver struct {
	name string
	move func(m *Monitor, dx, dy int)
}

func keyDriver(left, right, up, down string) func(m *Monitor, dx, dy int) {
	return func(m *Monitor, dx, dy int) {
		name := map[[2]int]string{{-1, 0}: left, {1, 0}: right, {0, -1}: up, {0, 1}: down}[[2]int{dx, dy}]
		m.overviewKey(ports.KeyEvent{Keysym: name, Pressed: true})
	}
}

var cascadeDrivers = []cascadeDriver{
	{"move", func(m *Monitor, dx, dy int) { m.OverviewMove(dx, dy) }},
	{"hjkl", keyDriver("h", "l", "k", "j")},
	{"arrows", keyDriver("Left", "Right", "Up", "Down")},
}

func focusedID(m *Monitor) WindowID {
	id, _ := m.Focused()
	return id
}

// ↑/↓ change band and stop at the ends; ←/→ walk the band, then leave it
// for the stash or the neighbor workspaces; Escape restores everything.
func TestOverviewCascadeKeys(t *testing.T) {
	for _, d := range cascadeDrivers {
		t.Run(d.name, func(t *testing.T) {
			m := cascadeOverview()
			w, prev, next := m.Current(), m.Workspaces[0], m.Workspaces[2]
			w.FocusID(5)
			m.ToggleOverview()
			sel := func() WindowID { return w.Columns[w.Focus].Windows[0] }
			d.move(m, 0, 1)
			if sel() != 8 {
				t.Fatalf("down: %d, want 8", sel())
			}
			d.move(m, 0, 1)
			d.move(m, 0, 1)
			if m.Current() != w || sel() != 10 {
				t.Fatalf("down at the last band: %d on the current workspace %v", sel(), m.Current() == w)
			}
			d.move(m, 0, -1)
			if sel() != 7 {
				t.Fatalf("up: %d, want 7", sel())
			}
			d.move(m, 0, -1)
			d.move(m, 0, -1)
			if m.Current() != w || sel() != 4 {
				t.Fatalf("up at the first band: %d", sel())
			}
			d.move(m, 1, 0)
			d.move(m, 1, 0)
			if sel() != 6 || m.Current() != w {
				t.Fatalf("right in the band: %d", sel())
			}
			d.move(m, 1, 0)
			if m.Current() != next {
				t.Fatal("right past the last column did not reach the next workspace")
			}
			m.CancelOverview()
			if m.Current() != w || focusedID(m) != 5 {
				t.Fatalf("cancel left %d", focusedID(m))
			}

			m.ToggleOverview()
			d.move(m, -1, 0)
			if sel() != 4 || m.Current() != w {
				t.Fatal("left within the band")
			}
			d.move(m, -1, 0)
			if m.Current() != prev {
				t.Fatal("left from the first column did not reach the previous workspace")
			}
			m.CancelOverview()

			// With a stash: left from the first column enters it, right
			// comes back to the band's first column, left twice goes to
			// the previous workspace.
			withStash(m, 2)
			w.FocusID(7)
			m.ToggleOverview()
			d.move(m, -1, 0)
			if m.cardAt(w) < 0 {
				t.Fatal("left from the first column did not select the stash")
			}
			d.move(m, 1, 0)
			if m.cardAt(w) >= 0 || sel() != 7 {
				t.Fatalf("right from the stash: card %d, column %d", m.cardAt(w), sel())
			}
			d.move(m, -1, 0)
			d.move(m, -1, 0)
			if m.Current() != prev {
				t.Fatal("left from the stash did not reach the previous workspace")
			}
			m.CancelOverview()
			if m.Current() != w || focusedID(m) != 7 {
				t.Fatalf("cancel left %d", focusedID(m))
			}
		})
	}
}

// T7b: with a stash card selected, up/down and the three-finger vertical
// swipe cycle the pile as in a vertical overview; confirming shows the picked window.
func TestOverviewCascadeStashPile(t *testing.T) {
	for _, d := range cascadeDrivers {
		t.Run(d.name, func(t *testing.T) {
			m := cascadeOverview()
			withStash(m, 3)
			w := m.Current()
			w.FocusID(7)
			m.ToggleOverview()
			d.move(m, -1, 0)
			if m.cardAt(w) != w.stashAt {
				t.Fatalf("entered at %d", m.cardAt(w))
			}
			at := m.cardAt(w)
			d.move(m, 0, -1)
			if m.Current() != w || m.cardAt(w) != at-1 {
				t.Fatalf("up: card %d, was %d", m.cardAt(w), at)
			}
			d.move(m, 0, 1)
			d.move(m, 0, 1)
			d.move(m, 0, 1)
			if m.Current() != w || m.cardAt(w) != len(w.Stash)-1 {
				t.Fatalf("down: card %d", m.cardAt(w))
			}
			d.move(m, 0, -1)
			picked := w.Stash[m.cardAt(w)].ID
			m.ToggleOverview()
			if focusedID(m) != picked || w.stashHidden {
				t.Fatalf("confirm on %d, want %d (hidden %v)", focusedID(m), picked, w.stashHidden)
			}
		})
	}

	c, _, sc, _ := stashRig(t, true)
	m := sc.mon
	w := m.Current()
	w.Overflow = OverflowCascade
	m.Focus(1)
	m.AddWindow(50)
	m.Focus(0)
	m.ToggleOverview()
	if m.cardAt(w) < 0 {
		t.Fatal("setup: no card selected")
	}
	at, before := m.cardAt(w), stashIDs(w)
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	for range 3 {
		c.swipeUpdate(ports.SwipeUpdate{DY: -40})
	}
	c.swipeEnd(ports.SwipeEnd{})
	if m.Current() != w || m.cardAt(w) != at-1 {
		t.Fatalf("swipe up: card %d, was %d", m.cardAt(w), at)
	}
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	for range 3 {
		c.swipeUpdate(ports.SwipeUpdate{DX: 40})
	}
	c.swipeEnd(ports.SwipeEnd{})
	// A horizontal three-finger swipe is a workspace step, even over a
	// stash card.
	if m.Current() != m.Workspaces[1] {
		t.Fatalf("horizontal swipe over a stash card: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
	if got := stashIDs(w); !slices.Equal(got, before) {
		t.Fatal("stash reordered")
	}
}

// T7c: three fingers follow the physical axes, once per gesture.
func TestOverviewCascadeThreeFinger(t *testing.T) {
	c, m := cascadeCore(t)
	w := m.Current()
	m.ToggleOverview()
	swipe := func(dx, dy float64) {
		t.Helper()
		c.swipeBegin(ports.SwipeBegin{Fingers: 3})
		for range 4 {
			c.swipeUpdate(ports.SwipeUpdate{DX: dx, DY: dy})
		}
		c.swipeEnd(ports.SwipeEnd{})
	}
	swipe(0, 40)
	if m.Current() != w || w.band(w.Focus) != 1 {
		t.Fatalf("one swipe down: band %d", w.band(w.Focus))
	}
	swipe(0, -40)
	if w.band(w.Focus) != 0 {
		t.Fatalf("one swipe up: band %d", w.band(w.Focus))
	}
	swipe(40, 0)
	if m.Current() != m.Workspaces[2] {
		t.Fatalf("one swipe right: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
	// Onto a scroll workspace and back: the vertical swipe is the scroll
	// overview's workspace step; neither bounces during the gesture.
	swipe(0, -40)
	if m.Current() != w {
		t.Fatalf("swipe up from the scroll workspace: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
	swipe(-40, 0)
	if m.Current() != m.Workspaces[0] {
		t.Fatalf("one swipe left: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
}

// Three fingers sideways walk the cards behind a maximized column one per
// gesture, like the arrows, then change workspace past the band's edge.
func TestOverviewCascadeThreeFingerMaximizedCards(t *testing.T) {
	c, m := cascadeCore(t)
	w := m.Current()
	w.ToggleFullWidth() // Column 2 hides 3, the other column of its band.
	m.ToggleOverview()
	swipe := func(dx float64) {
		t.Helper()
		c.swipeBegin(ports.SwipeBegin{Fingers: 3})
		for range 4 {
			c.swipeUpdate(ports.SwipeUpdate{DX: dx})
		}
		c.swipeEnd(ports.SwipeEnd{})
	}
	swipe(40)
	if m.Current() != w || m.overviewTarget() != 3 || !w.Columns[w.Focus].FullWidth {
		t.Fatalf("swipe right: workspace %d, target %d, want maximized 3", indexOf(m.Workspaces, m.Current()), m.overviewTarget())
	}
	swipe(-40)
	if m.Current() != w || m.overviewTarget() != 2 || !w.Columns[w.Focus].FullWidth {
		t.Fatalf("swipe left: workspace %d, target %d, want maximized 2", indexOf(m.Workspaces, m.Current()), m.overviewTarget())
	}
	swipe(40)
	swipe(40)
	if m.Current() != m.Workspaces[2] {
		t.Fatalf("swipe right past the band: workspace %d", indexOf(m.Workspaces, m.Current()))
	}

	// A covering float in front: the swipe changes workspace, as without
	// a maximized column.
	m.CancelOverview()
	w.AddFloating(90, 300, 200)
	m.ToggleOverview()
	if f := m.stackFront(w); f.kind != stackFloat || !w.Columns[w.Focus].FullWidth {
		t.Fatalf("setup: front %+v", f)
	}
	swipe(-40)
	if m.Current() != m.Workspaces[0] {
		t.Fatalf("swipe left over a float: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
}

// Left from the first column of a band whose maximized column hides it:
// the arrows and three fingers walk the cards, then enter the stash, or
// change workspace without one.
func TestOverviewCascadeMaximizedLeftEdge(t *testing.T) {
	for _, stash := range []bool{false, true} {
		c, m := cascadeCore(t)
		w := m.Current()
		if stash {
			w.AddWindow(40)
			w.FocusID(40)
			w.ToggleWindowStash()
		}
		w.FocusID(3) // Band {2, 3}: 3 hides 2.
		w.ToggleFullWidth()
		m.ToggleOverview()
		swipe := func() {
			c.swipeBegin(ports.SwipeBegin{Fingers: 3})
			for range 4 {
				c.swipeUpdate(ports.SwipeUpdate{DX: -40})
			}
			c.swipeEnd(ports.SwipeEnd{})
		}
		swipe()
		if m.Current() != w || m.overviewTarget() != 2 || !w.Columns[w.Focus].FullWidth {
			t.Fatalf("stash %v: first swipe: target %d", stash, m.overviewTarget())
		}
		swipe()
		if stash {
			if m.Current() != w || m.cardAt(w) < 0 {
				t.Fatalf("past the band: workspace %d, card %d; want the stash", indexOf(m.Workspaces, m.Current()), m.cardAt(w))
			}
		} else if m.Current() != m.Workspaces[0] {
			t.Fatalf("past the band: workspace %d, want 0", indexOf(m.Workspaces, m.Current()))
		}
	}
}

// With four or more columns in a band, the cards three or more columns
// away sit under the two-step card: arrows still reach every one, in
// order, each taking the maximization.
func TestOverviewCascadeFarCardsByArrows(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(1200, 600)
	m.SetMaxColumns(5)
	w := m.Current()
	w.Overflow = OverflowCascade
	for id := WindowID(1); id <= 5; id++ {
		m.AddWindow(id)
	}
	w.FocusID(1)
	w.ToggleFullWidth()
	m.ToggleOverview()
	ps := m.Layout()
	edge := previewOf(t, ps, 3).Rect
	for _, id := range []WindowID{4, 5} {
		if p := previewOf(t, ps, id); p.Hidden || !p.Peek || p.Rect.X != edge.X {
			t.Fatalf("far card %d: %+v, want a card under 3 at x %d", id, p, edge.X)
		}
	}
	for _, want := range []WindowID{2, 3, 4, 5} {
		m.OverviewMove(1, 0)
		if m.Current() != w || m.overviewTarget() != want || !w.Columns[w.Focus].FullWidth {
			t.Fatalf("right: target %d, want maximized %d", m.overviewTarget(), want)
		}
	}
	m.CancelOverview()
	if focusedID(m) != 1 || !w.Columns[w.Focus].FullWidth {
		t.Fatalf("escape: focus %d, want maximized 1", focusedID(m))
	}
}

// Right from a stash card lands on the maximized column, which keeps its
// maximization, not on the first card of its band.
func TestOverviewCascadeStashToMaximized(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 1)
	w := m.Current()
	w.FocusID(9)
	w.ToggleFullWidth()
	m.ToggleOverview()
	m.selectCard(w, w.stashAt) // As a click on the pile does.
	if m.cardAt(w) < 0 {
		t.Fatalf("setup: no stash card, target %d", m.overviewTarget())
	}
	m.OverviewMove(1, 0)
	if m.overviewTarget() != 9 || !w.Columns[w.Focus].FullWidth || len(w.maximized) == 0 || w.maximized[0] != 9 {
		t.Fatalf("target %d, maximized %v; want 9", m.overviewTarget(), w.maximized)
	}
}

// A named boundary of a cascade overview is a vertical rule strictly
// between the two slots.
func TestOverviewCascadeNamedDivider(t *testing.T) {
	m := namedOverviewMonitor()
	m.Workspaces[1].Overflow = OverflowCascade
	m.ToggleOverview()
	rows, dividers := m.overviewRows()
	if len(dividers) != 1 {
		t.Fatalf("dividers: %+v", dividers)
	}
	d, cur, dev := dividers[0].Rect, rows[m.Workspaces[1]], rows[m.byName("dev")]
	if d.W != 1 || d.H <= 1 || dividers[0].Active {
		t.Fatalf("divider is not an inactive vertical rule: %+v", dividers[0])
	}
	if d.X < cur.X+cur.W || d.X+d.W > dev.X {
		t.Fatalf("divider %v not between %v and %v", d, cur, dev)
	}
}

// Covering floats fan along the horizontal axis: left goes deeper in the
// stack (the cards behind show on the left), right comes back to the front.
func TestOverviewCascadeCoveringFloat(t *testing.T) {
	for _, d := range cascadeDrivers {
		t.Run(d.name, func(t *testing.T) {
			m := cascadeOverview()
			m.Current().AddFloating(90, 300, 200)
			w := m.Current()
			w.FocusID(10)
			m.ToggleOverview()
			if m.stackFront(w).kind != stackColumns {
				t.Fatal("setup: columns not in front")
			}
			d.move(m, 0, -1) // columns: up is a band
			d.move(m, -1, 0)
			if f := m.stackFront(w); f.kind != stackFloat || f.id != 90 {
				t.Fatalf("left from the first column: front %+v", f)
			}
			if p := previewOf(t, m.Layout(), 90); p.Peek || !p.Focused {
				t.Fatalf("float not in front: %+v", p)
			}
			d.move(m, 0, 1) // a float front takes no band move
			if f := m.stackFront(w); f.kind != stackFloat {
				t.Fatalf("down left the float: %+v", f)
			}
			d.move(m, 1, 0)
			if m.stackFront(w).kind != stackColumns || m.Current() != w {
				t.Fatalf("right from the float: front %+v", m.stackFront(w))
			}

			// The float in front of the columns: they fan to its left.
			m.CancelOverview()
			w.FocusID(90)
			m.ToggleOverview()
			if f := m.stackFront(w); f.kind != stackFloat {
				t.Fatalf("setup: front %+v", f)
			}
			behind := previewOf(t, m.Layout(), 10)
			if !behind.Peek {
				t.Fatalf("columns not behind the float: %+v", behind)
			}
			d.move(m, -1, 0)
			if m.stackFront(w).kind != stackColumns || m.Current() != w {
				t.Fatalf("left from the front float: front %+v", m.stackFront(w))
			}
			// The cards behind fan to the left: coming to the front, the
			// columns move right by one peek step.
			front := previewOf(t, m.Layout(), 10)
			if front.Peek || front.Rect.X-behind.Rect.X != w.peekStep() {
				t.Fatalf("columns behind %v, in front %v, peek step %d", behind.Rect, front.Rect, w.peekStep())
			}
			d.move(m, 1, 0)
			if f := m.stackFront(w); f.kind != stackFloat || f.id != 90 {
				t.Fatalf("right from the columns behind the float: front %+v", f)
			}
		})
	}
}

// T10b: a pinned fullscreen column keeps its Fullscreen tile in the band.
func TestOverviewCascadePinnedFullscreenTile(t *testing.T) {
	m := cascadeOverview()
	w := m.Current()
	w.FocusID(8)
	m.ToggleFullscreen()
	if !w.pinned() {
		t.Fatal("setup: not pinned")
	}
	m.ToggleOverview()
	ps := m.Layout()
	fs := previewOf(t, ps, 8)
	if !fs.Fullscreen || fs.Hidden {
		t.Fatalf("fullscreen tile: %+v", fs)
	}
	for _, id := range []WindowID{7, 9} {
		if p := previewOf(t, ps, id); p.Hidden || p.Fullscreen || p.Peek != (fs.Peek) {
			t.Fatalf("band tile %d: %+v", id, p)
		}
	}
	u := w.overviewArea()
	if !within1(centreY(fs.Rect), u.Y+u.H/2) {
		t.Fatalf("fullscreen tile not on the centre lane: %+v", fs.Rect)
	}
}

// The focus binds keep their meaning in a cascade overview.
func TestOverviewCascadeFocusBinds(t *testing.T) {
	m := cascadeOverview()
	w := m.Current()
	m.ToggleOverview()
	if !m.overviewFocus(ActionFocusWindowDown) || w.band(w.Focus) != 2 || m.Current() != w {
		t.Fatalf("focus-window-down: band %d", w.band(w.Focus))
	}
	if !m.overviewFocus(ActionFocusWindowUp) || w.band(w.Focus) != 1 {
		t.Fatalf("focus-window-up: band %d", w.band(w.Focus))
	}
	m.overviewFocus(ActionFocusWorkspaceNext)
	if m.Current() != m.Workspaces[2] {
		t.Fatal("focus-workspace-next did not change workspace")
	}
	m.overviewFocus(ActionFocusWorkspacePrev)
	if m.Current() != w {
		t.Fatal("focus-workspace-prev did not come back")
	}
	m.overviewFocus(ActionFocusWorkspacePrev)
	if m.Current() != m.Workspaces[0] {
		t.Fatal("focus-workspace-prev did not change workspace")
	}
	m.CancelOverview()

	// A selected stash card keeps the pile cycle.
	withStash(m, 3)
	w.FocusID(7)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	at := m.cardAt(w)
	m.overviewFocus(ActionFocusWorkspacePrev)
	if m.Current() != w || m.cardAt(w) != at-1 {
		t.Fatalf("focus-workspace-prev over a card: card %d, was %d", m.cardAt(w), at)
	}
	m.overviewFocus(ActionFocusWorkspaceNext)
	if m.Current() != w || m.cardAt(w) != at {
		t.Fatalf("focus-workspace-next over a card: card %d", m.cardAt(w))
	}
}

// A three-finger step that moves nothing reports no change: no scene
// transition, no keyboard grab. Here the first band of a cascade has no
// band above it.
func TestOverviewCascadeFingerNoopReportsNoChange(t *testing.T) {
	m := cascadeOverview()
	m.Current().FocusID(5)
	m.ToggleOverview()
	up := ports.PointerAxis{Source: ports.AxisFinger, Vertical: ports.ScrollAxis{Set: true, Value: -overviewScrollStep}}
	if m.overviewScroll(up) {
		t.Fatal("a step past the first band reported a change")
	}
}

func TestOverviewMixedLayoutGestureAndCancel(t *testing.T) {
	m := overviewMonitor()
	m.SetOverflow(OverflowFixed)
	m.Current().Overflow = OverflowCascade
	from := m.Current()
	m.ToggleOverview()
	rows, _ := m.overviewRows()
	if rows[m.Workspaces[1]].X <= rows[from].X {
		t.Fatal("cascade neighbor not horizontal")
	}
	m.overviewScroll(ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Value: overviewScrollStep}})
	if m.Current() == from {
		t.Fatal("horizontal gesture did not change workspace")
	}
	if m.Current().Overflow != OverflowFixed {
		t.Fatal("destination lost fixed policy")
	}
	if m.ov.scrollX != 0 || m.ov.scrollY != 0 || !m.ov.scrolled {
		t.Fatalf("scroll state after the axis change: %+v", m.ov)
	}
	m.CancelOverview()
	if m.Current() != from {
		t.Fatal("cancel lost source workspace")
	}
}

// Showing a workspace with another overview axis drops the scroll leftovers
// of the old one.
func TestOverviewAxisChangeResetsScroll(t *testing.T) {
	m := cascadeOverview()
	m.ToggleOverview()
	m.ov.scrollX, m.ov.scrollY, m.ov.sideways, m.ov.scrolled = 20, 20, true, true
	m.showOverview(m.Workspaces[2])
	if m.ov.scrollX != 0 || m.ov.scrollY != 0 || m.ov.sideways || !m.ov.scrolled {
		t.Fatalf("scroll kept or latch dropped: %+v", m.ov)
	}
	m.ov.scrollX = 20
	m.showOverview(m.Workspaces[0])
	if m.ov.scrollX != 20 {
		t.Fatal("scroll reset between workspaces of the same axis")
	}
}

// A reload that changes the current workspace's axes drops the scroll state;
// one that does not, keeps it.
func TestOverviewReloadResetsScrollOnAxisChange(t *testing.T) {
	c, _ := indicatorCore(t)
	m := c.cur().mon
	m.AddWindow(1)
	m.ToggleOverview()
	w := m.Current()
	set := func() { m.ov.scrollX, m.ov.scrollY, m.ov.scrolled, m.ov.sideways = 20, 10, true, true }
	set()
	cfg := c.cfg
	cfg.Layout.Gaps++
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	if m.ov.scrollX != 20 || !m.ov.scrolled {
		t.Fatalf("unrelated reload reset the scroll: %+v", m.ov)
	}
	cfg.Layout.Overflow = string(OverflowCascade)
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	if m.Current().policy().workspace != horizontalAxis {
		t.Fatal("setup: not cascade")
	}
	// The reload keeps the overview open on its selection, and cancelling
	// still gives the original focus back.
	if id, _ := w.Focused(); !m.ov.open || id != 1 {
		t.Fatalf("reload lost the overview selection: open %v, focus %d", m.ov.open, id)
	}
	// The scroll is dropped, but not the latch that waits for the fingers
	// to lift.
	if m.ov.scrollX != 0 || m.ov.scrollY != 0 || !m.ov.scrolled || m.ov.sideways {
		t.Fatalf("axis change kept the scroll or dropped the latch: %+v", m.ov)
	}
	set()
	cfg.Layout.Overflow = string(OverflowFixed)
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	if m.ov.scrollX != 0 || !m.ov.scrolled {
		t.Fatalf("workspace axis change kept the scroll or dropped the latch: %+v", m.ov)
	}
	m.CancelOverview()
	if id, _ := w.Focused(); id != 1 {
		t.Fatalf("cancel left the focus on %d", id)
	}
}

// The finger latch outlives an axis change: a gesture that turned the
// overview, then was turned back by a key, does not step again.
func TestOverviewFingerLatchSurvivesAxisChange(t *testing.T) {
	c, m := cascadeCore(t)
	w := m.Current()
	m.ToggleOverview()
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	c.swipeUpdate(ports.SwipeUpdate{DX: 70})
	if m.Current() != m.Workspaces[2] || m.overviewAxis() != verticalAxis {
		t.Fatalf("setup: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
	// Back to the cascade workspace by a key, with the fingers still down.
	m.overviewKey(ports.KeyEvent{Keysym: "k", Pressed: true})
	if m.Current() != w || m.overviewAxis() != horizontalAxis {
		t.Fatal("setup: the key did not turn the overview back")
	}
	for _, u := range []ports.SwipeUpdate{{DY: 70}, {DX: 70}, {DY: -70}, {DX: -70}} {
		c.swipeUpdate(u)
	}
	c.swipeEnd(ports.SwipeEnd{})
	if m.Current() != w || w.Focus != 0 {
		t.Fatalf("the gesture stepped again: workspace %d, column %d", indexOf(m.Workspaces, m.Current()), w.Focus)
	}
	// The next gesture steps again.
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	c.swipeUpdate(ports.SwipeUpdate{DY: 70})
	c.swipeEnd(ports.SwipeEnd{})
	if w.band(w.Focus) != 1 {
		t.Fatalf("the next gesture did not step: band %d", w.band(w.Focus))
	}
}

// A stash-only cascade workspace has no columns to select: right from its
// pile goes straight to the next workspace.
func TestOverviewCascadeStashOnlyRight(t *testing.T) {
	m := overviewMonitor()
	m.Focus(1)
	w := m.Current()
	w.Overflow = OverflowCascade
	w.ToggleWindowStash()
	m.Focus(2)
	m.AddWindow(30)
	m.Focus(1)
	m.ToggleOverview()
	if len(w.Columns) != 0 || m.cardAt(w) < 0 {
		t.Fatalf("setup: %d columns, card %d", len(w.Columns), m.cardAt(w))
	}
	m.OverviewMove(1, 0)
	if m.Current() != m.Workspaces[2] {
		t.Fatalf("right from the pile: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
	m.OverviewMove(0, -1) // the next workspace is a scroll one: up goes back
	if m.Current() != w || m.cardAt(w) < 0 {
		t.Fatal("setup: back on the stash-only workspace without its card")
	}
	m.OverviewMove(-1, 0)
	if m.Current() != m.Workspaces[0] {
		t.Fatalf("left from the pile: workspace %d", indexOf(m.Workspaces, m.Current()))
	}
}

// A cascade neighbor beside a cascade workspace draws its other lanes
// dimmed, its selected lane level with the centre lane.
func TestOverviewCascadeNeighborLanes(t *testing.T) {
	m := cascadeOverview()
	prev := m.Workspaces[0]
	prev.Overflow = OverflowCascade
	for id := WindowID(60); id <= 65; id++ {
		prev.AddWindow(id) // bands {1,2,3} {60,61,62} {63,64,65}, 65 selected
	}
	m.ToggleOverview()
	ps := m.Layout()
	centre := centreY(previewOf(t, ps, 8).Rect)
	sel := previewOf(t, ps, 65)
	if sel.Hidden || !sel.Peek || sel.Focused || !within1(centreY(sel.Rect), centre) {
		t.Fatalf("selected lane of the neighbor: %+v, centre lane %d", sel, centre)
	}
	above := previewOf(t, ps, 62)
	if above.Hidden || !above.Peek || above.Rect.Y+above.Rect.H > sel.Rect.Y {
		t.Fatalf("lane above in the neighbor: %+v, selected %v", above, sel.Rect)
	}
	if p := previewOf(t, ps, 3); !hiddenOff(p) {
		t.Fatalf("lane two away in the neighbor: %+v", p)
	}
}

// A scroll neighbor wider than its slot scrolls inside it: what leaves the
// slot is hidden, so it never sits on the current slot.
func TestOverviewCascadeWideScrollNeighborIsClipped(t *testing.T) {
	m := cascadeOverview()
	prev := m.Workspaces[0]
	for id := WindowID(60); id <= 67; id++ {
		prev.AddWindow(id)
	}
	m.ToggleOverview()
	rows, _ := m.overviewRows()
	cur, slot := rows[m.Current()], rows[prev]
	hidden, shown := 0, 0
	for _, p := range m.Layout() {
		if !prev.has(p.ID) {
			continue
		}
		if p.Hidden {
			hidden++
			continue
		}
		shown++
		if p.Rect.X < slot.X || p.Rect.X+p.Rect.W > slot.X+slot.W || p.Rect.X+p.Rect.W > cur.X {
			t.Fatalf("tile %d leaves its slot %v or reaches the current slot %v: %v", p.ID, slot, cur, p.Rect)
		}
	}
	if hidden == 0 || shown == 0 {
		t.Fatalf("setup: %d tiles shown, %d hidden", shown, hidden)
	}
}

// A wheel and a continuous scroll in a cascade overview: vertical changes
// workspace (and does not bounce back on a workspace with another axis),
// horizontal moves along the band.
func TestOverviewCascadeScroll(t *testing.T) {
	vertical := ports.ScrollAxis{Set: true, Value: overviewScrollStep, V120: 120}
	for _, source := range []ports.AxisSource{ports.AxisWheel, ports.AxisContinuous} {
		t.Run(fmt.Sprint(source), func(t *testing.T) {
			m := cascadeOverview()
			w := m.Current()
			m.ToggleOverview()
			m.overviewScroll(ports.PointerAxis{Source: source, Vertical: vertical})
			if m.Current() != m.Workspaces[2] || w.band(w.Focus) != 1 || m.ov.scrollX != 0 || m.ov.scrollY != 0 {
				t.Fatalf("vertical: workspace %d, band %d, scroll %v,%v", indexOf(m.Workspaces, m.Current()), w.band(w.Focus), m.ov.scrollX, m.ov.scrollY)
			}

			m = cascadeOverview()
			w = m.Current()
			w.FocusID(7)
			m.ToggleOverview()
			m.overviewScroll(ports.PointerAxis{Source: source, Horizontal: vertical})
			if m.Current() != w || w.Columns[w.Focus].Windows[0] != 8 {
				t.Fatalf("horizontal: column %d", w.Columns[w.Focus].Windows[0])
			}

			// Onto a workspace with another axis: one step, no bounce.
			m = overviewMonitor()
			m.SetOverflow(OverflowFixed)
			m.Workspaces[1].Overflow = OverflowCascade
			m.ToggleOverview()
			m.overviewScroll(ports.PointerAxis{Source: source, Vertical: vertical})
			if m.Current() != m.Workspaces[1] || m.ov.scrollX != 0 || m.ov.scrollY != 0 {
				t.Fatalf("mixed: workspace %d, scroll %v,%v", indexOf(m.Workspaces, m.Current()), m.ov.scrollX, m.ov.scrollY)
			}
		})
	}
}
