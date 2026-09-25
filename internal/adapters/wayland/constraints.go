package wayland

import (
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/pointerconstraints"
	"github.com/bnema/purego-libwayland/protocol/relativepointer"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

func registerPointerConstraints(d *server.Display, s *Server) error {
	if err := relativepointer.NewZwpRelativePointerManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = relativepointer.NewZwpRelativePointerManagerV1(c, int32(v), id, relativeManager{s})
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

type relativeHandler struct{}

func (relativeHandler) Destroy(*relativepointer.ZwpRelativePointerV1) {}

// relativeMotion sends the motion deltas to the window client's relative
// pointers. Absolute devices have no deltas and send nothing.
func (s *Server) relativeMotion(w *window, c ports.PointerMotionTo) {
	if c.DX == 0 && c.DY == 0 && c.UnaccelDX == 0 && c.UnaccelDY == 0 {
		return
	}
	if !w.mapped || !w.xdg.resource.Resource.Alive() {
		return
	}
	for _, rel := range s.relatives[w.xdg.resource.Client()] {
		if rel.Resource.Alive() {
			rel.SendRelativeMotion(uint32(c.TimeUsec>>32), uint32(c.TimeUsec), server.FixedFromFloat(c.DX), server.FixedFromFloat(c.DY), server.FixedFromFloat(c.UnaccelDX), server.FixedFromFloat(c.UnaccelDY))
		}
	}
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
	if surf == nil {
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
		return g.box
	}
	return ports.Rect{}
}

type lockHandler struct{ c *constraint }

func (lockHandler) Destroy(*pointerconstraints.ZwpLockedPointerV1) {}

// SetCursorPositionHint is ignored: the pointer stays where it locked.
func (lockHandler) SetCursorPositionHint(*pointerconstraints.ZwpLockedPointerV1, server.Fixed, server.Fixed) {
}
func (h lockHandler) SetRegion(_ *pointerconstraints.ZwpLockedPointerV1, reg *wayland.Region) {
	r := h.c.surface.server.regionBox(reg)
	h.c.pending = &r
}

type confineHandler struct{ c *constraint }

func (confineHandler) Destroy(*pointerconstraints.ZwpConfinedPointerV1) {}
func (h confineHandler) SetRegion(_ *pointerconstraints.ZwpConfinedPointerV1, reg *wayland.Region) {
	r := h.c.surface.server.regionBox(reg)
	h.c.pending = &r
}

// commitConstraint applies a pending region on the surface commit.
func (s *surface) commitConstraint() {
	c := s.server.constraints[s]
	if c == nil || c.pending == nil {
		return
	}
	c.region, c.pending = *c.pending, nil
	if c.active {
		s.server.emitConstraint(c)
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

// updateConstraint activates the constraint of the window holding both
// pointer and keyboard focus, and deactivates any other.
func (s *Server) updateConstraint() {
	var want *constraint
	if w := s.windows[s.pointerFocus]; w != nil && w.mapped && s.pointerFocus == s.focused {
		if c := s.constraints[w.xdg.surface]; c != nil && !c.defunct {
			want = c
		}
	}
	if want == s.constraint {
		return
	}
	if old := s.constraint; old != nil {
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
	}
	s.constraint = want
	if want == nil {
		s.emit(ports.PointerConstrained{})
		return
	}
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
	s.emit(ports.PointerConstrained{ID: x.window.id, PointerConstraint: ports.PointerConstraint{Mode: mode, Rect: r}})
}

// locked reports whether the window's pointer is locked.
func (s *Server) locked(w *window) bool {
	return s.constraint != nil && s.constraint.lock != nil && s.constraint.surface == w.xdg.surface
}
