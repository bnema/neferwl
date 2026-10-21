package core

import "testing"

func TestWorkspaceStepFollowsWorkspaceAxis(t *testing.T) {
	for _, tc := range []struct {
		name       string
		overflow   Overflow
		vertical   int
		horizontal int
	}{
		{"scroll", OverflowScroll, 1, 0},
		{"fixed", OverflowFixed, 1, 0},
		{"cascade", OverflowCascade, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Workspace{Overflow: tc.overflow}
			p := w.policy()
			for _, dir := range []int{-1, 1} {
				if got := p.workspaceStep(verticalAxis, dir); got != dir*tc.vertical {
					t.Errorf("vertical step %d = %d, want %d", dir, got, dir*tc.vertical)
				}
				if got := p.workspaceStep(horizontalAxis, dir); got != dir*tc.horizontal {
					t.Errorf("horizontal step %d = %d, want %d", dir, got, dir*tc.horizontal)
				}
			}
		})
	}
}

// oldValidSpans is the reference implementation validSpans must match.
func oldValidSpans(w *Workspace, first, last int) bool {
	if first > last || w.Overflow == OverflowFixed && !w.laneRow() {
		return false
	}
	stamp := w.Columns[first].spanStamp
	if stamp == 0 {
		return false
	}
	total, count := 0, 0
	for i := first; i <= last; i++ {
		if w.Columns[i].Span <= 0 || w.Columns[i].spanStamp != stamp {
			return false
		}
		total += w.Columns[i].Span
	}
	for _, c := range w.Columns {
		if c.spanStamp == stamp {
			count++
		}
	}
	return count == last-first+1 && (!w.policy().equalCells || total == 10000)
}

func TestScrollWidthsMatchPerColumnValidation(t *testing.T) {
	for _, n := range []int{1, 2, 5, 9} {
		for _, state := range []string{"unstamped", "stamped", "stale", "foreign", "zero"} {
			w := spanWorkspace(OverflowScroll, n)
			switch state {
			case "stamped":
				stampSpans(w, 0, n-1)
			case "stale":
				stampSpans(w, 0, n-1)
				w.Columns[n-1].spanStamp = 99
			case "foreign":
				stampSpans(w, 0, n-1)
				w.Columns = append(w.Columns, Column{Windows: []WindowID{100}, Span: 5, spanStamp: w.Columns[0].spanStamp})
			case "zero":
				stampSpans(w, 0, n-1)
				w.Columns[0].Span = 0
			}
			last := len(w.Columns) - 1
			if got, want := w.validSpans(0, last), oldValidSpans(w, 0, last); got != want {
				t.Fatal(n, state, got, want)
			}
			valid := w.scrollSpansValid()
			xs, widths := w.columnEdges()
			rs := w.columnRects()
			for i := range w.Columns {
				if widths[i] != w.scrollWidth(i, oldValidSpans(w, 0, last)) || valid != oldValidSpans(w, 0, last) {
					t.Fatal(n, state, i, "width")
				}
				if xs[i] != w.columnX(i) || widths[i] != w.columnWidth(i) || rs[i].W != widths[i] {
					t.Fatal(n, state, i, "edges")
				}
			}
		}
	}
}

func TestDropStaleSpansKeepsValidLanes(t *testing.T) {
	w := spanWorkspace(OverflowScroll, 4)
	// An unstamped workspace returns before any lane is measured.
	if n := testing.AllocsPerRun(10, w.dropStaleSpans); n != 0 {
		t.Fatalf("unstamped workspace allocates %v", n)
	}
	if w.Columns[0].Span != 0 || w.Columns[0].spanStamp != 0 {
		t.Fatal("unstamped workspace changed")
	}
	stampSpans(w, 0, 3)
	spans := []int{w.Columns[0].Span, w.Columns[3].Span}
	if n := testing.AllocsPerRun(10, w.dropStaleSpans); n != 0 {
		t.Fatalf("valid lane allocates %v", n)
	}
	if w.Columns[0].Span != spans[0] || w.Columns[3].Span != spans[1] {
		t.Fatal("valid lane dropped")
	}
	w.Columns[2].spanStamp = 77
	w.dropStaleSpans()
	for i, c := range w.Columns {
		if c.Span != 0 || c.spanStamp != 0 {
			t.Fatal("stale lane kept", i)
		}
	}
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
			w := spanWorkspace(tc.mode, 3)
			tc.setup(w)
			if tc.mode != OverflowScroll && w.scrollSpansValid() {
				t.Fatal("equal-cell workspace must not take the scroll span path")
			}
			xs, ws := w.columnEdges()
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
	}
}
