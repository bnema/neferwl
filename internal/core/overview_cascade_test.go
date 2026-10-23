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
	if one, three := cascadeWorkspace(3).overviewZoom(horizontalAxis), cascadeWorkspace(7).overviewZoom(horizontalAxis); one != three || one != overviewMaxZoom {
		t.Fatalf("zoom with one band %v, with three %v", one, three)
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
	w.FocusID(10) // New windows join the focused band, the last one.
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
	cur, left, right := rows.at(m.Workspaces[1]), rows.at(m.Workspaces[0]), rows.at(m.Workspaces[2])
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

// The stash pile is centred on the current row, straddling the bottom edge
// of the selected lane, over the tiles; it takes no room in the slot.
func TestOverviewCascadeStashInSlot(t *testing.T) {
	m := cascadeOverview()
	m.ToggleOverview()
	bare, _ := m.overviewRows()
	m.CancelOverview()
	withStash(m, 2)
	m.ToggleOverview()
	rows, _ := m.overviewRows()
	w := m.Current()
	cur, prev := rows.at(w), rows.at(m.Workspaces[0])
	if cur != bare.at(w) {
		t.Fatalf("the pile changed the slot: %v, without it %v", cur, bare.at(w))
	}
	ps := m.Layout()
	lane := previewOf(t, ps, 8).Rect
	// 41 is the front card of the pile, 40 behind it.
	front := previewOf(t, ps, 41)
	if front.Hidden || front.Peek || front.Preview != overviewCardZoom {
		t.Fatalf("front card: %+v", front)
	}
	if back := previewOf(t, ps, 40); back.Hidden || !back.Peek {
		t.Fatalf("card behind: %+v", back)
	}
	if !within1(front.Rect.X+front.Rect.W/2, cur.X+cur.W/2) {
		t.Fatalf("pile not centred on the row %v: %+v", cur, front.Rect)
	}
	// The pile sits under the lane, every card of it: the lane and the
	// pile are centred together in the slot.
	for _, id := range []WindowID{40, 41} {
		if c := previewOf(t, ps, id).Rect; c.Y <= lane.Y+lane.H {
			t.Fatalf("card %d %v not under the lane %v", id, c, lane)
		}
	}
	if top, bottom := lane.Y-cur.Y, cur.Y+cur.H-(front.Rect.Y+front.Rect.H); !within1(top, bottom) && abs(top-bottom) > lane.H/10 {
		t.Fatalf("lane and pile not centred together: %d above, %d below", top, bottom)
	}
	// The pile does not shift the band: its row stays centred on the area.
	u := m.Current().overviewArea()
	lo, hi := previewOf(t, ps, 7).Rect, previewOf(t, ps, 9).Rect
	if mid := (lo.X + hi.X + hi.W) / 2; !within1(mid, u.X+u.W/2) {
		t.Fatalf("band centre %d with a stash, area centre %d", mid, u.X+u.W/2)
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

			// With a stash: left from the first column still goes to the
			// previous workspace. Down past the last band enters the
			// stash, up comes back to the band, and left past the first
			// card goes to the previous workspace: two cards do not
			// wrap round.
			withStash(m, 2)
			w.FocusID(7)
			m.ToggleOverview()
			d.move(m, -1, 0)
			if m.Current() != prev {
				t.Fatal("left from the first column with a stash did not reach the previous workspace")
			}
			m.CancelOverview()
			m.ToggleOverview()
			d.move(m, 0, 1)
			if m.cardAt(w) >= 0 || sel() != 10 {
				t.Fatalf("down to the last band: card %d, column %d", m.cardAt(w), sel())
			}
			d.move(m, 0, 1)
			if m.cardAt(w) < 0 {
				t.Fatal("down past the last band did not select the stash")
			}
			d.move(m, 0, -1)
			if m.cardAt(w) >= 0 || sel() != 10 {
				t.Fatalf("up from the stash: card %d, column %d", m.cardAt(w), sel())
			}
			d.move(m, 0, 1)
			for range len(w.Stash) {
				d.move(m, -1, 0)
			}
			if m.Current() != prev {
				t.Fatal("left past the first card did not reach the previous workspace")
			}
			m.CancelOverview()
			if m.Current() != w || focusedID(m) != 7 {
				t.Fatalf("cancel left %d", focusedID(m))
			}
		})
	}
}

// T7b: with a stash card selected in a cascade overview, down walks the pile
// (stopping at its last card) and up leaves it for the last band, at the
// column it was entered from; confirming shows the picked window.
func TestOverviewCascadeStashPile(t *testing.T) {
	for _, d := range cascadeDrivers {
		t.Run(d.name, func(t *testing.T) {
			m := cascadeOverview()
			withStash(m, 3)
			w := m.Current()
			w.FocusID(10)
			m.ToggleOverview()
			d.move(m, 0, 1)
			if m.cardAt(w) != w.stashAt {
				t.Fatalf("entered at %d", m.cardAt(w))
			}
			at := m.cardAt(w)
			for i := range len(w.Stash) {
				d.move(m, 0, 1)
				if want := min(at+i+1, len(w.Stash)-1); m.Current() != w || m.cardAt(w) != want {
					t.Fatalf("down %d: card %d, want %d", i, m.cardAt(w), want)
				}
			}
			picked := w.Stash[m.cardAt(w)].ID
			m.ToggleOverview()
			if focusedID(m) != picked || w.stashHidden {
				t.Fatalf("confirm on %d, want %d (hidden %v)", focusedID(m), picked, w.stashHidden)
			}
		})
	}

	// Up from any card, whichever it was cycled to, leaves the pile at once.
	for _, d := range cascadeDrivers {
		t.Run(d.name+"/up", func(t *testing.T) {
			m := cascadeOverview()
			withStash(m, 3)
			w := m.Current()
			w.FocusID(10)
			m.ToggleOverview()
			d.move(m, 0, 1)
			d.move(m, 0, 1)
			if m.cardAt(w) < 0 {
				t.Fatal("setup: no card selected")
			}
			d.move(m, 0, -1)
			if m.Current() != w || m.cardAt(w) >= 0 || focusedID(m) != 10 || m.ov.selected != 10 {
				t.Fatalf("up: card %d, focus %d, selected %d", m.cardAt(w), focusedID(m), m.ov.selected)
			}
			// The pile is entered again from there.
			d.move(m, 0, 1)
			if m.cardAt(w) < 0 {
				t.Fatal("down after leaving did not enter the stash")
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
	swipeY := func(dy float64) {
		c.swipeBegin(ports.SwipeBegin{Fingers: 3})
		for range 3 {
			c.swipeUpdate(ports.SwipeUpdate{DY: dy})
		}
		c.swipeEnd(ports.SwipeEnd{})
	}
	// Fingers down walk the pile; fingers up leave it for the columns.
	swipeY(40)
	if want := min(at+1, len(w.Stash)-1); m.Current() != w || m.cardAt(w) != want {
		t.Fatalf("swipe down: card %d, want %d", m.cardAt(w), want)
	}
	swipeY(-40)
	if m.Current() != w || m.cardAt(w) >= 0 || m.ov.selected == 0 {
		t.Fatalf("swipe up: card %d, selected %d", m.cardAt(w), m.ov.selected)
	}
	swipeY(40)
	if m.cardAt(w) < 0 {
		t.Fatal("swipe down after leaving did not enter the stash")
	}
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	for range 3 {
		c.swipeUpdate(ports.SwipeUpdate{DX: 40})
	}
	c.swipeEnd(ports.SwipeEnd{})
	// A horizontal three-finger swipe walks the carousel, wrapping round.
	if want := (at + 1) % len(w.Stash); m.Current() != w || m.cardAt(w) != want {
		t.Fatalf("horizontal swipe over a stash card: workspace %d, card %d, want %d", indexOf(m.Workspaces, m.Current()), m.cardAt(w), want)
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

// Three fingers down past the last band enter the stash below it.
func TestOverviewCascadeThreeFingerDownEntersStash(t *testing.T) {
	c, m := cascadeCore(t)
	withStash(m, 2)
	w := m.Current()
	w.FocusID(6)
	m.ToggleOverview()
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	for range 4 {
		c.swipeUpdate(ports.SwipeUpdate{DY: 40})
	}
	c.swipeEnd(ports.SwipeEnd{})
	if m.Current() != w || m.cardAt(w) != w.stashAt {
		t.Fatalf("swipe down from the last band: card %d, want %d", m.cardAt(w), w.stashAt)
	}
}

// Up from a stash card lands on the maximized column of the last band,
// which keeps its maximization: it is alone in its band.
func TestOverviewCascadeStashToMaximized(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 1)
	w := m.Current()
	w.FocusID(10)
	w.ToggleFullWidth()
	m.ToggleOverview()
	m.selectCard(w, w.stashAt) // As a click on the pile does.
	if m.cardAt(w) < 0 {
		t.Fatalf("setup: no stash card, target %d", m.overviewTarget())
	}
	m.OverviewMove(0, -1)
	if m.overviewTarget() != 10 || !w.Columns[w.Focus].FullWidth || len(w.maximized) == 0 || w.maximized[0] != 10 {
		t.Fatalf("target %d, maximized %v; want 10", m.overviewTarget(), w.maximized)
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
	d, cur, dev := dividers[0].Rect, rows.at(m.Workspaces[1]), rows.at(m.byName("dev"))
	if d.W != 1 || d.H <= 1 || dividers[0].Active {
		t.Fatalf("divider is not an inactive vertical rule: %+v", dividers[0])
	}
	if d.X < cur.X+cur.W || d.X+d.W > dev.X {
		t.Fatalf("divider %v not between %v and %v", d, cur, dev)
	}
}

// A card behind the front one of the folded pile is clicked where it
// shows, above the front card.
func TestOverviewCascadeStashPeekClick(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 2)
	m.ToggleOverview()
	ps := m.Layout()
	front, back := previewOf(t, ps, 41).Rect, previewOf(t, ps, 40).Rect
	// The strip of 40 above the front card.
	x, y := float64(back.X+back.W-1), float64(back.Y)
	if back.Y >= front.Y {
		t.Fatalf("setup: back %v front %v", back, front)
	}
	if id := m.overviewHit(ps, x, y); id != 40 {
		t.Fatalf("clicked %d, want 40", id)
	}
	if id := m.overviewHit(ps, float64(front.X+front.W/2), float64(front.Y+front.H/2)); id != 41 {
		t.Fatalf("front card clicked %d, want 41", id)
	}
}

// The pile stays inside the area, down to a tiny output.
func TestOverviewCascadeStashInsideArea(t *testing.T) {
	for _, size := range [][2]int{{300, 200}, {1920, 1080}, {1080, 1920}, {3440, 1440}, {60, 40}} {
		m := cascadeOverview()
		m.SetOutput(size[0], size[1])
		withStash(m, 4)
		m.ToggleOverview()
		u := m.Current().overviewArea()
		for _, id := range []WindowID{40, 41, 42, 43} {
			p := previewOf(t, m.Layout(), id)
			if !p.Hidden && (p.Rect.Y < u.Y || p.Rect.Y+p.Rect.H > u.Y+u.H || p.Rect.X < u.X || p.Rect.X+p.Rect.W > u.X+u.W) {
				t.Fatalf("%v: card %d %v outside %v", size, id, p.Rect, u)
			}
		}
	}
}

// A stash takes no room in a cascade overview, on a neighbor either: a
// wide scroll workspace beside it keeps its zoom and slot.
func TestOverviewCascadeNeighborStashKeepsSlot(t *testing.T) {
	m := cascadeOverview()
	prev := m.Workspaces[0]
	m.Focus(0)
	for id := WindowID(60); id < 64; id++ {
		m.AddWindow(id)
	}
	m.Focus(1)
	m.ToggleOverview()
	bare, _ := m.overviewRows()
	zoom := prev.overviewZoom(horizontalAxis)
	m.CancelOverview()
	if zoom <= overviewMinZoom || zoom >= overviewMaxZoom {
		t.Fatalf("setup: neighbor zoom %v is at a bound", zoom)
	}
	// The same columns, with one more window stashed.
	m.Focus(0)
	m.AddWindow(64)
	prev.FocusID(64)
	prev.ToggleWindowStash()
	m.Focus(1)
	m.ToggleOverview()
	rows, _ := m.overviewRows()
	if rows.at(prev) != bare.at(prev) || prev.overviewZoom(horizontalAxis) != zoom {
		t.Fatalf("neighbor slot %v zoom %v with a stash, %v %v without", rows.at(prev), prev.overviewZoom(horizontalAxis), bare.at(prev), zoom)
	}
	if p := previewOf(t, m.Layout(), 64); p.Hidden || !p.Peek {
		t.Fatalf("neighbor pile card: %+v", p)
	}
}

// Down from a covering float enters the stash; up comes back to the
// float.
func TestOverviewCascadeFloatStashRight(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 1)
	w := m.Current()
	w.AddFloating(90, 300, 200)
	w.FocusID(90)
	m.ToggleOverview()
	if f := m.stackFront(w); f.kind != stackFloat {
		t.Fatalf("setup: front %+v", f)
	}
	m.OverviewMove(0, 1)
	if m.cardAt(w) < 0 {
		t.Fatal("down from the float did not select the stash")
	}
	m.OverviewMove(0, -1)
	if m.cardAt(w) >= 0 || m.stackFront(w).kind != stackFloat || m.overviewTarget() != 90 {
		t.Fatalf("up from the stash: card %d, front %+v, target %d", m.cardAt(w), m.stackFront(w), m.overviewTarget())
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
			if front.Peek || front.Rect.X-behind.Rect.X != w.peekStep(horizontalAxis) {
				t.Fatalf("columns behind %v, in front %v, peek step %d", behind.Rect, front.Rect, w.peekStep(horizontalAxis))
			}
			d.move(m, 1, 0)
			if f := m.stackFront(w); f.kind != stackFloat || f.id != 90 {
				t.Fatalf("right from the columns behind the float: front %+v", f)
			}
		})
	}
}

// T10b: a pinned fullscreen column keeps its Fullscreen tile, and the
// columns of the other bands stay shown.
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
		if p := previewOf(t, ps, id); p.Hidden || p.Fullscreen {
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

	// Over the unfolded stash they still change workspace: left and
	// right walk the cards.
	withStash(m, 3)
	w.FocusID(10)
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	if m.cardAt(w) < 0 {
		t.Fatal("setup: no card selected")
	}
	m.overviewFocus(ActionFocusWorkspacePrev)
	if m.Current() != m.Workspaces[0] {
		t.Fatal("focus-workspace-prev over a card did not change workspace")
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
	if rows.at(m.Workspaces[1]).X <= rows.at(from).X {
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

// A stash-only cascade workspace has nothing above its pile: right from its
// pile goes straight to the next workspace, left to the previous one.
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
	cur, slot := rows.at(m.Current()), rows.at(prev)
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

// outlineHits reports whether any outline line of ps covers a point of r.
func outlineHits(ps []Placement, r Rect) bool {
	for _, s := range squareOverviewOutline(nil, ps, 2) {
		if s.Rect.Overlaps(r) {
			return true
		}
	}
	return false
}

// The pile shows whenever the workspace has a stash, whatever is selected
// and however many windows it holds: the front cards are drawn and no
// lane tile is painted over them.
func TestOverviewCascadePileVisible(t *testing.T) {
	for _, n := range []int{1, 2, 4, 6} {
		for _, hidden := range []bool{false, true} {
			for _, down := range []int{0, 1, 2} {
				m := cascadeOverview()
				withStash(m, n)
				w := m.Current()
				w.stashHidden = hidden
				m.ToggleOverview()
				for range down {
					m.OverviewMove(0, 1)
				}
				ps := m.Layout()
				shown := 0
				for i, p := range ps {
					if w.stashIndex(p.ID) < 0 || p.Hidden {
						continue
					}
					shown++
					for _, q := range ps[i+1:] {
						if w.stashIndex(q.ID) < 0 && !q.Hidden && q.Preview > 0 && q.Rect.Overlaps(p.Rect) {
							t.Fatalf("n=%d hidden=%v down=%d: card %d painted under tile %d", n, hidden, down, p.ID, q.ID)
						}
					}
				}
				want := min(n, overviewCards)
				if m.cardAt(w) >= 0 {
					// Unfolded: every card.
					want = n
				}
				if shown != want {
					t.Fatalf("n=%d hidden=%v down=%d: %d cards shown, want %d", n, hidden, down, shown, want)
				}
			}
		}
	}
}

// The selection outline of a lane tile stops at the pile; a selected card
// is framed whole, and the lane keeps no frame then. Exactly one preview is
// framed at any time.
func TestOverviewCascadeOutlineAndPile(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 3)
	w := m.Current()
	w.FocusID(8)
	m.ToggleOverview()
	framed := func(ps []Placement) (n int) {
		for _, p := range ps {
			if p.Preview > 0 && p.Focused && !p.Hidden {
				n++
			}
		}
		return n
	}

	// A tile selected: the frame goes round the tile but not across any card.
	ps := m.Layout()
	if framed(ps) != 1 || !previewOf(t, ps, 8).Focused {
		t.Fatalf("lane selected: %d framed", framed(ps))
	}
	if len(squareOverviewOutline(nil, ps, 2)) == 0 {
		t.Fatal("lane selected: no frame")
	}
	for _, id := range []WindowID{40, 41, 42} {
		if outlineHits(ps, previewOf(t, ps, id).Rect) {
			t.Fatalf("the lane's frame crosses card %d", id)
		}
	}
	// The cut is the pile's: the tile still has frame lines beside it.
	if !outlineHits(ps, Rect{X: previewOf(t, ps, 8).Rect.X - 2, Y: previewOf(t, ps, 8).Rect.Y, W: 2, H: 10}) {
		t.Fatal("the lane lost its left edge")
	}

	// The pile selected: the card has all four sides, the lane none.
	m.OverviewMove(0, 1)
	m.OverviewMove(0, 1)
	m.OverviewMove(0, 1)
	at := m.cardAt(w)
	if at < 0 {
		t.Fatal("setup: no card selected")
	}
	ps = m.Layout()
	if framed(ps) != 1 {
		t.Fatalf("card selected: %d framed", framed(ps))
	}
	card := previewOf(t, ps, w.Stash[at].ID)
	if !card.Focused || !card.Card {
		t.Fatalf("selected card not framed: %+v", card)
	}
	out := squareOverviewOutline(nil, ps, 2)
	if len(out) != 4 {
		t.Fatalf("card frame is %d lines, want 4: %+v", len(out), out)
	}
	for _, s := range out {
		if !s.Rect.Overlaps(Rect{X: card.Rect.X - 2, Y: card.Rect.Y - 2, W: card.Rect.W + 4, H: card.Rect.H + 4}) {
			t.Fatalf("line %+v is away from the card %+v", s.Rect, card.Rect)
		}
	}
	for _, p := range ps {
		if w.stashIndex(p.ID) < 0 && p.Focused && !p.Hidden && p.Preview > 0 {
			t.Fatalf("lane tile %d still framed with a card selected", p.ID)
		}
	}

	// Back to a lane: the frame follows, the card has none.
	m.OverviewMove(0, -1)
	ps = m.Layout()
	if m.cardAt(w) >= 0 || framed(ps) != 1 {
		t.Fatalf("lane reselected: card %d, %d framed", m.cardAt(w), framed(ps))
	}
	for _, p := range ps {
		if w.stashIndex(p.ID) >= 0 && p.Focused {
			t.Fatalf("card %d still framed on the lane", p.ID)
		}
	}
}

// If two previews claim the selection, the stash card is the one framed.
func TestOverviewOutlinePrefersCard(t *testing.T) {
	ps := []Placement{
		{ID: 1, Rect: Rect{X: 0, Y: 0, W: 100, H: 100}, Preview: 0.5, Focused: true},
		{ID: 2, Rect: Rect{X: 40, Y: 80, W: 30, H: 30}, Preview: 0.2, Focused: true, Card: true},
	}
	out := squareOverviewOutline(nil, ps, 2)
	if len(out) != 4 || out[0].Rect != (Rect{X: 38, Y: 78, W: 34, H: 2}) {
		t.Fatalf("outline: %+v", out)
	}
}

// The outline is rebuilt every overview frame: cutting it around the pile
// costs only the returned slice.
func TestOverviewOutlinePileAllocations(t *testing.T) {
	m := cascadeOverview()
	withStash(m, 4)
	m.Current().FocusID(8)
	m.ToggleOverview()
	ps := m.Layout()
	card := false
	for _, p := range ps {
		card = card || p.Card && p.Preview > 0 && !p.Hidden
	}
	if !card || len(squareOverviewOutline(nil, ps, 2)) == 0 {
		t.Fatal("setup: no pile under a framed lane tile")
	}
	if n := testing.AllocsPerRun(50, func() { _ = squareOverviewOutline(nil, ps, 2) }); n > 2 {
		t.Fatalf("overviewOutline allocs = %v, want <= 2", n)
	}
}

func TestOverviewOutlineRoundedAllocations(t *testing.T) {
	ps := []Placement{{Rect: Rect{X: 20, Y: 20, W: 100, H: 80}, Preview: 0.5, Focused: true, Floating: true}}
	// Keep the returned outline alive so its one snapshot allocation is counted.
	var outline *ports.SelectionOutline
	n := testing.AllocsPerRun(50, func() {
		_, outline = overviewOutline(nil, ps, 2, 0, 12)
	})
	if outline == nil {
		t.Fatal("setup: no rounded outline")
	}
	if n != 1 {
		t.Fatalf("rounded overviewOutline allocs = %v, want 1 (outline only)", n)
	}
}

func TestSubtractRects(t *testing.T) {
	h := Rect{X: 0, Y: 10, W: 100, H: 2}
	v := Rect{X: 10, Y: 0, W: 2, H: 100}
	for name, tc := range map[string]struct {
		r    Rect
		cuts []Rect
		area int
	}{
		"none":       {h, nil, 200},
		"apart":      {h, []Rect{{X: 0, Y: 50, W: 10, H: 10}}, 200},
		"middle":     {h, []Rect{{X: 40, Y: 0, W: 20, H: 30}}, 160},
		"edge":       {h, []Rect{{X: 90, Y: 0, W: 50, H: 30}}, 180},
		"covered":    {h, []Rect{{X: -5, Y: 0, W: 200, H: 30}}, 0},
		"across":     {h, []Rect{{X: 40, Y: 11, W: 20, H: 30}}, 180},
		"two":        {h, []Rect{{X: 10, Y: 0, W: 10, H: 30}, {X: 50, Y: 0, W: 10, H: 30}}, 160},
		"overlapped": {h, []Rect{{X: 10, Y: 0, W: 20, H: 30}, {X: 20, Y: 0, W: 20, H: 30}}, 140},
		"vertical":   {v, []Rect{{X: 0, Y: 40, W: 30, H: 20}}, 160},
		"stacked":    {v, []Rect{{X: 0, Y: 10, W: 30, H: 10}, {X: 0, Y: 15, W: 30, H: 10}, {X: 0, Y: 90, W: 30, H: 30}}, 150},
	} {
		area := 0
		pieces := subtractRects(nil, tc.r, tc.cuts)
		for i, p := range pieces {
			area += p.W * p.H
			if p.W <= 0 || p.H <= 0 || p.X < tc.r.X || p.Y < tc.r.Y || p.X+p.W > tc.r.X+tc.r.W || p.Y+p.H > tc.r.Y+tc.r.H {
				t.Fatalf("%s: piece %v outside %v", name, p, tc.r)
			}
			for _, c := range tc.cuts {
				if p.Overlaps(c) {
					t.Fatalf("%s: piece %v overlaps cut %v", name, p, c)
				}
			}
			for _, q := range pieces[i+1:] {
				if p.Overlaps(q) {
					t.Fatalf("%s: pieces %v and %v overlap", name, p, q)
				}
			}
		}
		if area != tc.area {
			t.Fatalf("%s: area %d, want %d", name, area, tc.area)
		}
	}
}
