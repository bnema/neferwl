package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/tearingcontrol"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// Tearing control: a surface asks for async presentation. Outputs honour it
// only when they scan the surface out directly (ADR 006); the hint is
// double-buffered state applied on commit.

func registerTearing(d *server.Display, s *Server) error {
	return tearingcontrol.NewWpTearingControlManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = tearingcontrol.NewWpTearingControlManagerV1(c, int32(v), id, tearingManager{s})
	})
}

type tearingManager struct{ server *Server }

func (tearingManager) Destroy(*tearingcontrol.WpTearingControlManagerV1) {}

func (m tearingManager) GetTearingControl(r *tearingcontrol.WpTearingControlManagerV1, id uint32, surf *wayland.Surface) {
	var state *surface
	if surf != nil {
		state = m.server.surfaces[surf.Resource]
	}
	if state != nil && state.tearing != nil {
		r.PostError(uint32(tearingcontrol.WpTearingControlManagerV1ErrorTearingControlExists), "surface already has a tearing control")
		return
	}
	h := &tearingHandler{surface: state}
	res, err := tearingcontrol.NewWpTearingControlV1(r.Client(), r.Version(), id, h)
	if err != nil || state == nil {
		return
	}
	state.tearing = h
	res.OnDestroy = func() {
		// Back to vsync on the next commit.
		if state.tearing == h {
			state.tearing = nil
			state.next.async = false
		}
	}
}

type tearingHandler struct{ surface *surface }

func (h *tearingHandler) SetPresentationHint(_ *tearingcontrol.WpTearingControlV1, hint uint32) {
	if h.surface != nil && h.surface.tearing == h {
		h.surface.next.async = hint == uint32(tearingcontrol.WpTearingControlV1PresentationHintAsync)
	}
}

func (*tearingHandler) Destroy(*tearingcontrol.WpTearingControlV1) {}
