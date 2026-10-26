package core

// validSpans requires a complete, exclusive lane stamp: every column has
// a positive span and the same nonzero identity, with no members elsewhere
// in the workspace. Equal-cell lanes must also sum to 10000 basis points.
func (w *layoutView) validSpans(first, last int) bool {
	if first > last || w.inSpiral() {
		return false
	}
	stamp := w.Columns[first].spanStamp
	if stamp == 0 {
		return false
	}
	total := 0
	for i := first; i <= last; i++ {
		c := w.Columns[i]
		if c.Span <= 0 || c.spanStamp != stamp {
			return false
		}
		total += c.Span
	}
	count := 0
	for _, c := range w.Columns {
		if c.spanStamp == stamp {
			count++
		}
	}
	return count == last-first+1 && (total == 10000 || !w.layout.equalCells())
}

// dropStaleSpans runs before scroll measures mutated layout geometry. Decide
// validity before clearing any stamps, so shared identities cannot become
// valid merely because an earlier invalid lane was cleared.
func (w *lanes) dropStaleSpans() {
	stamped := false
	for _, c := range w.Columns {
		if c.Span != 0 || c.spanStamp != 0 {
			stamped = true
			break
		}
	}
	if !stamped {
		return
	}
	// A nil stale slice does not allocate, so valid lanes cost nothing here.
	var stale []int
	for first := 0; first < len(w.Columns); {
		_, last := w.laneBounds(w.laneOf(first))
		if !w.validSpans(first, last) {
			for i := first; i <= last; i++ {
				stale = append(stale, i)
			}
		}
		first = last + 1
	}
	for _, i := range stale {
		w.Columns[i].Span, w.Columns[i].spanStamp = 0, 0
	}
}

func (w *lanes) clearSpans(col int) {
	if col < 0 || col >= len(w.Columns) {
		return
	}
	first, last := w.laneBounds(w.laneOf(col))
	for i := first; i <= last; i++ {
		w.Columns[i].Span = 0
		w.Columns[i].spanStamp = 0
	}
}
