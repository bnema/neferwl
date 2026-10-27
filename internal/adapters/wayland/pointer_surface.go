package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// pointerHit samples committed stacking, from the topmost child down to the
// parent and finally the children below it. Unmapped ancestors hide their tree.
func (s *surface) pointerHit(x, y float64) (*surface, float64, float64) {
	if s.destroyed || !s.has {
		return nil, 0, 0
	}
	for i := len(s.sub.layout) - 1; i >= 0; i-- {
		item := s.sub.layout[i]
		if !item.below {
			if hit, sx, sy := item.child.pointerHit(x-float64(item.x), y-float64(item.y)); hit != nil {
				return hit, sx, sy
			}
		}
	}
	if x >= 0 && y >= 0 && x < float64(s.content.LogicalW) && y < float64(s.content.LogicalH) {
		if s.inputAll {
			return s, x, y
		}
		for _, r := range s.inputRects {
			if x >= float64(r.X) && y >= float64(r.Y) && x < float64(r.X+r.W) && y < float64(r.Y+r.H) {
				return s, x, y
			}
		}
	}
	for i := len(s.sub.layout) - 1; i >= 0; i-- {
		item := s.sub.layout[i]
		if item.below {
			if hit, sx, sy := item.child.pointerHit(x-float64(item.x), y-float64(item.y)); hit != nil {
				return hit, sx, sy
			}
		}
	}
	return nil, 0, 0
}

// pointerOffset finds a pinned surface even when the pointer is outside its
// input region. Offsets come from committed layout, not pending set_position.
func (s *surface) pointerOffset(target *wayland.Surface, x, y float64) (float64, float64, bool) {
	if s.destroyed || !s.has {
		return 0, 0, false
	}
	if s.wl == target {
		return x, y, true
	}
	for _, item := range s.sub.layout {
		if sx, sy, ok := item.child.pointerOffset(target, x-float64(item.x), y-float64(item.y)); ok {
			return sx, sy, true
		}
	}
	return 0, 0, false
}

func (s *Server) resolvePointerSurface(root *wayland.Surface, pointers []*wayland.Pointer, x, y float64) (*wayland.Surface, []*wayland.Pointer, float64, float64) {
	tree := s.surfaces[root.Resource]
	if tree == nil {
		return nil, nil, 0, 0
	}
	if len(s.seat.buttons) != 0 && s.seat.entered != nil {
		if sx, sy, ok := tree.pointerOffset(s.seat.entered, x, y); ok {
			return s.seat.entered, pointers, sx, sy
		}
		return nil, nil, 0, 0
	}
	if hit, sx, sy := tree.pointerHit(x, y); hit != nil {
		return hit.wl, pointers, sx, sy
	}
	// Preserve the root's default infinite input region. Core has already
	// selected the window; unlike children its extent is window policy.
	if tree.has && tree.inputAll && !tree.destroyed {
		return root, pointers, x, y
	}
	return nil, nil, 0, 0
}

func (s *Server) pointerMotionSurface(c ports.PointerMotionTo) {
	surface, pointers, x, y := s.resolvePointerFocus(c.ID, c.X, c.Y)
	s.enterPointerSurface(c.ID, c.X, c.Y, surface, pointers, x, y)
	s.seat.pointerX, s.seat.pointerY = c.X, c.Y
	if surface == nil || surface != s.seat.entered {
		return
	}
	for _, p := range pointers {
		s.stampPointer(p, c.Time)
		p.SendMotion(wireMsec(c.Time), server.FixedFromFloat(x), server.FixedFromFloat(y))
		pointerFrame(p)
	}
}

// invalidatePointerTree clears focus on unmap, detach or destruction, including
// a descendant of the disappearing surface. Re-entry waits for the next motion.
func (s *surface) invalidatePointerTree() {
	entered := s.server.seat.entered
	for node := s.server.surfacesResource(entered); node != nil; node = node.sub.parent {
		if node == s {
			clear(s.server.seat.buttons)
			s.server.changePointerFocus(0, 0, 0)
			return
		}
	}
}

func (s *Server) surfacesResource(wl *wayland.Surface) *surface {
	if wl == nil {
		return nil
	}
	return s.surfaces[wl.Resource]
}

// enteredPointerSurface initializes a late pointer from the seat's actual
// focus, not a fresh hit test: layout changes take effect on the next motion.
func (s *Server) enteredPointerSurface() (*wayland.Surface, []*wayland.Pointer, float64, float64) {
	entered := s.seat.entered
	if entered == nil || !entered.Alive() {
		return nil, nil, 0, 0
	}
	x, y := s.seat.pointerX, s.seat.pointerY
	var root *surface
	if w := s.windows[s.seat.pointerFocus]; w != nil && w.mapped {
		root = w.xdg.surface
		x, y = w.surfacePoint(x, y)
	} else if l := s.layers[s.seat.pointerFocus]; l != nil && l.mapped {
		root = l.surface
	} else if l := s.lockSurfaces[s.seat.pointerFocus]; l != nil && !l.closed {
		root = l.surface
	}
	if root != nil {
		if sx, sy, ok := root.pointerOffset(entered, x, y); ok {
			return entered, s.clientPointers(entered.Client()), sx, sy
		}
	}
	return nil, nil, 0, 0
}
