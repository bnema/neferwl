package workspace

import (
	"math"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/core/geom"
	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/motion"
	"github.com/bnema/neferwl/internal/ports"
)

// WindowID identifies a managed window.
type WindowID = ports.WindowID

// Rect is a logical-pixel rectangle.
type Rect = ports.Rect

// Placement describes a window and its presentation geometry.
// It is an alias for geom.Placement during the domain split.
type Placement = geom.Placement

// Workspace owns the window, focus and presentation model. Only the owner
// goroutine may read or mutate its state and reusable layout buffers.
type Workspace struct {
	// Lanes is the lower-level tiled layout state.
	layout.Lanes
	// ID stays with the workspace across reorder and monitor moves.
	ID uint64
	// Name is set for workspaces declared in config; empty for dynamic ones.
	Name string
	// OverviewAfter is the numbered workspace from which this named workspace
	// was invoked. It only orders overview rows; numbering stays unchanged.
	OverviewAfter *Workspace
	// recent holds one window per column the user focused, newest first
	// (switcher.go); columns that went are dropped when read.
	recent []WindowID
	// view is the presentation offset, in logical pixels.
	view motion.Slide
	// monitor is the full logical monitor and reserved its area left by
	// layer surfaces, both from the monitor; size is the override (0 inherits).
	monitor, reserved Rect
	size              [2]int
	// border is drawn around floating windows, inside their rect.
	border int
	// left is the window whose fullscreen the user left for another
	// window: its client requests are refused until it is focused again,
	// so an app that re-asks at once (Wine) does not cover that window.
	left WindowID
	// Floats are native floating windows in user selection order;
	// below/above and dialog groups determine their layout order.
	Floats     []Float
	floatFocus bool
	// Stash holds the windows set aside by toggle-window-stash, left to
	// right (stash.go); stashAt is the selected one. stashFocus is set
	// while it has the focus, under a focused native float.
	Stash   []Float
	stashAt int
	// stashView slides the stash: the selected window is drawn at
	// stashAt+stashView.off, in stash windows. Pure presentation over
	// stashAt.
	stashView   motion.Slide
	stashFocus  bool
	stashHidden bool
	// stashWidth is the width of a stashed window (0: the default) and
	// stashGap the space between it and its neighbors, in percent of the
	// usable width.
	stashWidth, stashGap int
	// hiddenFullscreen holds a stashed window's fullscreen while the stash
	// is hidden, restored when shown.
	hiddenFullscreen WindowID
	// stashOver is the covering fullscreen window the stash shows over, a
	// user override (ToggleStashVisible) or a stashed window's activation
	// (Activate); 0 without. Bound to that window,
	// it ends with its cover. Focusing another window ends it too.
	stashOver WindowID
	// Home is the monitor (key or connector) the workspace belongs to; ""
	// means the one it is on. On another monitor it is a guest; homePos is
	// its position there, where it returns.
	Home string
	// HomePos is the position to restore on the home monitor; core owns ordering.
	HomePos int
	// TermAt is when core last spawned a terminal for this workspace.
	TermAt time.Time
	// colBuf, rowBuf and tileBuf are appendLayout's scratch (column rects,
	// the rows of one column, the tiles before the floats are merged in),
	// reused across calls and never handed out: owner goroutine only, and
	// appendLayout does not nest on one workspace.
	colBuf, rowBuf []Rect
	tileBuf        []Placement
	// zoomBuf is overviewZoom's scratch for the tiles it measures.
	zoomBuf []Placement
}

// origPlace remembers where a stashed window was: its column, its row in a
// stacked column, the column's width in cells and slot.
type origPlace struct {
	col, row, slot int
	// stacked holds the windows left in the column: the window returns to
	// its row in the column that still holds one of them.
	stacked []WindowID
	cells   int
	// fullWidth is the column's maximize-column state.
	fullWidth bool
}

// Float is a floating window and its client size, logical.
type Float struct {
	// ID identifies the floating window.
	ID WindowID
	// W and H are the client size in logical pixels.
	W, H int
	// below places a covering native float behind columns and stash.
	below bool
	// back records the former column of a stashed window or a free float;
	// nil for native floats.
	back *origPlace
	// free marks a window the user floated (toggle-floating) or dragged:
	// core owns its size and its centre is at cx, cy, fractions of the
	// usable area, so it keeps its place on any output.
	free   bool
	cx, cy float64
	// over is the parent of a dialog: while the parent covers the output
	// fullscreen, the dialog shows over it and may take the focus.
	over WindowID
	// modal marks a dialog that blocks its parent: the user focusing the
	// parent focuses it (modalOf).
	modal bool
}

// Empty reports that columns, native floats and stash are all empty.
func (w *Workspace) Empty() bool {
	return len(w.Columns) == 0 && len(w.Floats) == 0 && len(w.Stash) == 0
}

// columnWindows counts the windows in the columns.
func (w *Workspace) columnWindows() int {
	n := 0
	for _, c := range w.Columns {
		n += len(c.Windows)
	}
	return n
}

// AppendHidden appends every window of w as a Hidden placement, in the order
// of windows (columns, stash, then floats when floats is set), without
// building the list.
func (w *Workspace) AppendHidden(dst []Placement, floats bool) []Placement {
	for _, c := range w.Columns {
		for _, id := range c.Windows {
			dst = append(dst, Placement{ID: id, Hidden: true})
		}
	}
	for _, f := range w.Stash {
		dst = append(dst, Placement{ID: f.ID, Hidden: true})
	}
	if floats {
		for _, f := range w.Floats {
			dst = append(dst, Placement{ID: f.ID, Hidden: true})
		}
	}
	return dst
}

// AppendWindows appends windows' list to dst.
func (w *Workspace) AppendWindows(dst []WindowID) []WindowID {
	for _, c := range w.Columns {
		dst = append(dst, c.Windows...)
	}
	for _, f := range w.Stash {
		dst = append(dst, f.ID)
	}
	for _, f := range w.Floats {
		dst = append(dst, f.ID)
	}
	return dst
}

func (w *Workspace) floatIndex(id WindowID) int {
	return slices.IndexFunc(w.Floats, func(f Float) bool { return f.ID == id })
}

// coversFloat uses the placed rectangle, not a map-time size. Allow up to
// two border widths plus two logical pixels per axis for scale rounding.
func (w *Workspace) coversFloat(f Float) bool {
	if w.Fullscreen == f.ID || w.Usable.W <= 0 || w.Usable.H <= 0 {
		return false
	}
	r := w.floatRect(f)
	return w.Usable.W-r.W <= 2*w.border+2 && w.Usable.H-r.H <= 2*w.border+2
}

// reconcileFloats promotes floats that no longer cover, without raising
// existing covering floats on automatic geometry changes. It also drops
// float focus when no raised float remains on top.
func (w *Workspace) reconcileFloats() {
	for i := range w.Floats {
		if w.Floats[i].below && !w.coversFloat(w.Floats[i]) && w.Fullscreen != w.Floats[i].ID {
			w.Floats[i].below = false
		}
	}
	if len(w.Floats) == 0 || w.Floats[len(w.Floats)-1].below {
		w.floatFocus = false
	}
}

// canLeaveFloat reports whether columns or a shown stash can receive focus.
func (w *Workspace) canLeaveFloat() bool {
	return len(w.Columns) > 0 || !w.stashHidden && len(w.Stash) > 0
}

// leaveFloat moves focus to the columns or shown stash under the float.
// Without either, it keeps focus and reports false.
func (w *Workspace) leaveFloat() bool {
	if !w.canLeaveFloat() {
		return false
	}
	w.floatFocus = false
	w.raiseColumns()
	return true
}

// raiseColumns moves only covering floats above the columns behind them.
// The user-selected column group rises as one; dialogs remain above it.
func (w *Workspace) raiseColumns() {
	for i := range w.Floats {
		if w.coversFloat(w.Floats[i]) {
			w.Floats[i].below = true
		}
	}
}

// AddFloating shows a native floating window over the columns and the
// stash, and focuses it.
func (w *Workspace) AddFloating(id WindowID, width, height int) {
	if id == 0 || w.Has(id) {
		return
	}
	w.Floats = append(w.Floats, Float{ID: id, W: width, H: height})
	w.floatFocus = true
}

// AddDialog is AddFloating for a dialog of parent: it shows, focused, over
// its parent's fullscreen. A dialog follows a user action in its parent
// (a portal request, a file chooser), so the user expects to see it.
func (w *Workspace) AddDialog(id, parent WindowID, width, height int) {
	if id == 0 || w.Has(id) {
		return
	}
	w.Floats = append(w.Floats, Float{ID: id, W: width, H: height, over: parent})
	w.floatFocus = true
}

// SetDialogParent follows a dialog's parent after it mapped: a cleared or
// changed parent ends its place over the old one's fullscreen.
func (w *Workspace) SetDialogParent(id, parent WindowID) {
	if i := w.floatIndex(id); i >= 0 {
		w.Floats[i].over = parent
		w.reconcileFloats()
	}
}

// SetModal marks a dialog as blocking its parent or not. It moves no focus
// (a client request, ADR 011): the next user focus on the parent goes to
// the dialog. A dialog maps focused anyway (AddDialog).
func (w *Workspace) SetModal(id WindowID, modal bool) {
	if i := w.floatIndex(id); i >= 0 {
		w.Floats[i].modal = modal
	}
}

// modalOf is the modal dialog that blocks window id, 0 when none: the
// topmost modal dialog of id, or of that dialog in turn (a confirmation
// over a "Save as…"). Only dialogs on id's workspace count.
func (w *Workspace) modalOf(id WindowID) WindowID {
	blocker := WindowID(0)
	// Each hop goes to another float: at most len(w.Floats) hops.
	for range len(w.Floats) {
		next := WindowID(0)
		for i := len(w.Floats) - 1; i >= 0; i-- {
			if f := w.Floats[i]; f.modal && f.over == id && id != 0 && f.ID != id && f.ID != blocker {
				next = f.ID
				break
			}
		}
		if next == 0 {
			break
		}
		blocker, id = next, next
	}
	return blocker
}

// FocusModal sends the focus the user just moved onto the parent of a
// modal dialog to that dialog, unless the move came from the dialog: a move
// away from a dialog reaches its parent, and the next one goes on. Nothing
// happens when the focus did not move.
func (w *Workspace) FocusModal(from WindowID) {
	to, ok := w.Focused()
	if !ok || to == from {
		return
	}
	d := w.modalOf(to)
	if d == 0 || d == from {
		return
	}
	if c := w.Cover(); c != 0 && c != d {
		if i := w.floatIndex(d); i >= 0 && !w.overCover(w.Floats[i]) {
			// Hidden under another window's fullscreen: leave it, as
			// Activate does for a window not shown over the cover, so
			// the dialog shows.
			w.leaveFullscreen()
		}
	}
	w.FocusID(d)
}

// overCover reports whether float f is drawn over the covering fullscreen
// window: a dialog of the cover, or of a stashed window while the stash
// shows over the cover (a "Save as…" of the app the user picked there),
// or a dialog of such a dialog (an "Overwrite?" over the "Save as…").
func (w *Workspace) overCover(f Float) bool {
	c := w.Cover()
	if c == 0 || f.ID == c {
		return false
	}
	owner := w.dialogOwner(f, c)
	return owner == c || owner != 0 && w.stashOverCover() && w.StashIndex(owner) >= 0
}

// dialogOwner is the window float f is a dialog of, through dialogs of
// dialogs: the first parent that is no float, or stop. 0 when f is no
// dialog, its chain ends on a plain float, or it loops.
func (w *Workspace) dialogOwner(f Float, stop WindowID) WindowID {
	// Each hop goes to another float: at most len(w.Floats) hops.
	for range len(w.Floats) {
		if f.over == 0 || f.over == f.ID {
			return 0
		}
		if f.over == stop {
			return stop
		}
		i := w.floatIndex(f.over)
		if i < 0 {
			return f.over
		}
		f = w.Floats[i]
	}
	return 0
}

// coverDialog is the topmost dialog shown over the covering fullscreen
// window (overCover) when the floats have the focus, else 0. The floats it
// hides cannot take the focus.
func (w *Workspace) coverDialog() WindowID {
	if !w.floatFocus {
		return 0
	}
	for i := len(w.Floats) - 1; i >= 0; i-- {
		if f := w.Floats[i]; !f.below && w.overCover(f) {
			return f.ID
		}
	}
	return 0
}

// coverDialogFocused reports whether a dialog over the covering fullscreen
// window has the focus.
func (w *Workspace) coverDialogFocused() bool { return w.coverDialog() != 0 }

// ResizeFloating records the size a floating window draws. Core sizes a
// free float: its reports only fill a missing size.
func (w *Workspace) ResizeFloating(id WindowID, width, height int) {
	if i := w.floatIndex(id); i >= 0 && (!w.Floats[i].free || w.Floats[i].W <= 0) {
		w.Floats[i].W, w.Floats[i].H = width, height
		w.reconcileFloats()
	}
}

// Has reports whether the workspace holds the window.
func (w *Workspace) Has(id WindowID) bool {
	if w.IsFloat(id) {
		return true
	}
	for _, c := range w.Columns {
		for _, v := range c.Windows {
			if v == id {
				return true
			}
		}
	}
	return false
}

// Focused is the window selected when the operation began.
func (w *Workspace) Focused() (WindowID, bool) {
	// Nothing else is drawn but its dialogs: the covering fullscreen window
	// has the focus unless one of them took it.
	if full := w.Cover(); full != 0 {
		if d := w.coverDialog(); d != 0 {
			return d, true
		}
		if w.stashOverCover() {
			return w.Stash[w.stashAt].ID, true
		}
		return full, true
	}
	if w.floatFocus && len(w.Floats) > 0 && !w.Floats[len(w.Floats)-1].below {
		return w.Floats[len(w.Floats)-1].ID, true
	}
	if w.stashFocused() {
		return w.Stash[w.stashAt].ID, true
	}
	if w.Focus < 0 || w.Focus >= len(w.Columns) {
		return 0, false
	}
	c := w.Columns[w.Focus]
	if c.Focus < 0 || c.Focus >= len(c.Windows) {
		return 0, false
	}
	return c.Windows[c.Focus], true
}

// minShare is the smallest row height set-window-height leaves, in basis points.
const minShare = 1000

// SetOutput sets the logical size of the monitor. The workspace viewport
// follows: the whole monitor, or the centered size override.
func (w *Workspace) SetOutput(width, height int) {
	old := w.monitor
	w.monitor = Rect{W: max(width, 0), H: max(height, 0)}
	if w.reserved == old {
		w.reserved = w.monitor
	}
	w.SetUsable(w.reserved)
}

// SetUsable takes the monitor area left by layer surfaces, clamps negative
// dimensions to zero and clips it to the monitor. The usable rectangle is
// its intersection with the viewport. Effective gaps shrink to fit it.
func (w *Workspace) SetUsable(r Rect) {
	r.X = min(max(r.X, 0), w.monitor.W)
	r.Y = min(max(r.Y, 0), w.monitor.H)
	r.W = min(max(r.W, 0), w.monitor.W-r.X)
	r.H = min(max(r.H, 0), w.monitor.H-r.Y)
	w.reserved = r
	w.fit()
}

// SetSize overrides the logical size of the workspace; a zero side inherits
// the monitor, one larger than it is clamped to it.
func (w *Workspace) SetSize(width, height int) {
	w.size = [2]int{max(width, 0), max(height, 0)}
	w.fit()
}

// fit derives the viewport and usable area from the monitor, its reservations
// and the size override.
func (w *Workspace) fit() {
	side := func(want, have int) int {
		if want <= 0 {
			return have
		}
		return min(want, have)
	}
	vw, vh := side(w.size[0], w.monitor.W), side(w.size[1], w.monitor.H)
	w.Output = Rect{X: (w.monitor.W - vw) / 2, Y: (w.monitor.H - vh) / 2, W: vw, H: vh}
	r, o := w.reserved, w.Output
	x0, y0 := max(r.X, o.X), max(r.Y, o.Y)
	x1, y1 := min(r.X+r.W, o.X+o.W), min(r.Y+r.H, o.Y+o.H)
	u := Rect{X: min(max(x0, o.X), o.X+o.W), Y: min(max(y0, o.Y), o.Y+o.H)}
	u.W, u.H = max(x1-u.X, 0), max(y1-u.Y, 0)
	w.Usable = u
	w.reconcileFloats()
	w.scroll()
}

// SetGaps changes tile spacing and scrolls to the focus.
func (w *Workspace) SetGaps(g int) {
	if g < 0 {
		g = 0
	}
	w.Gaps = g
	w.scroll()
}

// SetMaxColumns changes the cell count and scrolls to the focus.
func (w *Workspace) SetMaxColumns(n int) {
	w.MaxColumns = n
	w.scroll()
}

// OverviewArea is the monitor area the overview lays rows out in: the whole
// area left by layer surfaces, whatever the workspace's size. Tiles keep the
// workspace's own geometry, scaled uniformly.
func (w *Workspace) OverviewArea() Rect { return w.reserved }

// Layout places every window of the workspace in a fresh slice the caller
// owns. The per-frame paths use layoutInto instead.
func (w *Workspace) Layout() []Placement { return w.AppendLayout(nil) }

// AppendLayout appends the workspace's placements to dst.
func (w *Workspace) AppendLayout(dst []Placement) []Placement {
	gap := w.Gap()
	// The column and row rects live in the workspace's scratch: each is dead
	// once the placements it feeds are built (owner goroutine only, and
	// layoutInto does not nest on a workspace).
	w.colBuf = w.columnRectsInto(w.colBuf, false)
	cols := w.colBuf
	focusedID, _ := w.Focused()
	cover := w.Cover()
	// Facts of the layout, asked once, not per window.
	l := w.Engine()
	pins, maxHides := l.PinsFullscreen(), l.MaximizeHides()
	maxFocus := maxHides && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth
	tiles := w.tileBuf[:0]
	for i, c := range w.Columns {
		col := cols[i]
		n := len(c.Windows)
		if n == 0 {
			continue
		}
		fullColumn := w.FullscreenColumn(i)
		if fullColumn {
			col.W = w.Output.W
		}
		w.rowBuf = layout.RowRectsInto(w.rowBuf[:0], col, c, gap)
		rows := w.rowBuf
		for j, id := range c.Windows {
			r := rows[j]
			full := w.Fullscreen == id && id != 0
			// Fixed overflow cannot scroll to other columns while one fills
			// the view. Keep them in place, but out of the scene.
			hidden := (cover != 0 && id != cover) || ((fullColumn || pins && w.Fullscreen != 0) && !full) || maxFocus && i != w.Focus
			if full {
				// A layout that does not pin the cover aligns the view on the
				// column; a pinned one fills the output at once.
				r = w.Output
				if !pins {
					r.X = col.X
				}
			}
			if hidden {
				r = Rect{}
			}
			tiles = append(tiles, Placement{ID: id, Rect: r, Fullscreen: full, Focused: id == focusedID, Hidden: hidden})
		}
	}
	// Lines are shared within the usable area: a tile under a panel (the
	// next cascade band starts right below a bottom one) is no neighbor.
	setVisibleNeighbors(tiles, gap, w.Usable)
	// tiles lives in the workspace's scratch until they are copied below.
	w.tileBuf = tiles
	// The column/stash group sits between demoted covering floats and
	// floats above it. Keep the order within each group stable.
	result := dst
	const (
		belowTiles = iota
		coveringFloats
		dialogs
	)
	appendFloats := func(group int) {
		for _, f := range w.Floats {
			// A dialog shown over the cover stays a dialog whatever its
			// size, so a full-size one is not drawn under the stash.
			dialog := cover != 0 && w.overCover(f)
			below := f.below && w.coversFloat(f) && !dialog
			// Dialogs stay over covering floats even when the latter is
			// selected. Fullscreen floats remain in the upper group.
			order := dialogs
			if below {
				order = belowTiles
			} else if !dialog && (w.coversFloat(f) || w.Fullscreen == f.ID) {
				order = coveringFloats
			}
			if order != group {
				continue
			}
			// A covering fullscreen window hides every float but the
			// dialogs over it (overCover): the others show again when it
			// leaves fullscreen.
			p := Placement{ID: f.ID, Rect: w.floatRect(f), Floating: true, Below: below, Focused: f.ID == focusedID, Inset: ports.SideAll, Hidden: cover != 0 && f.ID != cover && !dialog}
			if p.Hidden {
				p.Rect = Rect{}
				result = append(result, p)
				continue
			}
			if w.Fullscreen == f.ID {
				p.Rect, p.Fullscreen, p.Inset = w.Output, true, 0
			}
			result = append(result, p)
		}
	}
	appendFloats(belowTiles)
	result = append(result, tiles...)
	if w.stashOverCover() {
		// The stash the user showed over a covering window tops it, under
		// the dialogs it may open meanwhile.
		appendFloats(coveringFloats)
		result = w.appendStash(result, focusedID, cover)
	} else {
		result = w.appendStash(result, focusedID, cover)
		appendFloats(coveringFloats)
	}
	appendFloats(dialogs)
	return result
}

// setVisibleNeighbors reserves client space only for shared lines that
// are actually in the usable area. A scrolled-off, hidden or under-panel
// tile cannot give a lone visible tile a border (or shrink its client).
func setVisibleNeighbors(tiles []Placement, gap int, area Rect) {
	visible := func(p Placement) bool {
		return !p.Hidden && !p.Fullscreen && p.Rect.Overlaps(area)
	}
	overlap := func(a0, a1, b0, b1 int) bool { return a0 < b1 && b0 < a1 }
	for i := range tiles {
		a := &tiles[i]
		a.Neighbors, a.Inset = 0, 0
		if !visible(*a) {
			continue
		}
		for j := range tiles {
			if i == j || !visible(tiles[j]) {
				continue
			}
			b, r := tiles[j].Rect, a.Rect
			// Only count a shared edge strictly within the area.
			switch {
			case r.X+r.W+gap == b.X && overlap(r.Y, r.Y+r.H, b.Y, b.Y+b.H) && r.X+r.W > area.X && r.X+r.W < area.X+area.W:
				a.Neighbors |= ports.SideRight
			case b.X+b.W+gap == r.X && overlap(r.Y, r.Y+r.H, b.Y, b.Y+b.H) && r.X > area.X && r.X < area.X+area.W:
				a.Neighbors |= ports.SideLeft
			case r.Y+r.H+gap == b.Y && overlap(r.X, r.X+r.W, b.X, b.X+b.W) && r.Y+r.H > area.Y && r.Y+r.H < area.Y+area.H:
				a.Neighbors |= ports.SideBottom
			case b.Y+b.H+gap == r.Y && overlap(r.X, r.X+r.W, b.X, b.X+b.W) && r.Y > area.Y && r.Y < area.Y+area.H:
				a.Neighbors |= ports.SideTop
			}
		}
		a.Inset = a.Neighbors
		if gap == 0 {
			a.Inset &= ports.SideRight | ports.SideBottom
		} else {
			a.Inset = ports.SideAll
		}
	}
}

// floatRect places a floating window, its border around its client size,
// clamped to the usable area: a native one centred, a free one at its
// centre, kept inside the area.
func (w *Workspace) floatRect(f Float) Rect {
	u := w.Usable
	fw, fh := f.W, f.H
	if fw <= 0 || fh <= 0 {
		fw, fh = u.W/2, u.H/2
	}
	fw, fh = min(fw+2*w.border, u.W), min(fh+2*w.border, u.H)
	if !f.free {
		return Rect{X: u.X + (u.W-fw)/2, Y: u.Y + (u.H-fh)/2, W: fw, H: fh}
	}
	x := u.X + int(math.Round(f.cx*float64(u.W))) - fw/2
	y := u.Y + int(math.Round(f.cy*float64(u.H))) - fh/2
	x = min(max(x, u.X), u.X+u.W-fw)
	y = min(max(y, u.Y), u.Y+u.H-fh)
	return Rect{X: x, Y: y, W: fw, H: fh}
}

// ImposedFloat reports whether core, rather than the client, owns the
// size of a floating window: a stashed one or a free float.
func (w *Workspace) ImposedFloat(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 {
		return w.Floats[i].free
	}
	return w.StashIndex(id) >= 0
}

// ToggleWindowStash stashes the focused tile, or returns the focused
// stashed window to its column. A native float becomes a new column.
func (w *Workspace) ToggleWindowStash() {
	id, ok := w.Focused()
	// A fullscreen window (a Wine game in scanout) keeps its place: it
	// leaves fullscreen first, then may float.
	if !ok || id == w.Fullscreen {
		return
	}
	switch {
	case w.floatIndex(id) >= 0 && w.Floats[w.floatIndex(id)].free:
		// A free float keeps its place and size in the stash.
		f := w.Floats[w.floatIndex(id)]
		w.RemoveWindow(id)
		w.addStash(f)
	case w.floatIndex(id) >= 0:
		w.RemoveWindow(id)
		w.restore(id, nil)
	case w.StashIndex(id) >= 0:
		if w.stashOverCover() {
			// Its column would be hidden under the cover: leave it.
			w.leaveFullscreen()
		}
		w.unstash(w.StashIndex(id))
	default:
		w.stashWindow(id)
	}
}
