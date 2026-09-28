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

// overviewMaxZoom caps the current row so its neighbors show above and
// below; overviewMinZoom floors the shrink of wide workspaces, which then
// scroll to keep the selected column in view.
const overviewMaxZoom, overviewMinZoom = 0.6, 0.25

// ToggleOverview opens the overview, or closes it on the selection. It
// does not open under a covering fullscreen window (a game in scanout).
func (m *Monitor) ToggleOverview() {
	if m.overview {
		m.overview, m.overviewFrom = false, nil
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
}

// CancelOverview closes the overview and returns to the workspace and
// window it opened on, if they are still there.
func (m *Monitor) CancelOverview() {
	from, id := m.overviewFrom, m.overviewFromID
	m.overview, m.overviewFrom = false, nil
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
// would open a terminal there.
func (m *Monitor) OverviewMove(dx, dy int) {
	if dy != 0 {
		i := m.Active + dy
		if m.shown != nil || i < 0 || i >= len(m.Workspaces) || m.Workspaces[i].empty() {
			return
		}
		m.Focus(i)
		return
	}
	w := m.Current()
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
	w.FocusID(id)
	m.overview, m.overviewFrom = false, nil
}

// overviewLayout places the current workspace's row in the middle of the
// usable area and its numbered neighbors above and below, dimmed. Other
// workspaces, stashes and native floats are hidden.
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
			result = append(result, w.previewRow(ry, w != cur)...)
		}
		for _, id := range w.windows() {
			if !shown || w.isFloat(id) {
				result = append(result, Placement{ID: id, Hidden: true})
			}
		}
	}
	return result
}

// overviewZoom is the scale that fits every column of w in the usable
// width, within the overview bounds.
func (w *Workspace) overviewZoom() float64 {
	_, span, _ := w.previewTiles()
	if span <= 0 {
		return overviewMaxZoom
	}
	room := float64(w.Usable.W) * 0.96
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
// row is wider. Dimmed rows (neighbor workspaces) have no focus.
func (w *Workspace) previewRow(y int, dim bool) []Placement {
	tiles, span, sel := w.previewTiles()
	u := w.Usable
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
		tiles[i].Focused = tiles[i].Focused && !dim
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
