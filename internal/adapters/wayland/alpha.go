package wayland

import (
	"math"

	"github.com/bnema/purego-libwayland/protocol/alphamodifier"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// wp_alpha_modifier_v1: a client fades a whole surface (Wine's layered
// windows) without redrawing it. The multiplier is double-buffered and
// travels with the content as Fade; the renderer blends it.

func registerAlphaModifier(d *server.Display, s *Server) error {
	return alphamodifier.NewWpAlphaModifierV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = alphamodifier.NewWpAlphaModifierV1(c, int32(v), id, alphaManager{s})
	})
}

type alphaManager struct{ server *Server }

func (alphaManager) Destroy(*alphamodifier.WpAlphaModifierV1) {}

func (m alphaManager) GetSurface(r *alphamodifier.WpAlphaModifierV1, id uint32, surf *wayland.Surface) {
	state := m.server.surfaceOf(surf)
	if state != nil && state.alpha != nil {
		r.PostError(uint32(alphamodifier.WpAlphaModifierV1ErrorAlreadyConstructed), "surface already has an alpha modifier")
		return
	}
	h := &alphaHandler{surface: state}
	res, err := alphamodifier.NewWpAlphaModifierSurfaceV1(r.Client(), r.Version(), id, h)
	if err != nil || state == nil {
		return
	}
	state.alpha = h
	res.OnDestroy = func() {
		// Destroying it sets the multiplier back to opaque, double-buffered.
		if state.alpha == h {
			state.alpha = nil
			state.next.fade = 0
		}
	}
}

type alphaHandler struct{ surface *surface }

func (*alphaHandler) Destroy(*alphamodifier.WpAlphaModifierSurfaceV1) {}

func (h *alphaHandler) SetMultiplier(r *alphamodifier.WpAlphaModifierSurfaceV1, factor uint32) {
	if h.surface == nil || h.surface.destroyed || h.surface.alpha != h {
		r.PostError(uint32(alphamodifier.WpAlphaModifierSurfaceV1ErrorNoSurface), "wl_surface was destroyed")
		return
	}
	h.surface.next.fade = fade(factor)
}

// fade turns a multiplier (0 transparent, UINT32_MAX opaque) into Fade.
func fade(factor uint32) float32 {
	return float32(1 - float64(factor)/math.MaxUint32)
}
