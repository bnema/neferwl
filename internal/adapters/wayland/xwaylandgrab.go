package wayland

import (
	"bnema.dev/purego-libwayland/server"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xwaylandkeyboardgrab"
	"github.com/bnema/neferwl/internal/domain"
)

// zwp_xwayland_keyboard_grab_v1. Xwayland asks for it when an X client
// takes an active keyboard grab. It is a shortcuts inhibitor, but only
// while the grabbing surface's window has the keyboard focus and is
// fullscreen: a windowed app must not lock the compositor binds out.
// Otherwise the request is accepted and has no effect. Never honoured
// while the session is protected. The global is visible to Xwayland only
// (xwaylandRestricted).

// xwaylandGrab is the seat's single grab.
type xwaylandGrab struct {
	res  *xwaylandkeyboardgrab.ZwpXwaylandKeyboardGrabV1
	surf *surface
}

func registerXwaylandGrab(d *server.Display, s *Server) error {
	return xwaylandkeyboardgrab.NewZwpXwaylandKeyboardGrabManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xwaylandkeyboardgrab.NewZwpXwaylandKeyboardGrabManagerV1(c, int32(v), id, xwaylandGrabManager{s})
	})
}

type xwaylandGrabManager struct{ s *Server }

func (xwaylandGrabManager) Destroy(*xwaylandkeyboardgrab.ZwpXwaylandKeyboardGrabManagerV1) {}

// GrabKeyboard replaces the seat's grab with a new one.
func (m xwaylandGrabManager) GrabKeyboard(r *xwaylandkeyboardgrab.ZwpXwaylandKeyboardGrabManagerV1, id uint32, surf *wayland.Surface, _ *wayland.Seat) {
	state := m.s.surfaceOf(surf)
	if state == nil {
		return
	}
	g := &xwaylandGrab{surf: state}
	res, err := xwaylandkeyboardgrab.NewZwpXwaylandKeyboardGrabV1(r.Client(), r.Version(), id, xwaylandGrabHandler{})
	if err != nil {
		return
	}
	g.res = res
	old := m.s.xgrab
	m.s.xgrab = g
	res.OnDestroy = func() {
		if m.s.xgrab == g {
			m.s.xgrab = nil
			m.s.syncKeyboardGrab()
		}
	}
	if old != nil && old.res != nil && old.res.Alive() {
		old.res.Destroy()
	}
	m.s.syncKeyboardGrab()
}

type xwaylandGrabHandler struct{}

func (xwaylandGrabHandler) Destroy(*xwaylandkeyboardgrab.ZwpXwaylandKeyboardGrabV1) {}

// xgrabWindow is the window whose grab is in effect (0: none).
func (s *Server) xgrabWindow() domain.WindowID {
	g := s.xgrab
	if g == nil || g.surf == nil || g.surf.destroyed || !g.res.Alive() {
		return 0
	}
	if s.security != nil && s.security.Snapshot().Protected {
		return 0
	}
	id := g.surf.root().windowID()
	if id == 0 || id != s.seat.focused {
		return 0
	}
	if w := s.windows[id]; w == nil || !w.hasLast || !w.last.Fullscreen {
		return 0
	}
	return id
}

// syncKeyboardGrab re-evaluates the grab after a focus or fullscreen
// change, and reports to core only when its effect changed.
func (s *Server) syncKeyboardGrab() {
	if id := s.xgrabWindow(); id != s.xgrabbed {
		s.xgrabbed = id
		s.syncInhibitors()
	}
}
