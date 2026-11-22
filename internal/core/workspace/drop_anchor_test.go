package workspace

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// anchorWorkspace has two side-by-side tiles 1 and 2, 1000x800 with 10 px
// gaps, and src holds tile 9, dragged onto them.
func anchorWorkspace() (w, src *Workspace) {
	w = workspace()
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	w.AddWindow(1)
	w.AddWindow(2)
	src = workspace()
	src.AddWindow(9)
	return w, src
}

func rectOf(t *testing.T, w *Workspace, id WindowID) Rect {
	t.Helper()
	for _, p := range w.Layout() {
		if p.ID == id {
			return p.Rect
		}
	}
	t.Fatalf("window %d not placed", id)
	return Rect{}
}

func TestAnchorBands(t *testing.T) {
	for _, tc := range []struct{ w, h, bx, by int }{
		{400, 400, 100, 100}, // a quarter
		{120, 1000, 40, 160}, // 48 capped at a third; 250 clamped to 160
		{100, 180, 33, 48},   // 48 capped at 33; 45 raised to 48
		{2000, 160, 160, 48}, // 500 clamped; 40 raised to 48, under 53
	} {
		bx, by := bands(Rect{W: tc.w, H: tc.h})
		if bx != tc.bx || by != tc.by {
			t.Errorf("bands(%dx%d) = %d,%d, want %d,%d", tc.w, tc.h, bx, by, tc.bx, tc.by)
		}
	}
}

func TestAnchorCornerTieBreak(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 400, H: 400} // 100 px bands
	p := uint8(1<<5 - 1)
	for _, tc := range []struct {
		x, y int
		want domain.DropSide
	}{
		{10, 30, domain.DropSideLeft},
		{30, 10, domain.DropSideTop},
		{20, 20, domain.DropSideLeft}, // a tie goes to the horizontal band
		{389, 395, domain.DropSideBottom},
		{395, 389, domain.DropSideRight},
		{200, 200, domain.DropSideCenter},
	} {
		if got := windowSide(r, tc.x, tc.y, p); got != tc.want {
			t.Errorf("(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	// Normalized depth: an 800x200 tile has 160 and 50 px bands; 40 px in
	// from the left (0.25) beats 20 px from the top (0.4).
	if got := windowSide(Rect{W: 800, H: 200}, 40, 20, p); got != domain.DropSideLeft {
		t.Fatal(got)
	}
}

func TestAnchorWindowSides(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		r := rectOf(t, w, 2)
		bx, by := bands(r)
		p := domain.DefaultDropPolicy()
		a := w.AnchorAt(src, 9, r.X+20, r.Y+r.H/2, Anchor{}, p)
		want := Anchor{Kind: AnchorWindow, Side: domain.DropSideLeft, Window: 2, Action: domain.DropColumn,
			Rect: Rect{X: r.X + 5, Y: r.Y + 5, W: bx - 10, H: r.H - 10}}
		if a != want {
			t.Fatalf("left = %+v, want %+v", a, want)
		}
		a = w.AnchorAt(src, 9, r.X+r.W/2, r.Y+r.H-40, Anchor{}, p)
		if a.Side != domain.DropSideBottom || a.Action != domain.DropStack {
			t.Fatalf("bottom = %+v", a)
		}
		a = w.AnchorAt(src, 9, r.X+r.W/2, r.Y+r.H/2, Anchor{}, p)
		center := Rect{X: r.X + bx + 5, Y: r.Y + by + 5, W: r.W - 2*bx - 10, H: r.H - 2*by - 10}
		if a.Side != domain.DropSideCenter || a.Action != domain.DropTab || a.Rect != center {
			t.Fatalf("center = %+v, want rect %+v", a, center)
		}
	})
}

func TestAnchorNoneSideWidensCenter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		r := rectOf(t, w, 2)
		bx, by := bands(r)
		var p domain.DropPolicy
		p.SetWindow(domain.DropSideLeft, domain.DropNone)
		p = p.Over(domain.DefaultDropPolicy())
		a := w.AnchorAt(src, 9, r.X+20, r.Y+r.H/2, Anchor{}, p)
		center := Rect{X: r.X + 5, Y: r.Y + by + 5, W: r.W - bx - 10, H: r.H - 2*by - 10}
		if a.Kind != AnchorWindow || a.Side != domain.DropSideCenter || a.Rect != center {
			t.Fatalf("%+v, want center %+v", a, center)
		}
		// A center at none has no zone at all.
		p.SetWindow(domain.DropSideCenter, domain.DropNone)
		if a := w.AnchorAt(src, 9, r.X+r.W/2, r.Y+r.H/2, Anchor{}, p); a.Kind != AnchorNone {
			t.Fatalf("%+v", a)
		}
	})
}

func TestAnchorFloatVeto(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		w.AddWindow(3)
		w.FocusID(3)
		w.ToggleFloating()
		f := rectOf(t, w, 3)
		p := domain.DefaultDropPolicy()
		if a := w.AnchorAt(src, 9, f.X+f.W/2, f.Y+f.H/2, Anchor{}, p); a.Kind != AnchorNone {
			t.Fatalf("over a float: %+v", a)
		}
	})
}

func TestAnchorHysteresis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		r := rectOf(t, w, 2)
		p := domain.DefaultDropPolicy()
		x := r.X + r.W/2
		top := w.AnchorAt(src, 9, x, r.Y+40, Anchor{}, p)
		if top.Side != domain.DropSideTop {
			t.Fatalf("%+v", top)
		}
		edge := top.Rect.Y + top.Rect.H - 1 // last row inside the zone
		if a := w.AnchorAt(src, 9, x, edge+7, top, p); a != top {
			t.Fatalf("7 px past: %+v", a)
		}
		if a := w.AnchorAt(src, 9, x, edge+9, top, p); a.Side != domain.DropSideCenter {
			t.Fatalf("9 px past: %+v", a)
		}
		// A zone the policy removed is not kept.
		var off domain.DropPolicy
		off.SetWindow(domain.DropSideTop, domain.DropNone)
		if a := w.AnchorAt(src, 9, x, edge+1, top, off.Over(p)); a == top {
			t.Fatal("kept a removed zone")
		}
	})
}

func TestAnchorGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		a1, a2 := rectOf(t, w, 1), rectOf(t, w, 2)
		mid := (a1.X + a1.W + a2.X) / 2
		p := domain.DefaultDropPolicy()
		a := w.AnchorAt(src, 9, mid, a1.Y+a1.H/2, Anchor{}, p)
		if a.Kind != AnchorGap || a.At != 1 || a.Window != 2 || a.Action != domain.DropColumn ||
			a.Rect.X != mid-gapPad || a.Rect.W != 2*gapPad {
			t.Fatalf("%+v", a)
		}
		var off domain.DropPolicy
		off.SetGap(domain.DropNone)
		if a := w.AnchorAt(src, 9, mid, a1.Y+a1.H/2, Anchor{}, off.Over(p)); a.Kind == AnchorGap {
			t.Fatalf("gap at none: %+v", a)
		}
	})
}

func TestAnchorAtNoAllocs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.SetOutput(1000, 800)
		w.SetGaps(10)
		w.Overflow = OverflowScroll
		for id := WindowID(1); id <= 4; id++ {
			w.AddWindow(id)
		}
		p := domain.DefaultDropPolicy()
		var prev Anchor
		x := 0
		allocs := testing.AllocsPerRun(200, func() {
			x = (x + 37) % 1000
			prev = w.AnchorAt(w, 1, x, 300, prev, p)
		})
		if allocs != 0 {
			t.Fatalf("AnchorAt allocates %.1f times per call", allocs)
		}
	})
}

// A workspace without columns has nothing to scroll: no scroll edge, and
// a step is a no-op rather than a panic.
func TestAnchorNoScrollEdgeWithoutColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		w.RemoveWindow(1)
		w.RemoveWindow(2)
		p := domain.DefaultDropPolicy()
		for _, x := range []int{5, 995} {
			if a := w.AnchorAt(src, 9, x, 400, Anchor{}, p); a.Kind == AnchorEdge {
				t.Errorf("x %d: scroll edge on an empty workspace: %+v", x, a)
			}
		}
		if w.ScrollStep(-1) || w.ScrollStep(1) {
			t.Fatal("an empty workspace scrolled")
		}
	})
}

// Edge bands win over the zone kept by hysteresis.
func TestAnchorEdgeBeatsHysteresis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		p := domain.DefaultDropPolicy()
		r := rectOf(t, w, 1)
		band := w.AnchorAt(src, 9, edgeBand+10, r.Y+r.H/2, Anchor{}, p)
		if band.Kind != AnchorWindow || band.Side != domain.DropSideLeft {
			t.Fatalf("%+v", band)
		}
		// Within the band's hysteresis and the left edge band.
		x := band.Rect.X - anchorHysteresis + 1
		if x >= edgeBand {
			t.Fatalf("test point %d off the edge band", x)
		}
		if a := w.AnchorAt(src, 9, x, r.Y+r.H/2, band, p); a.Kind != AnchorEdge || a.Side != domain.DropSideLeft {
			t.Fatalf("edge hidden by the kept band: %+v", a)
		}
		// An empty workspace: the edge is reachable over its whole depth.
		e, src := anchorWorkspace()
		e.RemoveWindow(1)
		e.RemoveWindow(2)
		empty := e.AnchorAt(src, 9, 500, 400, Anchor{}, p)
		if empty.Kind != AnchorGap {
			t.Fatalf("%+v", empty)
		}
		f := e.dropFrame()
		y := f.Y + f.H - edgeBand
		if a := e.AnchorAt(src, 9, 500, y, empty, p); a.Kind != AnchorEdge || a.Side != domain.DropSideBottom {
			t.Fatalf("y %d: bottom edge hidden by the empty zone: %+v", y, a)
		}
	})
}

// A stash or float edge passes the tile through a column: a full fixed
// workspace refuses one from elsewhere, but not its own tile.
func TestEdgeStashFloatOnFullWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace()
		w.SetOverflow(OverflowFixed)
		if !w.full() {
			t.Fatal("not full")
		}
		for _, act := range []domain.DropAction{domain.DropStash, domain.DropFloat} {
			a := Anchor{Kind: AnchorEdge, Side: domain.DropSideRight, Action: act, Dir: 1}
			if w.DropValid(src, 9, a) {
				t.Errorf("%v from another workspace onto a full one is valid", act)
			}
			if !w.DropValid(w, 1, a) {
				t.Errorf("%v of its own tile is invalid", act)
			}
		}
	})
}

// A tile shared with others pops out of its own column through its side
// bands; its other zones and a lone tile's bands stay closed.
func TestAnchorOwnSideBands(t *testing.T) {
	p := domain.DefaultDropPolicy()
	for _, tc := range []struct {
		name   string
		w      *Workspace
		id     WindowID
		want   [][]WindowID
		bars   int
		tabbed bool
	}{
		{"tabbed", func() *Workspace { w := tabbedWorkspace(OverflowScroll); w.Columns[0].Focus = 2; return w }(), 3, [][]WindowID{{1, 2}, {3}, {4}}, 1, true},
		{"stacked", cols(OverflowScroll, 3, 0, []WindowID{1, 2, 3}, []WindowID{4}), 3, [][]WindowID{{1, 2}, {3}, {4}}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := tc.w
				w.SetOutput(1000, 800)
				r := rectOf(t, w, tc.id)
				for _, pt := range [][2]int{{r.X + r.W/2, r.Y + r.H/2}, {r.X + r.W/2, r.Y + 30}, {r.X + r.W/2, r.Y + r.H - 30}} {
					if a := w.AnchorAt(w, tc.id, pt[0], pt[1], Anchor{}, p); a.Kind != AnchorNone && a.Window == tc.id {
						t.Fatalf("own zone at %v: %+v", pt, a)
					}
				}
				x, y := r.X+r.W-30, r.Y+r.H/2
				a := w.AnchorAt(w, tc.id, x, y, Anchor{}, p)
				if a.Kind != AnchorWindow || a.Window != tc.id || a.Side != domain.DropSideRight || a.Action != domain.DropColumn {
					t.Fatalf("right band = %+v", a)
				}
				if n := testing.AllocsPerRun(50, func() { w.AnchorAt(w, tc.id, x, y, a, p) }); n != 0 {
					t.Fatalf("AnchorAt allocates %v", n)
				}
				Drop(w, w, tc.id, a, x, y)
				if got := ids(w); !slices.EqualFunc(got, tc.want, slices.Equal) {
					t.Fatalf("columns %v, want %v", got, tc.want)
				}
				if w.Columns[0].Tabbed != tc.tabbed {
					t.Fatalf("source tabbed %v", w.Columns[0].Tabbed)
				}
			})
		})
	}
	t.Run("lone", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{2})
			w.SetOutput(1000, 800)
			r := rectOf(t, w, 1)
			for _, x := range []int{r.X + 30, r.X + r.W - 30} {
				if a := w.AnchorAt(w, 1, x, r.Y+r.H/2, Anchor{}, p); a.Kind == AnchorWindow && a.Window == 1 {
					t.Fatalf("lone own band at %d: %+v", x, a)
				}
			}
		})
	})
}

// A side band removed only because its drop changes nothing keeps the
// centre symmetric, and the pointer over that band targets the centre.
func TestAnchorInvalidSideKeepsCenterSymmetric(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, _ := anchorWorkspace()
		r := rectOf(t, w, 2)
		bx, by := bands(r)
		p := domain.DefaultDropPolicy()
		center := Rect{X: r.X + bx + 5, Y: r.Y + by + 5, W: r.W - 2*bx - 10, H: r.H - 2*by - 10}
		a := w.AnchorAt(w, 1, r.X+r.W/2, r.Y+r.H/2, Anchor{}, p)
		if a.Kind != AnchorWindow || a.Side != domain.DropSideCenter || a.Rect != center {
			t.Fatalf("center = %+v, want rect %+v", a, center)
		}
		for _, prev := range []Anchor{{}, a} {
			b := w.AnchorAt(w, 1, r.X+20, r.Y+r.H/2, prev, p)
			if b != a {
				t.Fatalf("left band from %+v = %+v, want %+v", prev, b, a)
			}
		}
	})
}
