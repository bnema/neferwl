package core

import (
	"math"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

type WindowID = ports.WindowID

type Rect = ports.Rect

// maxCells is the largest width a rule or a slot may declare, in cells.
const maxCells = 16

// Column.Cells is the width of the column in cells, a cell being one
// max-columns-th of the usable width. Zero (auto) shares the width equally
// with the other auto columns, up to MaxColumns visible at once.
type Column struct {
	// key is the workspace-local column identity; layout state keys
	// per-column data by it.
	key uint64
	// carry is what the old layout state let travel with the column to
	// another workspace (layoutState.removed). The new state takes what it
	// understands in inserted; the rest, or all of it in a stateless
	// workspace, stays here until the column leaves again.
	carry any
	// Span is a manually sized width in basis points; zero uses Cells.
	Span      int
	spanStamp uint64
	Windows   []WindowID
	Cells     int
	FullWidth bool
	Focus     int
	// Slot is the declared column number (workspace.<name>.column.N) of a
	// slot window; 0 for normal columns.
	Slot int
	// Shares are the row heights set by set-window-height, in basis points, in
	// Windows order. They apply only while there is one per window and
	// they sum to 10000; otherwise the rows share the height equally.
	Shares []int
}
type Placement struct {
	ID                          WindowID
	Rect                        Rect
	Fullscreen, Focused, Hidden bool
	// Floating windows sit at their own size. Below marks a native float
	// ordered behind the columns (never a stashed window).
	Floating, Below bool
	// Peek is a stashed window peeking in beside the selected one, or an
	// overview preview of a neighbor workspace: dimmed.
	Peek bool
	// Card marks a stash card of the overview's pile (as opposed to a tile
	// or a covering float): the selection outline goes around a selected
	// one and stays off the ones it would cross.
	Card bool
	// Veil is the share (0..1) of the peek veil (Stash.Dim) the window
	// draws: 1 for a settled peek, less while a stash slide brings it to
	// the centre. Set wherever Peek is, and on a sliding stash selection.
	Veil float64
	// Preview is the scale of an overview preview: its buffer is drawn
	// that much smaller in Rect, the client keeps its size. 0 otherwise.
	Preview float64
	// Neighbors are the sides touching a tile visible in the usable area.
	// Inset reserves room for drawn lines: all sides of a float; all sides
	// of a tile with gaps; only the right and bottom shared sides of a tile
	// without gaps.
	Neighbors, Inset ports.Sides
	// Leaving marks a window that closed or hid but is still drawn while
	// it fades out (transition.go). It stays Hidden, so input, focus,
	// popups and configures ignore it; only the scene shows it.
	Leaving bool
	// Fade, Dim and Zoom are set on shown layouts only (refreshShown),
	// animated: the window's fade (0 opaque, 1 invisible), a veil opacity
	// added to the one Veil draws (0..1), and the content zoom as drawn
	// when a scale motion runs (Preview times the drawn/settled width, 1
	// when the content is drawn at its size; 0 without one). Preview
	// itself stays the settled one: input, popups and the renderer's paint
	// order read it to tell a card from a window.
	Fade, Dim, Zoom float64
}

// peeking marks p a peek drawing share of the peek veil (Stash.Dim).
func (p *Placement) peeking(veil float64) { p.Peek, p.Veil = true, veil }

// Overflow says what happens past MaxColumns columns: the config name of a
// layout.
type Overflow string

// Each layout declares its own value next to its type (layout_<name>.go) and
// registers itself in layout_registry.go.

type Workspace struct {
	lay         layout      // Optional test override; production resolves Overflow lazily.
	lstate      layoutState // Owner-goroutine-only, initialized lazily.
	lstateReady bool        // Distinguishes stateless layouts from uninitialized state.
	// lcache is the layout resolved for lcacheFor (setOverflow and layout()
	// fill it); layout() re-resolves when Overflow no longer equals lcacheFor.
	lcache    layout
	lcacheFor Overflow
	colSeq    uint64 // Issues workspace-local column identities.
	// ID stays with the workspace across reorder and monitor moves.
	ID uint64
	// Name is set for workspaces declared in config; empty for dynamic ones.
	Name string
	// overviewAfter is the numbered workspace from which this named workspace
	// was invoked. It only orders overview rows; numbering stays unchanged.
	overviewAfter *Workspace
	Overflow      Overflow
	Columns       []Column
	// spanSeq issues workspace-local identities for complete lane stamps.
	spanSeq uint64
	// maximized remembers recently maximized columns by window ID, newest
	// first; stack() filters removed windows at read time.
	maximized []WindowID
	// recent holds one window per column the user focused, newest first
	// (switcher.go); columns that went are dropped when read.
	recent []WindowID
	Focus  int
	View   int
	// View is the settled offset along the layout's content axis.
	// view is its presentation offset, in logical pixels.
	view slide
	// Output is the effective viewport in monitor coordinates: the whole
	// monitor unless the workspace has a size override, then a centered
	// rectangle no larger than the monitor. Fullscreen fills it.
	Output Rect
	// Usable is the layer reservations of the monitor intersected with Output.
	Usable Rect
	// monitor is the full logical monitor and reserved its area left by
	// layer surfaces, both from the monitor; size is the override (0 inherits).
	monitor, reserved Rect
	size              [2]int
	Gaps              int
	MaxColumns        int
	// border is drawn around floating windows, inside their rect.
	border     int
	fullscreen WindowID
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
	stashView   slide
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
	// home is the monitor (key or connector) the workspace belongs to; ""
	// means the one it is on. On another monitor it is a guest; homePos is
	// its position there, where it returns.
	home    string
	homePos int
	// termAt is when core last spawned a terminal for this workspace.
	termAt time.Time
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
	ID   WindowID
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

func (w *Workspace) empty() bool {
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

// appendHidden appends every window of w as a Hidden placement, in the order
// of windows (columns, stash, then floats when floats is set), without
// building the list.
func (w *Workspace) appendHidden(dst []Placement, floats bool) []Placement {
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

// windows lists every window: columns first, then the stash, then native
// floats.
func (w *Workspace) windows() []WindowID { return w.appendWindows(nil) }

// appendWindows appends windows' list to dst.
func (w *Workspace) appendWindows(dst []WindowID) []WindowID {
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

// cell is the 1-based column and row of a tiled window, 0, 0 otherwise.
func (w *Workspace) cell(id WindowID) (col, row int) {
	for ci, c := range w.Columns {
		if ri := slices.Index(c.Windows, id); ri >= 0 {
			return ci + 1, ri + 1
		}
	}
	return 0, 0
}

func (w *Workspace) floatIndex(id WindowID) int {
	return slices.IndexFunc(w.Floats, func(f Float) bool { return f.ID == id })
}

// coversFloat uses the placed rectangle, not a map-time size. Allow up to
// two border widths plus two logical pixels per axis for scale rounding.
func (w *Workspace) coversFloat(f Float) bool {
	if w.fullscreen == f.ID || w.Usable.W <= 0 || w.Usable.H <= 0 {
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
		if w.Floats[i].below && !w.coversFloat(w.Floats[i]) && w.fullscreen != w.Floats[i].ID {
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
	if id == 0 || w.has(id) {
		return
	}
	w.Floats = append(w.Floats, Float{ID: id, W: width, H: height})
	w.floatFocus = true
}

// AddDialog is AddFloating for a dialog of parent: it shows, focused, over
// its parent's fullscreen. A dialog follows a user action in its parent
// (a portal request, a file chooser), so the user expects to see it.
func (w *Workspace) AddDialog(id, parent WindowID, width, height int) {
	if id == 0 || w.has(id) {
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

// focusModal sends the focus the user just moved onto the parent of a
// modal dialog to that dialog, unless the move came from the dialog: a move
// away from a dialog reaches its parent, and the next one goes on. Nothing
// happens when the focus did not move.
func (w *Workspace) focusModal(from WindowID) {
	to, ok := w.Focused()
	if !ok || to == from {
		return
	}
	d := w.modalOf(to)
	if d == 0 || d == from {
		return
	}
	if c := w.cover(); c != 0 && c != d {
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
	c := w.cover()
	if c == 0 || f.ID == c {
		return false
	}
	owner := w.dialogOwner(f, c)
	return owner == c || owner != 0 && w.stashOverCover() && w.stashIndex(owner) >= 0
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

// has reports whether the workspace holds the window.
func (w *Workspace) has(id WindowID) bool {
	if w.isFloat(id) {
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

func (w *Workspace) Focused() (WindowID, bool) {
	// Nothing else is drawn but its dialogs: the covering fullscreen window
	// has the focus unless one of them took it.
	if full := w.cover(); full != 0 {
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
func (w *Workspace) SetGaps(g int) {
	if g < 0 {
		g = 0
	}
	w.Gaps = g
	w.scroll()
}
func (w *Workspace) SetMaxColumns(n int) {
	w.MaxColumns = n
	w.scroll()
}

// overviewArea is the monitor area the overview lays rows out in: the whole
// area left by layer surfaces, whatever the workspace's size. Tiles keep the
// workspace's own geometry, scaled uniformly.
func (w *Workspace) overviewArea() Rect { return w.reserved }

func (w *Workspace) gap() int { return min(w.Gaps, w.Usable.W/2, w.Usable.H/2) }

// stackRects cuts r in n rows with a gap between them; the last row takes
// the rounding remainder. Rows past the bottom (gaps taller than r) are
// clamped to it, like windows stacked in a column.
func stackRects(r Rect, n, gap int) []Rect {
	return stackRectsInto(nil, r, n, gap)
}

// stackRectsInto appends the n rows of stackRects to dst (reset with dst[:0]
// to reuse its storage).
func stackRectsInto(dst []Rect, r Rect, n, gap int) []Rect {
	base := len(dst)
	dst = slices.Grow(dst, n)[:base+n]
	rows := dst[base:]
	avail := max(r.H-(n-1)*gap, 0)
	height := avail / n
	yy, bottom := r.Y, r.Y+r.H
	for i := range rows {
		hh := height
		if i == n-1 {
			hh = avail - height*(n-1)
		}
		yy = min(yy, bottom)
		hh = min(hh, bottom-yy)
		rows[i] = Rect{X: r.X, Y: yy, W: r.W, H: hh}
		yy += hh + gap
	}
	return dst
}

// rowRects splits column rect r into the rows of c: by c.Shares when they
// are valid, else equally (stackRects).
func rowRects(r Rect, c Column, gap int) []Rect {
	return rowRectsInto(nil, r, c, gap)
}

// rowRectsInto appends the rows of rowRects to dst (reset with dst[:0] to
// reuse its storage).
func rowRectsInto(dst []Rect, r Rect, c Column, gap int) []Rect {
	n := len(c.Windows)
	if !validShares(c) {
		return stackRectsInto(dst, r, n, gap)
	}
	base := len(dst)
	dst = slices.Grow(dst, n)[:base+n]
	rows := dst[base:]
	avail := max(r.H-(n-1)*gap, 0)
	yy, bottom, used := r.Y, r.Y+r.H, 0
	for i, share := range c.Shares {
		hh := avail * share / 10000
		if i == n-1 {
			hh = avail - used
		}
		used += hh
		yy = min(yy, bottom)
		hh = min(hh, bottom-yy)
		rows[i] = Rect{X: r.X, Y: yy, W: r.W, H: hh}
		yy += hh + gap
	}
	return dst
}

// validShares reports whether c.Shares has one share per window, each at
// least 1, summing to 10000.
func validShares(c Column) bool {
	if len(c.Shares) != len(c.Windows) || len(c.Shares) < 2 {
		return false
	}
	sum := 0
	for _, v := range c.Shares {
		if v < 1 {
			return false
		}
		sum += v
	}
	return sum == 10000
}

// split cuts r in two halves with a gap: top/bottom when vertical, else left/right.
func split(r Rect, gap int, vertical bool) (Rect, Rect) {
	if vertical {
		h := max((r.H-gap)/2, 0)
		return Rect{X: r.X, Y: r.Y, W: r.W, H: h}, Rect{X: r.X, Y: r.Y + h + gap, W: r.W, H: max(r.H-gap-h, 0)}
	}
	w := max((r.W-gap)/2, 0)
	return Rect{X: r.X, Y: r.Y, W: w, H: r.H}, Rect{X: r.X + w + gap, Y: r.Y, W: max(r.W-gap-w, 0), H: r.H}
}

// Layout places every window of the workspace in a fresh slice the caller
// owns. The per-frame paths use layoutInto instead.
func (w *Workspace) Layout() []Placement { return w.appendLayout(nil) }

// layoutInto is Layout built in dst[:0]: the result aliases dst's storage
// (a reused buffer's previous result is overwritten), so only a caller that
// is done with the previous one may pass it. It never returns the
// workspace's own scratch.
func (w *Workspace) layoutInto(dst []Placement) []Placement { return w.appendLayout(dst[:0]) }

// appendLayout appends the workspace's placements to dst.
func (w *Workspace) appendLayout(dst []Placement) []Placement {
	gap := w.gap()
	// The column and row rects live in the workspace's scratch: each is dead
	// once the placements it feeds are built (owner goroutine only, and
	// layoutInto does not nest on a workspace).
	w.colBuf = w.columnRectsInto(w.colBuf, false)
	cols := w.colBuf
	focusedID, _ := w.Focused()
	cover := w.cover()
	// Facts of the layout, asked once, not per window.
	l := w.layout()
	pins, maxHides := l.pinsFullscreen(), l.maximizeHides()
	maxFocus := maxHides && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth
	tiles := w.tileBuf[:0]
	for i, c := range w.Columns {
		col := cols[i]
		n := len(c.Windows)
		if n == 0 {
			continue
		}
		fullColumn := w.fullscreenColumn(i)
		if fullColumn {
			col.W = w.Output.W
		}
		w.rowBuf = rowRectsInto(w.rowBuf[:0], col, c, gap)
		rows := w.rowBuf
		for j, id := range c.Windows {
			r := rows[j]
			full := w.fullscreen == id && id != 0
			// Fixed overflow cannot scroll to other columns while one fills
			// the view. Keep them in place, but out of the scene.
			hidden := (cover != 0 && id != cover) || ((fullColumn || pins && w.fullscreen != 0) && !full) || maxFocus && i != w.Focus
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
			} else if !dialog && (w.coversFloat(f) || w.fullscreen == f.ID) {
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
			if w.fullscreen == f.ID {
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

// imposedFloat reports whether core, rather than the client, owns the
// size of a floating window: a stashed one or a free float.
func (w *Workspace) imposedFloat(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 {
		return w.Floats[i].free
	}
	return w.stashIndex(id) >= 0
}

// ToggleWindowStash stashes the focused tile, or returns the focused
// stashed window to its column. A native float becomes a new column.
func (w *Workspace) ToggleWindowStash() {
	id, ok := w.Focused()
	// A fullscreen window (a Wine game in scanout) keeps its place: it
	// leaves fullscreen first, then may float.
	if !ok || id == w.fullscreen {
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
	case w.stashIndex(id) >= 0:
		if w.stashOverCover() {
			// Its column would be hidden under the cover: leave it.
			w.leaveFullscreen()
		}
		w.unstash(w.stashIndex(id))
	default:
		w.stashWindow(id)
	}
}
