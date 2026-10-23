package core

// startsBand reports whether column i starts a band, n being the count of
// columns in the band before it.
func (w *Workspace) startsBand(i, n int) bool {
	if i == 0 {
		return true
	}
	c := w.Columns
	return w.bandStart(i) || n >= max(w.MaxColumns, 1) || c[i].FullWidth || c[i-1].FullWidth
}

// walkBands calls f with each band and its column range, in order, until f
// returns false.
func (w *Workspace) walkBands(f func(b, first, last int) bool) {
	b, first, n := 0, 0, 0
	for i := range w.Columns {
		if i > first && w.startsBand(i, n) {
			if !f(b, first, i-1) {
				return
			}
			b, first, n = b+1, i, 0
		}
		n++
	}
	if len(w.Columns) > 0 {
		f(b, first, len(w.Columns)-1)
	}
}

// band is the cascade band of column i.
func (w *Workspace) band(i int) (band int) {
	w.walkBands(func(b, _, last int) bool {
		band = b
		return last < i
	})
	return band
}

// bandBounds is the column range of band b, first > last past the bands.
func (w *Workspace) bandBounds(b int) (first, last int) {
	first, last = 0, -1
	if b < 0 {
		return first, last
	}
	w.walkBands(func(n, f, l int) bool {
		if n == b {
			first, last = f, l
		}
		return n < b
	})
	return first, last
}
