package workspace

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/motion"
)

func TestBorderAt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, mode := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll} {
			for _, gap := range []int{0, 10} {
				w := spanWorkspace(mode, 2)
				w.SetGaps(gap)
				w.View = 0
				w.view = motion.Slide{}
				rs := w.columnRects()
				x := rs[0].X + rs[0].W + gap/2
				y := rs[0].Y + 100
				b, ok := w.BorderAt(x, y, 6)
				if !ok || b.Kind != BorderColumn || b.Col != 0 {
					t.Fatal(mode, gap, b, ok)
				}
				if _, ok = w.BorderAt(x-5, y, 6); !ok {
					t.Fatal("padding")
				}
				if _, ok = w.BorderAt(rs[0].X+20, y, 6); ok {
					t.Fatal("tile interior")
				}
				w.Columns[0].Windows = append(w.Columns[0].Windows, 9)
				rows := layout.RowRects(rs[0], w.Columns[0], gap)
				b, ok = w.BorderAt(rs[0].X+20, rows[0].Y+rows[0].H+gap/2, 6)
				if !ok || b.Kind != BorderRow {
					t.Fatal("row", b, ok)
				}
				w.AddFloating(10, 400, 400)
				f := w.floatRect(w.Floats[0])
				if _, ok = w.BorderAt(f.X+f.W/2, f.Y+f.H/2, 6); ok {
					t.Fatal("float")
				}
				w.FocusID(1)
				w.SetFullscreen(1, true)
				if _, ok = w.BorderAt(x, y, 6); ok {
					t.Fatal("fullscreen")
				}
			}
		}
		w := spanWorkspace(OverflowFixed, 4)
		rs := w.columnRects()
		if _, ok := w.BorderAt(rs[0].X+rs[0].W+5, 100, 8); ok {
			t.Fatal("spiral")
		}
		w = spanWorkspace(OverflowFixed, 2)
		w.Columns[w.Focus].FullWidth = true
		if _, ok := w.BorderAt(500, 100, 8); ok {
			t.Fatal("maximized")
		}
		w = spanWorkspace(OverflowCascade, 4)
		rs = w.columnRects()
		if _, ok := w.BorderAt(rs[2].X+rs[2].W+5, 100, 8); ok {
			t.Fatal("different bands")
		}
		// Presentation offsets must be applied just like Layout.
		w = spanWorkspace(OverflowScroll, 4)
		w.View = 100
		rs = w.columnRects()
		b, ok := w.BorderAt(rs[0].X+rs[0].W+5, 100, 6)
		if !ok || b.Col != 0 {
			t.Fatal("view offset", b, ok)
		}

	})
}

func TestSetColumnBorder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, mode := range []Overflow{OverflowFixed, OverflowCascade} {
			for _, n := range []int{2, 3} {
				w := spanWorkspace(mode, n)
				before := w.columnRects()
				b := Border{Kind: BorderColumn, Col: 0}
				pos := before[0].X + before[0].W + 5 + 50
				w.MoveBorder(b, pos)
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
				w.MoveBorder(b, -1000)
				after = w.columnRects()
				if after[0].W < 48 || w.Columns[0].Span+w.Columns[1].Span != pair {
					t.Fatal("minimum", after)
				}
				w.MoveBorder(b, 10000)
				after = w.columnRects()
				if after[1].W < 48 {
					t.Fatal("right minimum", after)
				}
				w.ResetBorder(b)
				for _, c := range w.Columns {
					if c.Span != 0 || c.SpanStamp != 0 {
						t.Fatal("reset")
					}
				}
			}
		}

	})
}

func TestSetBorderCascadeBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := spanWorkspace(OverflowCascade, 5)
		before := w.columnRects()
		w.MoveBorder(Border{Kind: BorderColumn, Col: 0}, 400)
		after := w.columnRects()
		if !slices.Equal(before[3:], after[3:]) || w.Columns[3].Span != 0 {
			t.Fatal("other band changed", before, after)
		}

	})
}

func TestSetBorderScroll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := spanWorkspace(OverflowScroll, 3)
		before := w.columnRects()
		b := Border{Kind: BorderColumn, Col: 0}
		pos := before[0].X + before[0].W + 5 + 60
		w.MoveBorder(b, pos)
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
		w.MoveBorder(b, 10000)
		if w.columnRects()[0].W != 980 || w.Columns[1].Span != saved {
			t.Fatal("maximum")
		}
		w.MoveBorder(b, -10000)
		if w.columnRects()[0].W < 49 {
			t.Fatal("minimum")
		}

	})
}

func TestSetRowBorder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := spanWorkspace(OverflowFixed, 2)
		w.Columns[0].Windows = []WindowID{1, 3, 4}
		r := w.columnRects()[0]
		before := layout.RowRects(r, w.Columns[0], w.Gap())
		b := Border{Kind: BorderRow, Col: 0, Row: 0}
		pos := before[0].Y + before[0].H + 5 + 30
		w.MoveBorder(b, pos)
		after := layout.RowRects(r, w.Columns[0], w.Gap())
		if after[0].Y+after[0].H+5 != pos || after[2] != before[2] || sum(w.Columns[0].Shares) != 10000 {
			t.Fatal(before, after, w.Columns[0].Shares)
		}
		pair := w.Columns[0].Shares[0] + w.Columns[0].Shares[1]
		w.MoveBorder(b, -10000)
		if w.Columns[0].Shares[0] != minShare {
			t.Fatal("minimum")
		}
		w.MoveBorder(b, 10000)
		if w.Columns[0].Shares[1] != minShare || w.Columns[0].Shares[0]+w.Columns[0].Shares[1] != pair {
			t.Fatal("pair")
		}
		w.ResetBorder(b)
		if w.Columns[0].Shares != nil {
			t.Fatal("reset")
		}

	})
}

func TestBorderPixelRemainders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, mode := range []Overflow{OverflowFixed, OverflowCascade} {
			for n := 2; n <= 4; n++ {
				for width := 997; width <= 1003; width++ {
					for pair := 0; pair < n-1; pair++ {
						w := spanWorkspace(mode, n)
						w.SetMaxColumns(n)
						for i := range w.Columns {
							w.SetBandStart(i, false)
						}
						w.SetOutput(width, 800)
						before := w.columnRects()
						b := Border{Kind: BorderColumn, Col: pair}
						pos := before[pair].X + before[pair].W + 5
						w.MoveBorder(b, pos)
						if after := w.columnRects(); !slices.Equal(before, after) {
							t.Fatalf("stamp changed geometry: mode=%v n=%d width=%d pair=%d before=%v after=%v", mode, n, width, pair, before, after)
						}
						for delta := -20; delta <= 20; delta++ {
							w.MoveBorder(b, pos+delta)
							after := w.columnRects()
							for i := range before {
								if i != pair && i != pair+1 && after[i] != before[i] {
									t.Fatalf("untouched rect changed: mode=%v n=%d width=%d pair=%d delta=%d before=%v after=%v", mode, n, width, pair, delta, before, after)
								}
							}
							if after[pair].X+after[pair].W+5 != pos+delta {
								t.Fatal("border missed pointer", before, after)
							}
						}
					}
				}
			}
		}

	})
}

func TestBorderStampIgnoresMaximizedWidth(t *testing.T) {
	for _, mode := range []Overflow{OverflowScroll, OverflowFixed, OverflowCascade} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := spanWorkspace(mode, 3)
				normal := w.columnWidthFor(2, true)
				w.Columns[2].FullWidth = true
				rs := w.columnRects()
				w.MoveBorder(Border{Kind: BorderColumn, Col: 0}, rs[0].X+rs[0].W+15)
				w.Columns[2].FullWidth = false
				if got := w.columnWidth(2); got != normal {
					t.Fatalf("unmaximized width = %d, want %d", got, normal)
				}

			})
		})
	}
}

func TestSetBorderReportsChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, mode := range []Overflow{OverflowScroll, OverflowFixed} {
			w := spanWorkspace(mode, 2)
			b := Border{Kind: BorderColumn, Col: 0}
			x := w.columnRects()[0]
			pos := x.X + x.W + 5 + 40
			if !w.MoveBorder(b, pos) {
				t.Fatal(mode, "first move must report a change")
			}
			if w.MoveBorder(b, pos) {
				t.Fatal(mode, "same position reported a change")
			}
			w.MoveBorder(b, -10000)
			if w.MoveBorder(b, -20000) {
				t.Fatal(mode, "clamped at the minimum reported a change")
			}
		}
		w := spanWorkspace(OverflowFixed, 2)
		w.Columns[0].Windows = []WindowID{1, 3}
		b := Border{Kind: BorderRow, Col: 0}
		r := w.columnRects()[0]
		pos := layout.RowRects(r, w.Columns[0], w.Gap())[0].Y + 200
		if !w.MoveBorder(b, pos) || w.MoveBorder(b, pos) {
			t.Fatal("row border change reporting")
		}
		if w.MoveBorder(Border{Kind: BorderColumn, Col: 9}, 0) {
			t.Fatal("invalid border reported a change")
		}

	})
}

func TestSetBorderUndersizedPairReportsStampOnce(t *testing.T) {
	t.Run("row", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := spanWorkspace(OverflowFixed, 1)
			w.Columns[0].Windows = []WindowID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
			// Ten rows is the most that fit; pixel rounding leaves the last pair
			// just under twice the minimum share.
			b := Border{Kind: BorderRow, Col: 0, Row: 8}
			rows := layout.RowRects(w.columnRects()[0], w.Columns[0], w.Gap())
			pos := rows[8].Y + 1
			if !w.MoveBorder(b, pos) {
				t.Fatal("stamping an undersized row pair must report a change")
			}
			if c := w.Columns[0]; c.Shares[8]+c.Shares[9] >= 2*minShare {
				t.Fatal("row pair not undersized", c.Shares)
			}
			if w.MoveBorder(b, pos) {
				t.Fatal("repeat on an undersized row pair reported a change")
			}

		})
	})
	for _, mode := range []Overflow{OverflowFixed, OverflowCascade} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := spanWorkspace(mode, 3)
				w.border = 300
				b := Border{Kind: BorderColumn, Col: 0}
				pos := w.columnRects()[0].X + 100
				if !w.MoveBorder(b, pos) {
					t.Fatal("stamping an undersized column pair must report a change")
				}
				avail := w.Usable.W - w.Gap()*4
				if pair := w.Columns[0].Span + w.Columns[1].Span; pair >= 2*pixelShare(4*w.border, avail) {
					t.Fatal("column pair not undersized", pair)
				}
				if w.MoveBorder(b, pos) {
					t.Fatal("repeat on an undersized column pair reported a change")
				}

			})
		})
	}
}

func TestRestoreSizes(t *testing.T) {
	w := spanWorkspace(OverflowScroll, 2)
	w.AddWindow(3)
	w.Columns[0].Windows = append(w.Columns[0].Windows, 9)
	w.Columns[0].Shares = []int{4000, 6000}
	stampSpans(w, 0, 1)
	saved := w.SaveSizes()
	spans := [2]int{w.Columns[0].Span, w.Columns[1].Span}

	w.Columns[0].Shares[0] = 1
	w.Columns[0].Span, w.Columns[0].SpanStamp = 7, 0
	w.Columns[1].Span = 3
	if !w.SizesValid(saved) || !w.RestoreSizes(saved) {
		t.Fatal("restore refused on unchanged windows")
	}
	if got := w.Columns[0].Shares; !slices.Equal(got, []int{4000, 6000}) {
		t.Fatalf("shares = %v", got)
	}
	if w.Columns[0].Span != spans[0] || w.Columns[1].Span != spans[1] || w.Columns[0].SpanStamp == 0 {
		t.Fatalf("spans = %d %d stamp %d", w.Columns[0].Span, w.Columns[1].Span, w.Columns[0].SpanStamp)
	}
	w.Columns[0].Shares[0] = 1
	if !w.RestoreSizes(saved) || w.Columns[0].Shares[0] != 4000 {
		t.Fatal("restored shares alias the snapshot")
	}

	// A changed window list leaves the columns untouched.
	w.Columns[0].Shares[0] = 5
	w.Columns[1].Windows = append(w.Columns[1].Windows, 8)
	if w.SizesValid(saved) || w.RestoreSizes(saved) || w.Columns[0].Shares[0] != 5 {
		t.Fatal("restore wrote after the windows changed")
	}
	w.Columns[1].Windows = w.Columns[1].Windows[:1]
	w.AddWindow(4)
	if w.SizesValid(saved) || w.RestoreSizes(saved) || w.Columns[0].Shares[0] != 5 {
		t.Fatal("restore wrote after the column count changed")
	}
}
