package core

import (
	"context"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// overviewState owns the provisional selection and opening snapshot. row is
// the workspace whose stack has a provisional front; nil uses its real front.
type overviewState struct {
	open       bool
	from       *Workspace
	fromID     WindowID
	floats     []Float
	fullWidth  WindowID
	row        *Workspace
	front      stackItem
	frontAt    int
	cardOf     *Workspace
	card       WindowID
	selected   WindowID
	selectedAt int
	// FullWidth history is restored on Escape when active binds changed it.
	maximized        []WindowID
	scrollX, scrollY float64
}

// The overview scales workspace rows without resizing clients. Covering
// floats and the columns form a linear stack of cards; small floats stay
// hidden. Up/down traverse the stack before changing workspace. A pinned
// fullscreen window remains the only preview on its row.

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
	m.each(func(w *Workspace) { w.stopSlide() })
	m.ov.open, m.ov.from = true, w
	m.ov.fromID, _ = w.Focused()
	m.ov.floats = append([]Float(nil), w.Floats...)
	m.ov.maximized = slices.Clone(w.maximized)
	m.ov.fullWidth = 0
	if len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth {
		c := w.Columns[w.Focus]
		m.ov.fullWidth = c.Windows[c.Focus]
	}
	m.ov.scrollX, m.ov.scrollY = 0, 0
	m.overviewOpens++
	m.selectRow()
}

// selectRow resets provisional stack and stash selection for a new row.
func (m *Monitor) selectRow() {
	m.ov.card, m.ov.cardOf = 0, nil
	m.ov.front, m.ov.row, m.ov.frontAt = stackItem{}, nil, 0
	m.ov.selected, m.ov.selectedAt = 0, -1
	w := m.Current()
	if w.pinned() {
		return
	}
	if w.floatFocus && len(w.Columns) > 0 && (len(w.stack()) < 2 || m.stackFront(w).kind == stackColumns) {
		w.floatFocus = false
	}
	if len(w.Stash) > 0 && (len(w.Columns) == 0 || w.stashFocused() && !w.floatFocus) {
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

// closeOverview closes the overview on the selection: a card shows the
// stash on it. A fullscreen request another stashed window made while
// the stash was hidden is dropped: the picked card stays in front.
func (m *Monitor) closeOverview() {
	w := m.Current()
	if i := m.cardAt(w); i >= 0 {
		if w.hiddenFullscreen != w.Stash[i].ID {
			w.hiddenFullscreen = 0
		}
		w.stashAt = i
		w.showStash()
	} else if !w.pinned() && len(w.stack()) > 0 {
		item := m.stackFront(w)
		selected := m.ov.selected
		if item.kind == stackColumns && w.columnOf(selected) < 0 && len(w.Columns) > 0 {
			c := w.Columns[w.Focus]
			selected = c.Windows[c.Focus]
		}
		w.apply(item, selected)
	}
	m.ov = overviewState{}
}

// CancelOverview closes the overview and returns to the workspace and
// window it opened on, if they are still there.
func (m *Monitor) CancelOverview() {
	from, id := m.ov.from, m.ov.fromID
	if from != nil && m.has(from) {
		m.show(from)
		if id != 0 {
			from.FocusID(id)
		} else {
			from.floatFocus, from.stashFocus = false, false
		}
		// FocusID on a float raises it; restore the initial order and below
		// flags for floats still present, leaving new floats in their order.
		initial := m.ov.floats
		current := from.Floats
		from.Floats = nil
		for _, f := range initial {
			for i, now := range current {
				if now.ID == f.ID {
					now.below = f.below
					from.Floats = append(from.Floats, now)
					current = append(current[:i], current[i+1:]...)
					break
				}
			}
		}
		from.Floats = append(from.Floats, current...)
		// Fixed overflow maximizes at most one column: only the one holding
		// the anchor window, wherever active binds moved it. Scroll overflow
		// keeps each column's own width.
		if from.Overflow == OverflowFixed {
			from.unmaximize()
			for i := range from.Columns {
				if m.ov.fullWidth != 0 && slices.Contains(from.Columns[i].Windows, m.ov.fullWidth) {
					from.maximize(i)
					break
				}
			}
		}
		from.maximized = slices.Clone(m.ov.maximized)
		from.scroll()
	}
	m.ov = overviewState{}
}

// OverviewMove routes every vertical input through moveStack, then falls
// through to workspace navigation at the stack boundary. Horizontal moves
// stay inside a column group; left on a single card sends it behind.
func (m *Monitor) OverviewMove(dx, dy int) {
	w := m.Current()
	if dy != 0 {
		if m.moveStack(-dy) {
			return
		}
		i := m.Active + dy
		if m.shown != nil || i < 0 || i >= len(m.Workspaces) || m.Workspaces[i].empty() {
			return
		}
		m.Focus(i)
		m.selectRow()
		return
	}
	if w.pinned() {
		return
	}
	if at := m.cardAt(w); at >= 0 {
		switch {
		case at+dx >= 0 && at+dx < len(w.Stash):
			m.selectCard(w, at+dx)
		case dx > 0 && len(w.Columns) > 0:
			m.ov.card, m.ov.cardOf = 0, nil
			item := m.stackFront(w)
			if item.kind == stackColumns {
				w.selectOverviewColumn(0)
				m.ov.selected = w.Columns[0].Windows[w.Columns[0].Focus]
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
	if i < 0 {
		i = w.Focus
	}
	if dx < 0 && i == 0 && len(w.Stash) > 0 {
		m.selectCard(w, w.stashAt)
		return
	}
	next := min(max(i+dx, 0), len(w.Columns)-1)
	m.ov.selected = w.Columns[next].Windows[w.Columns[next].Focus]
	m.ov.selectedAt = next
	// Ordinary group navigation retains its existing scroll-to-selection.
	w.selectOverviewColumn(next)
}

// OverviewPick closes the overview on the clicked window.
func (m *Monitor) OverviewPick(id WindowID) {
	w, _ := m.find(id)
	if w == nil {
		return
	}
	if w.floatIndex(id) >= 0 && w.stashIndex(id) < 0 {
		i := w.floatIndex(id)
		if !w.coversFloat(w.Floats[i]) && !w.pinned() {
			return
		}
	}
	m.show(w)
	m.ov.card, m.ov.cardOf = 0, nil
	if i := w.stashIndex(id); i >= 0 {
		m.selectCard(w, i)
	} else if w.pinned() {
		w.FocusID(id)
	} else if item, ok := w.itemOf(id); ok {
		m.setFront(w, item)
		if i := w.columnOf(id); i >= 0 {
			if item.kind == stackColumns && !w.overviewMaximized() {
				w.selectOverviewColumn(i)
			}
			m.ov.selected = id
			if item.kind == stackColumns {
				for j, v := range w.Columns[i].Windows {
					if v == id {
						w.Columns[i].Focus = j
						break
					}
				}
			}
		}
	}
	m.closeOverview()
}

// overviewLayout places the current workspace's row in the middle of the
// usable area and its numbered neighbors above and below, dimmed, each
// with its stash pile and covering-float stack. Other workspaces are hidden.
func (m *Monitor) overviewLayout() []Placement {
	cur := m.Current()
	u := cur.Usable
	gap := u.H * 3 / 100
	var result []Placement
	hc := cur.rowHeight()
	y := u.Y + (u.H-hc)/2
	rows := map[*Workspace]int{}
	rows[cur] = y
	if i := indexOf(m.Workspaces, cur); i >= 0 && m.shown == nil {
		if i > 0 {
			up := m.Workspaces[i-1]
			rows[up] = y - gap - up.rowHeight()
		}
		if i+1 < len(m.Workspaces) {
			rows[m.Workspaces[i+1]] = y + hc + gap
		}
	}
	for _, w := range m.all() {
		ry, shown := rows[w]
		if shown && w.pinned() {
			// Only the covering window shows, as on screen.
			result = append(result, w.previewRow(ry, w != cur, true)...)
			for _, id := range w.windows() {
				if id != w.fullscreen {
					result = append(result, Placement{ID: id, Hidden: true})
				}
			}
			continue
		}
		if shown {
			at := m.cardAt(w)
			result = append(result, m.stackRow(w, ry, w != cur, at < 0)...)
			result = append(result, w.pile(ry, w != cur, at)...)
		}
		for _, f := range w.Floats {
			if !shown || len(w.stack()) < 2 || !w.coversFloat(f) {
				result = append(result, Placement{ID: f.ID, Hidden: true})
			}
		}
		if !shown {
			for _, id := range w.windows() {
				if w.floatIndex(id) < 0 {
					result = append(result, Placement{ID: id, Hidden: true})
				}
			}
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
				p.Peek = p.Peek || peek || dim
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
	if len(w.Stash) == 0 || w.pinned() {
		return 0
	}
	card := int(math.Round(float64(w.stashRect().W) * overviewCardZoom))
	// Never more than a third of the width: the columns keep the rest.
	return min(card+(min(len(w.Stash), overviewCards)-1)*w.cardStep()+w.Usable.W*2/100, w.Usable.W/3)
}

// cardStep is the offset between stacked cards, smaller on tiny outputs.
func (w *Workspace) cardStep() int {
	return min(overviewCardStep, w.Usable.W/100, w.Usable.H/50)
}

// pile places the stash of w as cards on the left of the row whose top is
// at y: entry front (the stash selection when -1) in front, the next ones
// behind it, dimmed, the rest hidden. Only a front card of the current row
// (dim false) with front set is selected.
func (w *Workspace) pile(y int, dim bool, front int) []Placement {
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
	u := w.Usable
	cw := int(math.Round(float64(r.W) * overviewCardZoom))
	ch := int(math.Round(float64(r.H) * overviewCardZoom))
	rowH := int(math.Round(float64(u.H) * w.overviewZoom()))
	x0, y0 := u.X+u.W/100, y+(rowH-ch)/2
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
	_, span, _ := w.previewTiles()
	if span <= 0 {
		return overviewMaxZoom
	}
	room := float64(w.Usable.W-w.pileWidth()) * 0.96
	z := min(max(room/float64(span), overviewMinZoom), overviewMaxZoom)
	if len(w.stack()) > 1 {
		// Reserve two clear peeks on either side of the front row.
		z = min(z, overviewMaxZoom/(1+4*overviewPeekStep))
	}
	return z
}

// rowHeight includes the largest possible stack envelope (two peeks above
// and two below). Neighbor rows use the same measure as the current row.
func (w *Workspace) rowHeight() int {
	h := int(math.Round(float64(w.Usable.H) * w.overviewZoom()))
	if len(w.stack()) > 1 {
		step := max(1, int(math.Round(float64(h)*overviewPeekStep)))
		h += 4 * step
	}
	return h
}

// previewTiles lays tiles out unscrolled, relative to the usable area's
// corner. Fixed overflow uses its on-screen geometry; scroll overflow uses
// an unscrolled row. span is the row's width; sel is the focused column.
func (w *Workspace) previewTiles() (tiles []Placement, span int, sel Rect) {
	g := w.gap()
	h := max(w.Usable.H-2*g, 0)
	if w.pinned() {
		// A covering window nothing else is reachable under (a game) is
		// the row's only tile, at the size of a fullscreen column. It
		// stays fullscreen.
		r := Rect{X: g, Y: g, W: max(w.Usable.W-2*g, 0), H: h}
		return []Placement{{ID: w.cover(), Rect: r, Focused: true, Fullscreen: true}}, r.X + r.W + g, r
	}
	var rects []Rect
	if w.Overflow == OverflowFixed {
		rects = w.columnRects()
		span = w.Usable.W // Fixed overflow never scrolls, even with a stash pile.
	}
	maximized := w.Overflow == OverflowFixed && len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth
	for i, c := range w.Columns {
		r := Rect{X: w.columnX(i) - w.Usable.X, Y: g, W: w.columnWidth(i), H: h}
		if w.Overflow == OverflowFixed {
			r = rects[i]
			r.X, r.Y = r.X-w.Usable.X, r.Y-w.Usable.Y
		} else if w.fullscreenColumn(i) {
			// A fullscreen column shows at the width it had in the row.
			r.W = max(w.Usable.W-2*g, 0)
		}
		if maximized && i != w.Focus {
			// FullWidth hides other columns on screen. Their old buffers
			// cannot fit their current spiral rects; show only the maximized
			// column until navigation restores the normal layout.
			for _, id := range c.Windows {
				tiles = append(tiles, Placement{ID: id, Hidden: true})
			}
			continue
		}
		for j, t := range stackRects(r, len(c.Windows), g) {
			tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: i == w.Focus && j == c.Focus})
		}
		if w.Overflow != OverflowFixed {
			span = max(span, r.X+r.W+g)
		}
		if i == w.Focus {
			sel = r
		}
	}
	return tiles, span, sel
}

// previewRow scales the tiles of w into a row whose top is at y, centred
// in the usable width, or scrolled so the focused column shows when the
// row is wider, right of the stash pile. Dimmed rows (neighbor
// workspaces) have no focus; neither has a row whose pile has it (lit
// false).
func (w *Workspace) previewRow(y int, dim, lit bool) []Placement {
	tiles, span, sel := w.previewTiles()
	return w.previewRowTiles(y, dim, lit, tiles, span, sel)
}

func (w *Workspace) previewRowTiles(y int, dim, lit bool, tiles []Placement, span int, sel Rect) []Placement {
	u := w.Usable
	left := w.pileWidth()
	u.X, u.W = u.X+left, u.W-left
	z := w.overviewZoom()
	scale := func(v int) int { return int(math.Round(float64(v) * z)) }
	width := scale(span)
	x := u.X + (u.W-width)/2
	if width > u.W {
		x = u.X + u.W/2 - scale(sel.X+sel.W/2)
		x = min(max(x, u.X+u.W-width), u.X)
	}
	for i := range tiles {
		if tiles[i].Hidden {
			continue
		}
		r := tiles[i].Rect
		x0, y0 := scale(r.X), scale(r.Y)
		tiles[i].Rect = Rect{X: x + x0, Y: y + y0, W: scale(r.X+r.W) - x0, H: scale(r.Y+r.H) - y0}
		tiles[i].Preview, tiles[i].Peek = z, dim
		tiles[i].Focused = tiles[i].Focused && !dim && lit
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

// overviewAt is the preview under the output-local point, if any. Front
// cards take priority over peeks, including peeks appended after a front
// tile in another row's layout.
func (m *Monitor) overviewAt(x, y float64) WindowID {
	layout := m.Layout()
	for i := len(layout) - 1; i >= 0; i-- {
		p := layout[i]
		r := p.Rect
		if p.Preview > 0 && !p.Hidden && !p.Peek && x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H) {
			return p.ID
		}
	}
	for i := len(layout) - 1; i >= 0; i-- {
		p := layout[i]
		r := p.Rect
		if p.Preview > 0 && !p.Hidden && x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H) {
			return p.ID
		}
	}
	return 0
}

// overviewClick closes the overview on the preview under the pointer, on
// the output it shows on. picked is false when no preview is there.
func (c *Core) overviewClick(ctx context.Context) (picked bool, err error) {
	o, ok := c.layout().At(c.cursorX, c.cursorY)
	if !ok {
		return false, nil
	}
	i := c.screenIndex(o.Info.Name)
	sc := c.screens[i]
	if !sc.mon.ov.open {
		return false, nil
	}
	id := sc.mon.overviewAt(c.cursorX-float64(o.X), c.cursorY-float64(o.Y))
	if id == 0 {
		return false, nil
	}
	sc.mon.OverviewPick(id)
	c.focusScreen = i
	c.keyboard.takeBack()
	if err := c.workspaceVisible(ctx, true); err != nil {
		return true, err
	}
	return true, c.publish(ctx)
}

// overviewSwipe moves the selection like the keys: a workspace swipe
// must not enter the empty workspace below the last one.
func (m *Monitor) overviewSwipe(a Action) {
	switch a {
	case ActionFocusColumnLeft:
		m.OverviewMove(-1, 0)
	case ActionFocusColumnRight:
		m.OverviewMove(1, 0)
	case ActionFocusWorkspaceUp:
		m.OverviewMove(0, -1)
	case ActionFocusWorkspaceDown:
		m.OverviewMove(0, 1)
	}
}

// overviewScrollStep is the two-finger scroll distance, in libinput's
// pointer units, that moves the selection one column or workspace.
const overviewScrollStep = 60

// overviewScroll moves the overview selection with a scroll frame: two
// fingers step once per overviewScrollStep on the axis they move most
// along, a wheel once per notch (high-resolution wheels add up their
// fractions of a notch). Vertical steps select cards first, then rows;
// touchpad.natural-scroll flips both axes as libinput reports.
func (m *Monitor) overviewScroll(a ports.PointerAxis) (changed bool) {
	if a.Vertical.Stop || a.Horizontal.Stop {
		// Fingers lifted: the next scroll starts from zero.
		m.ov.scrollX, m.ov.scrollY = 0, 0
		return false
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
	add(&m.ov.scrollY, a.Vertical)
	add(&m.ov.scrollX, a.Horizontal)
	for {
		switch {
		case math.Abs(m.ov.scrollY) >= overviewScrollStep && math.Abs(m.ov.scrollY) >= math.Abs(m.ov.scrollX):
			m.OverviewMove(0, sign(m.ov.scrollY))
			m.ov.scrollY -= float64(sign(m.ov.scrollY)) * overviewScrollStep
			m.ov.scrollX = 0
		case math.Abs(m.ov.scrollX) >= overviewScrollStep:
			m.OverviewMove(sign(m.ov.scrollX), 0)
			m.ov.scrollX -= float64(sign(m.ov.scrollX)) * overviewScrollStep
			m.ov.scrollY = 0
		default:
			return changed
		}
		changed = true
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
