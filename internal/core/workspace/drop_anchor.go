package workspace

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/geom"
	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

const (
	// anchorHysteresis keeps the previous zone while the pointer stays
	// within this many logical px past its rect.
	anchorHysteresis = 8
	// Window side bands are a quarter of the tile, clamped to these bounds
	// and to a third of the tile.
	anchorBandMin = 48
	anchorBandMax = 160
)

// AnchorKind is the kind of drop zone under the pointer.
type AnchorKind uint8

const (
	AnchorNone AnchorKind = iota
	AnchorWindow
	AnchorGap
	AnchorEdge
)

// Anchor is a drop zone of a dragged tile. It is a value: comparing two
// anchors tells whether the zone changed.
type Anchor struct {
	Kind AnchorKind
	Side domain.DropSide
	// Window is the hovered tile (AnchorWindow), or the first window of
	// column At, 0 for the end (AnchorGap).
	Window WindowID
	// At is the column insert index (AnchorGap).
	At int
	// Action is the resolved action, never DropAuto.
	Action domain.DropAction
	// Dir is -1 or +1 (AnchorEdge).
	Dir int
	// Rect is the zone, output-local.
	Rect Rect
}

// AnchorAt is the drop zone of tile id, dragged from src, at the
// output-local point x, y under policy p. prev is the zone of the last
// motion: it is kept while the point stays near it and it still exists.
// Hit order: floats veto, then edges, then the kept zone, then gaps,
// then tiles. It does not allocate once its scratch has grown (owner
// goroutine only).
func (w *Workspace) AnchorAt(src *Workspace, id WindowID, x, y int, prev Anchor, p domain.DropPolicy) Anchor {
	w.anchorBuf = w.AppendLayout(w.anchorBuf[:0])
	layout := w.anchorBuf
	for _, pl := range slices.Backward(layout) {
		// Floats and the stash are over the tiles: nothing lands there.
		if pl.Floating && !pl.Hidden && !pl.Below && pl.ID != id && contains(pl.Rect, x, y) {
			return Anchor{}
		}
	}
	d := w.dropCtx(src, id)
	cols := w.visibleColumns(layout)
	// Edges come before the zones they overlap: hysteresis keeps a
	// window or gap zone only where no edge band answers.
	kept := prev.Kind != AnchorNone && contains(grow(prev.Rect, anchorHysteresis), x, y) &&
		w.zoneExists(layout, cols, d, prev, p)
	if kept && prev.Kind == AnchorEdge {
		return prev
	}
	if a, ok := w.edgeAt(d, x, y, p); ok {
		return a
	}
	if kept {
		return prev
	}
	if w.noColumns(cols, id) {
		if !contains(w.Usable, x, y) {
			return Anchor{}
		}
		return w.emptyAnchor()
	}
	if a, ok := w.gapAnchor(cols, d, x, y, p, -1); ok {
		return a
	}
	for _, pl := range layout {
		if pl.Floating || pl.Hidden || !contains(pl.Rect, x, y) || w.ColumnOf(pl.ID) < 0 {
			continue
		}
		on := w.sidesOn(d, pl.ID, p)
		side := windowSide(pl.Rect, x, y, on)
		if on&(1<<domain.DropSideCenter) == 0 && side == domain.DropSideCenter {
			return Anchor{}
		}
		return Anchor{Kind: AnchorWindow, Side: side, Window: pl.ID, Action: p.Window[side], Rect: w.windowZone(pl.Rect, side, policyOn(p))}
	}
	return Anchor{}
}

// dropSource is what validity needs to know about the dragged tile.
type dropSource struct {
	id   WindowID
	same bool
	// col and row are its place in src; lone is true when it is alone in
	// its column. col is -1 when src does not tile it.
	col, row int
	lone     bool
	// full is true when id is fullscreen in src.
	full bool
	src  *Workspace
}

func (w *Workspace) dropCtx(src *Workspace, id WindowID) dropSource {
	d := dropSource{id: id, same: src == w, col: -1, row: -1, src: src}
	if src == nil {
		return d
	}
	if d.col = src.ColumnOf(id); d.col >= 0 {
		c := src.Columns[d.col].Windows
		d.row = slices.Index(c, id)
		d.lone = len(c) == 1
		d.full = src.Fullscreen == id
	}
	return d
}

// DropValid reports whether dropping tile id from src on anchor a changes
// something and fits w.
func (w *Workspace) DropValid(src *Workspace, id WindowID, a Anchor) bool {
	return w.valid(w.dropCtx(src, id), a)
}

func (w *Workspace) valid(d dropSource, a Anchor) bool {
	if d.col < 0 {
		return false
	}
	switch a.Kind {
	case AnchorWindow:
		ci := w.ColumnOf(a.Window)
		if ci < 0 {
			return false
		}
		if a.Window == d.id {
			// A tile shared with others pops out of its own column
			// through its side bands; nothing else lands on itself.
			if !d.same || d.lone || a.Action != domain.DropColumn {
				return false
			}
			switch a.Side {
			case domain.DropSideLeft:
				return w.columnValid(d, ci)
			case domain.DropSideRight:
				return w.columnValid(d, ci+1)
			}
			return false
		}
		// A tabbed column shows one window: nothing stacks above or
		// below it.
		if w.Columns[ci].Tabbed && (a.Side == domain.DropSideTop || a.Side == domain.DropSideBottom) {
			return false
		}
		switch a.Action {
		case domain.DropTab:
			return a.Side == domain.DropSideCenter && !slices.Contains(w.Columns[ci].Windows, d.id)
		case domain.DropStack:
			row := slices.Index(w.Columns[ci].Windows, a.Window)
			// Above the window just below it, or below the one just above: no move.
			if d.same && ci == d.col && (a.Side == domain.DropSideTop && row == d.row+1 ||
				a.Side == domain.DropSideBottom && row == d.row-1) {
				return false
			}
			return a.Side == domain.DropSideTop || a.Side == domain.DropSideBottom
		case domain.DropColumn:
			at := ci
			if a.Side == domain.DropSideRight {
				at++
			}
			return w.columnValid(d, at)
		case domain.DropSwap:
			if d.same && ci == d.col {
				return false
			}
			// Columns exchange as a whole; single tiles must be swappable.
			return d.same && d.lone || d.src.markable(d.id) && w.markable(a.Window)
		}
	case AnchorGap:
		return a.Action == domain.DropColumn && w.columnValid(d, a.At)
	case AnchorEdge:
		switch a.Action {
		case domain.DropColumn:
			at := 0
			if a.Dir > 0 {
				at = len(w.Columns)
			}
			return w.columnValid(d, at)
		case domain.DropStash, domain.DropFloat:
			// receive adds the tile as a column of w first: a full one has
			// no room for it.
			return !d.full && (d.same || !w.full())
		case domain.DropScroll:
			// EdgeAction checked the axis; there must be something to scroll.
			return len(w.Columns) > 0 && !w.Pinned()
		case domain.DropWorkspace:
			// A hover action: EdgeAction already checked the axis.
			return true
		}
	}
	return false
}

// columnValid reports whether a new column at index at is a move that fits.
func (w *Workspace) columnValid(d dropSource, at int) bool {
	if d.same && d.lone && (at == d.col || at == d.col+1) {
		return false
	}
	return !w.full() || d.same && d.lone
}

// sidesOn is the mask of the zones of tile win (bit per DropSide) whose
// action is set and valid.
func (w *Workspace) sidesOn(d dropSource, win WindowID, p domain.DropPolicy) uint8 {
	var on uint8
	for s := domain.DropSideCenter; s <= domain.DropSideBottom; s++ {
		if p.Window[s] != domain.DropNone && w.valid(d, Anchor{Kind: AnchorWindow, Side: s, Window: win, Action: p.Window[s]}) {
			on |= 1 << s
		}
	}
	return on
}

// policyOn is the zone mask of the sides policy p does not set to none.
// The centre is drawn from it, so a band removed only because its drop
// would change nothing leaves the centre symmetric; the pointer over that
// band still hits the centre.
func policyOn(p domain.DropPolicy) uint8 {
	var on uint8
	for s := domain.DropSideCenter; s <= domain.DropSideBottom; s++ {
		if p.Window[s] != domain.DropNone {
			on |= 1 << s
		}
	}
	return on
}

// noColumns reports whether w shows no column id could land beside.
func (w *Workspace) noColumns(cols []visibleColumn, id WindowID) bool {
	return len(w.Columns) == 0 || w.ColumnOf(id) < 0 && len(cols) == 0
}

// emptyAnchor inserts a column at the end of a workspace that shows none.
func (w *Workspace) emptyAnchor() Anchor {
	g := w.Gap()
	r := Rect{X: w.Usable.X + g, Y: w.Usable.Y + g, W: max(w.Usable.W-2*g, 0), H: max(w.Usable.H-2*g, 0)}
	return Anchor{Kind: AnchorGap, At: len(w.Columns), Action: domain.DropColumn, Rect: r}
}

// visibleColumns fills the scratch with the columns on screen, in order,
// each with the union of its windows' rects.
func (w *Workspace) visibleColumns(layout []Placement) []visibleColumn {
	cols := w.anchorCols[:0]
	for _, pl := range layout {
		if pl.Floating || pl.Hidden || !pl.Rect.Overlaps(w.Usable) {
			continue
		}
		ci := w.ColumnOf(pl.ID)
		if ci < 0 {
			continue
		}
		if n := len(cols); n > 0 && cols[n-1].index == ci {
			cols[n-1].rect = geom.Union(cols[n-1].rect, pl.Rect)
		} else {
			cols = append(cols, visibleColumn{index: ci, rect: pl.Rect})
		}
	}
	w.anchorCols = cols
	return cols
}

// zoneExists reports whether prev is still a valid zone of the layout
// under p, with the same rect and action.
func (w *Workspace) zoneExists(layout []Placement, cols []visibleColumn, d dropSource, prev Anchor, p domain.DropPolicy) bool {
	switch prev.Kind {
	case AnchorWindow:
		if p.Window[prev.Side] != prev.Action {
			return false
		}
		for _, pl := range layout {
			if pl.ID == prev.Window && !pl.Floating && !pl.Hidden {
				if w.ColumnOf(pl.ID) < 0 {
					return false
				}
				on := w.sidesOn(d, pl.ID, p)
				return on&(1<<prev.Side) != 0 && w.windowZone(pl.Rect, prev.Side, policyOn(p)) == prev.Rect
			}
		}
	case AnchorGap:
		if w.noColumns(cols, d.id) {
			return prev == w.emptyAnchor()
		}
		a, ok := w.gapAnchor(cols, d, 0, 0, p, prev.At)
		return ok && a == prev
	case AnchorEdge:
		a, ok := w.edgeAnchor(d, prev.Side, p)
		return ok && a == prev
	}
	return false
}

// gapAnchor is the gap zone at x, y in scroll overflow. With only >= 0 it
// skips the hit-test and returns the zone inserting at column only.
func (w *Workspace) gapAnchor(cols []visibleColumn, d dropSource, x, y int, p domain.DropPolicy, only int) (Anchor, bool) {
	if !w.scrollsRow() || p.Gap == domain.DropNone || len(cols) == 0 {
		return Anchor{}, false
	}
	pad := max(w.Gap()/2, gapPad)
	for i := 0; i <= len(cols); i++ {
		var mid, at int
		var ref Rect
		switch {
		case i == 0:
			ref = cols[0].rect
			mid, at = ref.X-w.Gap()/2, cols[0].index
		case i == len(cols):
			ref = cols[i-1].rect
			mid, at = ref.X+ref.W+w.Gap()/2, cols[i-1].index+1
		default:
			a, b := cols[i-1].rect, cols[i].rect
			ref = geom.Union(a, b)
			mid, at = (a.X+a.W+b.X)/2, cols[i].index
		}
		if only >= 0 {
			if at != only {
				continue
			}
		} else if x < mid-pad || x >= mid+pad || y < ref.Y || y >= ref.Y+ref.H {
			continue
		}
		var first WindowID
		if at < len(w.Columns) {
			c := w.Columns[at].Windows
			first = c[0]
			if first == d.id && len(c) > 1 {
				first = c[1]
			}
		}
		a := Anchor{Kind: AnchorGap, Window: first, At: at, Action: p.Gap,
			Rect: Rect{X: mid - gapPad, Y: ref.Y, W: 2 * gapPad, H: ref.H}}
		if !w.valid(d, a) {
			// A removed zone: the point falls through to the tiles.
			return Anchor{}, false
		}
		return a, true
	}
	return Anchor{}, false
}

// bandOn reports whether side is in the zone mask on.
func bandOn(on uint8, side domain.DropSide) bool { return on&(1<<side) != 0 }

// bands are the side band sizes of tile rect r.
func bands(r Rect) (bx, by int) {
	bx = min(min(max(r.W/4, anchorBandMin), anchorBandMax), r.W/3)
	by = min(min(max(r.H/4, anchorBandMin), anchorBandMax), r.H/3)
	return bx, by
}

// windowSide is the zone of tile rect r holding x, y, among the zones in
// mask p. A side without a band leaves its space to the center; where a horizontal and a vertical
// band overlap, the smaller depth relative to the band wins (ties go to
// the horizontal one).
func windowSide(r Rect, x, y int, p uint8) domain.DropSide {
	bx, by := bands(r)
	h, dx := domain.DropSideCenter, 0
	if l, rt := x-r.X, r.X+r.W-1-x; bandOn(p, domain.DropSideLeft) && l < bx && (l <= rt || !bandOn(p, domain.DropSideRight)) {
		h, dx = domain.DropSideLeft, l
	} else if bandOn(p, domain.DropSideRight) && rt < bx {
		h, dx = domain.DropSideRight, rt
	}
	v, dy := domain.DropSideCenter, 0
	if t, b := y-r.Y, r.Y+r.H-1-y; bandOn(p, domain.DropSideTop) && t < by && (t <= b || !bandOn(p, domain.DropSideBottom)) {
		v, dy = domain.DropSideTop, t
	} else if bandOn(p, domain.DropSideBottom) && b < by {
		v, dy = domain.DropSideBottom, b
	}
	switch {
	case v == domain.DropSideCenter:
		return h
	case h == domain.DropSideCenter:
		return v
	case dx*by <= dy*bx:
		return h
	}
	return v
}

// windowZone is the zone rect of side of tile rect r, inset by half the
// gap (at least 2 px). The center spans what the enabled bands leave.
func (w *Workspace) windowZone(r Rect, side domain.DropSide, p uint8) Rect {
	bx, by := bands(r)
	z := r
	switch side {
	case domain.DropSideLeft:
		z.W = bx
	case domain.DropSideRight:
		z.X, z.W = r.X+r.W-bx, bx
	case domain.DropSideTop:
		z.H = by
	case domain.DropSideBottom:
		z.Y, z.H = r.Y+r.H-by, by
	default:
		if bandOn(p, domain.DropSideLeft) {
			z.X, z.W = z.X+bx, z.W-bx
		}
		if bandOn(p, domain.DropSideRight) {
			z.W -= bx
		}
		if bandOn(p, domain.DropSideTop) {
			z.Y, z.H = z.Y+by, z.H-by
		}
		if bandOn(p, domain.DropSideBottom) {
			z.H -= by
		}
	}
	return inset(z, max(w.Gap()/2, 2))
}

// inset shrinks r by d on every side, never below zero size.
func inset(r Rect, d int) Rect {
	d = min(d, r.W/2, r.H/2)
	return Rect{X: r.X + d, Y: r.Y + d, W: r.W - 2*d, H: r.H - 2*d}
}

// grow enlarges r by d on every side.
func grow(r Rect, d int) Rect {
	return Rect{X: r.X - d, Y: r.Y - d, W: r.W + 2*d, H: r.H + 2*d}
}

// Drop moves tile id from src to anchor a of w, possibly another
// workspace; the tile is then focused in w. px, py is the pointer,
// output-local. An invalid anchor changes nothing.
func Drop(src, w *Workspace, id WindowID, a Anchor, px, py int) {
	if !w.DropValid(src, id, a) {
		return
	}
	if a.Kind == AnchorEdge {
		w.applyEdge(src, id, a, px, py)
		return
	}
	switch a.Action {
	case domain.DropStack:
		w.applyStack(src, id, a.Window, a.Side == domain.DropSideBottom)
	case domain.DropColumn:
		at := a.At
		if a.Kind == AnchorWindow {
			at = w.ColumnOf(a.Window)
			if a.Side == domain.DropSideRight {
				at++
			}
		}
		w.applyColumn(src, id, at)
	case domain.DropSwap:
		w.applySwap(src, id, a.Window)
	case domain.DropTab:
		w.applyTab(src, id, a.Window)
	}
}

// applyTab inserts id below target in target's column, which becomes
// tabbed with id the visible tab (applyStack activates it).
func (w *Workspace) applyTab(src *Workspace, id, target WindowID) {
	w.applyStack(src, id, target, true)
	if ci := w.ColumnOf(id); ci >= 0 && w.ColumnOf(target) == ci {
		w.Columns[ci].Tabbed = true
		w.Columns[ci].Shares = nil
	}
}

// DropBelow stacks tile id from src below anchor in w, as a mark move
// does. It does nothing when id is not a tile of src.
func DropBelow(src, w *Workspace, id, anchor WindowID) {
	if src.ColumnOf(id) < 0 {
		return
	}
	w.applyStack(src, id, anchor, true)
}

// takeTile removes tile id from src and returns it as a column: a lone
// tile keeps its column's cells.
func takeTile(src *Workspace, id WindowID) layout.Column {
	ci := src.ColumnOf(id)
	cells := 0
	if len(src.Columns[ci].Windows) == 1 {
		cells = src.Columns[ci].Cells
	}
	src.dropSlotOf(id)
	src.RemoveWindow(id)
	return layout.Column{Windows: []WindowID{id}, Cells: cells}
}

// applyStack inserts id above or below anchor, in anchor's column.
func (w *Workspace) applyStack(src *Workspace, id, anchor WindowID, below bool) {
	col := takeTile(src, id)
	ai := w.ColumnOf(anchor)
	if ai < 0 {
		w.receive(col, -1)
		return
	}
	tc := &w.Columns[ai]
	row := slices.Index(tc.Windows, anchor)
	if below {
		row++
	}
	if row == 0 {
		tc.Slot = 0
	}
	tc.Windows = slices.Insert(tc.Windows, row, id)
	tc.Shares = nil
	w.Activate(id)
}

// applyColumn inserts id as a new column at index at of w, counted
// before id leaves src.
func (w *Workspace) applyColumn(src *Workspace, id WindowID, at int) {
	ci := src.ColumnOf(id)
	lone := len(src.Columns[ci].Windows) == 1
	if src == w && lone && ci < at {
		at--
	}
	w.receive(takeTile(src, id), at)
}

// applySwap exchanges a lone tile's column with target's in the same
// workspace. Otherwise the two tiles trade places, across workspaces too.
// id is then focused in w.
func (w *Workspace) applySwap(src *Workspace, id, target WindowID) {
	ci := src.ColumnOf(id)
	if src == w && len(src.Columns[ci].Windows) == 1 {
		to := w.ColumnOf(target)
		w.Columns[ci], w.Columns[to] = w.Columns[to], w.Columns[ci]
		w.FocusID(id)
		return
	}
	if SwapWindows(src, id, w, target) {
		w.FocusID(id)
	}
}

// applyEdge runs the release action of edge anchor a. Hover actions
// (scroll, workspace) change nothing on release.
func (w *Workspace) applyEdge(src *Workspace, id WindowID, a Anchor, px, py int) {
	switch a.Action {
	case domain.DropColumn:
		at := 0
		if a.Dir > 0 {
			at = len(w.Columns)
		}
		w.applyColumn(src, id, at)
	case domain.DropStash, domain.DropFloat:
		if src != w {
			w.receive(takeTile(src, id), -1)
		}
		w.FocusID(id)
		if a.Action == domain.DropStash {
			w.ToggleWindowStash()
			return
		}
		w.ToggleFloating()
		// Centre the new float on the pointer.
		if r, ok := w.placed(id); ok {
			w.DragFloat(id, r, 0, px-(r.X+r.W/2), py-(r.Y+r.H/2), false)
		}
	}
}
