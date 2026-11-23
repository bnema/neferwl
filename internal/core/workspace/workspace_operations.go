package workspace

import (
	"cmp"
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

// EdgeKind describes when column navigation should visit another workspace.
type EdgeKind uint8

const (
	// EdgeNone keeps navigation in this workspace.
	EdgeNone EdgeKind = iota
	// EdgeAlways crosses the edge without attempting local focus.
	EdgeAlways
	// EdgeIfStuck crosses only when local focus cannot leave fullscreen.
	EdgeIfStuck
	// EdgeFirst attempts crossing before local focus.
	EdgeFirst
)

// ColumnEdge says whether a focus-column step toward dir leaves this
// workspace's content (to the neighbor monitor or workspace): always (EdgeAlways),
// only if a pinned fullscreen window blocks the step (EdgeIfStuck), or before
// the local focus step (EdgeFirst). EdgeNone keeps the step local.
func (w *Workspace) ColumnEdge(dir int) EdgeKind {
	if w.Engine().Bands() && w.Cover() != 0 && w.IsFloat(w.Cover()) && !w.stashOverCover() && !w.coverDialogFocused() {
		return EdgeAlways
	}
	if w.Pinned() && !w.stashOverCover() {
		return EdgeIfStuck
	}
	if w.LeavesLane(w.columnToward(dir)) && (!w.OnFloat() || w.floatFocus && !w.canLeaveFloat()) {
		return EdgeFirst
	}
	return EdgeNone
}

// MovableFullscreen reports a focused fullscreen tile or native float.
func (w *Workspace) MovableFullscreen() bool {
	fid, ok := w.Focused()
	return ok && w.Fullscreen == fid && (w.floatIndex(fid) >= 0 || !w.OnFloat())
}

// MoveFullscreen transfers the focused fullscreen window, keeping its cover.
// It reports false, moving nothing, when the focused tile's column cannot be
// taken (takeColumn fails: there are no columns, or a float or stash window
// has the focus). A caller that checked MovableFullscreen never gets false: it
// holds for a native float, which skips takeColumn, or for a focused tile.
func MoveFullscreen(from, to *Workspace, at int) bool {
	fid, _ := from.Focused()
	var col layout.Column
	if from.floatIndex(fid) < 0 {
		var ok bool
		if col, ok = from.takeColumn(); !ok {
			return false
		}
	}
	from.reconcileFloats()
	if len(col.Windows) == 0 {
		moveFloat(from, to, fid)
	} else {
		to.receive(col, at)
		to.SetFullscreen(fid, true)
	}
	return true
}

// MoveColumnTo transfers the focused tiled column to a workspace at index at.
// It reports false, moving nothing, when the column cannot be taken, such as
// while a floating or stash window has the focus.
func MoveColumnTo(from, to *Workspace, at int) bool {
	held := from.heldIn(from.focusedColumn())
	col, ok := from.takeColumn()
	if !ok {
		return false
	}
	to.receive(col, at)
	to.carryFullscreen(held)
	return true
}

// MoveWindowTo transfers one focused tile into a new column at index at.
// Like MoveColumnTo, it uses receive's insertion semantics even on a full fixed
// workspace. If the focused window cannot be taken, neither workspace changes.
func MoveWindowTo(from, to *Workspace, at int) bool {
	held := from.heldIn(from.focusedTile())
	id, ok := from.takeWindow()
	if !ok {
		return false
	}
	to.receive(layout.Column{Windows: []WindowID{id}}, at)
	to.carryFullscreen(held)
	return true
}

// MoveFocused transfers the focused column or window to a workspace. A moved
// column or window joins the destination at the layout's default position.
func MoveFocused(from, to *Workspace, column bool) bool {
	id, ok := from.Focused()
	if !ok {
		return false
	}
	if i := from.floatIndex(id); i >= 0 {
		f := from.Floats[i]
		if f.free && f.back != nil {
			f = f.rehome()
		}
		from.RemoveWindow(id)
		to.AddFloating(id, f.W, f.H)
		to.Floats[len(to.Floats)-1] = f
		to.Activate(id)
	} else if i := from.StashIndex(id); i >= 0 {
		f := from.st().entries[i].rehome()
		from.RemoveWindow(id)
		to.addStash(f)
		to.Activate(id)
	} else {
		col := layout.Column{Windows: []WindowID{id}}
		var held []WindowID
		if column {
			held = from.heldIn(from.focusedColumn())
			col, ok = from.takeColumn()
		} else {
			held = from.heldIn(from.focusedTile())
			_, ok = from.takeWindow()
		}
		if !ok {
			return false
		}
		to.receive(col, -1)
		to.carryFullscreen(held)
	}
	return true
}

// Expel takes one tiled window and receives it at the destination edge.
func Expel(from, to *Workspace, dir int) bool {
	id, ok := from.takeWindow()
	if !ok {
		return false
	}
	to.expelTo(id, dir)
	return true
}

// Merge hands the source windows to dst in tile, float, then stash order.
func Merge(dst, src *Workspace) {
	for _, c := range src.Columns {
		for _, id := range c.Windows {
			dst.AddWindow(id)
		}
	}
	for _, f := range src.Floats {
		if f.free && f.back != nil {
			f = f.rehome()
		}
		dst.AddFloating(f.ID, f.W, f.H)
		dst.Floats[len(dst.Floats)-1] = f
	}
	dst.reconcileFloats()
	// A lent stash stays with its owner, which lends it again; src's own
	// windows move once, through the stash handoff.
	src.Lend(nil)
	for i := range src.own.entries {
		src.own.entries[i] = src.own.entries[i].rehome()
	}
	// dst keeps its stash state (selection, visibility, focus), even empty.
	d := dst.st()
	at, view, focus, hidden, fs := d.at, d.view, d.focus, d.hidden, d.fullscreen
	src.GiveStash(d, false)
	d.at, d.view, d.focus, d.hidden, d.fullscreen = at, view, focus, hidden, cmp.Or(fs, d.fullscreen)
	dst.Reattach()
	// Held members stay held where the layout scrolls (hold refuses
	// elsewhere): in a column or band of their own, off screen.
	for _, id := range src.Held {
		if dst.Engine().Scrolls() && dst.ColumnOf(id) >= 0 {
			dst.isolate(id)
			dst.hold(id)
		}
	}
}

// FloatAt returns a native float and its placed rect, if present.
func (w *Workspace) FloatAt(id WindowID) (Float, Rect, bool) {
	i := w.floatIndex(id)
	if i < 0 {
		return Float{}, Rect{}, false
	}
	f := w.Floats[i]
	return f, w.floatRect(f), true
}

// Settle clears the fullscreen refusal before optionally recording MRU focus.
func (w *Workspace) Settle(noteFocus bool) {
	w.settleLeft()
	if noteFocus {
		w.noteFocus()
	}
}

// LandSwipe commits a swipe's focus and view before scrolling to the focus.
func (w *Workspace) LandSwipe(view int, forward bool) bool {
	focus := w.snapFocus(view, forward)
	if focus != w.Focus && w.Engine().MaximizeHides() {
		w.Unmaximize()
	}
	changed := w.Focus != focus
	w.Focus = focus
	w.View = view
	w.scroll()
	return changed
}

// LeaveCoverFor leaves a pinned cover when selecting a different window.
// In a fullscreen group a held member takes the cover over, the previous
// one held, and an unrelated window demotes the cover (demoteCover), with
// no leave latch either way. In cascade a lone tiled cover is held the same
// way; a floating cover, or one in fixed overflow, still leaves.
//
// The group cases read Fullscreen, not Cover: overview navigation already
// scrolled the view to the selection, off the cover.
func (w *Workspace) LeaveCoverFor(id WindowID) {
	if !w.groupLeaveTo(id) && w.Pinned() && id != w.Cover() {
		w.leaveFullscreen()
	}
}

// AddTile adds a window with its cell width, optionally keeping live focus.
func (w *Workspace) AddTile(id WindowID, cells int, quiet bool) {
	if quiet {
		w.addColumnQuiet(layout.Column{Windows: []WindowID{id}, Cells: cells})
	} else {
		w.addWindow(id, cells)
	}
}

// AddRuleFloat places a rule-selected float with plain mapping values.
func (w *Workspace) AddRuleFloat(id WindowID, floating bool, width, height int, quiet bool) {
	w.addFloat(w.ruleFloat(id, floating, width, height), quiet)
}

// NewFrom creates an empty workspace with the template's settings only:
// output geometry and reservations, gaps, border, stash sizes, column
// count and layout. Every per-workspace state starts fresh; a new field
// starts empty unless it is added here. Output and Usable are copied as
// the monitor size and reservations: the template never has a
// per-workspace size.
func NewFrom(t *Workspace, id uint64) *Workspace {
	w := &Workspace{
		Lanes:      layout.Lanes{Override: t.Override, Gaps: t.Gaps, MaxColumns: t.MaxColumns, Output: t.Output, Usable: t.Usable},
		ID:         id,
		monitor:    t.monitor,
		reserved:   t.reserved,
		border:     t.border,
		TabBar:     t.TabBar,
		stashWidth: t.stashWidth,
		stashGap:   t.stashGap,
	}
	w.SetOverflow(t.Overflow)
	return w
}

// Inherit adopts the output-wide settings without changing workspace identity.
func (w *Workspace) Inherit(t *Workspace) {
	// t.Output and t.Usable are read as the monitor size and reservations: the template has no size.
	w.Gaps = t.Gaps
	w.border = t.border
	w.TabBar = t.TabBar
	w.stashWidth, w.stashGap = t.stashWidth, t.stashGap
	w.SetOutput(t.Output.W, t.Output.H)
	w.SetUsable(t.Usable)
}

// SetBorder sets the floating border and reconciles covering floats.
func (w *Workspace) SetBorder(b int) { w.border = max(b, 0); w.reconcileFloats() }

// SetStash sets the stash width and gap percentages clamped to 10-90 and 0-10.
// A width of 0 stays 0 (it is not clamped).
func (w *Workspace) SetStash(width, gap int) {
	if width != 0 {
		width = min(max(width, 10), 90)
	}
	w.stashWidth, w.stashGap = width, min(max(gap, 0), 10)
}

// StashSwipeable reports a shown stash focused beneath no native float.
func (w *Workspace) StashSwipeable() bool { return w.stashFocused() && !w.floatFocus && w.StashShown() }

// StashVisibility returns the hidden state and covering-window override.
func (w *Workspace) StashVisibility() (bool, WindowID) { return w.st().hidden, w.stashOver }

// StashAt returns the settled stash selection index.
func (w *Workspace) StashAt() int { return w.st().at }

// ShowStashAt commits an overview stash selection, dropping another hidden cover.
func (w *Workspace) ShowStashAt(i int) {
	if w.st().fullscreen != w.st().entries[i].ID {
		w.st().fullscreen = 0
	}
	w.st().at = i
	w.showStash()
}

// DragFloat moves or resizes a native float within the usable area.
func (w *Workspace) DragFloat(id WindowID, from Rect, edges domain.ResizeEdges, dx, dy int, resize bool) bool {
	i := w.floatIndex(id)
	if i < 0 {
		return false
	}
	f := &w.Floats[i]
	w.makeFree(f)
	if resize {
		r := resized(from, edges, dx, dy, 2*w.border+freeMinSize, w.Usable)
		f.W, f.H = r.W-2*w.border, r.H-2*w.border
		w.moveFree(f, r, 0, 0)
	} else {
		w.moveFree(f, from, dx, dy)
	}
	return true
}

// ScrollStep moves the view one step toward dir (-1 or 1): a column in a
// scrolling row, else to the neighbouring snap point (a cascade band). It
// reports whether the view moved.
func (w *Workspace) ScrollStep(dir int) bool {
	if len(w.Columns) == 0 || w.Pinned() {
		return false
	}
	if w.scrollsRow() {
		return w.scrollBy(dir)
	}
	if !w.Engine().Scrolls() {
		return false
	}
	w.snapBuf = w.Engine().SnapPoints(w.Snapshot(), w.snapBuf[:0])
	view := float64(w.View)
	next, found := 0.0, false
	for _, p := range w.snapBuf {
		if dir < 0 && p < view-0.5 {
			next, found = p, true
		}
		if dir > 0 && p > view+0.5 {
			next, found = p, true
			break
		}
	}
	if !found {
		return false
	}
	w.StopColumns()
	w.View = int(next)
	return true
}

// FocusRow focuses window row of column col, as a click on it does.
func (w *Workspace) FocusRow(col, row int) { w.Columns[col].Focus = row }

// RestoreFloat puts f back in the place of the float with the same ID. It
// does nothing when that float is gone.
func (w *Workspace) RestoreFloat(f Float) {
	if i := w.floatIndex(f.ID); i >= 0 {
		w.Floats[i] = f
	}
}

// OverviewRow supplies the monitor's provisional selection for one model row.
type OverviewRow struct {
	// Axis is the row's layout axis in output-local coordinates.
	Axis layout.Axis
	// Slot is the row's output-local rectangle along Axis.
	Slot Rect
	// Shown and Current describe row visibility and focus presentation; a row
	// that is not Current is drawn dimmed.
	Shown, Current bool
	// Head and Items are the stack's first item and its item count up to 2,
	// as StackHead returns them. The caller reads them once per frame; they
	// matter only for a Shown row.
	Head  StackItem
	Items int
	// Front is the monitor's provisional front stack item.
	Front StackItem
	// HiddenColumn and Card are provisional column and stash selections.
	HiddenColumn, Card int
}

// EnterOverview focuses the cover and returns an initially selected stash
// card, or -1. No provisional front is chosen yet, so the stack's first item
// decides whether the columns take the focus back.
func (w *Workspace) EnterOverview() int {
	w.FocusCover()
	front, n := w.StackHead()
	if w.floatFocus && len(w.Columns) > 0 && (n < 2 || front.Kind == StackColumns) {
		w.floatFocus = false
	}
	if len(w.st().entries) > 0 && (n == 0 || w.stashFocused() && !w.floatFocus) {
		return w.st().at
	}
	return -1
}

// Pickable reports whether an overview window is not a small hidden float.
func (w *Workspace) Pickable(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 && w.StashIndex(id) < 0 {
		return w.coversFloat(w.Floats[i]) || id == w.Cover() && w.Pinned()
	}
	return true
}

// RowSpan measures the overview envelope along axis.
func (w *Workspace) RowSpan(axis layout.Axis) int {
	if axis == layout.Horizontal {
		return w.rowWidth(axis)
	}
	return w.rowHeight(axis)
}

// SaveRow snapshots live focus, float order and maximization for overview cancel.
func (w *Workspace) SaveRow() RowSnapshot {
	s := RowSnapshot{floats: append([]Float(nil), w.Floats...), maximized: slices.Clone(w.Maximized)}
	s.Focus, _ = w.Focused()
	if len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth {
		c := w.Columns[w.Focus]
		s.fullWidth = c.Selected()
	}
	return s
}

// RestoreRow restores still-present windows from an overview snapshot.
func (w *Workspace) RestoreRow(s RowSnapshot) { restoreRow(w, s) }

// AppendOverviewRow appends row, folded pile, then hidden windows. An unfolded
// pile is deferred to AppendPile by the monitor so it paints after every row.
func (w *Workspace) AppendOverviewRow(dst []Placement, r OverviewRow) ([]Placement, bool) {
	unfolded := false
	if r.Shown {
		slot := r.Slot
		slot.H -= w.pileBand(r.Axis)
		var tiles []Placement
		switch r.Items {
		case 0:
			tiles = w.previewRow(r.Axis, slot, !r.Current, r.Card < 0)
		case 1:
			tiles = w.previewItem(r.Head, r.HiddenColumn, r.Axis, slot, !r.Current, r.Card < 0)
		default:
			tiles = w.stackRow(r.Front, r.Current, r.HiddenColumn, r.Axis, slot, !r.Current, r.Card < 0)
		}
		dst = append(dst, tiles...)
		if r.Card >= 0 {
			unfolded = true
		} else {
			dst = w.pile(dst, r.Axis, slot, !r.Current, -1)
		}
	}
	for _, f := range w.Floats {
		if _, drawn := w.ItemOf(f.ID); !r.Shown || !drawn {
			dst = append(dst, Placement{ID: f.ID, Hidden: true})
		}
	}
	if !r.Shown {
		dst = w.AppendHidden(dst, false)
	}
	return dst, unfolded
}

// AppendPile appends a deferred unfolded pile in its row's original slot.
func (w *Workspace) AppendPile(dst []Placement, axis layout.Axis, slot Rect, dim bool, at int) []Placement {
	slot.H -= w.pileBand(axis)
	return w.pile(dst, axis, slot, dim, at)
}
