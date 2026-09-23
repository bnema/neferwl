package core

import (
	"sort"

	"github.com/bnema/nefertty/internal/ports"
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
			x, y := bounds.X+(bounds.W-s.Width)/2, bounds.Y+(bounds.H-s.Height)/2
			if s.Anchor&ports.AnchorLeft != 0 {
				x = bounds.X + left
			} else if s.Anchor&ports.AnchorRight != 0 {
				x = bounds.X + bounds.W - s.Width - right
			}
			if s.Anchor&ports.AnchorTop != 0 {
				y = bounds.Y + top
			} else if s.Anchor&ports.AnchorBottom != 0 {
				y = bounds.Y + bounds.H - s.Height - bottom
			}
			placed = append(placed, ports.SceneLayer{ID: s.ID, Layer: s.Layer, Rect: ports.Rect{X: x, Y: y, W: s.Width, H: s.Height}})
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
