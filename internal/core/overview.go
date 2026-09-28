package core

import (
	"context"
	"math"

	"github.com/bnema/neferwl/internal/ports"
)

// The overview shows every column of the workspace on screen in one row,
// each tile scaled by the same factor so windows keep their shape, with
// the workspaces above and below it dimmed. The previews are the windows'
// last buffers: clients are not resized. h/l (or left/right) select a
// column, j/k (or down/up) a workspace, Return keeps the selection and
// Escape returns to where the overview opened.

// A workspace's stash shows as a pile of cards pinned to the left edge of
// its row: the selected stashed window in front at overviewCardZoom, up to
// overviewCards-1 others behind it, each overviewCardStep further up and
// right, dimmed. The row takes the width the pile leaves.
const (
	overviewCardZoom = 0.2
	overviewCards    = 4
	overviewCardStep = 8
)

// overviewMaxZoom caps the current row so its neighbors show above and
// below; overviewMinZoom floors the shrink of wide workspaces, which then
// scroll to keep the selected column in view.
const overviewMaxZoom, overviewMinZoom = 0.6, 0.25

// ToggleOverview opens the overview, or closes it on the selection. It
// does not open under a covering fullscreen window (a game in scanout).
func (m *Monitor) ToggleOverview() {
	if m.overview {
		m.closeOverview()
		return
	}
	w := m.Current()
	if w.cover() != 0 {
		return
	}
	// A slide in progress lands at once: the overview lays out the
	// settled state.
	m.stopSwitch()
	m.each(func(w *Workspace) { w.stopSlide() })
	m.overview, m.overviewFrom = true, w
	m.overviewFromID, _ = w.Focused()
	m.selectRow()
}

// selectRow puts the selection in the pile when the current workspace
// has only stashed windows, else on its focused column.
func (m *Monitor) selectRow() {
	w := m.Current()
	m.overviewPile = len(w.Columns) == 0 && len(w.Stash) > 0
	m.overviewPileAt = w.stashAt
}

// pileAt is the selected pile entry of w, -1 when the selection is not in
// its pile. It follows stash changes made while the overview is open.
func (m *Monitor) pileAt(w *Workspace) int {
	if !m.overviewPile || w != m.Current() || len(w.Stash) == 0 {
		return -1
	}
	return min(max(m.overviewPileAt, 0), len(w.Stash)-1)
}

// closeOverview closes the overview on the selection: a pile entry shows
// the stash on it.
func (m *Monitor) closeOverview() {
	w := m.Current()
	if i := m.pileAt(w); i >= 0 {
		w.stashAt = i
		w.showStash()
	}
	m.overview, m.overviewFrom, m.overviewPile = false, nil, false
}

// CancelOverview closes the overview and returns to the workspace and
// window it opened on, if they are still there.
func (m *Monitor) CancelOverview() {
	from, id := m.overviewFrom, m.overviewFromID
	m.overview, m.overviewFrom, m.overviewPile = false, nil, false
	if from == nil || !m.has(from) {
		return
	}
	m.show(from)
	if id != 0 {
		from.FocusID(id)
	}
}

// OverviewMove selects the neighbor column (dx) or numbered workspace
// (dy). It stops at the ends and never enters an empty workspace, which
// would open a terminal there. Left of the first column is the stash
// pile; right of its last entry, the first column again.
func (m *Monitor) OverviewMove(dx, dy int) {
	if dy != 0 {
		i := m.Active + dy
		if m.shown != nil || i < 0 || i >= len(m.Workspaces) || m.Workspaces[i].empty() {
			return
		}
		m.Focus(i)
		m.selectRow()
		return
	}
	w := m.Current()
	if at := m.pileAt(w); at >= 0 {
		switch {
		case at+dx >= 0 && at+dx < len(w.Stash):
			m.overviewPileAt = at + dx
		case dx > 0 && len(w.Columns) > 0:
			m.overviewPile = false
			w.FocusID(w.Columns[0].Windows[w.Columns[0].Focus])
		}
		return
	}
	if dx < 0 && (len(w.Columns) == 0 || w.Focus == 0) && len(w.Stash) > 0 {
		m.overviewPile, m.overviewPileAt = true, w.stashAt
		return
	}
	if len(w.Columns) == 0 {
		return
	}
	c := w.Columns[min(max(w.Focus+dx, 0), len(w.Columns)-1)]
	w.FocusID(c.Windows[c.Focus])
}

// OverviewPick closes the overview on the clicked window.
func (m *Monitor) OverviewPick(id WindowID) {
	w, _ := m.find(id)
	if w == nil {
		return
	}
	m.show(w)
	if i := w.stashIndex(id); i >= 0 {
		m.overviewPile, m.overviewPileAt = true, i
	} else {
		m.overviewPile = false
		w.FocusID(id)
	}
	m.closeOverview()
}

// overviewLayout places the current workspace's row in the middle of the
// usable area and its numbered neighbors above and below, dimmed, each
// with its stash pile. Other workspaces and native floats are hidden.
func (m *Monitor) overviewLayout() []Placement {
	cur := m.Current()
	u := cur.Usable
	gap := u.H * 3 / 100
	var result []Placement
	zc := cur.overviewZoom()
	hc := int(math.Round(float64(u.H) * zc))
	y := u.Y + (u.H-hc)/2
	rows := map[*Workspace]int{}
	rows[cur] = y
	if i := indexOf(m.Workspaces, cur); i >= 0 && m.shown == nil {
		if i > 0 {
			up := m.Workspaces[i-1]
			rows[up] = y - gap - int(math.Round(float64(u.H)*up.overviewZoom()))
		}
		if i+1 < len(m.Workspaces) {
			rows[m.Workspaces[i+1]] = y + hc + gap
		}
	}
	for _, w := range m.all() {
		ry, shown := rows[w]
		if shown {
			at := m.pileAt(w)
			result = append(result, w.previewRow(ry, w != cur, at < 0)...)
			result = append(result, w.pile(ry, w != cur, at)...)
		}
		for _, f := range w.Floats {
			result = append(result, Placement{ID: f.ID, Hidden: true})
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

// pileWidth is the room the stash pile of w takes on the left of its row,
// its margin included; 0 without a stash.
func (w *Workspace) pileWidth() int {
	if len(w.Stash) == 0 {
		return 0
	}
	card := int(math.Round(float64(w.stashRect().W) * overviewCardZoom))
	return card + (min(len(w.Stash), overviewCards)-1)*overviewCardStep + w.Usable.W*2/100
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
	shown := min(n, overviewCards)
	out := make([]Placement, 0, n)
	// Farthest card first: the front one is drawn and hit on top.
	for k := shown - 1; k >= 0; k-- {
		i := (front + k) % n
		out = append(out, Placement{ID: w.Stash[i].ID, Rect: Rect{X: x0 + k*overviewCardStep, Y: y0 - k*overviewCardStep, W: cw, H: ch}, Preview: overviewCardZoom, Peek: dim || k > 0, Focused: k == 0 && selected})
	}
	for k := shown; k < n; k++ {
		out = append(out, Placement{ID: w.Stash[(front+k)%n].ID, Hidden: true})
	}
	return out
}

// overviewZoom is the scale that fits every column of w in the usable
// width, within the overview bounds.
func (w *Workspace) overviewZoom() float64 {
	_, span, _ := w.previewTiles()
	if span <= 0 {
		return overviewMaxZoom
	}
	room := float64(w.Usable.W-w.pileWidth()) * 0.96
	return min(max(room/float64(span), overviewMinZoom), overviewMaxZoom)
}

// previewTiles lays the tiles of w out unscrolled, relative to the
// usable area's corner: every column side by side at its width, as if
// the output were wide enough (fixed overflow's spiral included). span is
// the row's width; sel is the focused column.
func (w *Workspace) previewTiles() (tiles []Placement, span int, sel Rect) {
	g := w.gap()
	h := max(w.Usable.H-2*g, 0)
	for i, c := range w.Columns {
		r := Rect{X: w.columnX(i) - w.Usable.X, Y: g, W: w.columnWidth(i), H: h}
		if w.fullscreenColumn(i) {
			// A fullscreen column shows at the width it had in the row.
			r.W = max(w.Usable.W-2*g, 0)
		}
		for j, t := range stackRects(r, len(c.Windows), g) {
			tiles = append(tiles, Placement{ID: c.Windows[j], Rect: t, Focused: i == w.Focus && j == c.Focus})
		}
		span = max(span, r.X+r.W+g)
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

// overviewAt is the preview under the output-local point, if any.
func (m *Monitor) overviewAt(x, y float64) WindowID {
	layout := m.Layout()
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
	if !sc.mon.overview {
		return false, nil
	}
	id := sc.mon.overviewAt(c.cursorX-float64(o.X), c.cursorY-float64(o.Y))
	if id == 0 {
		return false, nil
	}
	sc.mon.OverviewPick(id)
	c.focusScreen, c.layerFocus = i, 0
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
