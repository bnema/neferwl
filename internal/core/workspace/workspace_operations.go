package workspace

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/ports"
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

// ColumnEdge preserves fullscreen and floating focus rules at a content edge.
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
// taken.
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
	col, ok := from.takeColumn()
	if !ok {
		return false
	}
	to.receive(col, at)
	return true
}

// MoveFocused transfers the focused column or window to a workspace.
func MoveFocused(from, to *Workspace, column bool, at int) bool {
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
		f := from.Stash[i].rehome()
		from.RemoveWindow(id)
		to.addStash(f)
		to.Activate(id)
	} else {
		col := layout.Column{Windows: []WindowID{id}}
		if column {
			col, ok = from.takeColumn()
		} else {
			_, ok = from.takeWindow()
		}
		if !ok {
			return false
		}
		to.receive(col, at)
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
	for _, f := range src.Stash {
		dst.Stash = append(dst.Stash, f.rehome())
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
func (w *Workspace) LeaveCoverFor(id WindowID) {
	if w.Pinned() && id != w.Cover() {
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

// NewFrom creates an empty workspace using the template's settings.
func NewFrom(t *Workspace, id uint64) *Workspace {
	w := *t
	w.colBuf, w.rowBuf, w.tileBuf, w.zoomBuf = nil, nil, nil, nil
	w.Columns, w.Floats, w.Maximized, w.floatFocus, w.Home = nil, nil, nil, false, ""
	w.OverviewAfter = nil
	w.DropState()
	w.Stash, w.stashAt, w.stashFocus, w.stashHidden, w.hiddenFullscreen = nil, 0, false, false, 0
	w.ID = id
	return &w
}

// Inherit adopts the output-wide settings without changing workspace identity.
func (w *Workspace) Inherit(t *Workspace) {
	w.Gaps = t.Gaps
	w.border = t.border
	w.stashWidth, w.stashGap = t.stashWidth, t.stashGap
	w.SetOutput(t.Output.W, t.Output.H)
	w.SetUsable(t.Usable)
}

// SetBorder sets the floating border and reconciles covering floats.
func (w *Workspace) SetBorder(b int) { w.border = max(b, 0); w.reconcileFloats() }

// SetStash sets the stash width and gap percentages with their existing limits.
func (w *Workspace) SetStash(width, gap int) {
	if width != 0 {
		width = min(max(width, 10), 90)
	}
	w.stashWidth, w.stashGap = width, min(max(gap, 0), 10)
}

// StashSwipeable reports a shown stash focused beneath no native float.
func (w *Workspace) StashSwipeable() bool { return w.stashFocused() && !w.floatFocus && w.StashShown() }

// StashVisibility returns the hidden state and covering-window override.
func (w *Workspace) StashVisibility() (bool, WindowID) { return w.stashHidden, w.stashOver }

// StashAt returns the settled stash selection index.
func (w *Workspace) StashAt() int { return w.stashAt }

// ShowStashAt commits an overview stash selection, dropping another hidden cover.
func (w *Workspace) ShowStashAt(i int) {
	if w.hiddenFullscreen != w.Stash[i].ID {
		w.hiddenFullscreen = 0
	}
	w.stashAt = i
	w.showStash()
}

// DragFloat moves or resizes a native float within the usable area.
func (w *Workspace) DragFloat(id WindowID, from Rect, edges ports.ResizeEdges, dx, dy int, resize bool) bool {
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

// EdgeScroll scrolls the columns when an output-local pointer reaches an edge.
func (w *Workspace) EdgeScroll(lx int) bool {
	if !w.scrollsRow() || len(w.Columns) == 0 || w.Pinned() {
		return false
	}
	dir := 0
	switch {
	case lx < w.Usable.X+edgeScroll:
		dir = -1
	case lx >= w.Usable.X+w.Usable.W-edgeScroll:
		dir = 1
	default:
		return false
	}
	return w.scrollBy(dir)
}

// Drop moves a tile to an output-local target, possibly in another workspace.
func Drop(src, w *Workspace, id WindowID, t DropSpot) {
	ci := src.ColumnOf(id)
	if ci < 0 {
		return
	}
	lone := len(src.Columns[ci].Windows) == 1
	if t.Kind == DropSwap && w == src && lone {
		to := w.ColumnOf(t.Anchor)
		w.Columns[ci], w.Columns[to] = w.Columns[to], w.Columns[ci]
		w.FocusID(id)
	} else {
		cells := 0
		if lone {
			cells = src.Columns[ci].Cells
		}
		srcIndex := ci
		src.dropSlotOf(id)
		src.RemoveWindow(id)
		col := layout.Column{Windows: []WindowID{id}, Cells: cells}
		switch t.Kind {
		case DropAbove, DropBelow:
			ai := w.ColumnOf(t.Anchor)
			if ai < 0 {
				w.receive(col, -1)
				break
			}
			tc := &w.Columns[ai]
			row := slices.Index(tc.Windows, t.Anchor)
			if t.Kind == DropBelow {
				row++
			}
			if row == 0 {
				tc.Slot = 0
			}
			tc.Windows = slices.Insert(tc.Windows, row, id)
			tc.Shares = nil
			w.Activate(id)
		default:
			at, anchorCol := len(w.Columns), -1
			if t.Anchor != 0 {
				if ai := w.ColumnOf(t.Anchor); ai >= 0 {
					at, anchorCol = ai, ai
					if t.Kind == DropSwap && w == src && ai >= srcIndex {
						at = ai + 1
					}
				}
			}
			if w.full() {
				i := min(at, len(w.Columns)-1)
				if anchorCol >= 0 {
					i = anchorCol
				}
				w.Columns[i].Windows = append(w.Columns[i].Windows, id)
				w.Columns[i].Shares = nil
				w.Activate(id)
			} else {
				w.receive(col, at)
			}
		}
	}
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
	// Shown, Current and Dim describe row visibility and focus presentation.
	Shown, Current, Dim bool
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
	if len(w.Stash) > 0 && (n == 0 || w.stashFocused() && !w.floatFocus) {
		return w.stashAt
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
		s.fullWidth = c.Windows[c.Focus]
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
		if r.Items == 0 {
			tiles = w.previewRow(r.Axis, slot, r.Dim, r.Card < 0)
		} else if r.Items == 1 {
			tiles = w.previewItem(r.Head, r.HiddenColumn, r.Axis, slot, r.Dim, r.Card < 0)
		} else {
			tiles = w.stackRow(r.Front, r.Current, r.HiddenColumn, r.Axis, slot, r.Dim, r.Card < 0)
		}
		dst = append(dst, tiles...)
		if r.Card >= 0 {
			unfolded = true
		} else {
			dst = w.pile(dst, r.Axis, slot, r.Dim, -1)
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
