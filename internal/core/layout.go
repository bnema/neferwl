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
	Windows   []WindowID
	Cells     int
	FullWidth bool
	Focus     int
	// Slot is the declared column number (workspace.<name>.column.N) of a
	// slot window; 0 for normal columns.
	Slot int
	// Shares are the row heights set by set-window-height, in percent, in
	// Windows order. They apply only while there is one per window and
	// they sum to 100; otherwise the rows share the height equally.
	Shares []int
	// BandStart marks the first column of a cascade band. It belongs to the
	// position, not the column: swaps leave it in place. See cascade.go.
	BandStart bool
	// pushedUp and pushedDown are the columns (by first window) that
	// maximize-column or cycle-column-width sent to the band above or
	// below; they come back when the column is restored.
	pushedUp, pushedDown []WindowID
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
	// Inset reserves room for drawn lines: all sides of a float; for
	// tiles without gaps, only the right and bottom shared sides.
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

// Overflow says what happens past MaxColumns columns.
type Overflow string

const (
	// OverflowScroll opens further columns to the right; the view scrolls.
	OverflowScroll Overflow = "scroll"
	// OverflowCascade wraps columns into vertically scrolling viewport bands.
	OverflowCascade Overflow = "cascade"
	// OverflowFixed keeps every column on screen: past the max, each new
	// column splits the newest one, alternating top/bottom and left/right
	// (dwindle spiral).
	OverflowFixed Overflow = "fixed"
)

type Workspace struct {
	// ID stays with the workspace across reorder and monitor moves.
	ID uint64
	// Name is set for workspaces declared in config; empty for dynamic ones.
	Name string
	// overviewAfter is the numbered workspace from which this named workspace
	// was invoked. It only orders overview rows; numbering stays unchanged.
	overviewAfter *Workspace
	Overflow      Overflow
	Columns       []Column
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
func (w *Workspace) AddWindow(id WindowID) { w.addWindow(id, 0) }

// addWindow is AddWindow with the width of the new column in cells (zero:
// automatic).
func (w *Workspace) addWindow(id WindowID, cells int) {
	if id == 0 || w.has(id) {
		return
	}
	if w.cover() != 0 {
		// A new window never moves the view off a covering fullscreen
		// window (ADR 011): it waits, hidden, after the focused column.
		at := len(w.Columns)
		if !w.policy().equalCells {
			// A covering float or stashed window may have no column under it.
			at = min(w.Focus+1, len(w.Columns))
		}
		w.Columns = slices.Insert(w.Columns, at, Column{Windows: []WindowID{id}, Cells: cells})
		return
	}
	w.addColumn(Column{Windows: []WindowID{id}, Cells: cells})
}

// addColumn places a new column like a new window and focuses it.
func (w *Workspace) addColumn(col Column) {
	at := 0
	col.BandStart = false
	if w.policy().wraps {
		// After the focused band: it joins it when it has room, else it
		// starts a band of its own below it.
		at, col.BandStart = w.spawnIndex()
	} else if w.policy().equalCells {
		// Ordered overflow depends only on window order: new windows go last.
		at = len(w.Columns)
	} else if len(w.Columns) > 0 {
		at = w.Focus + 1
	}
	if w.policy().maximizeHides() && len(w.Columns) > 0 {
		w.unmaximize()
	}
	w.Columns = append(w.Columns, Column{})
	copy(w.Columns[at+1:], w.Columns[at:])
	w.Columns[at] = col
	w.Focus = at
	w.scroll()
}

// AddSlotWindow places a slot window in its own column, ordered by slot
// number before the other columns. Focus stays on the focused window: slot
// windows are automatic events (ADR 011 golden rule).
func (w *Workspace) AddSlotWindow(id WindowID, slot int, cells int) {
	if id == 0 || w.has(id) {
		return
	}
	// Right after the last slot column with a lower number, else first.
	at := 0
	for i, c := range w.Columns {
		if c.Slot > 0 && c.Slot < slot {
			at = i + 1
		}
	}
	w.Columns = slices.Insert(w.Columns, at, Column{Windows: []WindowID{id}, Cells: cells, Slot: slot})
	if len(w.Columns) > 1 && at <= w.Focus {
		w.Focus++
	}
	w.scroll()
}

// setSlotCells updates the width in cells of slot column n, if present.
func (w *Workspace) setSlotCells(n int, cells int) {
	for i := range w.Columns {
		if w.Columns[i].Slot == n {
			w.Columns[i].Cells = cells
		}
	}
	w.scroll()
}

// inSlot reports whether window id is in slot column n.
func (w *Workspace) inSlot(id WindowID, n int) bool {
	for _, c := range w.Columns {
		if c.Slot == n && slices.Contains(c.Windows, id) {
			return true
		}
	}
	return false
}

// unslot turns slot column n into a normal column.
func (w *Workspace) unslot(n int) {
	for i := range w.Columns {
		if w.Columns[i].Slot == n {
			w.Columns[i].Slot = 0
		}
	}
}

func (w *Workspace) RemoveWindow(id WindowID) {
	w.maximized = slices.DeleteFunc(w.maximized, func(v WindowID) bool { return v == id })
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return v == id })
	if w.fullscreen == id {
		w.fullscreen = 0
	}
	if w.stashOver == id {
		w.stashOver = 0
	}
	if w.hiddenFullscreen == id {
		w.hiddenFullscreen = 0
	}
	if w.left == id {
		w.left = 0
	}
	if i := w.floatIndex(id); i >= 0 {
		w.Floats = slices.Delete(w.Floats, i, i+1)
		if len(w.Floats) == 0 || w.Floats[len(w.Floats)-1].below {
			w.floatFocus = false
		}
		return
	}
	if i := w.stashIndex(id); i >= 0 {
		// The right neighbor takes the selection, else the left one.
		w.Stash = slices.Delete(w.Stash, i, i+1)
		if i < w.stashAt || w.stashAt == len(w.Stash) {
			w.stashAt = max(w.stashAt-1, 0)
		}
		if len(w.Stash) == 0 {
			w.stashFocus, w.stashHidden, w.stashOver = false, false, 0
		}
		return
	}
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v != id {
				continue
			}
			c := &w.Columns[i]
			c.Windows = append(c.Windows[:j], c.Windows[j+1:]...)
			c.Shares = nil
			if len(c.Windows) == 0 {
				w.deleteColumn(i)
				if len(w.Columns) == 0 {
					w.Focus = 0
					w.View = 0
					return
				}
				if i < w.Focus || (i == w.Focus && i > 0) {
					w.Focus--
				}
			} else {
				if j < c.Focus {
					c.Focus--
				} else if j == c.Focus && c.Focus > 0 {
					c.Focus--
				}
			}
			w.scroll()
			return
		}
	}
}

// FocusID selects a window and scrolls its column into view; a native
// float is raised, a stashed window is selected.
func (w *Workspace) FocusID(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 {
		f := w.Floats[i]
		f.below = false
		w.Floats = append(slices.Delete(w.Floats, i, i+1), f)
		if !w.overCover(f) {
			// A dialog shown over the cover keeps the stash over it;
			// leaving it returns there.
			w.dropStashOver()
		}
		w.floatFocus = true
		return true
	}
	if i := w.stashIndex(id); i >= 0 {
		if w.stashHidden {
			return false
		}
		w.stashAt, w.stashFocus, w.floatFocus = i, true, false
		return true
	}
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v == id {
				w.dropStashOver()
				w.floatFocus, w.stashFocus = false, false
				w.raiseColumns()
				if w.policy().maximizeHides() && w.Focus != i && w.Focus < len(w.Columns) && !w.Columns[i].FullWidth {
					w.unmaximize()
				}
				w.Focus = i
				w.Columns[i].Focus = j
				w.scroll()
				return true
			}
		}
	}
	return false
}

// FocusColumn moves focus to the column on that side. From a covering
// fullscreen window it leaves fullscreen for the window there (leaveCover);
// false means it stays, with no window on that side.
func (w *Workspace) FocusColumn(dir int) bool {
	from, _ := w.Focused()
	defer w.focusModal(from)
	if w.coverDialogFocused() {
		// The first move goes back to the window under it: the stash
		// over the cover, or the cover.
		w.floatFocus = false
		return true
	}
	if w.pinned() && !w.stashOverCover() {
		return w.leaveCover(func() { w.focusColumn(dir) })
	}
	w.focusColumn(dir)
	return true
}

func (w *Workspace) focusColumn(dir int) {
	if w.floatFocus {
		// The first move leaves the native float for the stash or columns,
		// if there is one: otherwise the float keeps the focus.
		w.leaveFloat()
		return
	}
	if w.stashFocused() {
		// Inside the stash, stopping at its ends: only hiding it leaves.
		if (dir == -1 || dir == 1) && w.stashAt+dir >= 0 && w.stashAt+dir < len(w.Stash) {
			w.stashAt += dir
		}
		return
	}
	if i := w.columnToward(dir); i >= 0 {
		w.raiseColumns()
		if w.policy().maximizeHides() {
			w.unmaximize()
		}
		w.Focus = i
		w.scroll()
	}
}

// columnToward returns the column focus-column-left/right (dir -1/1) goes
// to, or -1 at the edge. Scroll overflow follows the column order; fixed
// overflow stacks columns (spiral), so it follows the screen: the column
// on that side. Cascade stays in its band: past the band edge is the edge.
func (w *Workspace) columnToward(dir int) int {
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) || w.pinned() {
		return -1
	}
	if w.onScreenFocus() {
		return w.screenNeighbor(dir, 0)
	}
	if i := w.Focus + dir; !w.leavesBand(i) {
		return i
	}
	return -1
}

// onScreenFocus reports whether directional focus follows the screen:
// fixed overflow with every column visible.
func (w *Workspace) onScreenFocus() bool {
	return w.Overflow == OverflowFixed && w.fullscreen == 0 && !w.Columns[w.Focus].FullWidth
}

// FocusWindow moves focus inside the column, then to a fixed-overflow column
// above or below on screen. Up at the top raises the topmost demoted covering
// float; false means no window is available in that direction. From a
// covering fullscreen window it leaves fullscreen for that window.
func (w *Workspace) FocusWindow(dir int) bool {
	from, _ := w.Focused()
	defer w.focusModal(from)
	if w.coverDialogFocused() {
		w.floatFocus = false
		return true
	}
	if w.pinned() && !w.stashOverCover() {
		return w.leaveCover(func() { w.focusWindow(dir) })
	}
	return w.focusWindow(dir)
}

func (w *Workspace) focusWindow(dir int) bool {
	if w.floatFocus {
		return w.leaveFloat()
	}
	if w.stashFocused() {
		// The stash is one row: up and down do nothing there.
		return true
	}
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) {
		return false
	}
	c := &w.Columns[w.Focus]
	if c.Focus+dir >= 0 && c.Focus+dir < len(c.Windows) {
		c.Focus += dir
		w.raiseColumns()
		w.scroll()
		return true
	}
	if w.policy().wraps && w.focusBand(dir) {
		return true
	}
	if w.onScreenFocus() {
		if i := w.screenNeighbor(0, dir); i >= 0 {
			w.Focus = i
			w.raiseColumns()
			w.Columns[i].Focus = 0
			if dir < 0 {
				w.Columns[i].Focus = len(w.Columns[i].Windows) - 1
			}
			return true
		}
	}
	if dir < 0 {
		for i := len(w.Floats) - 1; i >= 0; i-- {
			if f := w.Floats[i]; f.below && w.coversFloat(f) {
				return w.FocusID(f.ID)
			}
		}
	}
	return false
}

// screenNeighbor returns the column on screen next to the focused one, left
// or right (dx -1/1) or above or below (dy -1/1), or -1. The closest wins,
// then the one sharing the longest edge, then the nearest in column order.
func (w *Workspace) screenNeighbor(dx, dy int) int {
	rects := w.columnRects()
	cur := rects[w.Focus]
	best, bestDist, bestOverlap := -1, 0, 0
	for i, r := range rects {
		var overlap, dist int
		switch {
		case dx < 0:
			overlap, dist = min(cur.Y+cur.H, r.Y+r.H)-max(cur.Y, r.Y), cur.X-(r.X+r.W)
		case dx > 0:
			overlap, dist = min(cur.Y+cur.H, r.Y+r.H)-max(cur.Y, r.Y), r.X-(cur.X+cur.W)
		case dy < 0:
			overlap, dist = min(cur.X+cur.W, r.X+r.W)-max(cur.X, r.X), cur.Y-(r.Y+r.H)
		default:
			overlap, dist = min(cur.X+cur.W, r.X+r.W)-max(cur.X, r.X), r.Y-(cur.Y+cur.H)
		}
		if i == w.Focus || overlap <= 0 || dist < 0 {
			continue
		}
		closer := best >= 0 && (dist < bestDist || dist == bestDist && (overlap > bestOverlap ||
			overlap == bestOverlap && abs(i-w.Focus) < abs(best-w.Focus)))
		if best < 0 || closer {
			best, bestDist, bestOverlap = i, dist, overlap
		}
	}
	return best
}

func abs(n int) int { return max(n, -n) }
func (w *Workspace) MoveColumn(dir int) {
	if w.onFloat() {
		return
	}
	if (dir == -1 || dir == 1) && w.Focus+dir >= 0 && w.Focus+dir < len(w.Columns) {
		if w.policy().wraps && w.leavesBand(w.Focus+dir) {
			return // a cascade column never crosses into the adjacent band
		}
		i := w.Focus
		w.Columns[i], w.Columns[i+dir] = w.Columns[i+dir], w.Columns[i]
		// Band starts are positional: the flag stays at its index.
		w.Columns[i].BandStart, w.Columns[i+dir].BandStart = w.Columns[i+dir].BandStart, w.Columns[i].BandStart
		w.Focus += dir
		w.scroll()
	}
}

// MoveWindow moves the focused tile up or down (dir -1/1) inside its
// column; focus follows it. A slot window leaving row 0 releases the slot.
// Past the column, a cascade column moves to the adjacent band.
func (w *Workspace) MoveWindow(dir int) {
	if w.onFloat() || len(w.Columns) == 0 || (dir != -1 && dir != 1) || w.fullscreenHides() {
		return
	}
	c := &w.Columns[w.Focus]
	to := c.Focus + dir
	if to < 0 || to >= len(c.Windows) {
		w.moveBand(dir)
		return
	}
	if c.Focus == 0 || to == 0 {
		w.dropSlotOf(c.Windows[0])
	}
	c.Windows[c.Focus], c.Windows[to] = c.Windows[to], c.Windows[c.Focus]
	if validShares(*c) {
		c.Shares[c.Focus], c.Shares[to] = c.Shares[to], c.Shares[c.Focus]
	}
	c.Focus = to
	w.scroll()
}

// minShare is the smallest row height set-window-height leaves, percent.
const minShare = 10

// ResizeRow changes the focused window's height share by pct percent; the
// other rows of its column give or take it in proportion to their shares.
// Every row keeps at least minShare percent.
func (w *Workspace) ResizeRow(pct int) {
	if w.onFloat() || len(w.Columns) == 0 || w.fullscreenHides() {
		return
	}
	c := &w.Columns[w.Focus]
	n := len(c.Windows)
	if n < 2 || n*minShare > 100 {
		return
	}
	shares := c.Shares
	if !validShares(*c) {
		shares = make([]int, n)
		for i := range shares {
			shares[i] = 100 / n
		}
		shares[n-1] += 100 % n
	} else {
		shares = slices.Clone(shares)
	}
	f := c.Focus
	target := min(max(shares[f]+pct, minShare), 100-minShare*(n-1))
	delta := target - shares[f]
	if delta == 0 {
		return
	}
	// The others share -delta in proportion to their room: what they have
	// above minShare when shrinking, their share when growing.
	weight := func(v int) int {
		if delta > 0 {
			return v - minShare
		}
		return v
	}
	total := 0
	for i, v := range shares {
		if i != f {
			total += weight(v)
		}
	}
	left := delta
	for i, v := range shares {
		if i == f {
			continue
		}
		part := 0
		if total > 0 {
			part = delta * weight(v) / total
		}
		shares[i] = v - part
		left -= part
	}
	shares[f] = target
	// Rounding: the remainder goes to the rows that still have room, last
	// first.
	for i := n - 1; left != 0 && i >= 0; i-- {
		if i == f {
			continue
		}
		step := left
		if left > 0 {
			step = min(left, shares[i]-minShare)
		}
		shares[i] -= step
		left -= step
	}
	c.Shares = shares
}

// takeColumn removes the focused column and returns it, as a normal column.
func (w *Workspace) takeColumn() (Column, bool) {
	if len(w.Columns) == 0 || w.onFloat() {
		return Column{}, false
	}
	col := w.Columns[w.Focus]
	col.Slot = 0
	if slices.Contains(col.Windows, w.fullscreen) {
		w.fullscreen = 0
	}
	if slices.Contains(col.Windows, w.stashOver) {
		w.stashOver = 0
	}
	if slices.Contains(col.Windows, w.left) {
		w.left = 0
	}
	// Its windows leave this workspace's recent use with it.
	w.recent = slices.DeleteFunc(w.recent, func(v WindowID) bool { return slices.Contains(col.Windows, v) })
	col.BandStart = false
	w.deleteColumn(w.Focus)
	w.Focus = min(w.Focus, max(len(w.Columns)-1, 0))
	if len(w.Columns) == 0 {
		w.View = 0
	} else {
		w.scroll()
	}
	return col, true
}

// insertColumn adds a column at index at (clamped) and focuses it.
func (w *Workspace) insertColumn(at int, col Column) {
	at = min(max(at, 0), len(w.Columns))
	if w.policy().maximizeHides() && len(w.Columns) > 0 {
		w.unmaximize()
	}
	// In a cascade the column goes before column at, in its band.
	col.BandStart = false
	if w.policy().wraps && at < len(w.Columns) && w.Columns[at].BandStart {
		col.BandStart, w.Columns[at].BandStart = true, false
	}
	w.Columns = slices.Insert(w.Columns, at, col)
	w.Focus = at
	w.scroll()
}

// GrowColumn widens the focused column by one cell, taking it from another
// column of its lane, or from the empty space in scroll overflow. A lane
// that cannot grow further goes back to equal shares when cycle is set
// (cycle-column-width); otherwise nothing changes (set-column-width +N).
// A lane is the band of a cascade, or the whole row in fixed overflow while
// it holds MaxColumns columns at most; past that the spiral ignores cells.
func (w *Workspace) GrowColumn(cycle bool) {
	if w.onFloat() || len(w.Columns) == 0 || w.fullscreenHides() {
		return
	}
	defer w.scroll()
	if w.policy().wraps && w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	}
	w.unmaximize()
	k := max(w.MaxColumns, 1)
	if w.policy().equalCells {
		if !w.policy().wraps && !w.laneRow() {
			return
		}
		first, last := w.laneBounds(w.laneOf(w.Focus))
		n := last - first + 1
		pushed := false
		if w.policy().wraps && cycle && n > 1 && n >= k && !w.unevenLane(first, last) {
			// A full band makes room: a neighbor goes to the adjacent band.
			w.pushNeighbor()
			first, last = w.laneBounds(w.laneOf(w.Focus))
			n, pushed = last-first+1, true
		}
		if n == 1 || n >= k {
			if w.policy().wraps && cycle && !pushed && w.restoreBand() {
				// End of the cycle: the pushed columns came back.
				first, last = w.laneBounds(w.laneOf(w.Focus))
				w.setLaneCells(first, make([]int, last-first+1))
				return
			}
			// Nothing to grow, but a lane left uneven (a column joined
			// it, or max-columns shrank) still cycles back to equal.
			if cycle && slices.ContainsFunc(w.Columns[first:last+1], func(c Column) bool { return c.Cells != 0 }) {
				w.setLaneCells(first, make([]int, n))
			}
			return
		}
		cells, ok := w.laneCells(first, last, w.Focus)
		if !ok {
			w.setLaneCells(first, cells)
			if k%n > 0 {
				return // The remainder to the focused column was the step.
			}
		}
		if d := w.donor(first, last, cells); d >= 0 {
			w.moveCell(first, cells, d, w.Focus)
		} else if cycle {
			if w.policy().wraps && w.restoreBand() {
				// End of the cycle: the pushed columns came back.
				first, last = w.laneBounds(w.laneOf(w.Focus))
				n = last - first + 1
			}
			w.setLaneCells(first, make([]int, n))
		}
		return
	}
	c := &w.Columns[w.Focus]
	cur := w.scrollCells(w.Focus)
	switch {
	case cur < k:
		c.Cells = cur + 1
	case cycle:
		c.Cells = 0
	}
}

// ShrinkColumn narrows the focused column by one cell, giving it to its
// right neighbor in the lane (the left one at the lane's end). A column
// never goes below one cell.
func (w *Workspace) ShrinkColumn() {
	if w.onFloat() || len(w.Columns) == 0 || w.fullscreenHides() {
		return
	}
	defer w.scroll()
	if w.policy().wraps && w.Columns[w.Focus].FullWidth {
		w.unmaximizeBand()
	}
	w.unmaximize()
	k := max(w.MaxColumns, 1)
	if w.policy().equalCells {
		if !w.policy().wraps && !w.laneRow() {
			return
		}
		first, last := w.laneBounds(w.laneOf(w.Focus))
		n := last - first + 1
		if n == 1 || n >= k {
			return
		}
		cells, _ := w.laneCells(first, last, -1)
		if cells[w.Focus-first] <= 1 {
			return
		}
		to := w.Focus + 1
		if to > last {
			to = w.Focus - 1
		}
		w.moveCell(first, cells, w.Focus, to)
		return
	}
	w.Columns[w.Focus].Cells = max(w.scrollCells(w.Focus)-1, 1)
}

// scrollCells is the width in cells of column i in scroll overflow: its
// cells, at most the ones in view for an auto column.
func (w *Workspace) scrollCells(i int) int {
	if c := w.Columns[i].Cells; c > 0 {
		return c
	}
	k := max(w.MaxColumns, 1)
	return k / min(len(w.Columns), k)
}

// laneCells is the cells of the lane first..last, summing to MaxColumns.
// The stored cells are used when every column is set and they sum to it
// (ok); otherwise each column gets an equal share and the remainder goes
// to column remAt, or one cell each to the leftmost columns when remAt is
// negative.
func (w *Workspace) laneCells(first, last, remAt int) (cells []int, ok bool) {
	k := max(w.MaxColumns, 1)
	n := last - first + 1
	cells = make([]int, n)
	sum := 0
	for i := range cells {
		cells[i] = w.Columns[first+i].Cells
		sum += cells[i]
		if cells[i] == 0 {
			sum = -1 << 30
		}
	}
	if sum == k {
		return cells, true
	}
	for i := range cells {
		cells[i] = k / n
	}
	if remAt >= 0 {
		cells[remAt-first] += k % n
	} else {
		for i := range k % n {
			cells[i]++
		}
	}
	return cells, false
}

// unevenLane reports whether a column of the lane first..last has its
// own width.
func (w *Workspace) unevenLane(first, last int) bool {
	return slices.ContainsFunc(w.Columns[first:last+1], func(c Column) bool { return c.Cells != 0 })
}

// setLaneCells stores cells on the lane's columns, from column first.
func (w *Workspace) setLaneCells(first int, cells []int) {
	for i, c := range cells {
		w.Columns[first+i].Cells = c
	}
}

// donor is the lane column that gives a cell to the focused one: among the
// others with more than one cell, the largest; on a tie the nearest to the
// focus, the right side first. -1 when there is none.
func (w *Workspace) donor(first, last int, cells []int) int {
	best := -1
	for i := first; i <= last; i++ {
		c := cells[i-first]
		if i == w.Focus || c <= 1 {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		bc, bd, d := cells[best-first], abs(best-w.Focus), abs(i-w.Focus)
		if c > bc || c == bc && (d < bd || d == bd && i > w.Focus) {
			best = i
		}
	}
	return best
}

// moveCell moves one cell from column from to column to of the lane that
// starts at first, and stores the lane.
func (w *Workspace) moveCell(first int, cells []int, from, to int) {
	cells[from-first]--
	cells[to-first]++
	w.setLaneCells(first, cells)
}

// maximize sets FullWidth on column i; unlike ToggleFullWidth it never toggles.
func (w *Workspace) maximize(i int) {
	if i >= 0 && i < len(w.Columns) {
		w.Columns[i].FullWidth = true
		id := w.Columns[i].Windows[w.Columns[i].Focus]
		w.maximized = slices.DeleteFunc(w.maximized, func(v WindowID) bool { return v == id })
		w.maximized = slices.Insert(w.maximized, 0, id)
	}
}

// hiddenByMaximized reports whether the maximized focused column hides
// column i on screen: in fixed overflow it hides the rest of the row. A
// maximized cascade column has its band to itself, so it hides nothing.
func (w *Workspace) hiddenByMaximized(i int) bool {
	return w.policy().maximizeHides() && w.Focus < len(w.Columns) && w.Columns[w.Focus].FullWidth && i != w.Focus
}

// unmaximize clears the focused column in scroll and cascade, or all
// maximized columns in fixed overflow (including after Escape changed focus).
func (w *Workspace) unmaximize() {
	if w.policy().maximizeHides() {
		w.clearMaximized()
	} else if w.Focus < len(w.Columns) {
		w.Columns[w.Focus].FullWidth = false
	}
}

// clearMaximized unmaximizes every column and forgets the history.
func (w *Workspace) clearMaximized() {
	for i := range w.Columns {
		w.Columns[i].FullWidth = false
	}
	w.maximized = nil
}

// transferMaximization changes the maximized column without forgetting MRU.
func (w *Workspace) transferMaximization(i int) {
	if i == w.Focus || i < 0 || i >= len(w.Columns) {
		return
	}
	w.Columns[w.Focus].FullWidth = false
	w.maximize(i)
}

// ToggleFullWidth expands the focused tiled column without changing its saved width.
func (w *Workspace) ToggleFullWidth() {
	if len(w.Columns) == 0 || w.onFloat() {
		return
	}
	switch {
	case w.policy().wraps && w.Columns[w.Focus].FullWidth:
		w.unmaximizeBand()
	case w.policy().wraps:
		// A cascade column takes a band of its own; the others stay seen.
		w.isolateBand()
		w.maximize(w.Focus)
	case w.Columns[w.Focus].FullWidth:
		w.unmaximize()
	default:
		w.maximize(w.Focus)
	}
	w.scroll()
}

// unmaximizeBand unmaximizes the focused cascade column and brings back
// the columns its maximization sent to the adjacent bands.
func (w *Workspace) unmaximizeBand() {
	w.Columns[w.Focus].FullWidth = false
	w.restoreBand()
}

func (w *Workspace) ToggleFullscreen() {
	id, ok := w.Focused()
	if !ok {
		return
	}
	if w.stashOverCover() {
		// A stashed window the user picked over the cover replaces it.
		w.leaveFullscreen()
	}
	if w.fullscreen == id {
		// The user was on it: it keeps the focus, in front of floats that
		// mapped meanwhile. Cleared first, so that floats it covers drop
		// below the columns.
		w.fullscreen = 0
		w.FocusID(id)
		w.endFullscreen()
		return
	}
	w.fullscreen = id
	w.scroll()
}

// endFullscreen ends the fullscreen: floats it kept demoted are promoted
// again and the view follows the focus.
func (w *Workspace) endFullscreen() {
	w.fullscreen, w.stashOver = 0, 0
	w.reconcileFloats()
	w.scroll()
}

// leaveFullscreen ends the fullscreen for another window the user chose,
// and refuses its client's requests until it is focused again (mayCover,
// settleLeft).
func (w *Workspace) leaveFullscreen() {
	if w.fullscreen != 0 {
		w.left = w.fullscreen
	}
	w.endFullscreen()
}

// Activate focuses a window and makes it visible: it leaves another
// window's fullscreen, which hides everything else. Over a covering
// window, a dialog drawn over it, a stashed window or a dialog of one
// shows over it instead, as ToggleStashVisible does: the cover stays
// fullscreen behind, so a game never sees its fullscreen end. A stashed
// window whose own fullscreen waited while hidden replaces the cover
// (coverStays); a dialog of a stashed window activates that window
// first.
func (w *Workspace) Activate(id WindowID) {
	if c := w.cover(); c != 0 && id != c {
		owner := id
		if f := w.floatIndex(id); f >= 0 {
			owner = w.dialogOwner(w.Floats[f], c)
		}
		if owner != id && owner != c && w.stashIndex(owner) >= 0 && !w.coverStays(owner) {
			w.Activate(owner)
			w.FocusID(id)
			w.focusModal(0)
			return
		}
		if i := w.stashIndex(owner); i >= 0 && w.coverStays(owner) {
			if !w.stashOverCover() {
				w.showStashOver()
			}
			w.stashAt, w.stashFocus, w.floatFocus = i, true, false
		}
		if owner == c || w.stashOverCover() && w.stashIndex(owner) >= 0 {
			if owner != id {
				w.FocusID(id)
			}
			w.focusModal(0)
			return
		}
	}
	if i := w.stashIndex(id); i >= 0 && w.stashHidden {
		// A hidden stashed window must be seen: show the stash with it,
		// selected. Another window's fullscreen leaves first, so its own
		// pending fullscreen comes back.
		w.leaveFullscreen()
		w.stashAt = i
		w.showStash()
	}
	if w.fullscreen != 0 && w.fullscreen != id {
		w.leaveFullscreen()
	}
	w.FocusID(id)
	w.focusModal(0)
}

// coverStays reports whether activating stashed window id keeps the
// covering window fullscreen under the stash: a cover outside the stash,
// and no pending fullscreen of id's own that would replace it.
func (w *Workspace) coverStays(id WindowID) bool {
	c := w.cover()
	return c != 0 && w.stashIndex(c) < 0 && !(w.stashHidden && w.hiddenFullscreen == id)
}

// SetFullscreen applies a client request. It never moves focus: client
// requests are automatic events (ADR 011 golden rule).
func (w *Workspace) SetFullscreen(id WindowID, on bool) {
	if on && !w.mayCover(id) {
		return
	}
	if w.isFloat(id) {
		if w.stashHidden && w.stashIndex(id) >= 0 {
			// Applied when the stash is shown again.
			if on {
				w.hiddenFullscreen = id
			} else if w.hiddenFullscreen == id {
				w.hiddenFullscreen = 0
			}
			return
		}
	}
	if !w.has(id) {
		return
	}
	if !on {
		if w.fullscreen == id {
			w.endFullscreen()
		}
		return
	}
	if id != w.stashOver {
		// Another window covers: the override was for the old cover.
		w.dropStashOver()
	}
	w.fullscreen = id
	if !w.isFloat(id) {
		w.scroll()
	}
}

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

// settleLeft drops the latch once its window has the focus again, however
// it got it; core calls it after each event.
func (w *Workspace) settleLeft() {
	if id, ok := w.Focused(); ok && id == w.left {
		w.left = 0
	}
}

// mayCover reports whether a client may make id fullscreen. In fixed
// overflow it would cover the output at once and take the keyboard from
// the focused window, so only the focused window may; the request of
// another is refused and the user can still toggle-fullscreen it. A
// hidden stash's request waits for the show (showStash). A window whose
// fullscreen the user left may only once focused again, in any overflow.
func (w *Workspace) mayCover(id WindowID) bool {
	if id == w.left {
		if focused, ok := w.Focused(); !ok || focused != id {
			return false
		}
		w.left = 0
	}
	if !w.policy().equalCells || w.stashHidden && w.stashIndex(id) >= 0 {
		return true
	}
	focused, ok := w.Focused()
	return ok && focused == id
}

// pinned reports whether the covering fullscreen window hides every other
// window: focus moves leave it (leaveCover), and the overview shows it as
// the front card of its row. Only in scroll overflow does moving to a
// tiled neighbor scroll it off and show the target instead.
func (w *Workspace) pinned() bool {
	c := w.cover()
	return c != 0 && (w.policy().equalCells || w.isFloat(c))
}

// leaveCover runs a focus move from the covering fullscreen window as if it
// were not fullscreen. When the move reaches another window, fullscreen
// ends and that window shows, focused; otherwise (an edge) nothing changes
// and the caller goes on to the next workspace or monitor.
func (w *Workspace) leaveCover(move func()) bool {
	w.focusCover()
	full := w.fullscreen
	w.fullscreen = 0
	move()
	if id, ok := w.Focused(); ok && id != full {
		w.fullscreen = full
		w.leaveFullscreen()
		return true
	}
	w.fullscreen = full
	return false
}

// focusCover points the focus state at the covering fullscreen window,
// which Focused already reports, so a user action acts on the window on
// screen, not on one it hides.
func (w *Workspace) focusCover() {
	c := w.cover()
	if c == 0 {
		return
	}
	if d := w.coverDialog(); d != 0 {
		// Its dialog is on screen with the focus: the action is on it.
		w.FocusID(d)
		return
	}
	if w.stashOverCover() {
		// The stash shown over it has the focus.
		w.stashFocus, w.floatFocus = true, false
		return
	}
	if w.isFloat(c) {
		w.FocusID(c)
		return
	}
	w.floatFocus, w.stashFocus = false, false
	for i, col := range w.Columns {
		if j := slices.Index(col.Windows, c); j >= 0 {
			w.Focus, w.Columns[i].Focus = i, j
		}
	}
}

// cover returns the fullscreen window that covers the output, or 0. The
// fullscreen is exclusive: while it covers, no other window of the
// workspace is drawn, so the output can scan it out.
func (w *Workspace) cover() WindowID {
	if w.fullscreen == 0 {
		return 0
	}
	if w.isFloat(w.fullscreen) || w.policy().equalCells {
		return w.fullscreen
	}
	// Scroll overflow: only while its column is aligned on the output.
	for i, c := range w.Columns {
		if slices.Contains(c.Windows, w.fullscreen) && w.columnX(i)-w.View == 0 {
			return w.fullscreen
		}
	}
	return 0
}

// fullscreenHides reports whether a fullscreen window hides the focused
// column's layout: the others (fixed) or its own column (scroll). Column
// edits would change what the user cannot see.
func (w *Workspace) fullscreenHides() bool {
	return w.fullscreen != 0 && (w.policy().equalCells || w.fullscreenColumn(w.Focus))
}

func (w *Workspace) fullscreenColumn(i int) bool {
	for _, id := range w.Columns[i].Windows {
		if id == w.fullscreen && id != 0 {
			return true
		}
	}
	return false
}
func (w *Workspace) columnWidth(i int) int {
	return w.columnWidthFor(i, false)
}

func (w *Workspace) columnWidthFor(i int, ignoreFullWidth bool) int {
	if width, ok := w.specialWidth(i, ignoreFullWidth); ok {
		return width
	}
	if w.policy().wraps || w.laneRow() {
		return w.cellWidth(i)
	}
	return w.scrollWidth(i)
}

// specialWidth is the width of a maximized or fullscreen column.
func (w *Workspace) specialWidth(i int, ignoreFullWidth bool) (int, bool) {
	if w.Columns[i].FullWidth && !ignoreFullWidth {
		return max(w.Usable.W-2*w.gap(), 0), true
	}
	if w.fullscreenColumn(i) {
		return w.Output.W, true
	}
	return 0, false
}

// laneRow reports whether the columns share one row of cells: fixed
// overflow up to MaxColumns of them. Past that the spiral applies and
// cells are ignored.
func (w *Workspace) laneRow() bool {
	return w.Overflow == OverflowFixed && len(w.Columns) <= max(w.MaxColumns, 1)
}

// scrollWidth is the width of column i when it is sized on its own: auto
// columns share the usable width as the visible ones would, the others take
// their cells (at most MaxColumns) and the gaps between them.
func (w *Workspace) scrollWidth(i int) int {
	k := max(w.MaxColumns, 1)
	if w.policy().equalCells {
		// Fixed overflow past MaxColumns: the spiral's slots.
		return w.equalShare(k)
	}
	c := w.Columns[i].Cells
	if c <= 0 {
		return w.equalShare(min(len(w.Columns), k))
	}
	c = min(c, k)
	return max(c*w.equalShare(k)+(c-1)*w.gap(), 0)
}
func (w *Workspace) columnX(i int) int {
	x := w.Usable.X + w.gap()
	for j := 0; j < i; j++ {
		x += w.columnWidth(j) + w.gap()
	}
	return x
}
func (w *Workspace) scroll() {
	before := w.View
	defer func() { w.retarget(before) }()
	if w.policy().wraps {
		w.markBands()
		w.View = w.band(w.Focus) * w.Usable.H
		return
	}
	if w.Overflow == OverflowFixed {
		w.View = 0
		return
	}
	if len(w.Columns) == 0 {
		return
	}
	left := w.columnX(w.Focus) - w.View
	width := w.columnWidth(w.Focus)
	if w.fullscreenColumn(w.Focus) {
		w.View += left - w.Output.X
		return
	}
	minX := w.Usable.X + w.gap()
	maxX := w.Usable.X + w.Usable.W - w.gap()
	if width > maxX-minX || left < minX {
		w.View += left - minX
	} else if left+width > maxX {
		w.View += left + width - maxX
	}
}

// columnRects returns each column's area on screen, before stacking windows.
func (w *Workspace) columnRects() []Rect {
	return w.columnRectsInto(nil, false)
}

// columnRectsFor shares the fixed layout geometry with the overview's hidden
// columns card, without changing FullWidth or the clients' saved buffers.
func (w *Workspace) columnRectsFor(ignoreFullWidth bool) []Rect {
	return w.columnRectsInto(nil, ignoreFullWidth)
}

// columnRectsInto is columnRectsFor built in dst's storage (dst[:0] grown
// when too small): the result aliases dst, so a caller passing a reused
// buffer must be done with the previous result.
func (w *Workspace) columnRectsInto(dst []Rect, ignoreFullWidth bool) []Rect {
	if w.policy().wraps {
		return w.cascadeRectsInto(dst, ignoreFullWidth)
	}
	g := w.gap()
	y, h := w.Usable.Y+g, max(w.Usable.H-2*g, 0)
	rects := slices.Grow(dst[:0], len(w.Columns))[:len(w.Columns)]
	view := w.View + w.shiftPixels()
	x := w.Usable.X + g
	for i := range w.Columns {
		width := w.columnWidthFor(i, ignoreFullWidth)
		rects[i] = Rect{X: x - view, Y: y, W: width, H: h}
		x += width + g
	}
	if !ignoreFullWidth && w.Overflow == OverflowFixed && len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth {
		rects[w.Focus].X = w.Usable.X + g
		return rects
	}
	k := max(w.MaxColumns, 1)
	if w.Overflow != OverflowFixed || len(w.Columns) <= k {
		return rects
	}
	// The last slot holds the spiral; the columns before it keep their place.
	area := rects[k-1]
	for i := k - 1; i < len(w.Columns)-1; i++ {
		rects[i], area = split(area, g, (i-k+1)%2 == 0)
	}
	rects[len(rects)-1] = area
	return rects
}

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
		hh := avail * share / 100
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
// least 1, summing to 100.
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
	return sum == 100
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
			hidden := (cover != 0 && id != cover) || ((fullColumn || w.policy().equalCells && w.fullscreen != 0) && !full) || w.hiddenByMaximized(i)
			if full {
				// Scroll mode aligns the view on the column; fixed never scrolls.
				r = w.Output
				if !w.policy().equalCells {
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
