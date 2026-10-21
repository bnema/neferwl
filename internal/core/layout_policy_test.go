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
	w.dropStaleSpans()
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
