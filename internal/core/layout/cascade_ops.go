package layout

import "slices"

// markBands freezes the band starts into cascadeState.
func (w *Lanes) markBands() {
	// v shares the cascadeState pointer, so SetBandStart writes stay
	// visible to v.startsBand reads in the next iteration.
	v := w.Snapshot()
	n := 0
	for i := range w.Columns {
		s := v.startsBand(i, n)
		w.SetBandStart(i, s)
		if s {
			n = 0
		}
		n++
	}
}

// bandList copies the cascade columns into their bands.
func (w *Lanes) bandList() [][]Column {
	var bands [][]Column
	w.WalkBands(func(_, first, last int) bool {
		bands = append(bands, slices.Clone(w.Columns[first:last+1]))
		return true
	})
	return bands
}

// setBands stores bands as the columns, empty bands dropped, and focuses
// the column holding window focus.
func (w *Lanes) setBands(bands [][]Column, focus WindowID) {
	w.ensureKeys()
	cols := w.Columns[:0]
	var starts []bool
	for _, b := range bands {
		for i, c := range b {
			starts = append(starts, i == 0)
			if c.Key == 0 {
				c.Key = w.newKey()
			}
			cols = append(cols, c)
		}
	}
	w.Columns = cols
	w.cascadeState().bandStart = starts
	if i := w.ColumnOf(focus); i >= 0 {
		w.Focus = i
	}
}

// fits reports whether cols can join band b: it has room for them and
// neither side holds a maximized column, which keeps a band to itself.
func (w *Lanes) fits(bands [][]Column, b int, cols []Column) bool {
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
func (w *Lanes) sendBand(bands [][]Column, b, dir, at int, cols []Column) [][]Column {
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
func (w Edit) shiftBand(lo, hi, dir, at int) bool {
	if len(w.Columns) == 0 || lo < 0 || hi >= len(w.Columns) || lo > hi {
		return false
	}
	b := w.Band(lo)
	if w.Band(hi) != b {
		return false
	}
	first, last := w.bandBounds(b)
	if lo == first && hi == last && (b+dir < 0 || b+dir > w.Band(len(w.Columns)-1)) {
		return false // Alone at the outer band: nothing to cross.
	}
	id := w.Focused
	bands := w.bandList()
	cols := slices.Clone(bands[b][lo-first : hi-first+1])
	for k := range cols {
		w.cascadeCol(cols[k]).ownBand = false
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
func (w Edit) isolateBand() {
	b := w.Band(w.Focus)
	first, last := w.bandBounds(b)
	if first == last {
		return
	}
	id := w.Focused
	bands := w.bandList()
	p := w.Focus - first
	left := slices.Clone(bands[b][:p])
	right := slices.Clone(bands[b][p+1:])
	c := bands[b][p]
	// Both lists keep the band's left-to-right order. Earlier pushes
	// (cycle-column-width) took the far ends of the band, so the new ones
	// were nearer the column.
	w.cascadeCol(c).pushedUp = append(w.cascadeCol(c).pushedUp, columnIDs(left)...)
	w.cascadeCol(c).pushedDown = append(columnIDs(right), w.cascadeCol(c).pushedDown...)
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
func (w *Lanes) fullscreenBand(id WindowID) bool {
	i := w.ColumnOf(id)
	if i < 0 {
		return false
	}
	b := w.Band(i)
	first, last := w.bandBounds(b)
	if len(w.Columns[i].Windows) == 1 && first == last {
		return false
	}
	fc := w.Columns[w.Focus]
	focus := fc.Selected()
	bands := w.bandList()
	col := bands[b][i-first]
	nc := col
	if len(col.Windows) == 1 {
		// The column leaves its lane, as deleteColumn: manual widths of the
		// lane no longer add up. It keeps its slot and its own width.
		w.ClearSpans(i)
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
	if nc.Key == 0 {
		nc.Key = w.newKey()
	}
	w.cascadeCol(nc).ownBand = true
	bands = slices.Insert(bands, b+1, []Column{nc})
	w.setBands(bands, focus)
	return true
}

// pushNeighbor makes room in the focused column's full cascade band for a
// wider column: the column at the far end of the band leaves it. The
// rightmost goes to the band below; from the last column, the leftmost
// goes to the band above, at its right end. The column remembers it.
func (w Edit) pushNeighbor() {
	first, last := w.bandBounds(w.Band(w.Focus))
	if first == last {
		return
	}
	id := w.Focused
	c := w.Columns[w.Focus]
	if w.Focus < last {
		w.cascadeCol(c).pushedDown = append(w.cascadeCol(c).pushedDown, columnIDs(w.Columns[last:last+1])...)
		w.shiftBand(last, last, 1, 0)
	} else {
		w.cascadeCol(c).pushedUp = append(columnIDs(w.Columns[first:first+1]), w.cascadeCol(c).pushedUp...)
		w.shiftBand(first, first, -1, max(w.MaxColumns, 1))
	}
	w.Focus = w.ColumnOf(id)
}

// restoreBand brings back the columns isolateBand or pushNeighbor sent
// away, those still in the adjacent ordinary band (across automatic
// own-band isolates), as many as the band has room
// for, nearest to the focused column first: the ones sent up at the start
// of the band, the ones sent down at its end, in their former order. The
// others stay where they are. It reports whether the column remembered
// any.
func (w Edit) restoreBand() bool {
	c := w.Columns[w.Focus]
	up, down := w.cascadeCol(c).pushedUp, w.cascadeCol(c).pushedDown
	w.cascadeCol(c).pushedUp, w.cascadeCol(c).pushedDown = nil, nil
	if len(up) == 0 && len(down) == 0 {
		return false
	}
	id := w.Focused
	bands := w.bandList()
	b := w.Band(w.Focus)
	room := max(max(w.MaxColumns, 1)-len(bands[b]), 0)
	// Automatically isolated fullscreen bands do not separate a column
	// from its remembered neighbors. User-shaped bands still do: ordinary
	// ones and one whose column the user maximized.
	ownOnly := make([]bool, len(bands))
	for i, band := range bands {
		ownOnly[i] = len(band) > 0 && !slices.ContainsFunc(band, func(c Column) bool { return !w.cascadeCol(c).ownBand || c.FullWidth })
	}
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
				adjacent := !slices.ContainsFunc(nbs[:k], func(n int) bool { return !ownOnly[n] })
				i := slices.IndexFunc(bands[nb], func(c Column) bool {
					// A column the user maximized keeps its band.
					return c.Windows[0] == e[head] && !c.FullWidth && (adjacent || w.cascadeCol(c).ownBand)
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
	// reach crosses automatic own-band isolates, but looks only one band
	// beyond the first ordinary band, as before: user moves remain boundaries.
	reach := func(dir int) []int {
		var nbs []int
		for n := b + dir; n >= 0 && n < len(bands); n += dir {
			nbs = append(nbs, n)
			if len(nbs) > 1 && !ownOnly[n-dir] {
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

// AdoptFloat inserts the column half of an eligible float promotion below
// the selected band. Workspace checks eligibility and removes the float.
func (cascadeLayout) AdoptFloat(w Edit, id WindowID) bool {
	focus := w.Focused
	bands := w.bandList()
	at := 0
	if len(w.Columns) > 0 {
		at = w.Band(w.Focus) + 1
	}
	col := Column{Windows: []WindowID{id}, Key: w.newKey()}
	w.cascadeCol(col).ownBand = true
	bands = slices.Insert(bands, at, []Column{col})
	w.setBands(bands, focus)
	return true
}
