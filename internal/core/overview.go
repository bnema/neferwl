package core

import (
	"context"
	"math"

	"github.com/bnema/neferwl/internal/ports"
)

// The overview scales each workspace row without resizing clients. Covering
// native floats and the columns form a stack of cards in screen order; small
// floats remain hidden. A pinned fullscreen window remains the only preview.
// Left/right navigate the front item and rotate its stack at the edges;
// up/down change rows. Return commits the selection, Escape restores it.

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

// ToggleOverview opens the overview, or closes it on the selection. Over
// a covering fullscreen window (a game) too: its row shows it, composed
// rather than scanned out while the overview is open.
func (m *Monitor) ToggleOverview() {
	if m.overview {
		m.closeOverview()
		return
	}
	w := m.Current()
	// A slide in progress lands at once: the overview lays out the
	// settled state.
	m.stopSwitch()
	m.each(func(w *Workspace) { w.stopSlide() })
	m.overview, m.overviewFrom = true, w
	m.overviewFromID, _ = w.Focused()
	m.overviewFloats = append([]Float(nil), w.Floats...)
	m.scrollX, m.scrollY = 0, 0
	m.overviewOpens++
	m.selectRow()
}

// selectRow resets provisional stack and stash selection for a new row.
func (m *Monitor) selectRow() {
	m.overviewCard, m.overviewCardOf = 0, nil
	m.overviewStack, m.overviewStackOf = 0, nil
	w := m.Current()
	if w.pinned() {
		// The covering window is the row's only preview: it keeps the focus.
		return
	}
	// A float's focus moves to the selected column for keyboard navigation,
	// without calling FocusID (which would change the real stack order).
	if w.floatFocus && len(w.Columns) > 0 && (len(w.stackItems()) < 2 || m.stackFront(w) == 0) {
		w.floatFocus = false
	}
	if len(w.Stash) > 0 && (len(w.Columns) == 0 || w.stashFocused() && !w.floatFocus) {
		m.selectCard(w, w.stashAt)
	}
}

// selectCard selects stash entry i of w.
func (m *Monitor) selectCard(w *Workspace, i int) {
	m.overviewCard, m.overviewCardOf = w.Stash[i].ID, w
}

// card is the selected stash card, 0 when there is none: the overview is
// closed, the selection is on a column, or its window left the stash on
// screen (unmapped, unstashed, or another workspace shown by a bind).
func (m *Monitor) card() WindowID {
	w := m.overviewCardOf
	if !m.overview || w == nil || w != m.Current() || w.stashIndex(m.overviewCard) < 0 {
		return 0
	}
	return m.overviewCard
}

// cardAt is the pile entry of w that is selected, -1 when none.
func (m *Monitor) cardAt(w *Workspace) int {
	if id := m.card(); id != 0 && w == m.overviewCardOf {
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
	} else {
		if id := m.stackFront(w); id != 0 && len(w.stackItems()) > 1 {
			w.FocusID(id)
		} else if len(w.Columns) > 0 && !w.pinned() {
			c := w.Columns[w.Focus]
			w.FocusID(c.Windows[c.Focus])
		}
	}
	m.overview, m.overviewFrom = false, nil
	m.overviewCard, m.overviewCardOf = 0, nil
	m.overviewStack, m.overviewStackOf = 0, nil
	m.overviewFloats = nil
}

// CancelOverview closes the overview and returns to the workspace and
// window it opened on, if they are still there.
func (m *Monitor) CancelOverview() {
	from, id := m.overviewFrom, m.overviewFromID
	m.overview, m.overviewFrom = false, nil
	m.overviewCard, m.overviewCardOf = 0, nil
	m.overviewStack, m.overviewStackOf = 0, nil
	if from != nil && m.has(from) {
		m.show(from)
		if id != 0 {
			from.FocusID(id)
		} else {
			from.floatFocus, from.stashFocus = false, false
		}
		// FocusID on a float raises it; restore the initial order and below
		// flags for floats still present, leaving new floats in their order.
		initial := m.overviewFloats
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
	}
	m.overviewFloats = nil
}

// OverviewMove selects a column, stash card, or covering-float card (dx),
// or a numbered workspace (dy). It stops before empty workspaces. Left
// of the first column enters the stash; at the right edge the stack rotates
// provisionally. Right past the stash's last card returns to the columns.
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
	if w.pinned() {
		return
	}
	if at := m.cardAt(w); at >= 0 {
		switch {
		case at+dx >= 0 && at+dx < len(w.Stash):
			m.selectCard(w, at+dx)
		case dx > 0 && len(w.Columns) > 0:
			m.overviewCard, m.overviewCardOf = 0, nil
			// The columns come to the front of the stack.
			m.overviewStackOf, m.overviewStack = w, 0
			w.selectOverviewColumn(0)
		}
		return
	}
	front := m.stackFront(w)
	if front != 0 && len(w.stackItems()) > 1 {
		if dx < 0 && len(w.Stash) > 0 {
			m.selectCard(w, w.stashAt)
		} else {
			m.rotateStack(w, dx)
		}
		return
	}
	if dx < 0 && (len(w.Columns) == 0 || w.Focus == 0) && len(w.Stash) > 0 {
		m.selectCard(w, w.stashAt)
		return
	}
	if len(w.Columns) == 0 {
		return
	}
	if dx > 0 && w.Focus == len(w.Columns)-1 && len(w.stackItems()) > 1 {
		m.rotateStack(w, dx)
		return
	}
	w.selectOverviewColumn(min(max(w.Focus+dx, 0), len(w.Columns)-1))
}

// OverviewPick closes the overview on the clicked window.
func (m *Monitor) OverviewPick(id WindowID) {
	w, _ := m.find(id)
	if w == nil {
		return
	}
	m.show(w)
	m.overviewCard, m.overviewCardOf = 0, nil
	if i := w.stashIndex(id); i >= 0 {
		m.selectCard(w, i)
	} else if i := w.floatIndex(id); i >= 0 && w.coversFloat(w.Floats[i]) && !w.pinned() {
		m.overviewStackOf, m.overviewStack = w, id
	} else if w.pinned() {
		w.FocusID(id)
	} else if w.floatIndex(id) >= 0 {
		// A non-covering float is never a preview to pick.
		return
	} else {
		m.overviewStackOf, m.overviewStack = w, 0
		for i, c := range w.Columns {
			for j, v := range c.Windows {
				if v == id {
					w.selectOverviewColumn(i)
					w.Columns[i].Focus = j
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
			if !shown || len(w.stackItems()) < 2 || !w.coversFloat(f) {
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
	shown := min(n, overviewCards)
	out := make([]Placement, 0, n)
	// Farthest card first: the front one is drawn and hit on top.
	for k := shown - 1; k >= 0; k-- {
		i := (front + k) % n
		step := k * w.cardStep()
		out = append(out, Placement{ID: w.Stash[i].ID, Rect: Rect{X: x0 + step, Y: y0 - step, W: cw, H: ch}, Preview: overviewCardZoom, Peek: dim || k > 0, Focused: k == 0 && selected})
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
	if w.pinned() {
		// A covering window nothing else is reachable under (a game) is
		// the row's only tile, at the size of a fullscreen column. It
		// stays fullscreen.
		r := Rect{X: g, Y: g, W: max(w.Usable.W-2*g, 0), H: h}
		return []Placement{{ID: w.cover(), Rect: r, Focused: true, Fullscreen: true}}, r.X + r.W + g, r
	}
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
	if !sc.mon.overview {
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
// fractions of a notch). Scrolling down or right selects the workspace
// below or the column on the right; touchpad.natural-scroll flips both,
// as libinput reports. changed is false while the distance adds up.
func (m *Monitor) overviewScroll(a ports.PointerAxis) (changed bool) {
	if a.Vertical.Stop || a.Horizontal.Stop {
		// Fingers lifted: the next scroll starts from zero.
		m.scrollX, m.scrollY = 0, 0
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
	add(&m.scrollY, a.Vertical)
	add(&m.scrollX, a.Horizontal)
	for {
		switch {
		case math.Abs(m.scrollY) >= overviewScrollStep && math.Abs(m.scrollY) >= math.Abs(m.scrollX):
			m.OverviewMove(0, sign(m.scrollY))
			m.scrollY -= float64(sign(m.scrollY)) * overviewScrollStep
			m.scrollX = 0
		case math.Abs(m.scrollX) >= overviewScrollStep:
			m.OverviewMove(sign(m.scrollX), 0)
			m.scrollX -= float64(sign(m.scrollX)) * overviewScrollStep
			m.scrollY = 0
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
