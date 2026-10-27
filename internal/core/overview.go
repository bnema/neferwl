package core

import (
	"context"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
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

// The overview scales workspace rows without resizing clients. Covering
// floats and the columns form a linear stack of cards; small floats stay
// hidden. Moves along the workspace axis traverse the stack before changing
// workspace. A pinned fullscreen window is the front card of its row, the
// windows it hides behind it; choosing one of them leaves fullscreen.

// A workspace's stash shows as a pile of cards, folded or unfolded. Folded
// (not selected), the stash's own selection is in front at
// overviewCardZoom, up to overviewCards-1 others behind it, each
// overviewCardStep further up and right, dimmed. Selected, the pile
// unfolds along the overview axis into a carousel centred on the selection
// (unfold), framed, and the moves along that axis walk it round. A vertical
// overview pins the pile to the left edge of its row, which takes the width
// the pile leaves. A horizontal one centres it on its row, in a band
// reserved under the selected lane (pileBand): the lane and the band are
// centred together.
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
	m.each(func(w *Workspace) { w.StopColumns(); w.StopStash() })
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
		maximized: slices.Clone(w.Maximized),
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
		i := w.ColumnOf(item.id)
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
			i = w.ColumnOf(m.ov.selected)
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

// overviewOrder is where w sits among the overview rows built from the
// numbered workspaces list: its index in list, else (a named workspace) the
// index of its anchor plus a fraction below 1, ordered by configuration like
// overviewWorkspaces. It also places workspaces the rows drop. An anchor not
// in list is the last occupied one, or the first. It allocates nothing.
func (m *Monitor) overviewOrder(list []*Workspace, w *Workspace) float64 {
	if i := indexOf(list, w); i >= 0 {
		return float64(i)
	}
	at := indexOf(list, w.overviewAfter)
	if at < 0 {
		at = 0
		for i, n := range list {
			if !n.empty() {
				at = i
			}
		}
	}
	rank := len(m.named)
	for i, spec := range m.named {
		if spec.Name == w.Name {
			rank = i
			break
		}
	}
	return float64(at) + 0.5 + 0.4*float64(rank)/float64(len(m.named)+1)
}

// overviewWorkspaces inserts named rows below their invocation workspace,
// in configured order within each group. Unattached rows follow the last
// occupied numbered row (or the first). Empty invocation anchors stay visible;
// other empty rows are skipped unless current.
//
// The result is the monitor's scratch, rebuilt by the next call: callers
// read it and drop it before calling again (Clone it to keep it). It keeps
// the last result's workspaces reachable until then.
func (m *Monitor) overviewWorkspaces() []*Workspace {
	fallback := m.Workspaces[0]
	for _, w := range m.Workspaces {
		if !w.empty() {
			fallback = w
		}
	}
	rank := func(w *Workspace) int {
		for i, spec := range m.named {
			if spec.Name == w.Name {
				return i
			}
		}
		return len(m.named)
	}
	cur := m.Current()
	named := append(m.ovNamed[:0], m.hidden...)
	named = slices.DeleteFunc(named, func(n *Workspace) bool { return n.empty() && n != cur })
	m.ovNamed = named
	slices.SortStableFunc(named, func(a, b *Workspace) int { return rank(a) - rank(b) })
	rows := m.ovList[:0]
	for _, w := range m.Workspaces {
		// The row is kept when occupied, current or anchoring a named
		// row: placed first, dropped again if none of those holds.
		at := len(rows)
		rows = append(rows, w)
		for _, n := range named {
			anchor := n.overviewAfter
			if indexOf(m.Workspaces, anchor) < 0 {
				anchor = fallback
			}
			if anchor == w {
				rows = append(rows, n)
			}
		}
		if len(rows) == at+1 && w.empty() && w != cur {
			rows = rows[:at]
		}
	}
	// named is only needed here; the rows' tail is cleared so the scratch
	// holds no more than the last result.
	clear(named[:cap(named)])
	clear(rows[len(rows):cap(rows)])
	m.ovList = rows
	return rows
}

// showOverview changes rows without committing the provisional stack. Entering
// a named group uses its invocation anchor as the toggle return target, not
// rows merely browsed in the overview.
func (m *Monitor) showOverview(w *Workspace) {
	if _, axis := w.Engine().Axes(); axis != m.overviewAxis() {
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
	if m.overviewAxis() == layout.Horizontal {
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
// walks the stack cards before changing workspace (overviewVertical); from
// a selected stash card, which the move keys walk, it changes workspace
// directly. A side-by-side one always changes workspace, since its stack
// and unfolded stash run left and right.
func (m *Monitor) overviewWorkspaceStep(d int) {
	if m.overviewAxis() == layout.Vertical && m.cardAt(m.Current()) < 0 {
		m.overviewVertical(d)
		return
	}
	m.overviewStep(d)
}

// cycleCard selects the stash card d entries from the selected one of w in
// the unfolded pile, wrapping round (wrapsCards), else staying at either
// end. It reports whether a card was selected.
func (m *Monitor) cycleCard(w *Workspace, d int) bool {
	at := m.cardAt(w)
	if at < 0 {
		return false
	}
	m.stepCard(w, at, d)
	return true
}

// stepCard selects the stash card d entries from card at of w, wrapping
// round when the carousel does (wrapsCards). It reports false, selecting
// nothing, past an end of one that does not.
func (m *Monitor) stepCard(w *Workspace, at, d int) bool {
	n, next := len(w.Stash), at+d
	if next < 0 || next >= n {
		if !w.wrapsCards() {
			return false
		}
		next = (next%n + n) % n
	}
	m.selectCard(w, next)
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
		if i := w.LaneNeighbor(dy); i >= 0 {
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
	first, _ := w.LaneBounds(w.LaneOf(w.Focus))
	nfirst, nlast := w.LaneBounds(w.LaneOf(len(w.Columns) - 1))
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
// the unfolded stash, which wraps round (stepCard). Past the first column
// or a card that does not wrap, it goes to the previous workspace; past
// the last, to the next workspace. Covering floats fan along this axis and
// are traversed before a workspace: the card on the left is the one behind.
func (m *Monitor) overviewAcross(dx int) {
	w := m.Current()
	if at := m.cardAt(w); at >= 0 {
		if !m.stepCard(w, at, dx) {
			m.overviewStep(dx)
		}
		return
	}
	if m.stackFront(w).kind != stackColumns || len(w.Columns) == 0 {
		m.laneEdge(dx)
		return
	}
	first, last := w.LaneBounds(w.LaneOf(w.Focus))
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
// on the first or last row. The row selects its stack even when its stash
// has the focus, if the stash wraps round (wrapsCards): the moves that
// stepped there would only walk the carousel, never reaching the next row.
func (m *Monitor) overviewStep(d int) {
	rows := m.overviewWorkspaces()
	i := indexOf(rows, m.Current()) + d
	if i >= 0 && i < len(rows) {
		m.showOverview(rows[i])
		if w := m.Current(); m.cardAt(w) >= 0 && w.wrapsCards() {
			m.clearCard()
		}
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
	i := w.ColumnOf(m.ov.selected)
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
		if i := w.ColumnOf(id); i >= 0 {
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
func (m *Monitor) overviewAxis() layout.Axis {
	_, axis := m.Current().Engine().Axes()
	return axis
}

// overviewSlots are the slots of the overview rows: the current workspace
// and at most one neighbor on each side, in a fixed array (no allocation).
type overviewSlots struct {
	n int
	w [3]*Workspace
	r [3]Rect
}

func (s *overviewSlots) add(w *Workspace, r Rect) {
	s.w[s.n], s.r[s.n] = w, r
	s.n++
}

// lookup is the slot of w, if it has one.
func (s *overviewSlots) lookup(w *Workspace) (Rect, bool) {
	for i := range s.n {
		if s.w[i] == w {
			return s.r[i], true
		}
	}
	return Rect{}, false
}

// overviewRows places the current workspace and its overview neighbors in
// slots, in output-local pixels, along the overview axis; each workspace
// places its previews in its slot. The numbered/named boundary gets extra
// spacing and an inactive rule across the axis. The dividers are the
// monitor's scratch, rewritten by the next call: callers copy them.
//
// Vertical axis: full-width slots, the current one centred; the rule is
// horizontal. Horizontal axis: full-height slots as wide as their content,
// stack peeks included (the stash pile takes a band of the slot), the current one
// centred; the rule is vertical, in the spacing between two slots.
func (m *Monitor) overviewRows() (overviewSlots, []ports.Separator) {
	cur := m.Current()
	u := cur.overviewArea()
	gap := max(1, u.H*3/100)
	axis := m.overviewAxis()
	var cs Rect
	if axis == layout.Horizontal {
		row := cur.rowWidth(axis)
		cs = Rect{X: u.X + (u.W-row)/2, Y: u.Y, W: row, H: u.H}
	} else {
		hc := cur.rowHeight(axis)
		cs = Rect{X: u.X, Y: u.Y + (u.H-hc)/2, W: u.W, H: hc}
	}
	var rows overviewSlots
	rows.add(cur, cs)
	dividers := 0
	list := m.overviewWorkspaces()
	i := indexOf(list, cur)
	for _, next := range [2]int{i - 1, i + 1} {
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
		rows.add(w, slot)
		if boundary {
			m.ovDividers[dividers] = overviewDivider(axis, u, gapAt+spacing/2)
			dividers++
		}
	}
	if dividers == 0 {
		return rows, nil
	}
	return rows, m.ovDividers[:dividers]
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
	var unfolded *Workspace
	var unfoldedSlot Rect
	for w := range m.all() {
		slot, shown := rows.lookup(w)
		if shown {
			// The pile band goes under the row: the row is centred
			// with it.
			slot.H -= w.pileBand(axis)
			at := m.cardAt(w)
			result = append(result, m.stackRow(w, axis, slot, w != cur, at < 0)...)
			if at >= 0 {
				unfolded, unfoldedSlot = w, slot
			} else {
				result = w.pile(result, axis, slot, w != cur, -1)
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
	if unfolded != nil {
		result = unfolded.pile(result, axis, unfoldedSlot, false, m.cardAt(unfolded))
	}
	return result
}

// outlineLines is the lines of an uncut outline: one per edge.
const outlineLines = 4

// overviewClickFade is the fade from which a card takes no click.
const overviewClickFade = 0.5

// overviewHit is overviewIn with the stash piles first: the cards behind a
// folded pile's front one peek out where tiles may be drawn over them.
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
	c.requestFlash(flashWindow, id)
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
	repeating := finger && m.ov.sideways
	if !finger && m.ov.sideways {
		m.ov.scrollX, m.ov.scrollY = 0, 0
	}
	stepX := float64(overviewScrollStep)
	if repeating {
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
	if !repeating {
		add(&m.ov.scrollY, a.Vertical)
	}
	add(&m.ov.scrollX, a.Horizontal)
	// A step that turns the overview returns, so the axis holds for the loop.
	axis := m.overviewAxis()
	sideways := axis == layout.Horizontal
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
			case sideways && !finger && m.cardAt(m.Current()) >= 0:
				// A wheel walks the unfolded stash, a row.
				m.OverviewMove(d, 0)
			case sideways && !finger:
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
			if sideways && finger {
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
func (m *Monitor) overviewTurned(before layout.Axis, finger bool) bool {
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
