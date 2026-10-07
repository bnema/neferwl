package core

import (
	"context"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// overviewState owns the provisional selection and the snapshot of each row
// it showed. row is the workspace whose stack has a provisional front; nil
// uses its real front.
type overviewState struct {
	open             bool
	from             *Workspace
	back             *Workspace
	rows             map[*Workspace]rowSnapshot
	row              *Workspace
	front            stackItem
	frontAt          int
	cardOf           *Workspace
	card             WindowID
	selected         WindowID
	selectedAt       int
	scrollX, scrollY float64
	// scrolled is set once a three-finger gesture stepped up or down: it
	// waits for the fingers to lift before the next step. sideways is set
	// once it stepped to a column: it stays on columns and steps once per
	// overviewScrollRepeat.
	scrolled, sideways bool
}

// dropScroll forgets the scroll an overview axis accumulated, which belongs
// to the old axis once it turns. The finger latch (scrolled) stays: it waits
// for the fingers to lift.
func (o *overviewState) dropScroll() { o.scrollX, o.scrollY, o.sideways = 0, 0, false }

// rowSnapshot is a row as the overview first showed it: browsing moves its
// focus and scroll, closing puts them back.
type rowSnapshot struct {
	focus     WindowID
	floats    []Float
	fullWidth WindowID
	// FullWidth history is restored when active binds changed it.
	maximized []WindowID
}

// The overview scales workspace rows without resizing clients. Covering
// floats and the columns form a linear stack of cards; small floats stay
// hidden. Up/down traverse the stack before changing workspace. A pinned
// fullscreen window is the front card of its row, the windows it hides
// behind it; choosing one of them leaves fullscreen.

// A workspace's stash shows as a pile of cards pinned to the left edge of
// its row: the selected stashed window in front at overviewCardZoom, up to
// overviewCards-1 others behind it, each overviewCardStep further up and
// right, dimmed. The row takes the width the pile leaves.
const (
	overviewCardZoom = 0.2
	overviewCards    = 4
	overviewCardStep = 8
	overviewPeekStep = 0.07
)

// overviewMaxZoom caps the current row so its neighbors show above and
// below; overviewMinZoom floors the shrink of wide workspaces, which then
// scroll to keep the selected column in view.
const overviewMaxZoom, overviewMinZoom = 0.6, 0.25

// ToggleOverview opens the overview, or closes it on the selection. Over
// a covering fullscreen window (a game) too: its row shows it, composed
// rather than scanned out while the overview is open.
func (m *Monitor) ToggleOverview() {
	if m.ov.open {
		m.closeOverview()
		return
	}
	w := m.Current()
	// A slide in progress lands at once: the overview lays out the
	// settled state.
	m.stopSwitch()
	m.each(func(w *Workspace) { w.view.stop(); w.stashView.stop() })
	m.ov.open, m.ov.from, m.ov.back = true, w, m.back
	m.ov.scrollX, m.ov.scrollY, m.ov.scrolled, m.ov.sideways = 0, 0, false, false
	m.overviewOpens++
	m.selectRow()
}

// remember snapshots w the first time the overview shows it.
func (m *Monitor) remember(w *Workspace) {
	if _, ok := m.ov.rows[w]; ok {
		return
	}
	if m.ov.rows == nil {
		m.ov.rows = make(map[*Workspace]rowSnapshot)
	}
	s := rowSnapshot{
		floats:    append([]Float(nil), w.Floats...),
		maximized: slices.Clone(w.maximized),
	}
	s.focus, _ = w.Focused()
	if len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth {
		c := w.Columns[w.Focus]
		s.fullWidth = c.Windows[c.Focus]
	}
	m.ov.rows[w] = s
}

// selectRow resets provisional stack and stash selection for a new row.
func (m *Monitor) selectRow() {
	m.remember(m.Current())
	m.ov.card, m.ov.cardOf = 0, nil
	m.ov.front, m.ov.row, m.ov.frontAt = stackItem{}, nil, 0
	m.ov.selected, m.ov.selectedAt = 0, -1
	w := m.Current()
	// A fullscreen window has the focus: its card is in front.
	w.focusCover()
	if w.floatFocus && len(w.Columns) > 0 && (len(w.stack()) < 2 || m.stackFront(w).kind == stackColumns) {
		w.floatFocus = false
	}
	if len(w.Stash) > 0 && (len(w.stack()) == 0 || w.stashFocused() && !w.floatFocus) {
		m.selectCard(w, w.stashAt)
	}
}

// selectCard selects stash entry i of w.
func (m *Monitor) selectCard(w *Workspace, i int) {
	m.ov.card, m.ov.cardOf = w.Stash[i].ID, w
}

// card is the selected stash card, 0 when there is none: the overview is
// closed, the selection is on a column, or its window left the stash on
// screen (unmapped, unstashed, or another workspace shown by a bind).
func (m *Monitor) card() WindowID {
	w := m.ov.cardOf
	if !m.ov.open || w == nil || w != m.Current() || w.stashIndex(m.ov.card) < 0 {
		return 0
	}
	return m.ov.card
}

// cardAt is the pile entry of w that is selected, -1 when none.
func (m *Monitor) cardAt(w *Workspace) int {
	if id := m.card(); id != 0 && w == m.ov.cardOf {
		return w.stashIndex(id)
	}
	return -1
}

// overviewTarget resolves the active preview to one window for validation
// and window actions. Presentation state never falls back to a covering float.
func (m *Monitor) overviewTarget() WindowID {
	if id := m.card(); id != 0 {
		return id
	}
	w := m.Current()
	item := m.stackFront(w)
	switch item.kind {
	case stackFloat:
		return item.id
	case stackColumn:
		i := w.columnOf(item.id)
		if i >= 0 {
			c := w.Columns[i]
			return c.Windows[c.Focus]
		}
	case stackColumns:
		i := w.Focus
		if len(w.Columns) == 0 {
			return 0
		}
		if w.overviewMaximized() {
			i = w.columnOf(m.ov.selected)
			if i < 0 || i == w.Focus {
				i = m.hiddenColumn(w)
			}
		}
		if i >= 0 {
			c := w.Columns[i]
			return c.Windows[c.Focus]
		}
	}
	return 0
}

// overviewBlocks makes window mutations explicit: they are disabled while
// selection is provisional. Workspace and monitor navigation and moves to
// another workspace remain active.
func overviewBlocks(a Action) bool {
	if _, _, ok := ResizeArg(a); ok {
		return true
	}
	switch a {
	case ActionMoveColumnLeft, ActionMoveColumnRight, ActionCycleColumnWidth,
		ActionMaximizeColumn, ActionToggleFullscreen, ActionToggleWindowStash,
		ActionToggleStashVisible, ActionConsumeOrExpelLeft,
		ActionConsumeOrExpelRight, ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight,
		ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown,
		ActionMoveWindowUp, ActionMoveWindowDown, ActionMoveWorkspacePrev,
		ActionMoveWorkspaceNext, ActionToggleFloating:
		return true
	}
	return false
}

// overviewMoveTo moves the selected preview at once, like close-window: the
// overview stays open and closing it does not undo the move. Nothing moves
// without a selected preview, from a stash card, or when the bind has
// nowhere to go.
func (m *Monitor) overviewMoveTo(a Action) {
	cur, id := m.Current(), m.overviewTarget()
	i, column, ok := m.moveDest(a)
	if m.card() != 0 || id == 0 || !ok || m.Workspaces[i] == cur {
		return
	}
	if cur.pinned() && id != cur.cover() {
		// Focused reports the covering window while it covers.
		cur.leaveFullscreen()
	}
	cur.FocusID(id)
	m.MoveToWorkspace(i, column)
	// The row that received the window keeps it focused on close.
	if to, _ := m.find(id); to != nil && to != cur {
		delete(m.ov.rows, to)
	}
	m.selectRow()
}

// closeOverview closes the overview on the selection: a card shows the
// stash on it. A fullscreen request another stashed window made while
// the stash was hidden is dropped: the picked card stays in front. A
// window a pinned fullscreen window hides leaves that fullscreen.
func (m *Monitor) closeOverview() {
	w := m.Current()
	front, target := m.stackFront(w), m.overviewTarget()
	if w.pinned() && target != w.cover() {
		w.leaveFullscreen()
	}
	if i := m.cardAt(w); i >= 0 {
		if w.hiddenFullscreen != w.Stash[i].ID {
			w.hiddenFullscreen = 0
		}
		w.stashAt = i
		w.showStash()
	} else if front != (stackItem{}) {
		w.apply(front, target)
	}
	for r, s := range m.ov.rows {
		if r != w && m.has(r) {
			restoreRow(r, s)
		}
	}
	m.ov = overviewState{}
}

// CancelOverview closes the overview and returns to the workspace and
// window it opened on, if they are still there. Every row it showed gets
// its focus back.
func (m *Monitor) CancelOverview() {
	if from := m.ov.from; from != nil && m.has(from) {
		m.show(from)
	}
	for r, s := range m.ov.rows {
		if m.has(r) {
			restoreRow(r, s)
		}
	}
	m.back = m.ov.back
	if m.back != nil && !m.has(m.back) {
		m.back = nil
	}
	m.ov = overviewState{}
}

// restoreRow puts w back as snapshot s found it.
func restoreRow(w *Workspace, s rowSnapshot) {
	if s.focus != 0 {
		w.FocusID(s.focus)
	} else {
		w.floatFocus, w.stashFocus = false, false
	}
	// FocusID on a float raises it; restore the initial order and below
	// flags for floats still present, leaving new floats in their order.
	current := w.Floats
	w.Floats = nil
	for _, f := range s.floats {
		for i, now := range current {
			if now.ID == f.ID {
				now.below = f.below
				w.Floats = append(w.Floats, now)
				current = append(current[:i], current[i+1:]...)
				break
			}
		}
	}
	w.Floats = append(w.Floats, current...)
	// Fixed overflow maximizes at most one column: only the one holding
	// the anchor window, wherever active binds moved it. Scroll overflow
	// keeps each column's own width.
	if w.policy().equalCells {
		w.unmaximize()
		for i := range w.Columns {
			if s.fullWidth != 0 && slices.Contains(w.Columns[i].Windows, s.fullWidth) {
				w.maximize(i)
				break
			}
		}
	}
	// Windows that left the row since leave its maximize history too.
	w.maximized = slices.DeleteFunc(slices.Clone(s.maximized), func(id WindowID) bool { return !w.has(id) })
	w.scroll()
}

// overviewWorkspaces inserts named rows below their invocation workspace,
// in configured order within each group. Unattached rows follow the last
// occupied numbered row (or the first). Empty invocation anchors stay visible;
// other empty rows are skipped unless current.
func (m *Monitor) overviewWorkspaces() []*Workspace {
	fallback := m.Workspaces[0]
	for _, w := range m.Workspaces {
		if !w.empty() {
			fallback = w
		}
	}
	groups := make(map[*Workspace][]*Workspace)
	named := slices.Clone(m.hidden)
	rank := func(w *Workspace) int {
		for i, spec := range m.named {
			if spec.Name == w.Name {
				return i
			}
		}
		return len(m.named)
	}
	slices.SortStableFunc(named, func(a, b *Workspace) int { return rank(a) - rank(b) })
	for _, w := range named {
		if w.empty() && w != m.Current() {
			continue
		}
		anchor := w.overviewAfter
		if indexOf(m.Workspaces, anchor) < 0 {
			anchor = fallback
		}
		groups[anchor] = append(groups[anchor], w)
	}
	var rows []*Workspace
	for _, w := range m.Workspaces {
		if !w.empty() || w == m.Current() || len(groups[w]) > 0 {
			rows = append(rows, w)
			rows = append(rows, groups[w]...)
		}
	}
	return rows
}

// showOverview changes rows without committing the provisional stack. Entering
// a named group uses its invocation anchor as the toggle return target, not
// rows merely browsed in the overview.
func (m *Monitor) showOverview(w *Workspace) {
	if w.policy().workspace != m.overviewAxis() {
		m.ov.dropScroll()
	}
	if w != m.Current() && m.isHidden(w) && !m.isHidden(m.Current()) {
		m.back = m.Current()
		if indexOf(m.Workspaces, w.overviewAfter) >= 0 {
			m.back = w.overviewAfter
		}
	}
	m.show(w)
	m.selectRow()
}

// OverviewMove shares navigation rules across keys, scrolls and swipes. It
// is the physical entry point: it maps dx and dy to the overview's moves.
func (m *Monitor) OverviewMove(dx, dy int) {
	if m.overviewAxis() == horizontalAxis {
		if dy != 0 {
			m.overviewLane(dy)
		} else if dx != 0 {
			m.overviewAcross(dx)
		}
		return
	}
	m.overviewMoveVertical(dx, dy)
}

// overviewWorkspaceStep is a focus-workspace bind. A vertical overview
// walks the stack cards before changing workspace (overviewVertical). A
// side-by-side one always changes workspace, since its stack fans left and
// right. A selected stash card cycles the pile first, in every layout.
func (m *Monitor) overviewWorkspaceStep(d int) {
	if m.overviewAxis() == verticalAxis {
		m.overviewVertical(d)
		return
	}
	if !m.cycleCard(m.Current(), d) {
		m.overviewStep(d)
	}
}

// cycleCard brings the stash card d entries from the selected one of w to
// the front, stopping at either end. It reports whether a card was selected.
func (m *Monitor) cycleCard(w *Workspace, d int) bool {
	at := m.cardAt(w)
	if at < 0 {
		return false
	}
	m.selectCard(w, min(max(at+d, 0), len(w.Stash)-1))
	return true
}

// overviewLane is the vertical move of an overview whose workspaces sit side
// by side (cascade): a selected stash card cycles the pile; else the
// selection goes to the previous or next lane (band), at the closest column,
// stopping at the first and last. Covering floats stack along the horizontal
// axis, not this one.
func (m *Monitor) overviewLane(dy int) {
	w := m.Current()
	if m.cycleCard(w, dy) || m.stackFront(w).kind != stackColumns || len(w.Columns) == 0 {
		return
	}
	if i := w.laneNeighbor(dy); i >= 0 {
		m.selectLaneColumn(w, i)
	}
}

// selectLaneColumn selects column i of w.
func (m *Monitor) selectLaneColumn(w *Workspace, i int) {
	w.selectOverviewColumn(i)
	m.ov.selected = w.Columns[i].Windows[w.Columns[i].Focus]
	m.ov.selectedAt = i
}

// overviewAcross is the horizontal move of an overview whose workspaces sit
// side by side (cascade): a column inside the selected lane. Past the
// lane's first column it goes to the stash, else to the previous workspace;
// past the last column, to the next workspace. Covering floats fan along
// this axis and are traversed before the stash or a workspace: the card on
// the left is the one behind.
func (m *Monitor) overviewAcross(dx int) {
	w := m.Current()
	if m.cardAt(w) >= 0 {
		if dx < 0 || len(w.stack()) == 0 {
			// Left leaves the pile; a stash-only workspace has nothing
			// to its right to select.
			m.overviewStep(dx)
			return
		}
		m.ov.card, m.ov.cardOf = 0, nil
		m.ov.selected = 0
		if m.stackFront(w).kind == stackColumns && len(w.Columns) > 0 {
			first, _ := w.laneBounds(w.laneOf(w.Focus))
			m.selectLaneColumn(w, first)
		}
		return
	}
	if m.stackFront(w).kind != stackColumns || len(w.Columns) == 0 {
		m.laneEdge(w, dx)
		return
	}
	first, last := w.laneBounds(w.laneOf(w.Focus))
	if next := w.Focus + dx; next >= first && next <= last {
		m.selectLaneColumn(w, next)
		return
	}
	m.laneEdge(w, dx)
}

// laneEdge is a horizontal move off the edge of the selected lane, or off a
// card that is not the columns: the stack first, then the stash on the left
// or a workspace.
func (m *Monitor) laneEdge(w *Workspace, dx int) {
	if m.moveStack(-dx) {
		return
	}
	if dx < 0 && len(w.Stash) > 0 {
		m.selectCard(w, w.stashAt)
		return
	}
	m.overviewStep(dx)
}

// overviewMoveVertical is the move of an overview whose workspaces stack
// vertically. Vertical moves stay in a selected stash pile; left stays, and
// right deselects it, even when the row has no main card.
func (m *Monitor) overviewMoveVertical(dx, dy int) {
	if dy != 0 {
		m.overviewVertical(dy)
	} else if dx != 0 {
		m.overviewHorizontal(dx)
	}
}

func (m *Monitor) overviewVertical(dy int) {
	if m.cycleCard(m.Current(), dy) {
		return
	}
	if m.moveStack(-dy) {
		return
	}
	m.overviewStep(dy)
}

// overviewStep shows the workspace d rows from the current one; it stays
// on the first or last row.
func (m *Monitor) overviewStep(d int) {
	rows := m.overviewWorkspaces()
	i := indexOf(rows, m.Current()) + d
	if i >= 0 && i < len(rows) {
		m.showOverview(rows[i])
	}
}

// overviewHorizontal is the horizontal move of an overview whose workspaces
// stack vertically: it never changes workspace.
func (m *Monitor) overviewHorizontal(dx int) {
	w := m.Current()
	if m.cardAt(w) >= 0 {
		if dx > 0 {
			m.ov.card, m.ov.cardOf = 0, nil
			m.ov.selected = 0
			item := m.stackFront(w)
			if item.kind == stackColumns {
				if w.overviewMaximized() {
					i := m.hiddenColumn(w)
					m.ov.selected, m.ov.selectedAt = w.Columns[i].Windows[w.Columns[i].Focus], i
				} else {
					w.selectOverviewColumn(0)
					m.ov.selected = w.Columns[0].Windows[w.Columns[0].Focus]
				}
			}
		}
		return
	}
	front := m.stackFront(w)
	if front.kind != stackColumns {
		if dx < 0 && m.moveStack(1) {
			return
		}
		if dx < 0 && len(w.Stash) > 0 {
			m.selectCard(w, w.stashAt)
		}
		return
	}
	if len(w.Columns) == 0 {
		return
	}
	i := w.columnOf(m.ov.selected)
	if i < 0 || w.overviewMaximized() && i == w.Focus {
		if w.overviewMaximized() {
			i = m.hiddenColumn(w)
		} else {
			i = w.Focus
		}
	}
	first := 0
	if w.overviewMaximized() && w.Focus == 0 {
		first = 1
	}
	if dx < 0 && i == first && len(w.Stash) > 0 {
		m.selectCard(w, w.stashAt)
		return
	}
	next := min(max(i+dx, 0), len(w.Columns)-1)
	if w.overviewMaximized() && next == w.Focus {
		next = min(max(next+dx, 0), len(w.Columns)-1)
		if next == w.Focus {
			return
		}
	}
	m.ov.selected = w.Columns[next].Windows[w.Columns[next].Focus]
	m.ov.selectedAt = next
	if !w.overviewMaximized() {
		// Ordinary group navigation retains its existing scroll-to-selection.
		w.selectOverviewColumn(next)
	}
}

// OverviewPick closes the overview on the clicked window.
func (m *Monitor) OverviewPick(id WindowID) {
	w, _ := m.find(id)
	if w == nil {
		return
	}
	if w.floatIndex(id) >= 0 && w.stashIndex(id) < 0 {
		i := w.floatIndex(id)
		if !w.coversFloat(w.Floats[i]) && !(id == w.cover() && w.pinned()) {
			return
		}
	}
	m.showOverview(w)
	m.ov.card, m.ov.cardOf = 0, nil
	if i := w.stashIndex(id); i >= 0 {
		m.selectCard(w, i)
	} else if item, ok := w.itemOf(id); ok {
		m.setFront(w, item)
		if i := w.columnOf(id); i >= 0 {
			if item.kind == stackColumns && !w.overviewMaximized() {
				w.selectOverviewColumn(i)
			}
			m.ov.selected = id
			for j, v := range w.Columns[i].Windows {
				if v == id {
					w.Columns[i].Focus = j
					break
				}
			}
		}
	}
	m.closeOverview()
}

// overviewAxis is the axis the overview lays workspaces out along, and the
// stacked cards of a row fan along: the navigation axis of the current
// workspace's layout.
func (m *Monitor) overviewAxis() layoutAxis { return m.Current().policy().workspace }

// overviewRows places the current workspace and its overview neighbors in
// slots, in output-local pixels, along the overview axis; each workspace
// places its previews in its slot. The numbered/named boundary gets extra
// spacing and an inactive rule across the axis.
//
// Vertical axis: full-width slots, the current one centred; the rule is
// horizontal. Horizontal axis: full-height slots as wide as their content,
// stash pile and stack peeks included, the current one centred; the rule is
// vertical, in the spacing between two slots.
func (m *Monitor) overviewRows() (map[*Workspace]Rect, []ports.Separator) {
	cur := m.Current()
	u := cur.overviewArea()
	gap := max(1, u.H*3/100)
	axis := m.overviewAxis()
	var cs Rect
	if axis == horizontalAxis {
		// The row stays centred on the area; the pile extends the slot to
		// the left, pushing the previous workspace, unless it would leave
		// the area.
		pile, row := cur.pileWidth(), cur.rowWidth()
		cs = Rect{X: max(u.X+(u.W-row)/2-pile, u.X), Y: u.Y, W: pile + row, H: u.H}
	} else {
		hc := cur.rowHeight()
		cs = Rect{X: u.X, Y: u.Y + (u.H-hc)/2, W: u.W, H: hc}
	}
	rows := map[*Workspace]Rect{cur: cs}
	var dividers []ports.Separator
	list := m.overviewWorkspaces()
	i := indexOf(list, cur)
	for _, next := range []int{i - 1, i + 1} {
		if next < 0 || next >= len(list) {
			continue
		}
		w := list[next]
		spacing := gap
		boundary := m.isHidden(w) != m.isHidden(cur)
		if boundary {
			spacing *= 2
		}
		slot, gapAt := neighborSlot(axis, u, cs, w, next < i, spacing)
		rows[w] = slot
		if boundary {
			dividers = append(dividers, overviewDivider(axis, u, gapAt+spacing/2))
		}
	}
	return rows, dividers
}

// neighborSlot is the slot of w, the neighbor before or after the current
// slot cs along axis, spacing away; gapAt is where the spacing begins.
func neighborSlot(axis layoutAxis, u, cs Rect, w *Workspace, before bool, spacing int) (slot Rect, gapAt int) {
	if axis == horizontalAxis {
		slot = Rect{Y: u.Y, W: w.slotWidth(), H: u.H}
		if before {
			gapAt = cs.X - spacing
			slot.X = gapAt - slot.W
		} else {
			gapAt = cs.X + cs.W
			slot.X = gapAt + spacing
		}
		return slot, gapAt
	}
	h := w.rowHeight()
	slot = Rect{X: u.X, W: u.W, H: h}
	if before {
		gapAt = cs.Y - spacing
		slot.Y = gapAt - h
	} else {
		gapAt = cs.Y + cs.H
		slot.Y = gapAt + spacing
	}
	return slot, gapAt
}

// overviewDivider is the inactive rule across axis at pos, inset from the
// edges of u.
func overviewDivider(axis layoutAxis, u Rect, pos int) ports.Separator {
	if axis == horizontalAxis {
		return ports.Separator{Rect: Rect{X: pos, Y: u.Y + u.H*4/100, W: 1, H: u.H * 92 / 100}}
	}
	margin := u.W * 4 / 100
	return ports.Separator{Rect: Rect{X: u.X + margin, Y: pos, W: u.W - 2*margin, H: 1}}
}

// overviewLayout places the current workspace and its overview neighbors,
// each with its stash pile and covering-float stack. Other rows
// stay hidden; numbered and named workspaces share the same preview geometry.
func (m *Monitor) overviewLayout(result []Placement) []Placement {
	cur := m.Current()
	rows, _ := m.overviewRows()
	axis := m.overviewAxis()
	for w := range m.all() {
		slot, shown := rows[w]
		if shown {
			at := m.cardAt(w)
			result = append(result, m.stackRow(w, axis, slot, w != cur, at < 0)...)
			if len(w.Stash) > 0 {
				result = append(result, w.pile(w.pileBand(axis, slot), w != cur, at)...)
			}
		}
		for _, f := range w.Floats {
			if _, drawn := w.itemOf(f.ID); !shown || !drawn {
				result = append(result, Placement{ID: f.ID, Hidden: true})
			}
		}
		if !shown {
			// Columns and stash only: the floats were listed above.
			result = w.appendHidden(result, false)
		}
	}
	return result
}

// fan places front and neighboring cards, farthest visible first and front
// last. A card may contain several tiles; omitted cards stay in the layout
// hidden so their clients are not left showing on screen.
func fan(cards [][]Placement, front, maxBehind, maxBefore int, circular bool,
	behind, before func(int) (int, int), dim, lit bool) []Placement {
	n := len(cards)
	if n == 0 {
		return nil
	}
	front = min(max(front, 0), n-1)
	out := make([]Placement, 0, n)
	add := func(i, distance int, offset func(int) (int, int), peek bool) {
		dx, dy := offset(distance)
		for _, p := range cards[i] {
			if !p.Hidden {
				p.Rect.X += dx
				p.Rect.Y += dy
				if p.Peek || peek || dim {
					p.peeking(1)
				}
				p.Focused = p.Focused && !peek && !dim && lit
			}
			out = append(out, p)
		}
	}
	shown := make([]bool, n)
	// Behind and before are in nearest-first order; paint farthest first.
	for d := min(maxBehind, n-1); d >= 1; d-- {
		i := front + d
		if circular {
			i = (front + d) % n
		}
		if i >= n {
			continue
		}
		add(i, d, behind, true)
		shown[i] = true
	}
	for d := min(maxBefore, n-1); d >= 1; d-- {
		i := front - d
		if circular {
			i = (front - d + n) % n
		}
		if i < 0 {
			continue
		}
		if shown[i] {
			continue
		}
		add(i, d, before, true)
		shown[i] = true
	}
	add(front, 0, behind, false)
	shown[front] = true
	for i, card := range cards {
		if shown[i] {
			continue
		}
		for _, p := range card {
			p.Hidden = true
			p.Preview = 0
			p.Focused = false
			out = append(out, p)
		}
	}
	return out
}

// pileWidth is the room the stash pile of w takes on the left of its row,
// its margin included; 0 without a stash.
func (w *Workspace) pileWidth() int {
	if len(w.Stash) == 0 {
		return 0
	}
	card := int(math.Round(float64(w.stashRect().W) * overviewCardZoom))
	// Never more than a third of the width: the columns keep the rest.
	a := w.overviewArea()
	return min(card+(min(len(w.Stash), overviewCards)-1)*w.cardStep()+a.W*2/100, a.W/3)
}

// cardStep is the offset between stacked cards, smaller on tiny outputs.
func (w *Workspace) cardStep() int {
	a := w.overviewArea()
	return min(overviewCardStep, a.W/100, a.H/50)
}

// pile places the stash of w as cards on the left of band (see pileBand):
// entry front (the stash selection when -1) in
// front, the next ones behind it, dimmed, the rest hidden. Only a front card
// of the current row (dim false) with front set is selected.
func (w *Workspace) pile(band Rect, dim bool, front int) []Placement {
	n := len(w.Stash)
	if n == 0 {
		return nil
	}
	selected := front >= 0 && !dim
	if front < 0 {
		front = w.stashAt
	}
	front = min(max(front, 0), n-1)
	r := w.stashRect()
	u := w.overviewArea()
	cw := int(math.Round(float64(r.W) * overviewCardZoom))
	ch := int(math.Round(float64(r.H) * overviewCardZoom))
	rowH := int(math.Round(float64(w.Usable.H) * w.overviewZoom()))
	// Stack rows reserve two peek steps above the front card. Align the
	// stash with that front band, not with the top of its envelope.
	x0, y0 := band.X+u.W/100, band.Y+(rowH-ch)/2
	cards := make([][]Placement, n)
	for i := range w.Stash {
		cards[i] = []Placement{{ID: w.Stash[i].ID, Rect: Rect{X: x0, Y: y0, W: cw, H: ch}, Preview: overviewCardZoom, Focused: selected}}
	}
	step := w.cardStep()
	return fan(cards, front, min(n, overviewCards)-1, 0, true,
		func(d int) (int, int) { return d * step, -d * step },
		func(int) (int, int) { return 0, 0 }, dim, selected)
}

// overviewZoom is the scale that fits every column of w in the usable
// width, within the overview bounds.
func (w *Workspace) overviewZoom() float64 {
	z, _ := w.zoomSpan()
	return z
}

// zoomSpan is overviewZoom with the unscaled span of the tiles it fits. A
// cascade workspace spans one screen whatever its band count.
func (w *Workspace) zoomSpan() (float64, int) {
	// Only the span is read: the tiles go in the zoom scratch, dead on return.
	var span int
	w.zoomBuf, span, _ = w.previewTilesInto(w.zoomBuf[:0])
	if span <= 0 {
		return overviewMaxZoom, span
	}
	room := float64(w.overviewArea().W-w.pileWidth()) * 0.96
	z := min(max(room/float64(span), overviewMinZoom), overviewMaxZoom)
	if w.stacked() {
		// Reserve two clear peeks on either side of the front row.
		z = min(z, overviewMaxZoom/(1+4*overviewPeekStep))
	}
	return z, span
}

// slotWidth is the width of the slot of w in a horizontal overview: its
// stash pile and its row.
func (w *Workspace) slotWidth() int {
	return w.pileWidth() + w.rowWidth()
}

// rowWidth is the width of the row of w in a horizontal overview: what its
// tiles take (at most what the pile leaves of the area), plus the room for
// the covering floats peeking on either side of the front card.
func (w *Workspace) rowWidth() int {
	z, span := w.zoomSpan()
	if span <= 0 {
		// An empty workspace keeps the room of a screen.
		span = w.Usable.W
	}
	width := min(int(math.Round(float64(span)*z)), w.overviewArea().W-w.pileWidth())
	if w.stacked() {
		width += 4 * w.peekStep()
	}
	return width
}

// peekStep is the shared step for a stack row and its front-card inset.
func (w *Workspace) peekStep() int {
	return max(1, int(math.Round(float64(w.Usable.H)*w.overviewZoom()*overviewPeekStep)))
}

// frontSlot is the part of slot the front card of w takes: slot shifted
// along axis past the room reserved for two cards peeking before it, and
// (along a horizontal axis, where the slot is as wide as its cards) without
// the room of the two after it. A single-card row keeps slot.
func (w *Workspace) frontSlot(axis layoutAxis, slot Rect) Rect {
	if w.stacked() {
		p := w.peekStep()
		axis.offset(&slot, 2*p)
		if axis == horizontalAxis {
			slot.W -= 4 * p
		}
	}
	return slot
}

// pileBand is the band the stash pile of w aligns with in its slot. A
// vertical overview aligns it with the front card (see frontSlot). A
// horizontal one puts it level with the selected lane, left of the whole
// stack of cards.
func (w *Workspace) pileBand(axis layoutAxis, slot Rect) Rect {
	if axis == verticalAxis {
		return w.frontSlot(axis, slot)
	}
	slot.Y += (slot.H - int(math.Round(float64(w.Usable.H)*w.overviewZoom()))) / 2
	return slot
}

// rowHeight includes the largest possible stack envelope (two peeks above
// and two below). Neighbor rows use the same measure as the current row.
func (w *Workspace) rowHeight() int {
	h := int(math.Round(float64(w.Usable.H) * w.overviewZoom()))
	if w.stacked() {
		h += 4 * w.peekStep()
	}
	return h
}

// previewTiles lays tiles out unscrolled, relative to the usable area's
// corner. Fixed overflow uses its on-screen geometry; scroll overflow uses
// an unscrolled row. span is the row's width; sel is the focused column.
func (w *Workspace) previewTiles() (tiles []Placement, span int, sel Rect) {
	return w.previewTilesInto(make([]Placement, 0, w.columnWindows()))
}

// previewTilesInto is previewTiles appending to tiles (dst[:0] to reuse its
// storage: the result then aliases it). The column rects use w.colBuf, dead
// when it returns.
func (w *Workspace) previewTilesInto(tiles []Placement) ([]Placement, int, Rect) {
	var span int
	var sel Rect
	if w.pinned() {
		c := w.cover()
		// The row shows the columns a pinned fullscreen window hides,
		// laid out as without it; its own tile stays marked fullscreen.
		v := *w
		v.fullscreen = 0
		tiles, span, sel = v.previewTilesInto(tiles)
		w.colBuf, w.rowBuf = v.colBuf, v.rowBuf
		for i := range tiles {
			tiles[i].Fullscreen = tiles[i].ID == c
		}
		return tiles, span, sel
	}
	g := w.gap()
	h := max(w.Usable.H-2*g, 0)
	var rects []Rect
	if w.Overflow == OverflowFixed {
		w.colBuf = w.columnRectsInto(w.colBuf, false)
		rects = w.colBuf
		span = w.Usable.W // Fixed overflow never scrolls, even with a stash pile.
	}
	maximized := w.policy().equalCells && len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth
	for i, c := range w.Columns {
		r := Rect{X: w.columnX(i) - w.Usable.X, Y: g, W: w.columnWidth(i), H: h}
		if w.policy().wraps {
			// The band is the lane: it is encoded in Y, one screen apart.
			// A maximized column fills its band as on screen.
			r.X = w.cellX(i)
			r.Y = g + w.band(i)*w.Usable.H
			r.W = w.cellWidth()
			if c.FullWidth {
				r.X, r.W = g, max(w.Usable.W-2*g, 0)
			}
		} else if w.Overflow == OverflowFixed {
			r = rects[i]
			r.X, r.Y = r.X-w.Usable.X, r.Y-w.Usable.Y
		} else if w.fullscreenColumn(i) {
			// A fullscreen column shows at the width it had in the row.
			r.W = max(w.Usable.W-2*g, 0)
		}
		if maximized && i != w.Focus && w.sameBand(i, w.Focus) {
			// FullWidth hides the other columns of its band on screen.
			// Their old buffers cannot fit their current rects; show only
			// the maximized column until navigation restores the layout.
			for _, id := range c.Windows {
				tiles = append(tiles, Placement{ID: id, Hidden: true})
			}
			continue
		}
		w.rowBuf = rowRectsInto(w.rowBuf[:0], r, c, g)
		for j, t := range w.rowBuf {
			tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: i == w.Focus && j == c.Focus})
		}
		if w.Overflow != OverflowFixed && !w.policy().wraps {
			span = max(span, r.X+r.W+g)
		}
		if i == w.Focus {
			sel = r
		}
	}
	if w.policy().wraps && len(w.Columns) > 0 {
		span = w.Usable.W
	}
	return tiles, span, sel
}

// previewRow scales the tiles of w into a row placed in slot, centred
// in the slot's width, or scrolled so the focused column shows when the
// row is wider, right of the stash pile. Dimmed rows (neighbor
// workspaces) have no focus; neither has a row whose pile has it (lit
// false).
func (w *Workspace) previewRow(axis layoutAxis, slot Rect, dim, lit bool) []Placement {
	tiles, span, sel := w.previewTiles()
	return w.previewRowTiles(axis, slot, dim, lit, w.policy().wraps, tiles, span, sel)
}

// previewRowTiles scales tiles, laid out unscrolled over span with sel the
// focused one, into slot of an overview along axis. Placement stays inside
// slot.
//
// With lanes set (the tiles of a cascade workspace), the tiles carry their
// band in Y, one Usable.H apart. The band of sel is the selected lane,
// centred across the axis; the other bands sit a lane away from it, dimmed,
// and are hidden when they leave the area. A workspace whose lanes run along
// the overview axis shows only its selected lane. In a horizontal overview,
// tiles entirely outside the area are hidden.
func (w *Workspace) previewRowTiles(axis layoutAxis, slot Rect, dim, lit, lanes bool, tiles []Placement, span int, sel Rect) []Placement {
	z := w.overviewZoom()
	scale := func(v int) int { return int(math.Round(float64(v) * z)) }
	width := scale(span)
	u := w.overviewArea()
	top := slot.Y
	if axis == horizontalAxis {
		top = slot.Y + (slot.H-scale(w.Usable.H))/2
	}
	// Rows stay centred on the output like rows without a stash; the pile
	// only pushes a row right when they would overlap.
	left := w.pileWidth()
	x := max(slot.X+(slot.W-width)/2, slot.X+left)
	ux, uw := slot.X+left, slot.W-left
	if width > uw {
		x = ux + uw/2 - scale(sel.X+sel.W/2)
		x = min(max(x, ux+uw-width), ux)
	}
	stride := w.policy().content.span(w.Usable)
	lanes = lanes && stride > 0
	// Lanes along the overview axis would overlap the neighbor workspaces.
	otherLanes := w.policy().content != axis
	laneStep := scale(stride) + max(1, u.H*3/100)
	selLane := 0
	if lanes {
		selLane = max(sel.Y, 0) / stride
	}
	for i := range tiles {
		if tiles[i].Hidden {
			continue
		}
		r := tiles[i].Rect
		lane, ly := 0, r.Y
		if lanes {
			l := r.Y / stride
			lane, ly = l-selLane, r.Y-l*stride
		}
		x0, y0 := scale(r.X), scale(ly)
		tiles[i].Rect = Rect{X: x + x0, Y: top + y0 + lane*laneStep, W: scale(r.X+r.W) - x0, H: scale(ly+r.H) - y0}
		tiles[i].Preview = z
		if dim || lane != 0 {
			tiles[i].peeking(1)
		}
		tiles[i].Focused = tiles[i].Focused && !dim && lit && lane == 0
		out := false
		if axis == horizontalAxis {
			// A row wider than its slot scrolls: what leaves the slot
			// would sit on the neighbor workspaces.
			out = tiles[i].Rect.X < ux || tiles[i].Rect.X+tiles[i].Rect.W > ux+uw
		}
		if lane != 0 && !otherLanes || out || (lanes || axis == horizontalAxis) && !tiles[i].Rect.Overlaps(u) {
			tiles[i].Hidden, tiles[i].Preview, tiles[i].Focused = true, 0, false
		}
	}
	return tiles
}

// overviewOutline frames the selected preview with active lines of width
// b, just outside it.
func overviewOutline(layout []Placement, b int) []ports.Separator {
	for _, p := range layout {
		if p.Preview > 0 && p.Focused && !p.Hidden {
			r := p.Rect
			return []ports.Separator{
				{Rect: Rect{X: r.X - b, Y: r.Y - b, W: r.W + 2*b, H: b}, Active: true},
				{Rect: Rect{X: r.X - b, Y: r.Y + r.H, W: r.W + 2*b, H: b}, Active: true},
				{Rect: Rect{X: r.X - b, Y: r.Y, W: b, H: r.H}, Active: true},
				{Rect: Rect{X: r.X + r.W, Y: r.Y, W: b, H: r.H}, Active: true},
			}
		}
	}
	return nil
}

// overviewClickFade is the fade from which a card takes no click.
const overviewClickFade = 0.5

// overviewIn is the preview under the output-local point in layout, if
// any. Front cards take priority over peeks, including peeks appended after
// a front tile in another row's layout. The click path passes the shown
// layout, so a card is hit where it is drawn while it moves. A card that is
// nearly invisible (still fading in) is not clickable.
func overviewIn(layout []Placement, x, y float64) WindowID {
	for i := len(layout) - 1; i >= 0; i-- {
		p := layout[i]
		r := p.Rect
		if p.Preview > 0 && !p.Hidden && !p.Peek && p.Fade < overviewClickFade && x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H) {
			return p.ID
		}
	}
	for i := len(layout) - 1; i >= 0; i-- {
		p := layout[i]
		r := p.Rect
		if p.Preview > 0 && !p.Hidden && p.Fade < overviewClickFade && x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H) {
			return p.ID
		}
	}
	return 0
}

// overviewClick closes the overview on the preview under the pointer, on
// the output it shows on. picked is false when no preview is there.
func (c *Core) overviewClick(ctx context.Context) (picked bool, err error) {
	i := c.screenAt(c.cursorX, c.cursorY)
	if i < 0 {
		return false, nil
	}
	sc := c.screens[i]
	if !sc.mon.ov.open {
		return false, nil
	}
	// The cards are hit where they are drawn: a click during the open or a
	// navigation animation picks what moves under the pointer.
	id := overviewIn(sc.shownLayout(), c.cursorX-float64(sc.x), c.cursorY-float64(sc.y))
	if id == 0 {
		return false, nil
	}
	now := c.now()
	shots := c.snapshot(now)
	sc.mon.OverviewPick(id)
	c.transition(shots, now)
	c.focusScreen = i
	c.keyboard.takeBack()
	if err := c.workspaceVisible(ctx, true); err != nil {
		return true, err
	}
	return true, c.publish(ctx)
}

// overviewAction runs a bind while the overview is open: focus binds move
// the selection, close-window and move-to-workspace binds act on the
// selected preview, other mutations are dropped. handled is false for binds
// that run as usual.
func (m *Monitor) overviewAction(a Action) (e Effect, handled bool) {
	if !m.ov.open {
		return Effect{}, false
	}
	if a == ActionCloseWindow {
		return Effect{Close: m.overviewTarget()}, true
	}
	if movesToWorkspace(a) {
		m.overviewMoveTo(a)
		return Effect{}, true
	}
	return Effect{}, m.overviewFocus(a) || overviewBlocks(a)
}

// overviewFocus runs a focus bind as an overview move, so the focus binds
// and the bare keys navigate the overview the same way: true when a was
// one of them.
func (m *Monitor) overviewFocus(a Action) bool {
	switch a {
	case ActionFocusColumnLeft:
		m.OverviewMove(-1, 0)
	case ActionFocusColumnRight:
		m.OverviewMove(1, 0)
	case ActionFocusWindowUp:
		m.OverviewMove(0, -1)
	case ActionFocusWindowDown:
		m.OverviewMove(0, 1)
	case ActionFocusWorkspacePrev:
		m.overviewWorkspaceStep(-1)
	case ActionFocusWorkspaceNext:
		m.overviewWorkspaceStep(1)
	default:
		return false
	}
	return true
}

const (
	// overviewScrollStep is the scroll distance, in libinput's pointer
	// units, that moves the selection one column or workspace.
	overviewScrollStep = 60
	// overviewScrollRepeat is the three-finger distance of each further
	// column step in one scroll: longer than the first, so a short scroll
	// reaches the next column exactly and a long one goes on.
	overviewScrollRepeat = 150
)

// overviewScroll moves the overview selection with a scroll frame: a row
// step when the vertical scroll reaches overviewScrollStep and leads, else
// a column step. Three-finger updates (encoded here as AxisFinger) step
// to the next column after
// overviewScrollStep, then once per overviewScrollRepeat, and stay on
// columns until they lift; up or down they step once per scroll, so a card
// in a stack is easy to pick.
// Continuous scrolling steps once per overviewScrollStep and a wheel once
// per notch (high-resolution wheels add up their fractions of a notch).
// Vertical steps select cards first, then rows; natural scroll
// (touchpad.* or mouse.*) flips both axes as libinput reports. The end of
// a scroll resets it (scrollStop).
func (m *Monitor) overviewScroll(a ports.PointerAxis) (changed bool) {
	if a.Vertical.Stop || a.Horizontal.Stop {
		return false
	}
	finger := a.Source == ports.AxisFinger
	if finger && m.ov.scrolled {
		return false
	}
	// A sideways finger scroll stays on columns; other sources do not, and
	// start from zero rather than from its leftover.
	sideways := finger && m.ov.sideways
	if !finger && m.ov.sideways {
		m.ov.scrollX, m.ov.scrollY = 0, 0
	}
	stepX := float64(overviewScrollStep)
	if sideways {
		stepX = overviewScrollRepeat
	}
	add := func(acc *float64, ax ports.ScrollAxis) {
		switch {
		case !ax.Set:
		case a.Source == ports.AxisWheel:
			*acc += float64(ax.V120) / 120 * overviewScrollStep
		default:
			*acc += ax.Value
		}
	}
	if !sideways {
		add(&m.ov.scrollY, a.Vertical)
	}
	add(&m.ov.scrollX, a.Horizontal)
	// A step that turns the overview returns, so the axis holds for the loop.
	axis := m.overviewAxis()
	cascade := axis == horizontalAxis
	for {
		switch {
		case math.Abs(m.ov.scrollY) >= overviewScrollStep && math.Abs(m.ov.scrollY) >= math.Abs(m.ov.scrollX):
			d := sign(m.ov.scrollY)
			// The step leaves before the move: a move that turns the
			// overview drops the scroll left (showOverview).
			m.ov.scrollY -= float64(d) * overviewScrollStep
			m.ov.scrollX = 0
			if cascade && !finger && m.cardAt(m.Current()) < 0 {
				m.overviewStep(d)
			} else {
				m.OverviewMove(0, d)
			}
			if m.overviewTurned(axis, finger) {
				return true
			}
			if finger {
				m.ov.scrollX, m.ov.scrollY, m.ov.scrolled = 0, 0, true
				return true
			}
		case math.Abs(m.ov.scrollX) >= stepX:
			d := sign(m.ov.scrollX)
			if cascade && finger {
				// One workspace per gesture, even over a stash card.
				m.overviewStep(d)
				m.ov.scrollX, m.ov.scrollY, m.ov.scrolled = 0, 0, true
				return true
			}
			m.ov.scrollX -= float64(d) * stepX
			m.ov.scrollY = 0
			m.OverviewMove(d, 0)
			if m.overviewTurned(axis, finger) {
				return true
			}
			if finger {
				m.ov.sideways, stepX = true, overviewScrollRepeat
			}
		default:
			return changed
		}
		changed = true
	}
}

// overviewTurned reports whether a scroll step turned the overview from the
// axis before. A finger scroll then waits for the fingers to lift, so it
// cannot bounce back.
func (m *Monitor) overviewTurned(before layoutAxis, finger bool) bool {
	if m.overviewAxis() == before {
		return false
	}
	m.ov.scrolled = m.ov.scrolled || finger
	return true
}

// scrollStop ends a scroll on every monitor: the fingers lifted, maybe
// after the pointer took the focus to another output, so the next scroll
// starts from zero wherever it goes.
func (c *Core) scrollStop(a ports.PointerAxis) {
	if !a.Vertical.Stop && !a.Horizontal.Stop {
		return
	}
	for _, sc := range c.screens {
		sc.mon.ov.scrollX, sc.mon.ov.scrollY, sc.mon.ov.scrolled, sc.mon.ov.sideways = 0, 0, false, false
	}
}

func sign(v float64) int {
	if v < 0 {
		return -1
	}
	return 1
}

// overviewKeyboardTaken reports whether a layer surface (a launcher) or a
// grabbing menu has the keyboard: it gets the keys, not the overview.
func (c *Core) overviewKeyboardTaken() bool {
	id := c.keyboardFocus()
	_, _, layer := c.layerOf(id)
	return layer || c.popups[id] != nil
}

// overviewKey runs an overview key: true when the key was one.
func (m *Monitor) overviewKey(key ports.KeyEvent) bool {
	if key.Mods != 0 {
		return false
	}
	switch keyName(key.Keysym) {
	case "h", "Left":
		m.OverviewMove(-1, 0)
	case "l", "Right":
		m.OverviewMove(1, 0)
	case "k", "Up":
		m.OverviewMove(0, -1)
	case "j", "Down":
		m.OverviewMove(0, 1)
	case "Return", "KP_Enter":
		m.ToggleOverview()
	case "Escape":
		m.CancelOverview()
	default:
		return false
	}
	return true
}
