package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/pointerconstraints"
	"github.com/bnema/go-wayland-bindings/server/pointerwarp"
	"github.com/bnema/go-wayland-bindings/server/relativepointer"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

func registerPointerConstraints(d *server.Display, s *Server) error {
	if err := relativepointer.NewZwpRelativePointerManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = relativepointer.NewZwpRelativePointerManagerV1(c, int32(v), id, relativeManager{s})
	}); err != nil {
		return err
	}
	if err := pointerwarp.NewWpPointerWarpV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = pointerwarp.NewWpPointerWarpV1(c, int32(v), id, pointerWarp{s})
	}); err != nil {
		return err
	}
	return pointerconstraints.NewZwpPointerConstraintsV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = pointerconstraints.NewZwpPointerConstraintsV1(c, int32(v), id, constraintManager{s})
	})
}

type relativeManager struct{ server *Server }

func (relativeManager) Destroy(*relativepointer.ZwpRelativePointerManagerV1) {}
func (m relativeManager) GetRelativePointer(r *relativepointer.ZwpRelativePointerManagerV1, id uint32, _ *wayland.Pointer) {
	rel, err := relativepointer.NewZwpRelativePointerV1(r.Client(), r.Version(), id, relativeHandler{})
	if err != nil {
		return
	}
	s, c := m.server, r.Client()
	s.relatives[c] = append(s.relatives[c], rel)
	rel.OnDestroy = func() {
		if list := removeItem(s.relatives[c], rel); len(list) > 0 {
			s.relatives[c] = list
		} else {
			delete(s.relatives, c)
		}
	}
}

// pointerWarp is wp_pointer_warp_v1: a client with the pointer moves it on
// its window (Wine's SetCursorPos). Warps without the pointer, with a serial
// other than that pointer's current enter, or outside the window are ignored.
type pointerWarp struct{ server *Server }

func (pointerWarp) Destroy(*pointerwarp.WpPointerWarpV1) {}
func (h pointerWarp) WarpPointer(r *pointerwarp.WpPointerWarpV1, surf *wayland.Surface, p *wayland.Pointer, x, y server.Fixed, serial uint32) {
	s := h.server
	if s.protected() || surf == nil || p == nil || p.Client() != r.Client() {
		return
	}
	if enter, ok := s.seat.enters[p.Resource]; !ok || enter != serial {
		return
	}
	w := s.windows[s.seat.pointerFocus]
	if w == nil || !w.mapped || w.xdg.resource.Client() != r.Client() || s.surfaces[surf.Resource] != w.xdg.surface {
		return
	}
	// Surface-local to window-local (core's, from the geometry origin).
	g := w.xdg.geometry
	s.emit(ports.PointerWarp{ID: w.id, X: x.Float() - float64(g.X), Y: y.Float() - float64(g.Y)})
}

type relativeHandler struct{}

func (relativeHandler) Destroy(*relativepointer.ZwpRelativePointerV1) {}

// relativeMotion sends the motion deltas to the window client's relative
// pointers. Absolute devices have no deltas and send nothing.
// It reports whether any event was sent.
func (s *Server) relativeMotion(w *window, c ports.PointerMotionTo) bool {
	if s.protected() || c.DX == 0 && c.DY == 0 && c.UnaccelDX == 0 && c.UnaccelDY == 0 {
		return false
	}
	if !w.mapped || !w.xdg.resource.Resource.Alive() {
		return false
	}
	sent := false
	for _, rel := range s.relatives[w.xdg.resource.Client()] {
		if rel.Resource.Alive() {
			rel.SendRelativeMotion(uint32(c.TimeUsec>>32), uint32(c.TimeUsec), server.FixedFromFloat(c.DX), server.FixedFromFloat(c.DY), server.FixedFromFloat(c.UnaccelDX), server.FixedFromFloat(c.UnaccelDY))
			sent = true
		}
	}
	return sent
}

// constraint is a zwp_locked_pointer_v1 or zwp_confined_pointer_v1 on a
// surface. Its region is surface-local; empty means the whole surface.
type constraint struct {
	surface    *surface
	lock       *pointerconstraints.ZwpLockedPointerV1
	confine    *pointerconstraints.ZwpConfinedPointerV1
	region     ports.Rect
	pending    *ports.Rect // set_region waiting for the surface commit
	persistent bool
	active     bool
	defunct    bool // a oneshot constraint that was deactivated
}

type constraintManager struct{ server *Server }

func (constraintManager) Destroy(*pointerconstraints.ZwpPointerConstraintsV1) {}
func (m constraintManager) LockPointer(r *pointerconstraints.ZwpPointerConstraintsV1, id uint32, surf *wayland.Surface, _ *wayland.Pointer, reg *wayland.Region, lifetime uint32) {
	c := m.create(r, surf, reg, lifetime)
	if c == nil {
		// An unknown surface: an inert object the client can destroy.
		_, _ = pointerconstraints.NewZwpLockedPointerV1(r.Client(), r.Version(), id, lockHandler{&constraint{}})
		return
	}
	lock, err := pointerconstraints.NewZwpLockedPointerV1(r.Client(), r.Version(), id, lockHandler{c})
	if err != nil {
		delete(m.server.constraints, c.surface)
		return
	}
	c.lock = lock
	lock.OnDestroy = func() { m.server.dropConstraint(c) }
	m.server.updateConstraint()
}
func (m constraintManager) ConfinePointer(r *pointerconstraints.ZwpPointerConstraintsV1, id uint32, surf *wayland.Surface, _ *wayland.Pointer, reg *wayland.Region, lifetime uint32) {
	c := m.create(r, surf, reg, lifetime)
	if c == nil {
		_, _ = pointerconstraints.NewZwpConfinedPointerV1(r.Client(), r.Version(), id, confineHandler{&constraint{}})
		return
	}
	confine, err := pointerconstraints.NewZwpConfinedPointerV1(r.Client(), r.Version(), id, confineHandler{c})
	if err != nil {
		delete(m.server.constraints, c.surface)
		return
	}
	c.confine = confine
	confine.OnDestroy = func() { m.server.dropConstraint(c) }
	m.server.updateConstraint()
}

// create validates the request and registers the constraint on the surface.
func (m constraintManager) create(r *pointerconstraints.ZwpPointerConstraintsV1, surf *wayland.Surface, reg *wayland.Region, lifetime uint32) *constraint {
	s := m.server
	if s.protected() || surf == nil {
		return nil
	}
	state := s.surfaces[surf.Resource]
	if state == nil {
		return nil
	}
	if s.constraints[state] != nil {
		r.PostError(uint32(pointerconstraints.ZwpPointerConstraintsV1ErrorAlreadyConstrained), "surface already constrained")
		return nil
	}
	if state.kind == roleCursor {
		return nil
	}
	c := &constraint{surface: state, region: s.regionBox(reg), persistent: lifetime == uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent)}
	s.constraints[state] = c
	return c
}

// regionBox copies a region: the protocol reads it at request time.
func (s *Server) regionBox(reg *wayland.Region) ports.Rect {
	if reg == nil || reg.Resource == nil {
		return ports.Rect{}
	}
	if g := s.regions[reg.Resource]; g != nil {
		return g.boxRect()
	}
	return ports.Rect{}
}

type lockHandler struct{ c *constraint }

func (lockHandler) Destroy(*pointerconstraints.ZwpLockedPointerV1) {}

// SetCursorPositionHint is ignored: the pointer stays where it locked.
func (lockHandler) SetCursorPositionHint(*pointerconstraints.ZwpLockedPointerV1, server.Fixed, server.Fixed) {
}
func (h lockHandler) SetRegion(_ *pointerconstraints.ZwpLockedPointerV1, reg *wayland.Region) {
	if h.c.surface == nil {
		return
	}
	r := h.c.surface.server.regionBox(reg)
	h.c.pending = &r
}

type confineHandler struct{ c *constraint }

func (confineHandler) Destroy(*pointerconstraints.ZwpConfinedPointerV1) {}
func (h confineHandler) SetRegion(_ *pointerconstraints.ZwpConfinedPointerV1, reg *wayland.Region) {
	if h.c.surface == nil {
		return
	}
	r := h.c.surface.server.regionBox(reg)
	h.c.pending = &r
}

// commitConstraint applies this commit's captured region after its geometry;
// later set_region requests remain pending. Core receives window-local state.
func (s *surface) commitConstraint(c *constraint, region *ports.Rect, geometry bool) {
	if c == nil || s.server.constraints[s] != c {
		if !geometry {
			return
		}
		c = s.server.constraints[s]
		region = nil // never apply an old constraint's region to its replacement
	}
	if c == nil || (region == nil && !geometry) {
		return
	}
	if region != nil && s.server.constraints[s] == c {
		c.region = *region
	}
	if c.active {
		s.server.emitConstraint(c)
	} else {
		s.server.updateConstraint()
	}
}

// dropConstraint forgets a destroyed constraint or one on a destroyed surface.
func (s *Server) dropConstraint(c *constraint) {
	if s.constraints[c.surface] == c {
		delete(s.constraints, c.surface)
	}
	if s.constraint == c {
		c.active = false
		s.constraint = nil
		if c.lock != nil && c.lock.Resource.Alive() {
			c.lock.SendUnlocked()
		}
		if c.confine != nil && c.confine.Resource.Alive() {
			c.confine.SendUnconfined()
		}
		s.emit(ports.PointerConstrained{})
	}
	s.updateConstraint()
}

// deactivateConstraint ends the active constraint; a oneshot cannot reactivate.
func (s *Server) deactivateConstraint() {
	old := s.constraint
	if old == nil {
		return
	}
	old.active = false
	if !old.persistent {
		old.defunct = true
	}
	if old.lock != nil && old.lock.Resource.Alive() {
		old.lock.SendUnlocked()
	}
	if old.confine != nil && old.confine.Resource.Alive() {
		old.confine.SendUnconfined()
	}
	s.constraint = nil
	s.emit(ports.PointerConstrained{})
}

// updateConstraint activates the constraint of the window holding both
// pointer and keyboard focus, and deactivates any other.
func (s *Server) updateConstraint() {
	if s.protected() {
		s.deactivateConstraint()
		return
	}
	var want *constraint
	if w := s.windows[s.seat.pointerFocus]; w != nil && w.mapped && s.seat.pointerFocus == s.seat.focused {
		if c := s.constraints[w.xdg.surface]; c != nil && !c.defunct && (c.active || c.contains(w)) {
			want = c
		}
	}
	if want == s.constraint {
		return
	}
	s.deactivateConstraint()
	if want == nil {
		return
	}
	s.constraint = want
	want.active = true
	if want.lock != nil {
		want.lock.SendLocked()
	} else {
		want.confine.SendConfined()
	}
	s.emitConstraint(want)
}

// emitConstraint tells core about the active constraint, window-local.
func (s *Server) emitConstraint(c *constraint) {
	if s.protected() {
		return
	}
	x := c.surface.xdg
	if x == nil || x.window == nil {
		return
	}
	mode := ports.ConstraintConfine
	if c.lock != nil {
		mode = ports.ConstraintLock
	}
	r := c.region
	if r.W > 0 && r.H > 0 {
		r.X -= x.geometry.X
		r.Y -= x.geometry.Y
	}
	// Core supports one confinement rect, so this bounds the intersection;
	// contains uses the exact input region to decide activation.
	if mode == ports.ConstraintConfine {
		if all, rects := c.surface.effectiveInput(); !all {
			var box ports.Rect
			for _, input := range rects {
				if r.W > 0 && r.H > 0 {
					input = intersectRect(input, r)
				}
				if input.W <= 0 {
					continue
				}
				if box.W == 0 {
					box = input
				} else {
					x0, y0 := min(box.X, input.X), min(box.Y, input.Y)
					box = ports.Rect{X: x0, Y: y0, W: max(box.X+box.W, input.X+input.W) - x0, H: max(box.Y+box.H, input.Y+input.H) - y0}
				}
			}
			r = box
		}
	}
	s.emit(ports.PointerConstrained{ID: x.window.id, PointerConstraint: ports.PointerConstraint{Mode: mode, Rect: r}})
}

// contains reports whether the pointer is in the region: a constraint
// activates only there, so it never warps the pointer.
func (c *constraint) contains(w *window) bool {
	r := c.region
	x, y := w.surfacePoint(w.xdg.server.seat.pointerX, w.xdg.server.seat.pointerY)
	all, rects := c.surface.effectiveInput()
	if !all {
		localX, localY := x-float64(w.xdg.geometry.X), y-float64(w.xdg.geometry.Y)
		inside := false
		for _, input := range rects {
			if localX >= float64(input.X) && localX < float64(input.X+input.W) && localY >= float64(input.Y) && localY < float64(input.Y+input.H) {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	if r.W <= 0 || r.H <= 0 {
		return true
	}
	return x >= float64(r.X) && x < float64(r.X+r.W) && y >= float64(r.Y) && y < float64(r.Y+r.H)
}

// locked reports whether the window's pointer is locked.
func (s *Server) locked(w *window) bool {
	return !s.protected() && s.constraint != nil && s.constraint.lock != nil && s.constraint.surface == w.xdg.surface
}
