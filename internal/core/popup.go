package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// popupState is a placed xdg_popup. Rect is relative to the parent's
// window geometry, as sent to the client.
type popupState struct {
	id, parent WindowID
	rect       Rect
	grab       bool
	mapped     bool
}

// place computes a popup rectangle relative to its parent, kept inside
// bounds (also parent-relative) as the positioner's adjustments allow.
func place(p ports.Positioner, bounds Rect) Rect {
	r := initial(p, p.Anchor, p.Gravity)
	if bounds.W <= 0 || bounds.H <= 0 {
		return r
	}
	outX := func(r Rect) bool { return r.X < bounds.X || r.X+r.W > bounds.X+bounds.W }
	outY := func(r Rect) bool { return r.Y < bounds.Y || r.Y+r.H > bounds.Y+bounds.H }
	if outX(r) && p.Adjust&ports.AdjustFlipX != 0 {
		if f := initial(p, flipX(p.Anchor), flipX(p.Gravity)); !outX(f) {
			r.X = f.X
		}
	}
	if outY(r) && p.Adjust&ports.AdjustFlipY != 0 {
		if f := initial(p, flipY(p.Anchor), flipY(p.Gravity)); !outY(f) {
			r.Y = f.Y
		}
	}
	if outX(r) && p.Adjust&ports.AdjustSlideX != 0 {
		r.X = max(min(r.X, bounds.X+bounds.W-r.W), bounds.X)
	}
	if outY(r) && p.Adjust&ports.AdjustSlideY != 0 {
		r.Y = max(min(r.Y, bounds.Y+bounds.H-r.H), bounds.Y)
	}
	if outX(r) && p.Adjust&ports.AdjustResizeX != 0 {
		x0, x1 := max(r.X, bounds.X), min(r.X+r.W, bounds.X+bounds.W)
		if x1 > x0 {
			r.X, r.W = x0, x1-x0
		}
	}
	if outY(r) && p.Adjust&ports.AdjustResizeY != 0 {
		y0, y1 := max(r.Y, bounds.Y), min(r.Y+r.H, bounds.Y+bounds.H)
		if y1 > y0 {
			r.Y, r.H = y0, y1-y0
		}
	}
	return r
}

// initial is the unconstrained placement for an anchor and gravity.
func initial(p ports.Positioner, anchor, gravity uint32) Rect {
	a := p.AnchorRect
	x, y := a.X+a.W/2, a.Y+a.H/2
	switch anchor {
	case ports.EdgeLeft, ports.EdgeTopLeft, ports.EdgeBottomLeft:
		x = a.X
	case ports.EdgeRight, ports.EdgeTopRight, ports.EdgeBottomRight:
		x = a.X + a.W
	}
	switch anchor {
	case ports.EdgeTop, ports.EdgeTopLeft, ports.EdgeTopRight:
		y = a.Y
	case ports.EdgeBottom, ports.EdgeBottomLeft, ports.EdgeBottomRight:
		y = a.Y + a.H
	}
	x, y = x+p.OffsetX, y+p.OffsetY
	r := Rect{X: x - p.Width/2, Y: y - p.Height/2, W: p.Width, H: p.Height}
	switch gravity {
	case ports.EdgeLeft, ports.EdgeTopLeft, ports.EdgeBottomLeft:
		r.X = x - p.Width
	case ports.EdgeRight, ports.EdgeTopRight, ports.EdgeBottomRight:
		r.X = x
	}
	switch gravity {
	case ports.EdgeTop, ports.EdgeTopLeft, ports.EdgeTopRight:
		r.Y = y - p.Height
	case ports.EdgeBottom, ports.EdgeBottomLeft, ports.EdgeBottomRight:
		r.Y = y
	}
	return r
}

func flipX(e uint32) uint32 {
	switch e {
	case ports.EdgeLeft:
		return ports.EdgeRight
	case ports.EdgeRight:
		return ports.EdgeLeft
	case ports.EdgeTopLeft:
		return ports.EdgeTopRight
	case ports.EdgeTopRight:
		return ports.EdgeTopLeft
	case ports.EdgeBottomLeft:
		return ports.EdgeBottomRight
	case ports.EdgeBottomRight:
		return ports.EdgeBottomLeft
	}
	return e
}

func flipY(e uint32) uint32 {
	switch e {
	case ports.EdgeTop:
		return ports.EdgeBottom
	case ports.EdgeBottom:
		return ports.EdgeTop
	case ports.EdgeTopLeft:
		return ports.EdgeBottomLeft
	case ports.EdgeBottomLeft:
		return ports.EdgeTopLeft
	case ports.EdgeTopRight:
		return ports.EdgeBottomRight
	case ports.EdgeBottomRight:
		return ports.EdgeTopRight
	}
	return e
}

// windowRect is where a window, layer surface or popup is on its screen: its client
// area, output-local and logical. ok is false when it is not on screen.
func (c *Core) windowRect(id WindowID) (*screen, Rect, bool) {
	if p := c.popups[id]; p != nil {
		sc, pr, ok := c.windowRect(p.parent)
		// A peeking stashed window shows no popups: they would cover the
		// selected one.
		if !ok || c.peeking(p.parent) {
			return nil, Rect{}, false
		}
		return sc, Rect{X: pr.X + p.rect.X, Y: pr.Y + p.rect.Y, W: p.rect.W, H: p.rect.H}, true
	}
	if sc, l, ok := c.layerOf(id); ok {
		if !shown(sc, l) {
			return nil, Rect{}, false
		}
		return sc, l.Rect, true
	}
	for _, sc := range c.screens {
		for _, pl := range sc.mon.Layout() {
			if pl.ID == id {
				// Hidden or scrolled off: not on screen, like its popups.
				// A preview takes no input and shows no popups.
				if !onScreen(pl, sc.mon.Output()) || pl.Preview > 0 {
					return nil, Rect{}, false
				}
				return sc, c.clientRect(pl), true
			}
		}
	}
	return nil, Rect{}, false
}

// peeking reports whether id is a stashed window peeking in on screen.
func (c *Core) peeking(id WindowID) bool {
	for _, sc := range c.screens {
		for _, pl := range sc.mon.Layout() {
			if pl.ID == id {
				return pl.Peek
			}
		}
	}
	return false
}

// placePopup places a new or repositioned popup and configures it; a popup
// whose parent is not on screen is dismissed.
func (c *Core) placePopup(ctx context.Context, v ports.PopupRequest) error {
	sc, pr, ok := c.windowRect(v.Parent)
	if !ok {
		if c.popups[v.ID] != nil {
			return c.closePopup(ctx, v.ID)
		}
		return c.command(ctx, ports.ClosePopup{ID: v.ID})
	}
	o := sc.mon.Output()
	bounds := Rect{X: o.X - pr.X, Y: o.Y - pr.Y, W: o.W, H: o.H}
	p := c.popups[v.ID]
	if p == nil {
		p = &popupState{id: v.ID, parent: v.Parent}
		c.popups[v.ID] = p
		c.popupOrder = append(c.popupOrder, v.ID)
	}
	p.grab = v.Grab
	p.rect = place(v.Positioner, bounds)
	return c.command(ctx, ports.ConfigurePopup{ID: v.ID, Rect: p.rect, Reposition: v.Reposition, Token: v.Token})
}

// closePopup dismisses a popup after its children, topmost first as
// xdg-shell requires, and forgets them.
func (c *Core) closePopup(ctx context.Context, id WindowID) error {
	if err := c.closePopupsOf(ctx, id); err != nil {
		return err
	}
	if err := c.command(ctx, ports.ClosePopup{ID: id}); err != nil {
		return err
	}
	c.forgetPopup(id)
	return nil
}

// closePopupsOf dismisses the popups hanging from a window or popup,
// newest first.
func (c *Core) closePopupsOf(ctx context.Context, id WindowID) error {
	for _, child := range slices.Backward(slices.Clone(c.popupOrder)) {
		if p := c.popups[child]; p != nil && p.parent == id {
			if err := c.closePopup(ctx, child); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropPopup forgets a popup the client unmapped and dismisses its children.
func (c *Core) dropPopup(ctx context.Context, id WindowID) error {
	if c.popups[id] == nil {
		return nil
	}
	if err := c.closePopupsOf(ctx, id); err != nil {
		return err
	}
	c.forgetPopup(id)
	return nil
}

func (c *Core) forgetPopup(id WindowID) {
	delete(c.popups, id)
	c.popupOrder = slices.DeleteFunc(c.popupOrder, func(v WindowID) bool { return v == id })
	if c.pointer == id {
		c.pointer = 0
	}
}

// closeHiddenPopups dismisses popups whose window left the screen.
func (c *Core) closeHiddenPopups(ctx context.Context) error {
	for _, id := range slices.Backward(slices.Clone(c.popupOrder)) {
		if c.popups[id] == nil {
			continue
		}
		if _, _, ok := c.windowRect(id); !ok {
			if err := c.closePopup(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// dismissGrabs closes the grabbing popups unless the click hit one of
// them: a click outside a menu closes it.
func (c *Core) dismissGrabs(ctx context.Context, hit WindowID) error {
	for _, id := range slices.Backward(slices.Clone(c.popupOrder)) {
		p := c.popups[id]
		if p == nil || !p.grab {
			continue
		}
		// A click on a popup of the same chain keeps it; a click on
		// anything else, its parent window included, closes it.
		if c.popups[hit] != nil && (hit == id || c.isAncestor(id, hit) || c.isAncestor(hit, id)) {
			continue
		}
		if err := c.closePopup(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// isAncestor reports whether a is a popup ancestor of b.
func (c *Core) isAncestor(a, b WindowID) bool {
	for p := c.popups[b]; p != nil; p = c.popups[p.parent] {
		if p.parent == a {
			return true
		}
	}
	return false
}

// grabFocus is the topmost mapped grabbing popup, which takes the keyboard.
func (c *Core) grabFocus() WindowID {
	for i := len(c.popupOrder) - 1; i >= 0; i-- {
		// A popup hidden with its parent (under a fullscreen window) has
		// no keyboard.
		if p := c.popups[c.popupOrder[i]]; p != nil && p.grab && p.mapped && c.visible(p.id) {
			return p.id
		}
	}
	return 0
}

// popupAt returns the topmost mapped popup under the output-local point,
// among layer popups (overLayers) or window popups.
func (c *Core) popupAt(sc *screen, lx, ly float64, overLayers bool) (WindowID, float64, float64) {
	for i := len(c.popupOrder) - 1; i >= 0; i-- {
		p := c.popups[c.popupOrder[i]]
		if p == nil || !p.mapped || c.onLayer(c.popupRoot(p.id)) != overLayers {
			continue
		}
		s, r, ok := c.windowRect(p.id)
		if !ok || s != sc {
			continue
		}
		if lx >= float64(r.X) && lx < float64(r.X+r.W) && ly >= float64(r.Y) && ly < float64(r.Y+r.H) && c.acceptsInput(p.id, lx-float64(r.X), ly-float64(r.Y)) {
			return p.id, lx - float64(r.X), ly - float64(r.Y)
		}
	}
	return 0, 0, 0
}

// popupRoot is the window or layer a popup chain hangs from; id itself
// when it is not a popup.
func (c *Core) popupRoot(id WindowID) WindowID {
	for p := c.popups[id]; p != nil; p = c.popups[id] {
		id = p.parent
	}
	return id
}

// onLayer reports whether id is a mapped layer surface.
func (c *Core) onLayer(id WindowID) bool {
	for _, sc := range c.screens {
		for _, l := range sc.layers {
			if l.ID == id {
				return true
			}
		}
	}
	return false
}

// scenePopups are the mapped popups on a screen, bottom to top.
func (c *Core) scenePopups(sc *screen) []ports.SceneWindow {
	var out []ports.SceneWindow
	for _, id := range c.popupOrder {
		p := c.popups[id]
		if p == nil || !p.mapped {
			continue
		}
		if s, r, ok := c.windowRect(id); ok && s == sc {
			out = append(out, ports.SceneWindow{ID: id, Rect: r, Popup: true, OverLayers: c.onLayer(c.popupRoot(id))})
		}
	}
	return out
}
