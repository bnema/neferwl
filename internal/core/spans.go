package core

// validSpans requires a complete, exclusive lane stamp: every column has
// a positive span and the same nonzero identity, with no members elsewhere
// in the workspace. Equal-cell lanes must also sum to 10000 basis points.
func (w *View) validSpans(first, last int) bool {
	if first > last || w.InSpiral() {
		return false
	}
	stamp := w.Columns[first].SpanStamp
	if stamp == 0 {
		return false
	}
	total := 0
	for i := first; i <= last; i++ {
		c := w.Columns[i]
		if c.Span <= 0 || c.SpanStamp != stamp {
			return false
		}
		total += c.Span
	}
	count := 0
	for _, c := range w.Columns {
		if c.SpanStamp == stamp {
			count++
		}
	}
	return count == last-first+1 && (total == 10000 || !w.Layout.EqualCells())
}

// dropStaleSpans runs before scroll measures mutated layout geometry. Decide
// validity before clearing any stamps, so shared identities cannot become
// valid merely because an earlier invalid lane was cleared.
func (w *Lanes) dropStaleSpans() {
	stamped := false
	for _, c := range w.Columns {
		if c.Span != 0 || c.SpanStamp != 0 {
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
		_, last := w.LaneBounds(w.LaneOf(first))
		if !w.ValidSpans(first, last) {
			for i := first; i <= last; i++ {
				stale = append(stale, i)
			}
		}
		first = last + 1
	}
	for _, i := range stale {
		w.Columns[i].Span, w.Columns[i].SpanStamp = 0, 0
	}
}

// ClearSpans clears manual widths for the lane containing col.
func (w *Lanes) ClearSpans(col int) {
	if col < 0 || col >= len(w.Columns) {
		return
	}
	first, last := w.LaneBounds(w.LaneOf(col))
	for i := first; i <= last; i++ {
		w.Columns[i].Span = 0
		w.Columns[i].SpanStamp = 0
	}
}
