package core

import "slices"

// Cascade bands are explicit: Column.BandStart marks the first column of
// each band, and moves keep the other bands as they are. A band also ends
// where it would pass MaxColumns columns (max-columns shrank, or a column
// was inserted into a full band), and a maximized column sits alone in its
// band. scroll freezes those ends into BandStart, so the bands stay when
// max-columns grows again or the column is unmaximized. Cells only share
// a band's width (laneWidth); they never move a column to a band.

// startsBand reports whether column i starts a band, n being the count of
// columns in the band before it.
func (w *Workspace) startsBand(i, n int) bool {
	if i == 0 {
		return true
	}
	c := w.Columns
	return c[i].BandStart || n >= max(w.MaxColumns, 1) || c[i].FullWidth || c[i-1].FullWidth
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

// markBands freezes the band starts into BandStart.
func (w *Workspace) markBands() {
	n := 0
	for i := range w.Columns {
		s := w.startsBand(i, n)
		w.Columns[i].BandStart = s
		if s {
			n = 0
		}
		n++
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

// cellX is the x of column i in its lane, from the usable area's left edge.
func (w *Workspace) cellX(i int) int {
	first, last := w.laneBounds(w.laneOf(i))
	S, u, sum := w.laneStats(first, last)
	x := w.gap()
	valid := w.validSpans(first, last)
	for j := first; j < i; j++ {
		x += w.laneWidth(j, first, last, S, u, sum, valid) + w.gap()
	}
	return x
}
func (w *Workspace) cascadeBands() (n, spacing int) {
	if len(w.Columns) == 0 || w.Usable.H <= 0 {
		return 1, 0
	}
	return w.band(len(w.Columns)-1) + 1, w.Usable.H
}

// cellWidth is the width of column i in its lane: the columns of a band
// (the whole row in fixed overflow) share the usable width by their cells,
// auto columns sharing what the others leave, so a lone column fills it.
func (w *Workspace) cellWidth(i int) int {
	first, last := w.laneBounds(w.laneOf(i))
	S, u, sum := w.laneStats(first, last)
	return w.laneWidth(i, first, last, S, u, sum, w.validSpans(first, last))
}

// laneStats measures the lane first..last: S is the sum of the cells of
// its set columns, u the count of its auto columns and sum the total of
// their weights (laneWeight).
func (w *Workspace) laneStats(first, last int) (S, u, sum int) {
	for i := first; i <= last; i++ {
		if c := w.Columns[i].Cells; c > 0 {
			S += c
		} else {
			u++
		}
	}
	for i := first; i <= last; i++ {
		sum += w.laneWeight(i, S, u)
	}
	return S, u, sum
}

// laneWeight is the share of column i in its lane, given its stats. With
// room left for every auto column, they share the cells the set ones leave
// and the set ones keep their cells; otherwise an auto column weighs one
// cell.
func (w *Workspace) laneWeight(i, S, u int) int {
	k := max(w.MaxColumns, 1)
	c := w.Columns[i].Cells
	switch {
	case u == 0:
		return c
	case k-S >= u:
		if c > 0 {
			return c * u
		}
		return k - S
	case c > 0:
		return c
	}
	return 1
}

// laneWidth is the width of column i of the lane first..last with the
// stats of laneStats: the usable width after gaps, split by weight. The
// last column receives the pixel remainder, just as for stamped spans.
// The caller passes the lane's validSpans so a loop over the lane checks it once.
func (w *Workspace) laneWidth(i, first, last, S, u, sum int, spansValid bool) int {
	if spansValid {
		avail := max(w.Usable.W-w.gap()*(last-first+2), 0)
		if i == last {
			used := 0
			for j := first; j < last; j++ {
				used += avail * w.Columns[j].Span / 10000
			}
			return avail - used
		}
		return avail * w.Columns[i].Span / 10000
	}
	if sum <= 0 {
		return 0
	}
	avail := max(w.Usable.W-w.gap()*(last-first+2), 0)
	// floor(avail*weight/sum) without overflowing on a huge area.
	width := func(j int) int {
		weight := w.laneWeight(j, S, u)
		return avail/sum*weight + avail%sum*weight/sum
	}
	if i == last {
		used := 0
		for j := first; j < last; j++ {
			used += width(j)
		}
		return avail - used
	}
	return width(i)
}

// equalShare is the width of each of k columns sharing the usable width
// after gaps; a remainder under k pixels stays empty.
func (w *Workspace) equalShare(k int) int {
	k = max(k, 1)
	return max((w.Usable.W-w.gap()*(k+1))/k, 0)
}

// focusBand focuses the column in the band dir away; a maximized column
// keeps its own band, so it stays maximized.
func (w *Workspace) focusBand(dir int) bool {
	i := w.laneNeighbor(dir)
	if i < 0 {
		return false
	}
	w.Focus = i
	w.Columns[i].Focus = 0
	if dir < 0 {
		w.Columns[i].Focus = len(w.Columns[i].Windows) - 1
	}
	w.raiseColumns()
	w.scroll()
	return true
}

// cascadeRectsAt is cascadeRectsInto with the view slide's shift given.
func (w *Workspace) cascadeRectsAt(dst []Rect, ignoreFullWidth bool, shift int) []Rect {
	g := w.gap()
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + shift
	w.walkBands(func(lane, first, last int) bool {
		S, u, sum := w.laneStats(first, last)
		x := g
		valid := w.validSpans(first, last)
		for i := first; i <= last; i++ {
			cw := w.laneWidth(i, first, last, S, u, sum, valid)
			r := Rect{X: w.Usable.X + x, Y: w.Usable.Y + g + lane*w.Usable.H - view, W: cw, H: max(w.Usable.H-2*g, 0)}
			if w.Columns[i].FullWidth && !ignoreFullWidth {
				r.X = w.Usable.X + g
				r.W = max(w.Usable.W-2*g, 0)
			}
			rects[i] = r
			x += cw + g
		}
		return true
	})
	return rects
}

func (w *Workspace) cascadePoints() []float64 {
	n, spacing := w.cascadeBands()
	points := make([]float64, n)
	for i := range points {
		points[i] = float64(i * spacing)
	}
	return points
}

func (w *Workspace) cascadeFocus(view int) int {
	if len(w.Columns) == 0 {
		return 0
	}
	b := min(max(view/max(w.Usable.H, 1), 0), w.band(len(w.Columns)-1))
	return w.laneNeighbor(b - w.band(w.Focus))
}

// laneOf is the lane of column i in the overview: its band, or the only
// lane of a layout that does not wrap.
func (w *Workspace) laneOf(i int) int {
	if w.policy().wraps {
		return w.band(i)
	}
	return 0
}

// laneBounds is the range of columns of a lane, first > last when the lane
// does not exist. A layout that does not wrap has the whole row as lane 0.
func (w *Workspace) laneBounds(lane int) (first, last int) {
	if w.policy().wraps {
		return w.bandBounds(lane)
	}
	if lane != 0 || len(w.Columns) == 0 {
		return 0, -1
	}
	return 0, len(w.Columns) - 1
}

// leavesBand reports whether column i, where a left/right move from the
// focused column goes, is past the workspace or in another cascade band:
// the move then goes to the neighbor monitor first.
func (w *Workspace) leavesBand(i int) bool {
	return i < 0 || i >= len(w.Columns) || w.policy().wraps && w.band(i) != w.band(w.Focus)
}

// moveBand moves the focused cascade column to the band dir away
// (move-window-up/down past its column), keeping its offset in the band;
// focus follows it. See shiftBand.
func (w *Workspace) moveBand(dir int) {
	if !w.policy().wraps || len(w.Columns) == 0 {
		return
	}
	first, _ := w.bandBounds(w.band(w.Focus))
	if w.shiftBand(w.Focus, w.Focus, dir, w.Focus-first) {
		w.scroll()
	}
}

// laneNeighbor is the closest column to the focus in the lane d lanes away,
// the focus's offset in its lane kept; -1 past the first or last lane.
func (w *Workspace) laneNeighbor(d int) int {
	lane := w.laneOf(w.Focus)
	first, _ := w.laneBounds(lane)
	nfirst, nlast := w.laneBounds(lane + d)
	if nfirst > nlast {
		return -1
	}
	return min(nfirst+w.Focus-first, nlast)
}

// bandList copies the cascade columns into their bands.
func (w *Workspace) bandList() [][]Column {
	var bands [][]Column
	w.walkBands(func(_, first, last int) bool {
		bands = append(bands, slices.Clone(w.Columns[first:last+1]))
		return true
	})
	return bands
}

// setBands stores bands as the columns, empty bands dropped, and focuses
// the column holding window focus.
func (w *Workspace) setBands(bands [][]Column, focus WindowID) {
	cols := w.Columns[:0]
	for _, b := range bands {
		for i, c := range b {
			c.BandStart = i == 0
			cols = append(cols, c)
		}
	}
	w.Columns = cols
	if i := w.columnOf(focus); i >= 0 {
		w.Focus = i
	}
}

// fits reports whether cols can join band b: it has room for them and
// neither side holds a maximized column, which keeps a band to itself.
func (w *Workspace) fits(bands [][]Column, b int, cols []Column) bool {
	if b < 0 || b >= len(bands) || len(bands[b])+len(cols) > max(w.MaxColumns, 1) {
		return false
	}
	full := func(c Column) bool { return c.FullWidth }
	return !slices.ContainsFunc(bands[b], full) && !slices.ContainsFunc(cols, full)
}

// sendBand moves cols, already taken out of band b, to the band dir away:
// they join it at index at (clamped) when they fit, else they make a new
// band between the two, or past the outer band. When cols were alone in
// band b, it goes away and a band that cannot take them is passed: the
// new band goes on its far side, so repeated moves always progress.
func (w *Workspace) sendBand(bands [][]Column, b, dir, at int, cols []Column) [][]Column {
	t, nb := b+dir, b // A new band between b and t, before b going up.
	if dir > 0 {
		nb = b + 1
	}
	if len(bands[b]) == 0 {
		bands = slices.Delete(bands, b, b+1)
		t, nb = b-1, b-1 // Going up: before band t.
		if dir > 0 {
			t, nb = b, b+1 // Going down: after band t.
		}
	}
	if w.fits(bands, t, cols) {
		at = min(max(at, 0), len(bands[t]))
		bands[t] = slices.Insert(bands[t], at, cols...)
		return bands
	}
	return slices.Insert(bands, min(max(nb, 0), len(bands)), slices.Clone(cols))
}

// shiftBand moves the columns lo..hi of one cascade band to the band dir
// away (see sendBand), at offset at in it. It reports whether they moved.
func (w *Workspace) shiftBand(lo, hi, dir, at int) bool {
	if len(w.Columns) == 0 || lo < 0 || hi >= len(w.Columns) || lo > hi {
		return false
	}
	b := w.band(lo)
	if w.band(hi) != b {
		return false
	}
	first, last := w.bandBounds(b)
	if lo == first && hi == last && (b+dir < 0 || b+dir > w.band(len(w.Columns)-1)) {
		return false // Alone at the outer band: nothing to cross.
	}
	id, _ := w.Focused()
	bands := w.bandList()
	cols := slices.Clone(bands[b][lo-first : hi-first+1])
	for k := range cols {
		cols[k].ownBand = false
	}
	bands[b] = slices.Delete(bands[b], lo-first, hi-first+1)
	w.setBands(w.sendBand(bands, b, dir, at, cols), id)
	return true
}

// columnIDs lists the windows of each column, in order: its identity for
// restoreBand. The column is the one headed by the first of them still
// heading a column, so it is found after its first window closed or its
// stack changed order, and a window that left it for a column of its own
// comes after the one that stayed first.
func columnIDs(cols []Column) [][]WindowID {
	ids := make([][]WindowID, len(cols))
	for i, c := range cols {
		ids[i] = slices.Clone(c.Windows)
	}
	return ids
}

// isolateBand gives the focused cascade column a band of its own: the
// columns on its left go to the band above, the ones on its right to the
// band below (sendBand), and the column remembers them for restoreBand.
func (w *Workspace) isolateBand() {
	b := w.band(w.Focus)
	first, last := w.bandBounds(b)
	if first == last {
		return
	}
	id, _ := w.Focused()
	bands := w.bandList()
	p := w.Focus - first
	left := slices.Clone(bands[b][:p])
	right := slices.Clone(bands[b][p+1:])
	c := bands[b][p]
	// Both lists keep the band's left-to-right order. Earlier pushes
	// (cycle-column-width) took the far ends of the band, so the new ones
	// were nearer the column.
	c.pushedUp = append(c.pushedUp, columnIDs(left)...)
	c.pushedDown = append(columnIDs(right), c.pushedDown...)
	bands[b] = []Column{c}
	if len(right) > 0 {
		bands = w.sendBand(bands, b, 1, 0, right)
	}
	if len(left) > 0 {
		bands = w.sendBand(bands, b, -1, max(w.MaxColumns, 1), left)
	}
	w.setBands(bands, id)
}

// fullscreenBand gives tiled window id, about to go fullscreen, a band of
// its own right below the band it is in, so the other columns keep their
// band and a focus change among them never touches the fullscreen output.
// A window stacked in a column is expelled into a column of its own first.
// Leaving fullscreen does not move it back: the column keeps its band, so
// nothing jumps when the user leaves. Only cascade layouts do this; the
// focus stays on the same window.
func (w *Workspace) fullscreenBand(id WindowID) {
	i := w.columnOf(id)
	if !w.policy().wraps || i < 0 {
		return
	}
	b := w.band(i)
	first, last := w.bandBounds(b)
	if len(w.Columns[i].Windows) == 1 && first == last {
		return
	}
	fc := w.Columns[w.Focus]
	focus := fc.Windows[fc.Focus]
	bands := w.bandList()
	col := bands[b][i-first]
	nc := col
	if len(col.Windows) == 1 {
		// The column leaves its lane, as deleteColumn: manual widths of the
		// lane no longer add up. It keeps its slot and its own width.
		w.clearSpans(i)
		bands[b] = slices.Delete(bands[b], i-first, i-first+1)
	} else {
		j := slices.Index(col.Windows, id)
		// The expelled window gets a plain column: no Span (a manual width
		// is the stack's, and the new band is alone), no shares.
		nc = Column{Windows: []WindowID{id}, Cells: col.Cells}
		col.Windows = slices.Delete(slices.Clone(col.Windows), j, j+1)
		col.Shares = nil
		if j == 0 {
			col.Slot = 0 // the slot window left
		}
		if col.Focus > j || col.Focus >= len(col.Windows) {
			col.Focus = max(min(col.Focus-1, len(col.Windows)-1), 0)
		}
		bands[b][i-first] = col
	}
	nc.ownBand = true
	bands = slices.Insert(bands, b+1, []Column{nc})
	w.setBands(bands, focus)
}

// pushNeighbor makes room in the focused column's full cascade band for a
// wider column: the column at the far end of the band leaves it. The
// rightmost goes to the band below; from the last column, the leftmost
// goes to the band above, at its right end. The column remembers it.
func (w *Workspace) pushNeighbor() {
	first, last := w.bandBounds(w.band(w.Focus))
	if first == last {
		return
	}
	id, _ := w.Focused()
	c := &w.Columns[w.Focus]
	if w.Focus < last {
		c.pushedDown = append(c.pushedDown, columnIDs(w.Columns[last:last+1])...)
		w.shiftBand(last, last, 1, 0)
	} else {
		c.pushedUp = append(columnIDs(w.Columns[first:first+1]), c.pushedUp...)
		w.shiftBand(first, first, -1, max(w.MaxColumns, 1))
	}
	w.Focus = w.columnOf(id)
}

// restoreBand brings back the columns isolateBand or pushNeighbor sent
// away, those still in the adjacent band, as many as the band has room
// for, nearest to the focused column first: the ones sent up at the start
// of the band, the ones sent down at its end, in their former order. The
// others stay where they are. It reports whether the column remembered
// any.
func (w *Workspace) restoreBand() bool {
	c := &w.Columns[w.Focus]
	up, down := c.pushedUp, c.pushedDown
	c.pushedUp, c.pushedDown = nil, nil
	if len(up) == 0 && len(down) == 0 {
		return false
	}
	id, _ := w.Focused()
	bands := w.bandList()
	b := w.band(w.Focus)
	room := max(max(w.MaxColumns, 1)-len(bands[b]), 0)
	// take removes from the bands nbs the remembered columns found there,
	// at most room of them, in the order of ids walked from the focused
	// column outward (rev: ids end nearest).
	take := func(nbs []int, ids [][]WindowID, rev bool) []Column {
		var got []Column
		for k := range ids {
			if room == 0 {
				break
			}
			e := ids[k]
			if rev {
				e = ids[len(ids)-1-k]
			}
			// The column is named across the workspace first: one that
			// moved farther away stays there, with no stand-in.
			head := slices.IndexFunc(e, func(v WindowID) bool {
				return slices.ContainsFunc(w.Columns, func(c Column) bool { return c.Windows[0] == v })
			})
			if head < 0 {
				continue
			}
			for k, nb := range nbs {
				// Past the adjacent band, only a column fullscreenBand put in
				// a band of its own is taken: one the user moved there stays.
				i := slices.IndexFunc(bands[nb], func(c Column) bool {
					return c.Windows[0] == e[head] && (k == 0 || c.ownBand)
				})
				if i < 0 {
					continue
				}
				got = append(got, bands[nb][i])
				bands[nb] = slices.Delete(bands[nb], i, i+1)
				room--
				break
			}
		}
		if rev {
			slices.Reverse(got)
		}
		return got
	}
	// reach lists the bands dir away to look in: the adjacent one, and the
	// next, where a column that fullscreenBand split off may lie.
	reach := func(dir int) []int {
		var nbs []int
		for n := b + dir; n >= 0 && n < len(bands); n += dir {
			nbs = append(nbs, n)
			if len(nbs) == 2 {
				break
			}
		}
		return nbs
	}
	above := take(reach(-1), up, true)
	below := take(reach(1), down, false)
	bands[b] = slices.Concat(above, bands[b], below)
	w.setBands(bands, id)
	return true
}

// spawnIndex is where a new cascade column goes: after the focused band,
// which it joins when it has room; otherwise it starts a band of its own
// between it and the next (newBand).
func (w *Workspace) spawnIndex() (at int, newBand bool) {
	if len(w.Columns) == 0 {
		return 0, true
	}
	b := w.band(w.Focus)
	first, last := w.bandBounds(b)
	bands := [][]Column{w.Columns[first : last+1]}
	return last + 1, !w.fits(bands, 0, []Column{{}})
}

// deleteColumn removes column i; a band it started keeps its start.
func (w *Workspace) deleteColumn(i int) {
	// A removed member cannot be detected by counting surviving stamp members.
	w.clearSpans(i)
	if w.policy().wraps && w.Columns[i].BandStart && i+1 < len(w.Columns) {
		w.Columns[i+1].BandStart = true
	}
	w.Columns = slices.Delete(w.Columns, i, i+1)
}

// forgetBands drops the cascade band starts and pushed columns: the bands
// pack again in column order.
func (w *Workspace) forgetBands() {
	for i := range w.Columns {
		c := &w.Columns[i]
		c.BandStart, c.pushedUp, c.pushedDown = false, nil, nil
	}
}

// fullscreenFloatBand promotes a parentless float into a permanent cascade
// column below the selected column's band. Dialogs and stash entries keep
// their floating placement. Like fullscreenBand, this never changes focus.
func (w *Workspace) fullscreenFloatBand(id WindowID) {
	i := w.floatIndex(id)
	if !w.policy().wraps || i < 0 || w.Floats[i].over != 0 || w.stashIndex(id) >= 0 {
		return
	}
	focus, _ := w.Focused()
	bands := w.bandList()
	at := 0
	if len(w.Columns) > 0 {
		at = w.band(w.Focus) + 1
	}
	w.Floats = slices.Delete(w.Floats, i, i+1)
	bands = slices.Insert(bands, at, []Column{{Windows: []WindowID{id}, ownBand: true}})
	w.setBands(bands, focus)
	if focus == id {
		w.floatFocus, w.stashFocus = false, false
	}
}
