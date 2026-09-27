package wayland

import (
	"slices"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// intersectRect returns the overlap of two half-open rectangles.
func intersectRect(a, b ports.Rect) ports.Rect {
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	x2, y2 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x2 <= x || y2 <= y {
		return ports.Rect{}
	}
	return ports.Rect{X: x, Y: y, W: x2 - x, H: y2 - y}
}

func (g *region) boxRect() ports.Rect {
	if len(g.rects) == 0 {
		return ports.Rect{}
	}
	x, y := g.rects[0].X, g.rects[0].Y
	x2, y2 := x+g.rects[0].W, y+g.rects[0].H
	for _, r := range g.rects[1:] {
		x = min(x, r.X)
		y = min(y, r.Y)
		x2 = max(x2, r.X+r.W)
		y2 = max(y2, r.Y+r.H)
	}
	return ports.Rect{X: x, Y: y, W: x2 - x, H: y2 - y}
}

// effectiveInput includes all committed, bounded subsurface regions.
func (s *surface) effectiveInput() (bool, []ports.Rect) {
	var rects []ports.Rect
	var visit func(*surface, int, int)
	visit = func(v *surface, x, y int) {
		if v.has {
			bounds := ports.Rect{W: v.content.LogicalW, H: v.content.LogicalH}
			if v.inputAll {
				if bounds.W > 0 && bounds.H > 0 {
					rects = append(rects, ports.Rect{X: x, Y: y, W: bounds.W, H: bounds.H})
				}
			} else {
				for _, r := range v.inputRects {
					if r = intersectRect(r, bounds); r.W > 0 {
						r.X += x
						r.Y += y
						rects = append(rects, r)
					}
				}
			}
		}
		for _, ch := range v.sub.children {
			visit(ch, x+ch.sub.x, y+ch.sub.y)
		}
	}
	if s.inputAll && len(s.sub.children) == 0 {
		return true, nil
	}
	visit(s, 0, 0)
	if s.xdg != nil {
		for i := range rects {
			rects[i].X -= s.xdg.geometry.X
			rects[i].Y -= s.xdg.geometry.Y
		}
	}
	return false, rects
}

// resetInputEmission makes the next mapping publish its current region again.
func (s *surface) resetInputEmission() {
	s.sentInput = false
	s.lastInputAll = false
	s.lastInputRects = nil
}

func (s *surface) emitInput() {
	root := s.root()
	id := root.windowID()
	if id == 0 {
		return
	}
	all, rects := root.effectiveInput()
	if !root.sentInput && all {
		return
	}
	if root.sentInput && root.lastInputAll == all && slices.Equal(root.lastInputRects, rects) {
		return
	}
	root.sentInput = true
	root.lastInputAll = all
	root.lastInputRects = slices.Clone(rects)
	s.server.emit(ports.InputRegionChanged{ID: id, All: all, Rects: rects})
	if c := s.server.constraints[root]; c != nil {
		if c.active {
			s.server.emitConstraint(c)
		} else {
			s.server.updateConstraint()
		}
	}
}

func (s *surface) SetInputRegion(_ *wayland.Surface, reg *wayland.Region) {
	s.pendingInputSet = true
	s.pendingInputAll = reg == nil || reg.Resource == nil
	s.pendingInputRects = nil
	if !s.pendingInputAll {
		if g := s.server.regions[reg.Resource]; g != nil {
			s.pendingInputRects = slices.Clone(g.rects)
		}
	}
}
