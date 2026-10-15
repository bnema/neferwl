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
	// scrolled is set once a three-finger gesture took its one step per
	// gesture (up or down, a cascade's horizontal step, or a turn of the
	// overview): it waits for the fingers to lift. sideways is set
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
// hidden. Moves along the workspace axis traverse the stack before changing
// workspace. A pinned fullscreen window is the front card of its row, the
// windows it hides behind it; choosing one of them leaves fullscreen.

// A workspace's stash shows as a pile of cards, folded or unfolded. Folded
// (not selected), the stash's own selection is in front at
// overviewCardZoom, up to overviewCards-1 others behind it, each
// overviewCardStep further up and right, dimmed. Selected, the pile
// unfolds along the overview axis into a strip of every card (unfold), the
// selection framed, and the moves along that axis walk it. A vertical
// overview pins the pile to the left edge of its row, which takes the width
// the pile leaves. A horizontal one centres it on its row, straddling the
// bottom edge of the selected lane, over the tiles.
const (
	overviewCardZoom = 0.2
	overviewCards    = 4
	overviewCardStep = 8
	overviewPeekStep = 0.07
)

// overviewMaxZoom caps the current row so its neighbors show beside it
// along the workspace axis; overviewMinZoom floors the shrink of wide
// workspaces, which then scroll to keep the selected column in view.
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
	m.clearCard()
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

// clearCard deselects the stash card.
func (m *Monitor) clearCard() { m.ov.card, m.ov.cardOf = 0, nil }

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
	if w.policy().maximizeHides() {
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
	// Workspaces stack vertically: vertical moves stay in a selected stash
	// pile; left stays, and right deselects it.
	if dy != 0 {
		m.overviewVertical(dy)
	} else if dx != 0 {
		m.overviewHorizontal(dx)
	}
}

// overviewWorkspaceStep is a focus-workspace bind. A vertical overview
// walks the unfolded stash and the stack cards before changing workspace
// (overviewVertical). A side-by-side one always changes workspace, since
// its stack and unfolded stash run left and right.
func (m *Monitor) overviewWorkspaceStep(d int) {
	if m.overviewAxis() == verticalAxis {
		m.overviewVertical(d)
		return
	}
	m.overviewStep(d)
}

// cycleCard selects the stash card d entries from the selected one of w in
// the unfolded pile, stopping at either end. It reports whether a card was
// selected.
func (m *Monitor) cycleCard(w *Workspace, d int) bool {
	at := m.cardAt(w)
	if at < 0 {
		return false
	}
	m.selectCard(w, min(max(at+d, 0), len(w.Stash)-1))
	return true
}

// overviewLane is the vertical move of an overview whose workspaces sit side
// by side (cascade). From the unfolded stash, up leaves it for the last lane
// (leavePile) and down stays: the stash is the bottom. Else the selection
// goes to the previous or next lane (band), at the closest column, stopping
// at the first. Down past the last lane, or from a card that is not the
// columns, enters the stash, which sits below. Covering floats stack along
// the horizontal axis, not this one.
func (m *Monitor) overviewLane(dy int) {
	w := m.Current()
	if m.cardAt(w) >= 0 {
		if dy < 0 {
			m.leavePile(w)
		}
		return
	}
	if m.stackFront(w).kind == stackColumns && len(w.Columns) > 0 {
		if i := w.laneNeighbor(dy); i >= 0 {
			m.selectLaneColumn(w, i)
			return
		}
	}
	if dy > 0 && len(w.Stash) > 0 {
		m.selectCard(w, w.stashAt)
	}
}

// leavePile moves the selection from a stash card back up to where the pile
// sits below: the last lane, at the column the pile was entered from (the
// focused column's offset in its lane, or the lane's last column when it is
// shorter). With a covering float in front it only deselects the card; a
// stash-only workspace has nothing above the pile, so the card stays.
func (m *Monitor) leavePile(w *Workspace) {
	if len(w.stack()) == 0 {
		return
	}
	front := m.stackFront(w)
	m.clearCard()
	m.ov.selected = 0
	if front.kind != stackColumns || len(w.Columns) == 0 {
		return
	}
	first, _ := w.laneBounds(w.laneOf(w.Focus))
	nfirst, nlast := w.laneBounds(w.laneOf(len(w.Columns) - 1))
	m.selectLaneColumn(w, min(nfirst+w.Focus-first, nlast))
}

// selectLaneColumn selects column i of w.
func (m *Monitor) selectLaneColumn(w *Workspace, i int) {
	w.selectOverviewColumn(i)
	m.ov.selected = w.Columns[i].Windows[w.Columns[i].Focus]
	m.ov.selectedAt = i
}

// overviewAcross is the horizontal move of an overview whose workspaces sit
// side by side (cascade): a column inside the selected lane, or a card of
// the unfolded stash. Past the first column or card it goes to the previous
// workspace; past the last, to the next workspace. Covering floats fan
// along this axis and are traversed before a workspace: the card on the
// left is the one behind.
func (m *Monitor) overviewAcross(dx int) {
	w := m.Current()
	if at := m.cardAt(w); at >= 0 {
		if next := at + dx; next >= 0 && next < len(w.Stash) {
			m.selectCard(w, next)
			return
		}
		m.overviewStep(dx)
		return
	}
	if m.stackFront(w).kind != stackColumns || len(w.Columns) == 0 {
		m.laneEdge(dx)
		return
	}
	first, last := w.laneBounds(w.laneOf(w.Focus))
	if next := w.Focus + dx; next >= first && next <= last {
		m.selectLaneColumn(w, next)
		return
	}
	m.laneEdge(dx)
}

// laneEdge is a horizontal move off the edge of the selected lane, or off a
// card that is not the columns: the stack first, then a workspace.
func (m *Monitor) laneEdge(dx int) {
	if m.moveStack(-dx) {
		return
	}
	m.overviewStep(dx)
}

// overviewVertical is the vertical move of an overview whose workspaces
// stack vertically: through a selected stash pile, then the stack, then
// to the next workspace.
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
			m.clearCard()
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
	m.clearCard()
	if i := w.stashIndex(id); i >= 0 {
		m.selectCard(w, i)
	} else if item, ok := w.itemOf(id); ok {
		m.setFront(w, item)
		if i := w.columnOf(id); i >= 0 {
			// The clicked window first: a maximization moving to its
			// column records it in the maximize history.
			for j, v := range w.Columns[i].Windows {
				if v == id {
					w.Columns[i].Focus = j
					break
				}
			}
			if item.kind == stackColumns && !w.overviewMaximized() {
				w.selectOverviewColumn(i)
			}
			m.ov.selected = id
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
// stack peeks included (the stash pile sits over the row), the current one
// centred; the rule is vertical, in the spacing between two slots.
func (m *Monitor) overviewRows() (map[*Workspace]Rect, []ports.Separator) {
	cur := m.Current()
	u := cur.overviewArea()
	gap := max(1, u.H*3/100)
	axis := m.overviewAxis()
	var cs Rect
	if axis == horizontalAxis {
		row := cur.rowWidth(axis)
		cs = Rect{X: u.X + (u.W-row)/2, Y: u.Y, W: row, H: u.H}
	} else {
		hc := cur.rowHeight(axis)
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
		slot = Rect{Y: u.Y, W: w.rowWidth(axis), H: u.H}
		if before {
			gapAt = cs.X - spacing
			slot.X = gapAt - slot.W
		} else {
			gapAt = cs.X + cs.W
			slot.X = gapAt + spacing
		}
		return slot, gapAt
	}
	h := w.rowHeight(axis)
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
	// The unfolded stash goes last: its strip may run over the neighbor
	// rows, which must not cover it.
	var unfolded []Placement
	for w := range m.all() {
		slot, shown := rows[w]
		if shown {
			// The pile band goes under the row: the row is centred
			// with it.
			slot.H -= w.pileBand(axis)
			at := m.cardAt(w)
			result = append(result, m.stackRow(w, axis, slot, w != cur, at < 0)...)
			if len(w.Stash) > 0 {
				if cards := w.pile(axis, slot, w != cur, at); at >= 0 {
					unfolded = cards
				} else {
					result = append(result, cards...)
				}
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
	return append(result, unfolded...)
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

// pileWidth is the room the stash pile of w takes on the left of its row in
// a vertical overview, its margin included; 0 without a stash.
func (w *Workspace) pileWidth() int {
	if len(w.Stash) == 0 {
		return 0
	}
	card := int(math.Round(float64(w.stashRect().W) * overviewCardZoom))
	// Never more than a third of the width: the columns keep the rest.
	a := w.overviewArea()
	return min(card+(min(len(w.Stash), overviewCards)-1)*w.cardStep()+a.W*2/100, a.W/3)
}

// cardSize is the size of a stash card in the overview.
func (w *Workspace) cardSize() (int, int) {
	r := w.stashRect()
	return int(math.Round(float64(r.W) * overviewCardZoom)), int(math.Round(float64(r.H) * overviewCardZoom))
}

// pileBand is the height a horizontal overview gives the stash of w under
// its selected lane: the spacing, then the folded pile with its cards
// behind. 0 without a stash, or in a vertical overview, where the pile
// sits left of the row. The lane and the band are centred together.
func (w *Workspace) pileBand(axis layoutAxis) int {
	if axis != horizontalAxis || len(w.Stash) == 0 {
		return 0
	}
	_, ch := w.cardSize()
	return max(1, w.overviewArea().H*3/100) + ch + (min(len(w.Stash), overviewCards)-1)*w.cardStep()
}

// cardStep is the offset between stacked cards, smaller on tiny outputs.
func (w *Workspace) cardStep() int {
	a := w.overviewArea()
	return min(overviewCardStep, a.W/100, a.H/50)
}

// pile places the stash of w as cards in its slot along axis (see the
// overview's stash comment). With entry at selected (at >= 0, on the
// current row: dim false) the pile unfolds; else it is folded, the stash
// selection in front, the next ones behind it, dimmed, the rest hidden.
func (w *Workspace) pile(axis layoutAxis, slot Rect, dim bool, at int) []Placement {
	n := len(w.Stash)
	if n == 0 {
		return nil
	}
	u := w.overviewArea()
	cw, ch := w.cardSize()
	rowH := int(math.Round(float64(w.Usable.H) * w.overviewZoom(axis)))
	var x0, y0 int
	if axis == horizontalAxis {
		// Centred on the row, under the selected lane: slot is the
		// lane's, the pile band (pileBand) taken off its height, so the
		// band starts below the lane. The cards behind rise from the
		// front one, which goes at the bottom of the band. Both scale
		// with the area (the lane at most overviewMaxZoom of it), so
		// the pile stays inside whatever its size.
		x0 = slot.X + (slot.W-cw)/2
		y0 = slot.Y + (slot.H-rowH)/2 + rowH + w.pileBand(axis) - ch
	} else {
		// Stack rows reserve two peek steps above the front card. Align
		// the stash with that front band, not with the top of its envelope.
		band := w.frontSlot(axis, slot)
		x0, y0 = band.X+u.W/100, band.Y+(rowH-ch)/2
	}
	folded := Rect{X: x0, Y: y0, W: cw, H: ch}
	if at >= 0 && !dim {
		return w.unfold(axis, folded, min(at, n-1))
	}
	cards := make([][]Placement, n)
	for i := range w.Stash {
		cards[i] = []Placement{{ID: w.Stash[i].ID, Rect: folded, Preview: overviewCardZoom, Card: true}}
	}
	step := w.cardStep()
	return fan(cards, min(max(w.stashAt, 0), n-1), min(n, overviewCards)-1, 0, true,
		func(d int) (int, int) { return d * step, -d * step },
		func(int) (int, int) { return 0, 0 }, dim, false)
}

// unfold lays the stash of w out as a strip of every card along axis, from
// the folded pile's card rect: a row in a horizontal overview, a column in
// a vertical one. The strip is centred on the pile and kept in the overview
// area. Longer than the area, it scrolls like a wide row: the selected card
// at is centred, the strip stopping at the area's edges. The other cards
// are dimmed; those out of the area are hidden.
func (w *Workspace) unfold(axis layoutAxis, folded Rect, at int) []Placement {
	n := len(w.Stash)
	u := w.overviewArea()
	size, origin, lo, hi := folded.W, folded.X, u.X, u.X+u.W
	if axis == verticalAxis {
		size, origin, lo, hi = folded.H, folded.Y, u.Y, u.Y+u.H
	}
	stride := size + 2*w.cardStep()
	total := n*stride - (stride - size)
	start := origin + size/2 - total/2
	if total > hi-lo {
		start = origin + size/2 - at*stride - size/2
		start = min(max(start, hi-total), lo)
	} else {
		start = min(max(start, lo), hi-total)
	}
	out := make([]Placement, n)
	for i := range w.Stash {
		r := folded
		if axis == verticalAxis {
			r.Y = start + i*stride
		} else {
			r.X = start + i*stride
		}
		p := Placement{ID: w.Stash[i].ID, Rect: r, Preview: overviewCardZoom, Card: true, Focused: i == at}
		if i != at {
			p.peeking(1)
		}
		if !r.Overlaps(u) {
			p.Hidden, p.Preview, p.Focused = true, 0, false
		}
		out[i] = p
	}
	return out
}

// overviewZoom is the scale that fits every column of w in the usable
// width, within the overview bounds, in an overview along axis: only a
// vertical one sets the stash pile beside the row.
func (w *Workspace) overviewZoom(axis layoutAxis) float64 {
	z, _ := w.zoomSpan(axis)
	return z
}

// zoomSpan is overviewZoom with the unscaled span of the tiles it fits. A
// cascade workspace spans one screen whatever its band count.
func (w *Workspace) zoomSpan(axis layoutAxis) (float64, int) {
	// Only the span is read: the tiles go in the zoom scratch, dead on return.
	var span int
	w.zoomBuf, span, _ = w.previewTilesInto(w.zoomBuf[:0])
	if span <= 0 {
		return overviewMaxZoom, span
	}
	room := float64(w.overviewArea().W) * 0.96
	if axis == verticalAxis {
		room = float64(w.overviewArea().W-w.pileWidth()) * 0.96
	}
	z := min(max(room/float64(span), overviewMinZoom), overviewMaxZoom)
	if w.stacked() {
		// Reserve two clear peeks on either side of the front row.
		z = min(z, overviewMaxZoom/(1+4*overviewPeekStep))
	}
	return z, span
}

// rowWidth is the width of the slot of w in a horizontal overview: what its
// tiles take (at most the area), plus the room for the covering floats
// peeking on either side of the front card.
func (w *Workspace) rowWidth(axis layoutAxis) int {
	z, span := w.zoomSpan(axis)
	if span <= 0 {
		// An empty workspace keeps the room of a screen.
		span = w.Usable.W
	}
	width := min(int(math.Round(float64(span)*z)), w.overviewArea().W)
	if w.stacked() {
		width += 4 * w.peekStep(axis)
	}
	return width
}

// peekStep is the shared step for a stack row and its front-card inset.
func (w *Workspace) peekStep(axis layoutAxis) int {
	return max(1, int(math.Round(float64(w.Usable.H)*w.overviewZoom(axis)*overviewPeekStep)))
}

// frontSlot is the part of slot the front card of w takes: slot shifted
// along axis past the room reserved for two cards peeking before it, and
// (along a horizontal axis, where the slot is as wide as its cards) without
// the room of the two after it. A single-card row keeps slot.
func (w *Workspace) frontSlot(axis layoutAxis, slot Rect) Rect {
	if w.stacked() {
		p := w.peekStep(axis)
		axis.offset(&slot, 2*p)
		if axis == horizontalAxis {
			slot.W -= 4 * p
		}
	}
	return slot
}

// rowHeight includes the largest possible stack envelope (two peeks above
// and two below). Neighbor rows use the same measure as the current row.
func (w *Workspace) rowHeight(axis layoutAxis) int {
	h := int(math.Round(float64(w.Usable.H) * w.overviewZoom(axis)))
	if w.stacked() {
		h += 4 * w.peekStep(axis)
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
	for i, c := range w.Columns {
		r := Rect{Y: g, H: h}
		if w.policy().wraps {
			// The band is the lane: it is encoded in Y, one screen apart.
			// A maximized column fills its band, alone, as on screen.
			r.X = w.cellX(i)
			r.Y = g + w.band(i)*w.Usable.H
			r.W = w.cellWidth(i)
			if c.FullWidth {
				r.X, r.W = g, max(w.Usable.W-2*g, 0)
			}
		} else if w.Overflow == OverflowFixed {
			r = rects[i]
			r.X, r.Y = r.X-w.Usable.X, r.Y-w.Usable.Y
		} else {
			r.X, r.W = w.columnX(i)-w.Usable.X, w.columnWidth(i)
			if w.fullscreenColumn(i) {
				// A fullscreen column shows at the width it had in the row.
				r.W = max(w.Usable.W-2*g, 0)
			}
		}
		if w.hiddenByMaximized(i) {
			// Fixed overflow shows the hidden columns on their own card
			// (previewItem). Their old buffers cannot fit their current
			// rects; show only the maximized column here.
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
// row is wider (right of the stash pile in a vertical overview). Dimmed rows (neighbor
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
	z := w.overviewZoom(axis)
	scale := func(v int) int { return int(math.Round(float64(v) * z)) }
	width := scale(span)
	u := w.overviewArea()
	top := slot.Y
	if axis == horizontalAxis {
		top = slot.Y + (slot.H-scale(w.Usable.H))/2
	}
	// Rows stay centred on the output like rows without a stash; in a
	// vertical overview the pile only pushes a row right when they would
	// overlap. A horizontal one draws the pile over the row.
	left := 0
	if axis == verticalAxis {
		left = w.pileWidth()
	}
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
	// The lanes below the selected one leave room for the stash pile.
	below := w.pileBand(axis)
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
		y := top + y0 + lane*laneStep
		if lanes && lane > 0 {
			y += below
		}
		tiles[i].Rect = Rect{X: x + x0, Y: y, W: scale(r.X+r.W) - x0, H: scale(ly+r.H) - y0}
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
// b, just outside it. The selection is one preview: a selected stash card
// wins over any other focused one, which must then not be framed. A tile's
// frame is cut where the cards of the pile sit over it, so it never runs
// across the pile; a selected card keeps its complete frame.
func overviewOutline(layout []Placement, b int) []ports.Separator {
	sel := -1
	for i, p := range layout {
		if p.Preview > 0 && p.Focused && !p.Hidden && (sel < 0 || p.Card && !layout[sel].Card) {
			sel = i
		}
	}
	if sel < 0 {
		return nil
	}
	r := layout[sel].Rect
	edges := [4]Rect{
		{X: r.X - b, Y: r.Y - b, W: r.W + 2*b, H: b},
		{X: r.X - b, Y: r.Y + r.H, W: r.W + 2*b, H: b},
		{X: r.X - b, Y: r.Y, W: b, H: r.H},
		{X: r.X + r.W, Y: r.Y, W: b, H: r.H},
	}
	out := make([]ports.Separator, 0, len(edges))
	cut := false
	if !layout[sel].Card {
		for _, p := range layout {
			if p.Card && p.Preview > 0 && !p.Hidden {
				cut = true
				break
			}
		}
	}
	if !cut {
		for _, e := range edges {
			out = append(out, ports.Separator{Rect: e, Active: true})
		}
		return out
	}
	// Stack buffers: the outline is rebuilt every overview frame.
	var cardBuf [overviewCards]Rect
	var pieceBuf [8]Rect
	cards := cardBuf[:0]
	for _, p := range layout {
		if p.Card && p.Preview > 0 && !p.Hidden {
			cards = append(cards, p.Rect)
		}
	}
	for _, e := range edges {
		for _, piece := range subtractRects(pieceBuf[:0], e, cards) {
			out = append(out, ports.Separator{Rect: piece, Active: true})
		}
	}
	return out
}

// subtractRects is what of r no rect of cuts covers, as rects. The edges of
// an outline are thin, so a cut splits one into at most two along its length
// (or drops it, or cuts across its thickness): the pieces are cut one rect
// at a time. The pieces are appended to dst; cuts that miss r allocate
// nothing.
func subtractRects(dst []Rect, r Rect, cuts []Rect) []Rect {
	var bufA, bufB [8]Rect
	pieces, spare := append(bufA[:0], r), bufB[:0]
	for _, c := range cuts {
		next := spare[:0]
		for _, p := range pieces {
			if !p.Overlaps(c) {
				next = append(next, p)
				continue
			}
			// The four bands of p around c: above, below, left, right.
			if c.Y > p.Y {
				next = append(next, Rect{X: p.X, Y: p.Y, W: p.W, H: c.Y - p.Y})
			}
			if c.Y+c.H < p.Y+p.H {
				next = append(next, Rect{X: p.X, Y: c.Y + c.H, W: p.W, H: p.Y + p.H - c.Y - c.H})
			}
			top, bottom := max(p.Y, c.Y), min(p.Y+p.H, c.Y+c.H)
			if c.X > p.X {
				next = append(next, Rect{X: p.X, Y: top, W: c.X - p.X, H: bottom - top})
			}
			if c.X+c.W < p.X+p.W {
				next = append(next, Rect{X: c.X + c.W, Y: top, W: p.X + p.W - c.X - c.W, H: bottom - top})
			}
		}
		// Swap the buffers: next becomes the pieces, the old pieces'
		// storage the next round's.
		pieces, spare = next, pieces
	}
	return append(dst, pieces...)
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

// overviewHit is overviewIn with the stash piles first: a pile is drawn
// over its row (a horizontal overview), the cards behind its front one
// included.
func (m *Monitor) overviewHit(layout []Placement, x, y float64) WindowID {
	var piles []Placement
	for _, p := range layout {
		if w, _ := m.find(p.ID); w != nil && w.stashIndex(p.ID) >= 0 {
			piles = append(piles, p)
		}
	}
	if id := overviewIn(piles, x, y); id != 0 {
		return id
	}
	return overviewIn(layout, x, y)
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
	id := sc.mon.overviewHit(sc.shownLayout(), c.cursorX-float64(sc.x), c.cursorY-float64(sc.y))
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
// to the next column after overviewScrollStep, then once per
// overviewScrollRepeat, and stay on columns until they lift; up or down they
// step once per scroll, so a card in a stack is easy to pick. In a cascade
// overview they take one step per gesture on both axes: a band vertically,
// a card or workspace horizontally.
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
	// A finger step reports a change only when the selection moved: at an
	// edge or over a float it does nothing, and the scene stays. The
	// snapshot is taken only before a step: stackFront allocates.
	var before overviewSelection
	for {
		switch {
		case math.Abs(m.ov.scrollY) >= overviewScrollStep && math.Abs(m.ov.scrollY) >= math.Abs(m.ov.scrollX):
			d := sign(m.ov.scrollY)
			// The step leaves before the move: a move that turns the
			// overview drops the scroll left (showOverview).
			m.ov.scrollY -= float64(d) * overviewScrollStep
			m.ov.scrollX = 0
			if finger {
				before = m.overviewSelection()
			}
			switch {
			case cascade && !finger && m.cardAt(m.Current()) >= 0:
				// A wheel walks the unfolded stash, a row.
				m.OverviewMove(d, 0)
			case cascade && !finger:
				m.overviewStep(d)
			default:
				m.OverviewMove(0, d)
			}
			if m.overviewTurned(axis, finger) {
				return true
			}
			if finger {
				m.ov.scrollX, m.ov.scrollY, m.ov.scrolled = 0, 0, true
				return m.overviewSelection() != before
			}
		case math.Abs(m.ov.scrollX) >= stepX:
			d := sign(m.ov.scrollX)
			if cascade && finger {
				before = m.overviewSelection()
				// One step per gesture: one card of the unfolded
				// stash, else one workspace.
				if m.cardAt(m.Current()) >= 0 {
					m.OverviewMove(d, 0)
				} else {
					m.overviewStep(d)
				}
				m.ov.scrollX, m.ov.scrollY, m.ov.scrolled = 0, 0, true
				return m.overviewSelection() != before
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

// overviewSelection is what an overview step can move: the workspace, its
// stack front, stash card, focused column and selected window.
type overviewSelection struct {
	w              *Workspace
	front          stackItem
	card, selected WindowID
	focus          int
}

func (m *Monitor) overviewSelection() overviewSelection {
	w := m.Current()
	return overviewSelection{w: w, front: m.stackFront(w), card: m.card(), selected: m.ov.selected, focus: w.Focus}
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
