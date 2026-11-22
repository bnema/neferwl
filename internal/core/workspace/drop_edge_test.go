package workspace

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

func TestEdgeAction(t *testing.T) {
	L, R, T, B := domain.DropSideLeft, domain.DropSideRight, domain.DropSideTop, domain.DropSideBottom
	none, auto, ws, sc := domain.DropNone, domain.DropAuto, domain.DropWorkspace, domain.DropScroll
	for _, tc := range []struct {
		overflow Overflow
		side     domain.DropSide
		in, want domain.DropAction
	}{
		{OverflowScroll, L, auto, sc}, {OverflowScroll, R, auto, sc},
		{OverflowScroll, T, auto, ws}, {OverflowScroll, B, auto, ws},
		{OverflowScroll, L, ws, none}, {OverflowScroll, T, sc, none},
		{OverflowCascade, L, auto, ws}, {OverflowCascade, T, auto, sc},
		{OverflowCascade, B, ws, none}, {OverflowCascade, R, sc, none},
		{OverflowFixed, T, auto, ws}, {OverflowFixed, L, auto, none},
		{OverflowFixed, L, sc, none}, {OverflowFixed, R, domain.DropStash, domain.DropStash},
		{OverflowFixed, domain.DropSideCenter, domain.DropColumn, none},
	} {
		synctest.Test(t, func(t *testing.T) {
			w := workspace()
			w.SetOverflow(tc.overflow)
			if got := w.EdgeAction(tc.side, tc.in); got != tc.want {
				t.Errorf("%s side %d %v = %v, want %v", tc.overflow, tc.side, tc.in, got, tc.want)
			}
		})
	}
}

func TestEdgeAnchors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, src := anchorWorkspace() // scroll: left/right scroll, top/bottom workspace
		p := domain.DefaultDropPolicy()
		for _, tc := range []struct {
			x, y int
			side domain.DropSide
			a    domain.DropAction
			dir  int
		}{
			{5, 400, domain.DropSideLeft, domain.DropScroll, -1},
			{995, 400, domain.DropSideRight, domain.DropScroll, 1},
			{500, 3, domain.DropSideTop, domain.DropWorkspace, -1},
			{500, 797, domain.DropSideBottom, domain.DropWorkspace, 1},
			{10, 20, domain.DropSideLeft, domain.DropScroll, -1},   // corner: 10 < 20
			{20, 10, domain.DropSideTop, domain.DropWorkspace, -1}, // corner: 10 < 20
			{15, 15, domain.DropSideLeft, domain.DropScroll, -1},   // tie: left/right
		} {
			a := w.AnchorAt(src, 9, tc.x, tc.y, Anchor{}, p)
			if a.Kind != AnchorEdge || a.Side != tc.side || a.Action != tc.a || a.Dir != tc.dir {
				t.Errorf("(%d,%d) = %+v", tc.x, tc.y, a)
			}
		}
		f := w.dropFrame()
		if a := w.AnchorAt(src, 9, 5, 400, Anchor{}, p); a.Rect != (Rect{X: f.X, Y: f.Y, W: edgeBand, H: f.H}) {
			t.Errorf("left rect %+v", a.Rect)
		}
		// An explicit workspace on a content edge has no zone: the tile under it answers.
		var q domain.DropPolicy
		q.SetEdge(domain.DropSideLeft, domain.DropWorkspace)
		if a := w.AnchorAt(src, 9, 20, 400, Anchor{}, q.Over(p)); a.Kind == AnchorEdge {
			t.Errorf("explicit workspace on the left edge: %+v", a)
		}
		// Hysteresis keeps an edge zone.
		left := w.AnchorAt(src, 9, 5, 400, Anchor{}, p)
		if a := w.AnchorAt(src, 9, edgeBand+7, 400, left, p); a != left {
			t.Errorf("edge not kept: %+v", a)
		}
	})
}

func TestEdgeAnchorsByLayout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := domain.DefaultDropPolicy()
		for _, tc := range []struct {
			overflow Overflow
			x, y     int
			want     domain.DropAction // DropNone: no edge zone
		}{
			{OverflowCascade, 5, 400, domain.DropWorkspace},
			{OverflowCascade, 500, 3, domain.DropScroll},
			{OverflowFixed, 500, 3, domain.DropWorkspace},
			{OverflowFixed, 5, 400, domain.DropNone},
		} {
			w, src := anchorWorkspace()
			w.SetOverflow(tc.overflow)
			a := w.AnchorAt(src, 9, tc.x, tc.y, Anchor{}, p)
			got := domain.DropNone
			if a.Kind == AnchorEdge {
				got = a.Action
			}
			if got != tc.want {
				t.Errorf("%s (%d,%d): %+v", tc.overflow, tc.x, tc.y, a)
			}
		}
	})
}

func TestEdgeRelease(t *testing.T) {
	edge := func(side domain.DropSide, a domain.DropAction) domain.DropPolicy {
		var p domain.DropPolicy
		p.SetEdge(side, a)
		return p.Over(domain.DefaultDropPolicy())
	}
	t.Run("column", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, src := anchorWorkspace()
			a := w.AnchorAt(src, 9, 5, 400, Anchor{}, edge(domain.DropSideLeft, domain.DropColumn))
			Drop(src, w, 9, a, 5, 400)
			b := w.AnchorAt(w, 1, 995, 400, Anchor{}, edge(domain.DropSideRight, domain.DropColumn))
			Drop(w, w, 1, b, 995, 400)
			if got := ids(w); len(got) != 3 || got[0][0] != 9 || got[1][0] != 2 || got[2][0] != 1 {
				t.Fatal(got)
			}
		})
	})
	t.Run("stash", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, src := anchorWorkspace()
			a := w.AnchorAt(src, 9, 995, 400, Anchor{}, edge(domain.DropSideRight, domain.DropStash))
			if a.Action != domain.DropStash {
				t.Fatalf("%+v", a)
			}
			Drop(src, w, 9, a, 995, 400)
			if w.StashIndex(9) < 0 || len(src.Columns) != 0 {
				t.Fatalf("not stashed: %v / %v", ids(w), ids(src))
			}
		})
	})
	t.Run("float", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, _ := anchorWorkspace()
			a := w.AnchorAt(w, 1, 995, 400, Anchor{}, edge(domain.DropSideRight, domain.DropFloat))
			Drop(w, w, 1, a, 700, 300)
			if !w.IsFloat(1) {
				t.Fatal("not floating")
			}
			r, ok := w.placed(1)
			// Centred on the pointer, kept inside the usable area: the
			// float is as tall as the tile was, so only x can centre.
			if !ok || r.X+r.W/2 != 700 || !w.Usable.Contains(r) {
				t.Fatalf("float %+v not centred on x 700 inside %+v", r, w.Usable)
			}
		})
	})
	t.Run("hover edges change nothing on release", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, _ := anchorWorkspace()
			a := w.AnchorAt(w, 1, 5, 400, Anchor{}, domain.DefaultDropPolicy())
			if a.Action != domain.DropScroll {
				t.Fatalf("%+v", a)
			}
			Drop(w, w, 1, a, 5, 400)
			if got := ids(w); len(got) != 2 || got[0][0] != 1 {
				t.Fatal(got)
			}
		})
	})
	t.Run("fullscreen cannot stash", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, _ := anchorWorkspace()
			w.SetFullscreen(1, true)
			a := Anchor{Kind: AnchorEdge, Side: domain.DropSideRight, Action: domain.DropStash, Dir: 1}
			if w.DropValid(w, 1, a) {
				t.Fatal("fullscreen stash valid")
			}
		})
	})
}
