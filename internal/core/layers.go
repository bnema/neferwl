package core

import (
	"sort"

	"github.com/bnema/neferwl/internal/ports"
)

// arrangeLayers places exclusive surfaces first, then sorts the resulting scene by layer.
func arrangeLayers(outW, outH int, layers []ports.LayerSurface) (placed []ports.SceneLayer, usable ports.Rect) {
	full := ports.Rect{W: max(outW, 0), H: max(outH, 0)}
	usable = full
	for pass := 0; pass < 2; pass++ {
		for _, s := range layers {
			if (s.ExclusiveZone > 0) != (pass == 0) {
				continue
			}
			bounds := usable
			if s.ExclusiveZone == -1 {
				bounds = full
			}
			top, right, bottom, left := int(s.Margin[0]), int(s.Margin[1]), int(s.Margin[2]), int(s.Margin[3])
			w, h := s.Width, s.Height
			x, y := bounds.X+(bounds.W-w)/2, bounds.Y+(bounds.H-h)/2
			switch s.Anchor & (ports.AnchorLeft | ports.AnchorRight) {
			case ports.AnchorLeft | ports.AnchorRight:
				span := bounds.W - left - right
				if w == 0 {
					w = span
				}
				x = bounds.X + left + (span-w)/2
			case ports.AnchorLeft:
				x = bounds.X + left
			case ports.AnchorRight:
				x = bounds.X + bounds.W - w - right
			}
			switch s.Anchor & (ports.AnchorTop | ports.AnchorBottom) {
			case ports.AnchorTop | ports.AnchorBottom:
				span := bounds.H - top - bottom
				if h == 0 {
					h = span
				}
				y = bounds.Y + top + (span-h)/2
			case ports.AnchorTop:
				y = bounds.Y + top
			case ports.AnchorBottom:
				y = bounds.Y + bounds.H - h - bottom
			}
			placed = append(placed, ports.SceneLayer{ID: s.ID, Layer: s.Layer, Rect: ports.Rect{X: x, Y: y, W: w, H: h}})
			if s.ExclusiveZone <= 0 {
				continue
			}
			a := s.Anchor
			z := int(s.ExclusiveZone)
			switch {
			case a&ports.AnchorTop != 0 && a&ports.AnchorBottom == 0 && (a&(ports.AnchorLeft|ports.AnchorRight) == 0 || a&(ports.AnchorLeft|ports.AnchorRight) == ports.AnchorLeft|ports.AnchorRight):
				z = min(max(z+top, 0), usable.H)
				usable.Y += z
				usable.H -= z
			case a&ports.AnchorBottom != 0 && a&ports.AnchorTop == 0 && (a&(ports.AnchorLeft|ports.AnchorRight) == 0 || a&(ports.AnchorLeft|ports.AnchorRight) == ports.AnchorLeft|ports.AnchorRight):
				z = min(max(z+bottom, 0), usable.H)
				usable.H -= z
			case a&ports.AnchorLeft != 0 && a&ports.AnchorRight == 0 && (a&(ports.AnchorTop|ports.AnchorBottom) == 0 || a&(ports.AnchorTop|ports.AnchorBottom) == ports.AnchorTop|ports.AnchorBottom):
				z = min(max(z+left, 0), usable.W)
				usable.X += z
				usable.W -= z
			case a&ports.AnchorRight != 0 && a&ports.AnchorLeft == 0 && (a&(ports.AnchorTop|ports.AnchorBottom) == 0 || a&(ports.AnchorTop|ports.AnchorBottom) == ports.AnchorTop|ports.AnchorBottom):
				z = min(max(z+right, 0), usable.W)
				usable.W -= z
			}
		}
	}
	sort.SliceStable(placed, func(i, j int) bool { return placed[i].Layer < placed[j].Layer })
	return
}

// layerOf finds a placed layer surface by ID.
func (c *Core) layerOf(id WindowID) (*screen, ports.SceneLayer, bool) {
	for _, sc := range c.screens {
		for _, l := range sc.placed {
			if l.ID == id {
				return sc, l, true
			}
		}
	}
	return nil, ports.SceneLayer{}, false
}

// hasFullscreen reports whether a visible fullscreen window covers the screen.
func hasFullscreen(sc *screen) bool {
	for _, p := range sc.mon.Layout() {
		if p.Fullscreen && !p.Hidden {
			return true
		}
	}
	return false
}

// shown reports whether a layer is drawn: a fullscreen window hides the
// bottom and top layers, as the renderer does.
func shown(sc *screen, layer ports.Layer) bool {
	return !(hasFullscreen(sc) && (layer == ports.LayerBottom || layer == ports.LayerTop))
}

// layerAt returns the topmost shown layer surface under the output-local
// point among those above (or below) the windows.
func layerAt(sc *screen, lx, ly float64, above bool) (WindowID, float64, float64) {
	full := hasFullscreen(sc)
	for i := len(sc.placed) - 1; i >= 0; i-- {
		l := sc.placed[i]
		r := l.Rect
		if (l.Layer >= ports.LayerTop) != above || (full && (l.Layer == ports.LayerBottom || l.Layer == ports.LayerTop)) || r.W <= 0 || r.H <= 0 {
			continue
		}
		if lx >= float64(r.X) && lx < float64(r.X+r.W) && ly >= float64(r.Y) && ly < float64(r.Y+r.H) {
			return l.ID, lx - float64(r.X), ly - float64(r.Y)
		}
	}
	return 0, 0, 0
}

// onDemand reports whether id is a mapped layer surface with on-demand
// keyboard interactivity.
func (c *Core) onDemand(id WindowID) bool {
	for _, sc := range c.screens {
		for _, l := range sc.layers {
			if l.ID == id {
				return l.Keyboard == 2
			}
		}
	}
	return false
}
