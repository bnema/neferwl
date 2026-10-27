package layout

import (
	"slices"
	"testing"
	"testing/synctest"
)

func TestRowRectsEqualWithoutShares(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := Rect{X: 0, Y: 0, W: 50, H: 103}
		c := Column{Windows: []WindowID{1, 2, 3}}
		if !slices.Equal(RowRects(r, c, 5), StackRectsInto(nil, r, 3, 5)) {
			t.Fatal(RowRects(r, c, 5))
		}
		c.Shares = []int{5000, 5000} // wrong length: ignored
		if !slices.Equal(RowRects(r, c, 5), StackRectsInto(nil, r, 3, 5)) {
			t.Fatal(RowRects(r, c, 5))
		}
		c.Shares = []int{5000, 3000, 3000} // wrong sum: ignored
		if !slices.Equal(RowRects(r, c, 5), StackRectsInto(nil, r, 3, 5)) {
			t.Fatal(RowRects(r, c, 5))
		}
		c.Shares = []int{5000, 3000, 2000}
		rows := RowRects(r, c, 5)
		if rows[0].H != 46 || rows[1].H != 27 || rows[2].H != 93-46-27 || rows[2].Y+rows[2].H != 103 {
			t.Fatal(rows)
		}

	})
}

// TestRectHelpersKeepAppendSemantics: the Into helpers append after dst's
// length and give the same rows as the fresh ones.
func TestRectHelpersKeepAppendSemantics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := Rect{X: 3, Y: 4, W: 50, H: 103}
		pre := []Rect{{W: 1}, {W: 2}}
		got := StackRectsInto(slices.Clone(pre), r, 3, 5)
		if !slices.Equal(got[:2], pre) || !slices.Equal(got[2:], StackRectsInto(nil, r, 3, 5)) {
			t.Fatalf("stackRectsInto: %+v", got)
		}
		c := Column{Windows: []WindowID{1, 2, 3}, Shares: []int{5000, 3000, 2000}}
		got = RowRectsInto(slices.Clone(pre), r, c, 5)
		if !slices.Equal(got[:2], pre) || !slices.Equal(got[2:], RowRects(r, c, 5)) {
			t.Fatalf("rowRectsInto: %+v", got)
		}
		// A reused destination overwrites in place.
		buf := make([]Rect, 0, 8)
		a := StackRectsInto(buf, r, 3, 5)
		b := StackRectsInto(buf, r, 2, 5)
		if &a[0] != &b[0] || len(b) != 2 {
			t.Fatalf("not reused: %d %d", len(a), len(b))
		}

	})
}

// cloneScene copies every slice a scene carries.
