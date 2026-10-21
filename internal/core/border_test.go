package core

import (
	"slices"
	"testing"
)

func TestBorderAt(t *testing.T) {
	for _, mode := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll} {
		for _, gap := range []int{0, 10} {
			w := spanWorkspace(mode, 2)
			w.SetGaps(gap)
			w.View = 0
			w.view = slide{}
			rs := w.columnRects()
			x := rs[0].X + rs[0].W + gap/2
			y := rs[0].Y + 100
			b, ok := w.borderAt(x, y, 6)
			if !ok || b.kind != borderColumn || b.col != 0 {
				t.Fatal(mode, gap, b, ok)
			}
			if _, ok = w.borderAt(x-5, y, 6); !ok {
				t.Fatal("padding")
			}
			if _, ok = w.borderAt(rs[0].X+20, y, 6); ok {
				t.Fatal("tile interior")
			}
			w.Columns[0].Windows = append(w.Columns[0].Windows, 9)
			rows := rowRects(rs[0], w.Columns[0], gap)
			b, ok = w.borderAt(rs[0].X+20, rows[0].Y+rows[0].H+gap/2, 6)
			if !ok || b.kind != borderRow {
				t.Fatal("row", b, ok)
			}
			w.AddFloating(10, 400, 400)
			f := w.floatRect(w.Floats[0])
			if _, ok = w.borderAt(f.X+f.W/2, f.Y+f.H/2, 6); ok {
				t.Fatal("float")
			}
			w.FocusID(1)
			w.SetFullscreen(1, true)
			if _, ok = w.borderAt(x, y, 6); ok {
				t.Fatal("fullscreen")
			}
		}
	}
	w := spanWorkspace(OverflowFixed, 4)
	rs := w.columnRects()
	if _, ok := w.borderAt(rs[0].X+rs[0].W+5, 100, 8); ok {
		t.Fatal("spiral")
	}
	w = spanWorkspace(OverflowFixed, 2)
	w.Columns[w.Focus].FullWidth = true
	if _, ok := w.borderAt(500, 100, 8); ok {
		t.Fatal("maximized")
	}
	w = spanWorkspace(OverflowCascade, 4)
	rs = w.columnRects()
	if _, ok := w.borderAt(rs[2].X+rs[2].W+5, 100, 8); ok {
		t.Fatal("different bands")
	}
	// Presentation offsets must be applied just like Layout.
	w = spanWorkspace(OverflowScroll, 4)
	w.View = 100
	rs = w.columnRects()
	b, ok := w.borderAt(rs[0].X+rs[0].W+5, 100, 6)
	if !ok || b.col != 0 {
		t.Fatal("view offset", b, ok)
	}
}

func TestSetColumnBorder(t *testing.T) {
	for _, mode := range []Overflow{OverflowFixed, OverflowCascade} {
		for _, n := range []int{2, 3} {
			w := spanWorkspace(mode, n)
			before := w.columnRects()
			b := border{kind: borderColumn, col: 0}
			pos := before[0].X + before[0].W + 5 + 50
			w.setBorder(b, pos)
			after := w.columnRects()
			if after[0].X+after[0].W+5 != pos {
				t.Fatal("position", mode, n, pos, after)
			}
			if n == 3 && after[2] != before[2] {
				t.Fatal("third moved", before, after)
			}
			if after[n-1].X+after[n-1].W != 990 {
				t.Fatal("not pixel filled", after)
			}
			total := 0
			for _, c := range w.Columns {
				total += c.Span
			}
			if total != 10000 {
				t.Fatal(total)
			}
			pair := w.Columns[0].Span + w.Columns[1].Span
			w.setBorder(b, -1000)
			after = w.columnRects()
			if after[0].W < 48 || w.Columns[0].Span+w.Columns[1].Span != pair {
				t.Fatal("minimum", after)
			}
			w.setBorder(b, 10000)
			after = w.columnRects()
			if after[1].W < 48 {
				t.Fatal("right minimum", after)
			}
			w.resetBorder(b)
			for _, c := range w.Columns {
				if c.Span != 0 || c.spanLane != 0 {
					t.Fatal("reset")
				}
			}
		}
	}
}

func TestSetBorderCascadeBand(t *testing.T) {
	w := spanWorkspace(OverflowCascade, 5)
	before := w.columnRects()
	w.setBorder(border{kind: borderColumn, col: 0}, 400)
	after := w.columnRects()
	if !slices.Equal(before[3:], after[3:]) || w.Columns[3].Span != 0 {
		t.Fatal("other band changed", before, after)
	}
}

func TestSetBorderScroll(t *testing.T) {
	w := spanWorkspace(OverflowScroll, 3)
	before := w.columnRects()
	b := border{kind: borderColumn, col: 0}
	pos := before[0].X + before[0].W + 5 + 60
	w.setBorder(b, pos)
	after := w.columnRects()
	if after[0].X+after[0].W+5 != pos {
		t.Fatal("position", after)
	}
	for i := 1; i < len(after); i++ {
		if after[i].W != before[i].W || after[i].X != before[i].X+60 {
			t.Fatal("right columns", before, after)
		}
	}
	saved := w.Columns[1].Span
	w.setBorder(b, 10000)
	if w.columnRects()[0].W != 980 || w.Columns[1].Span != saved {
		t.Fatal("maximum")
	}
	w.setBorder(b, -10000)
	if w.columnRects()[0].W < 49 {
		t.Fatal("minimum")
	}
}

func TestSetRowBorder(t *testing.T) {
	w := spanWorkspace(OverflowFixed, 2)
	w.Columns[0].Windows = []WindowID{1, 3, 4}
	r := w.columnRects()[0]
	before := rowRects(r, w.Columns[0], w.gap())
	b := border{kind: borderRow, col: 0, row: 0}
	pos := before[0].Y + before[0].H + 5 + 30
	w.setBorder(b, pos)
	after := rowRects(r, w.Columns[0], w.gap())
	if after[0].Y+after[0].H+5 != pos || after[2] != before[2] || sum(w.Columns[0].Shares) != 10000 {
		t.Fatal(before, after, w.Columns[0].Shares)
	}
	pair := w.Columns[0].Shares[0] + w.Columns[0].Shares[1]
	w.setBorder(b, -10000)
	if w.Columns[0].Shares[0] != minShare {
		t.Fatal("minimum")
	}
	w.setBorder(b, 10000)
	if w.Columns[0].Shares[1] != minShare || w.Columns[0].Shares[0]+w.Columns[0].Shares[1] != pair {
		t.Fatal("pair")
	}
	w.resetBorder(b)
	if w.Columns[0].Shares != nil {
		t.Fatal("reset")
	}
}
