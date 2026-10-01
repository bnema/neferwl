package core

import (
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// overviewMonitor has workspace 1 with columns 1, 2, 3 (a third of the
// width each) on a 300x200 output, and workspace 2 with column 4.
func overviewMonitor() *Monitor {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	m.Focus(1)
	m.AddWindow(4)
	m.Focus(0)
	return m
}

func previewOf(t *testing.T, ps []Placement, id WindowID) Placement {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no placement for %d", id)
	return Placement{}
}

// Previews keep each tile's shape: width and height shrink by the same
// factor, and the row fits the usable width.
func TestOverviewPreviewsKeepAspect(t *testing.T) {
	m := overviewMonitor()
	normal := previewOf(t, m.Layout(), 2).Rect
	m.ToggleOverview()
	ps := m.Layout()
	for id := WindowID(1); id <= 3; id++ {
		p := previewOf(t, ps, id)
		if p.Hidden || p.Preview <= 0 || p.Peek {
			t.Fatalf("%d: %+v", id, p)
		}
		if p.Rect.X < 0 || p.Rect.X+p.Rect.W > 300 {
			t.Fatalf("%d off screen: %+v", id, p.Rect)
		}
	}
	p := previewOf(t, ps, 2)
	got, want := float64(p.Rect.W)/float64(p.Rect.H), float64(normal.W)/float64(normal.H)
	if math.Abs(got-want) > 0.02 {
		t.Fatalf("aspect %v, want %v (%+v)", got, want, p.Rect)
	}
	if math.Abs(float64(p.Rect.W)-float64(normal.W)*p.Preview) > 1 {
		t.Fatalf("width %d, zoom %v of %d", p.Rect.W, p.Preview, normal.W)
	}
	// Workspace 2 is below, dimmed and unfocused.
	below := previewOf(t, ps, 4)
	if below.Hidden || !below.Peek || below.Focused || below.Rect.Y <= p.Rect.Y+p.Rect.H {
		t.Fatalf("below %+v, current %+v", below, p)
	}
}

// Many columns shrink down to the floor, then the row scrolls to keep
// the selected column on screen.
func TestOverviewWideWorkspaceScrolls(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetMaxColumns(1)
	for id := WindowID(1); id <= 10; id++ {
		m.AddWindow(id)
	}
	m.ToggleOverview()
	ps := m.Layout()
	p := previewOf(t, ps, 10)
	if p.Preview != overviewMinZoom || !p.Focused || p.Rect.X < 0 || p.Rect.X+p.Rect.W > 300 {
		t.Fatalf("%+v", p)
	}
}

// h/l and j/k move the selection; Escape returns, Return keeps it.
func TestOverviewNavigation(t *testing.T) {
	m := overviewMonitor()
	m.Current().FocusID(3)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if id, _ := m.Focused(); id != 2 {
		t.Fatalf("left: %d", id)
	}
	m.OverviewMove(0, 1)
	if id, _ := m.Focused(); id != 4 || m.Active != 1 {
		t.Fatalf("down: %d on %d", id, m.Active)
	}
	// The empty workspace below the last one is not entered.
	m.OverviewMove(0, 1)
	if m.Active != 1 {
		t.Fatalf("entered empty workspace %d", m.Active)
	}
	m.CancelOverview()
	if id, _ := m.Focused(); id != 3 || m.Active != 0 || m.ov.open {
		t.Fatalf("cancel: %d on %d", id, m.Active)
	}
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	m.ToggleOverview()
	if id, _ := m.Focused(); id != 4 || m.Active != 1 || m.ov.open {
		t.Fatalf("confirm: %d on %d", id, m.Active)
	}
}

// A click picks the preview under it.
func TestOverviewPick(t *testing.T) {
	m := overviewMonitor()
	m.ToggleOverview()
	below := previewOf(t, m.Layout(), 4).Rect
	id := m.overviewAt(float64(below.X+1), float64(below.Y+1))
	if id != 4 {
		t.Fatalf("hit %d", id)
	}
	m.OverviewPick(id)
	if f, _ := m.Focused(); f != 4 || m.ov.open {
		t.Fatalf("pick: %d", f)
	}
}

// gameMonitor has, on a 300x200 output, window 1 on workspace 1 and, on
// workspace 2, a floating game 2 (fixed size) fullscreen; workspace 1 is
// on screen.
func gameMonitor(o Overflow) *Monitor {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(o)
	m.AddWindow(1)
	m.Focus(1)
	m.AddFloating(2, 300, 200)
	m.SetFullscreen(2, true)
	m.Focus(0)
	return m
}

// A floating game covering its workspace shows as that row's preview: the
// row is not empty.
func TestOverviewFullscreenFloat(t *testing.T) {
	for _, o := range []Overflow{OverflowFixed, OverflowScroll} {
		t.Run(string(o), func(t *testing.T) {
			m := gameMonitor(o)
			m.ToggleOverview()
			ps := m.Layout()
			cur, game := previewOf(t, ps, 1), previewOf(t, ps, 2)
			if game.Hidden || !game.Peek || game.Focused || game.Preview <= 0 || game.Rect.Y <= cur.Rect.Y {
				t.Fatalf("game %+v under row %+v", game, cur)
			}
			// Selected below, then picked: fullscreen and focused.
			m.OverviewMove(0, 1)
			if g := previewOf(t, m.Layout(), 2); !g.Focused || g.Peek {
				t.Fatalf("selected game %+v", g)
			}
			m.OverviewMove(-1, 0) // nothing else on its row
			m.ToggleOverview()
			if f, _ := m.Focused(); f != 2 || m.ov.open || m.Active != 1 || !previewOf(t, m.Layout(), 2).Fullscreen {
				t.Fatalf("confirm: %d on %d", f, m.Active)
			}
		})
	}
}

// A click picks the game; Escape from its row returns where the overview
// opened.
func TestOverviewFullscreenFloatPickAndCancel(t *testing.T) {
	m := gameMonitor(OverflowFixed)
	m.ToggleOverview()
	r := previewOf(t, m.Layout(), 2).Rect
	if id := m.overviewAt(float64(r.X+1), float64(r.Y+1)); id != 2 {
		t.Fatalf("hit %d", id)
	}
	m.OverviewMove(0, 1)
	m.CancelOverview()
	if f, _ := m.Focused(); f != 1 || m.Active != 0 {
		t.Fatalf("cancel: %d on %d", f, m.Active)
	}
	m.ToggleOverview()
	m.OverviewPick(2)
	if f, _ := m.Focused(); f != 2 || m.ov.open {
		t.Fatalf("pick: %d", f)
	}
}

// The overview opens over the game too: the game is the selected row,
// the workspace above shows dimmed, and Escape returns to the game.
func TestOverviewOpensOverFullscreenFloat(t *testing.T) {
	m := gameMonitor(OverflowFixed)
	m.Focus(1)
	m.ToggleOverview()
	if !m.ov.open {
		t.Fatal("overview did not open")
	}
	ps := m.Layout()
	game, above := previewOf(t, ps, 2), previewOf(t, ps, 1)
	if !game.Focused || game.Peek || game.Preview <= 0 || !above.Peek || above.Rect.Y >= game.Rect.Y {
		t.Fatalf("game %+v, above %+v", game, above)
	}
	m.CancelOverview()
	if f, _ := m.Focused(); f != 2 || m.Active != 1 || m.ov.open || !previewOf(t, m.Layout(), 2).Fullscreen {
		t.Fatalf("cancel: %d on %d", f, m.Active)
	}
}

// A tiled fullscreen window (fixed overflow) is its row's only preview
// too: the selection cannot leave it for a hidden column, and Return keeps
// it fullscreen.
func TestOverviewOpensOverFullscreenTile(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(OverflowFixed)
	m.AddWindow(1)
	m.AddWindow(2)
	m.ToggleFullscreen()
	m.ToggleOverview()
	game := previewOf(t, m.Layout(), 2)
	if !m.ov.open || !game.Focused || game.Peek || !game.Fullscreen {
		t.Fatalf("game %+v", game)
	}
	m.OverviewMove(-1, 0)
	m.ToggleOverview()
	if f, _ := m.Focused(); f != 2 || m.Active != 0 || !previewOf(t, m.Layout(), 2).Fullscreen {
		t.Fatalf("confirm: %d on %d", f, m.Active)
	}
}

// Under scroll overflow a fullscreen column does not pin the row: another
// column can be selected and picked, as scrolling would on screen.
func TestOverviewOverScrollFullscreenPicksColumn(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetOverflow(OverflowScroll)
	m.AddWindow(1)
	m.AddWindow(2)
	m.SetFullscreen(2, true)
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 1); p.Hidden {
		t.Fatalf("column 1 hidden: %+v", p)
	}
	m.OverviewMove(-1, 0)
	m.ToggleOverview()
	if f, _ := m.Focused(); f != 1 {
		t.Fatalf("focused %d", f)
	}
}

// Only the game shows on its row: its dialogs and the columns under it
// stay hidden, as on screen.
func TestOverviewFullscreenFloatHidesOthers(t *testing.T) {
	m := gameMonitor(OverflowScroll)
	m.Focus(1)
	m.AddWindow(5)
	m.Current().FocusID(2) // back on the game, over column 5
	m.AddWindow(3)
	m.AddFloating(4, 50, 50)
	m.Focus(0)
	m.ToggleOverview()
	ps := m.Layout()
	if previewOf(t, ps, 2).Hidden {
		t.Fatal("game hidden")
	}
	for _, id := range []WindowID{3, 4, 5} {
		if p := previewOf(t, ps, id); !p.Hidden {
			t.Fatalf("%d shown: %+v", id, p)
		}
	}
}

// pileMonitor is overviewMonitor with 5, 6 and 7 stashed on workspace 1
// (7 selected) and the stash hidden.
func pileMonitor() *Monitor {
	m := overviewMonitor()
	w := m.Current()
	for id := WindowID(5); id <= 7; id++ {
		w.AddWindow(id)
		w.FocusID(id)
		w.ToggleWindowStash()
	}
	w.ToggleStashVisible()
	w.FocusID(1)
	return m
}

// The stash shows as a pile on the left of its row, even hidden: its
// selected window in front, the others behind, dimmed. The row starts
// right of it.
func TestOverviewPileLayout(t *testing.T) {
	m := pileMonitor()
	m.ToggleOverview()
	ps := m.Layout()
	front, back := previewOf(t, ps, 7), previewOf(t, ps, 5)
	if front.Hidden || front.Peek || front.Focused || front.Preview != overviewCardZoom {
		t.Fatalf("front %+v", front)
	}
	if back.Hidden || !back.Peek || back.Rect.X <= front.Rect.X || back.Rect.Y >= front.Rect.Y {
		t.Fatalf("back %+v, front %+v", back, front)
	}
	first := previewOf(t, ps, 1)
	if !first.Focused || first.Rect.X < back.Rect.X+back.Rect.W {
		t.Fatalf("row %+v overlaps the pile %+v", first.Rect, back.Rect)
	}
	// Front last: drawn and hit on top.
	idx := func(id WindowID) int {
		for i, p := range ps {
			if p.ID == id {
				return i
			}
		}
		return -1
	}
	if idx(7) < idx(5) || idx(7) < idx(6) {
		t.Fatal("front card drawn under the others")
	}
}

// A stash pile does not shift a row that fits beside it: the row stays
// centred like the same workspace without a stash.
func TestOverviewPileKeepsRowCentred(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(3000, 1200)
	m.SetOverflow(OverflowFixed)
	m.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	m.ToggleOverview()
	want := previewOf(t, m.Layout(), 1).Rect
	m.CancelOverview()
	w := m.Current()
	w.AddWindow(4)
	w.FocusID(4)
	w.ToggleWindowStash()
	w.FocusID(1)
	m.ToggleOverview()
	if got := previewOf(t, m.Layout(), 1).Rect; got != want {
		t.Fatalf("row with a stash at %+v, without at %+v", got, want)
	}
}

// h from column 1 enters the pile on the stash's selection, h/l browse
// it, l past its end returns to column 1; Return shows the stash on the
// chosen window.
func TestOverviewPileNavigation(t *testing.T) {
	m := pileMonitor()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if at := m.cardAt(w); at != 2 {
		t.Fatalf("entered at %d", at)
	}
	if f := previewOf(t, m.Layout(), 7); !f.Focused {
		t.Fatalf("front card not selected: %+v", f)
	}
	if previewOf(t, m.Layout(), 1).Focused {
		t.Fatal("column still selected")
	}
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0)
	m.OverviewMove(-1, 0) // stops at the first entry
	if at := m.cardAt(w); at != 0 {
		t.Fatalf("left end %d", at)
	}
	m.OverviewMove(1, 0)
	m.ToggleOverview()
	if f, _ := m.Focused(); f != 6 || w.stashHidden || m.ov.open {
		t.Fatalf("confirm on %d, hidden %v", f, w.stashHidden)
	}
	// Back in on the stash's selection (6), then l past the last entry
	// (7) returns to column 1.
	w.FocusID(1)
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	if at := m.cardAt(w); at != 1 {
		t.Fatalf("entered at %d", at)
	}
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0)
	if m.cardAt(w) >= 0 {
		t.Fatal("still in the pile")
	}
	if f, _ := m.Focused(); f != 1 {
		t.Fatalf("back on %d", f)
	}
	// Escape leaves the stash as it was.
	m.OverviewMove(-1, 0)
	m.CancelOverview()
	if f, _ := m.Focused(); f != 1 {
		t.Fatalf("cancel on %d", f)
	}
}

// A click on a card shows the stash on it.
func TestOverviewPilePick(t *testing.T) {
	m := pileMonitor()
	m.ToggleOverview()
	r := previewOf(t, m.Layout(), 7).Rect
	id := m.overviewAt(float64(r.X+1), float64(r.Y+r.H-1))
	if id != 7 {
		t.Fatalf("hit %d", id)
	}
	m.OverviewPick(id)
	if f, _ := m.Focused(); f != 7 || m.Current().stashHidden {
		t.Fatalf("pick on %d", f)
	}
}

// A workspace with only stashed windows opens with the pile selected, and
// its neighbor's pile is dimmed and unselected.
func TestOverviewPileOnly(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.AddWindow(1)
	w := m.Current()
	w.ToggleWindowStash()
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 1); !p.Focused || p.Hidden {
		t.Fatalf("%+v", p)
	}
	m.CancelOverview()
	m.Focus(1)
	m.AddWindow(2)
	m.Focus(0)
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	if p := previewOf(t, m.Layout(), 1); p.Focused || !p.Peek || p.Hidden {
		t.Fatalf("neighbor pile %+v", p)
	}
}

// Fixed overflow previews use the actual buffer geometry, including the
// spiral and window stacks, rather than laying full-height columns in a row.
func TestOverviewFixedOverflowGeometry(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		t.Run(map[bool]string{false: "spiral", true: "expanded"}[expanded], func(t *testing.T) {
			m := newMonitor("", "")
			m.SetOutput(600, 400)
			m.SetOverflow(OverflowFixed)
			m.SetMaxColumns(3)
			for id := WindowID(1); id <= 5; id++ {
				m.AddWindow(id)
			}
			w := m.Current()
			if expanded {
				w.Columns[2].Expanded = true
			}
			w.Columns[3].Windows = append(w.Columns[3].Windows, 6)
			w.Columns[3].Focus = 1
			w.FocusID(6)
			screen := w.Layout()
			m.ToggleOverview()
			if _, span, _ := w.previewTiles(); span != w.Usable.W {
				t.Fatalf("span %d, usable %d", span, w.Usable.W)
			}
			ps := m.Layout()
			z := previewOf(t, ps, 6).Preview
			scale := func(v int) int { return int(math.Round(float64(v) * z)) }
			anchor, previewAnchor := previewOf(t, screen, 1).Rect, previewOf(t, ps, 1).Rect
			for _, id := range []WindowID{1, 2, 3, 4, 5, 6} {
				r, p := previewOf(t, screen, id).Rect, previewOf(t, ps, id)
				if p.Hidden || p.Preview != z || math.Abs(float64(p.Rect.W-scale(r.W))) > 1 || math.Abs(float64(p.Rect.H-scale(r.H))) > 1 || math.Abs(float64(p.Rect.X-previewAnchor.X-scale(r.X-anchor.X))) > 1 || math.Abs(float64(p.Rect.Y-previewAnchor.Y-scale(r.Y-anchor.Y))) > 1 {
					t.Fatalf("%d: screen %+v preview %+v anchor %+v %+v", id, r, p, anchor, previewAnchor)
				}
				if p.Focused != (id == 6) {
					t.Fatalf("%d focus: %+v", id, p)
				}
				for _, other := range []WindowID{1, 2, 3, 4, 5, 6} {
					if other > id && p.Rect.Overlaps(previewOf(t, ps, other).Rect) {
						t.Fatalf("%d overlaps %d", id, other)
					}
				}
				if p.Rect.W > 0 && p.Rect.H > 0 && m.overviewAt(float64(p.Rect.X+p.Rect.W/2), float64(p.Rect.Y+p.Rect.H/2)) != id {
					t.Fatalf("%d not hit at %+v", id, p.Rect)
				}
			}
			if len(overviewOutline(ps, 2)) != 4 {
				t.Fatal("missing selection outline")
			}
			m.OverviewMove(-1, 0)
			if id, _ := m.Focused(); id != 3 {
				t.Fatalf("left selected %d", id)
			}
			m.OverviewMove(1, 0)
			if id, _ := m.Focused(); id != 6 {
				t.Fatalf("right selected %d", id)
			}
		})
	}
}

func TestOverviewFixedFullWidthShowsHiddenColumnsCard(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(600, 400)
	m.SetOverflow(OverflowFixed)
	m.SetMaxColumns(3)
	for id := WindowID(1); id <= 5; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	w.ToggleFullWidth()
	m.ToggleOverview()
	if p := previewOf(t, m.Layout(), 5); p.Hidden || p.Peek || !p.Focused {
		t.Fatalf("front %+v", p)
	}
	for _, id := range []WindowID{1, 2} {
		if p := previewOf(t, m.Layout(), id); p.Hidden || !p.Peek {
			t.Fatalf("peek %d %+v", id, p)
		}
	}
	m.OverviewMove(0, -1)
	m.ToggleOverview()
	if id, _ := w.Focused(); id != 1 || !w.Columns[0].FullWidth || w.Columns[4].FullWidth {
		t.Fatalf("transfer %d %+v", id, w.Columns)
	}
}

// The selected card follows its window: removing an earlier stash entry
// keeps it, removing the card itself drops the selection to the column.
// The card is what Focused reports, and close-window closes it.
func TestOverviewCardFollowsWindow(t *testing.T) {
	m := pileMonitor()
	w := m.Current()
	m.ToggleOverview()
	m.OverviewMove(-1, 0) // on 7
	if id, _ := m.Focused(); id != 7 {
		t.Fatalf("focused %d, want the card", id)
	}
	if e := m.Apply(ActionCloseWindow); e.Close != 7 {
		t.Fatalf("close %d, want the card", e.Close)
	}
	m.RemoveWindow(5)
	if id, _ := m.Focused(); id != 7 {
		t.Fatalf("after removing 5: %d", id)
	}
	m.RemoveWindow(7)
	if id, _ := m.Focused(); id != 1 || m.cardAt(w) >= 0 {
		t.Fatalf("after removing the card: %d", id)
	}
}

// A workspace switched by a bind does not inherit the card selection.
func TestOverviewCardAfterBindSwitch(t *testing.T) {
	m := pileMonitor()
	m.Workspaces[1].AddWindow(8)
	m.Workspaces[1].FocusID(8)
	m.Workspaces[1].ToggleWindowStash()
	m.Workspaces[1].ToggleStashVisible()
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	m.FocusNumber(2)
	if id, _ := m.Focused(); id != 4 {
		t.Fatalf("focused %d after the switch", id)
	}
	m.ToggleOverview()
	if !m.Workspaces[1].stashHidden {
		t.Fatal("the other workspace's stash was shown")
	}
}

// Picking a card wins over another stashed window's pending fullscreen.
func TestOverviewCardOverPendingFullscreen(t *testing.T) {
	m := pileMonitor()
	w := m.Current()
	w.SetFullscreen(5, true) // hidden stash: pending
	m.ToggleOverview()
	m.OverviewMove(-1, 0)
	m.ToggleOverview()
	if id, _ := m.Focused(); id != 7 || w.fullscreen != 0 {
		t.Fatalf("focused %d, fullscreen %d", id, w.fullscreen)
	}
}

// On a tiny output the pile leaves room for the row.
func TestOverviewPileTinyOutput(t *testing.T) {
	m := pileMonitor()
	m.SetOutput(30, 20)
	m.ToggleOverview()
	for _, p := range m.Layout() {
		if !p.Hidden && (p.Rect.W < 0 || p.Rect.X < 0 || p.Rect.X > 30) {
			t.Fatalf("%+v", p)
		}
	}
	if w := m.Current(); w.pileWidth() > 10 {
		t.Fatalf("pile %d of 30", w.pileWidth())
	}
}

// The overview opens on what has the focus: a shown stash's card, else
// the column under a focused native float (hidden in the overview), and
// Return keeps it. Escape gives the float its focus back.
func TestOverviewOpensOnFocus(t *testing.T) {
	m := pileMonitor()
	w := m.Current()
	w.ToggleStashVisible()
	m.ToggleOverview()
	if at := m.cardAt(w); at != w.stashAt {
		t.Fatalf("opened on card %d, want the stash's %d", at, w.stashAt)
	}
	m.ToggleOverview()

	m = overviewMonitor()
	w = m.Current()
	col, _ := w.Focused()
	w.AddFloating(9, 10, 10)
	w.FocusID(9)
	m.ToggleOverview()
	if f, _ := m.Focused(); f != col || !previewOf(t, m.Layout(), col).Focused {
		t.Fatalf("opened on %d, want column %d", f, col)
	}
	m.CancelOverview()
	if f, _ := m.Focused(); f != 9 {
		t.Fatalf("escape on %d, want the float", f)
	}
	w.FocusID(9)
	m.ToggleOverview()
	m.ToggleOverview()
	if f, _ := m.Focused(); f != col {
		t.Fatalf("return on %d, want column %d", f, col)
	}
}

// Scrolling adds up to steps: what is left when the overview closes does
// not count when it opens again, and a wheel frame of two notches moves
// twice.
func TestOverviewScrollSteps(t *testing.T) {
	m := overviewMonitor()
	w := m.Current()
	finger := func(dx float64) ports.PointerAxis {
		return ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Value: dx}}
	}
	m.ToggleOverview()
	start, _ := m.Focused()
	if m.overviewScroll(finger(-50)) {
		t.Fatal("a partial scroll moved")
	}
	m.CancelOverview()
	m.ToggleOverview()
	if m.overviewScroll(finger(-50)) {
		t.Fatal("the last session's scroll carried over")
	}
	m.CancelOverview()
	w.FocusID(3)
	m.ToggleOverview()
	if !m.overviewScroll(ports.PointerAxis{Source: ports.AxisWheel, Horizontal: ports.ScrollAxis{Set: true, V120: -240}}) {
		t.Fatal("wheel did not move")
	}
	if f, _ := m.Focused(); f != 1 {
		t.Fatalf("two notches from 3 landed on %d, want 1 (started on %d)", f, start)
	}
}

// A two-finger scroll sideways steps to the next column after
// overviewScrollStep, then once per overviewScrollRepeat, and stays on
// columns; up or down it steps once until the fingers lift. Reopening the
// overview, a wheel notch and continuous scrolling are not held by it.
func TestOverviewFingerScroll(t *testing.T) {
	finger := func(dx, dy float64) ports.PointerAxis {
		return ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Value: dx}, Vertical: ports.ScrollAxis{Set: true, Value: dy}}
	}
	m := overviewMonitor()
	focused := func() WindowID { f, _ := m.Focused(); return f }
	m.Current().FocusID(3)
	m.ToggleOverview()
	// 70: one column; 100 more is under the longer repeat step.
	if !m.overviewScroll(finger(-70, 0)) || focused() != 2 {
		t.Fatalf("first step on %d", focused())
	}
	if m.overviewScroll(finger(-100, 0)) {
		t.Fatal("stepped again before overviewScrollRepeat")
	}
	// The fingers drift down: still on columns, no row step.
	if m.overviewScroll(finger(0, 200)) {
		t.Fatal("a sideways scroll stepped down")
	}
	if !m.overviewScroll(finger(-50, 0)) || focused() != 1 {
		t.Fatalf("long scroll on %d, want 1", focused())
	}

	// Vertical: one step per scroll, through the stack's cards.
	sm := stackMonitor()
	sm.ToggleOverview()
	before, _ := sm.Focused()
	if !sm.overviewScroll(finger(0, 70)) {
		t.Fatal("first vertical step")
	}
	after, _ := sm.Focused()
	if after == before {
		t.Fatal("vertical step selected nothing new")
	}
	if sm.overviewScroll(finger(0, 400)) {
		t.Fatal("the same vertical scroll stepped twice")
	}
	if !sm.overviewScroll(ports.PointerAxis{Source: ports.AxisWheel, Vertical: ports.ScrollAxis{Set: true, V120: -120}}) {
		t.Fatal("wheel held by the finger scroll")
	}
	sm.CancelOverview()
	sm.ToggleOverview()
	if !sm.overviewScroll(finger(0, 70)) {
		t.Fatal("reopened overview still held the old scroll")
	}
	cont := ports.PointerAxis{Source: ports.AxisContinuous, Vertical: ports.ScrollAxis{Set: true, Value: -70}}
	if !sm.overviewScroll(cont) {
		t.Fatal("continuous scroll held by the finger scroll")
	}
}
