package core

import (
	"fmt"
	"testing"
	"testing/synctest"
)

// Each state of a scroll lane's manual widths has a known validity: a complete
// stamp is valid; a stamp one column left, a stamped column outside the lane
// and a zero span are not. A workspace whose every column carries the stamp
// is one lane again, whatever joined it. Valid lanes size columns by span,
// the others by cells.
func TestScrollWidthsByLaneValidity(t *testing.T) {
	for _, n := range []int{1, 2, 5, 9} {
		for _, tc := range []struct {
			state string
			valid bool // the whole workspace is one valid lane
			setup func(w *Workspace)
		}{
			{"unstamped", false, func(w *Workspace) {}},
			{"stamped", true, func(w *Workspace) { stampSpans(w, 0, n-1) }},
			{"stale", n == 1, func(w *Workspace) { stampSpans(w, 0, n-1); w.Columns[n-1].SpanStamp = 99 }},
			{"foreign", true, func(w *Workspace) {
				stampSpans(w, 0, n-1)
				w.Columns = append(w.Columns, Column{Windows: []WindowID{100}, Span: 5, SpanStamp: w.Columns[0].SpanStamp})
			}},
			{"zero", false, func(w *Workspace) { stampSpans(w, 0, n-1); w.Columns[0].Span = 0 }},
		} {
			t.Run(fmt.Sprintf("n=%d/%s", n, tc.state), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					w := spanWorkspace(OverflowScroll, n)
					tc.setup(w)
					last := len(w.Columns) - 1
					if got := w.ValidSpans(0, last); got != tc.valid {
						t.Fatalf("validSpans = %v, want %v", got, tc.valid)
					}
					if tc.state == "foreign" && w.ValidSpans(0, n-1) {
						t.Fatal("a stamped column outside the lane must invalidate it")
					}
					f := w.widthFacts()
					if w.scrollSpansValid() != tc.valid {
						t.Fatalf("spansValid = %v, want %v", w.scrollSpansValid(), tc.valid)
					}
					xs, widths := w.ColumnEdges()
					rs := w.columnRects()
					avail := w.Usable.W - 2*w.Gap()
					for i := range w.Columns {
						if tc.valid {
							if want := w.Columns[i].Span * avail / 10000; widths[i] != want {
								t.Fatalf("column %d width %d, want its span's %d", i, widths[i], want)
							}
						} else if widths[i] != w.scrollWidth(i, f) || widths[i] != w.EqualShare(min(len(w.Columns), 3)) {
							t.Fatalf("column %d width %d, want the cells' %d", i, widths[i], w.EqualShare(min(len(w.Columns), 3)))
						}
						if xs[i] != w.columnX(i) || widths[i] != w.columnWidth(i) || rs[i].W != widths[i] {
							t.Fatalf("column %d: edges (%d,%d), columnX %d, columnWidth %d, rect %d", i, xs[i], widths[i], w.columnX(i), w.columnWidth(i), rs[i].W)
						}
					}

				})
			})
		}
	}
}

func TestDropStaleSpansKeepsValidLanes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := spanWorkspace(OverflowScroll, 4)
		// An unstamped workspace returns before any lane is measured.
		if n := testing.AllocsPerRun(10, w.DropStaleSpans); n != 0 {
			t.Fatalf("unstamped workspace allocates %v", n)
		}
		if w.Columns[0].Span != 0 || w.Columns[0].SpanStamp != 0 {
			t.Fatal("unstamped workspace changed")
		}
		stampSpans(w, 0, 3)
		spans := []int{w.Columns[0].Span, w.Columns[3].Span}
		if n := testing.AllocsPerRun(10, w.DropStaleSpans); n != 0 {
			t.Fatalf("valid lane allocates %v", n)
		}
		if w.Columns[0].Span != spans[0] || w.Columns[3].Span != spans[1] {
			t.Fatal("valid lane dropped")
		}
		w.Columns[2].SpanStamp = 77
		w.DropStaleSpans()
		for i, c := range w.Columns {
			if c.Span != 0 || c.SpanStamp != 0 {
				t.Fatal("stale lane kept", i)
			}
		}

	})
}

// TestColumnEdgesMatchColumnRects checks columnEdges against columnRects, an
// independent path that builds the same row with the view shift applied.
func TestColumnEdgesMatchColumnRects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  Overflow
		setup func(w *Workspace)
		// xs is compared where the rects keep the row's x (not cascade bands,
		// nor the focused maximized column of fixed overflow).
		xs bool
	}{
		{"scroll plain", OverflowScroll, func(w *Workspace) {}, true},
		{"scroll stamped", OverflowScroll, func(w *Workspace) { stampSpans(w, 0, len(w.Columns)-1) }, true},
		{"scroll full width", OverflowScroll, func(w *Workspace) { w.Columns[1].FullWidth = true }, true},
		{"scroll fullscreen", OverflowScroll, func(w *Workspace) { w.SetFullscreen(w.Columns[2].Windows[0], true) }, true},
		{"scroll viewed", OverflowScroll, func(w *Workspace) { w.View = 137 }, true},
		{"fixed", OverflowFixed, func(w *Workspace) {}, true},
		{"fixed full width", OverflowFixed, func(w *Workspace) { w.Columns[1].FullWidth = true; w.Focus = 0 }, true},
		{"fixed focused full width", OverflowFixed, func(w *Workspace) { w.Columns[1].FullWidth = true; w.Focus = 1 }, false},
		{"fixed fullscreen", OverflowFixed, func(w *Workspace) { w.SetFullscreen(w.Columns[2].Windows[0], true) }, true},
		{"fixed stamped", OverflowFixed, func(w *Workspace) { stampSpans(w, 0, len(w.Columns)-1) }, true},
		{"cascade", OverflowCascade, func(w *Workspace) {}, false},
		{"cascade full width", OverflowCascade, func(w *Workspace) { w.Columns[1].FullWidth = true }, false},
		{"cascade stamped", OverflowCascade, func(w *Workspace) { stampSpans(w, 0, len(w.Columns)-1) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := spanWorkspace(tc.mode, 3)
				tc.setup(w)
				if tc.mode != OverflowScroll && w.scrollSpansValid() {
					t.Fatal("equal-cell workspace must not take the scroll span path")
				}
				xs, ws := w.ColumnEdges()
				rs := w.columnRectsAt(nil, false, 0)
				if len(xs) != len(rs) || len(ws) != len(rs) {
					t.Fatal("length", len(xs), len(ws), len(rs))
				}
				for i, r := range rs {
					if ws[i] != r.W {
						t.Errorf("column %d width %d, rect %d", i, ws[i], r.W)
					}
					if tc.xs && xs[i] != r.X+w.View {
						t.Errorf("column %d x %d, rect %d + view %d", i, xs[i], r.X, w.View)
					}
				}

			})
		})
	}
}
