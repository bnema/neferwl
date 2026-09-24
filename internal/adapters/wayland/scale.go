package wayland

import (
	"math"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/fractionalscale"
	"github.com/bnema/purego-libwayland/protocol/viewporter"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgoutput"
	"github.com/bnema/purego-libwayland/server"
)

// Output scale (HiDPI). Clients that bind wp_fractional_scale_v1 get the exact
// scale in 1/120 steps and draw at physical size through wp_viewporter. Others
// get wl_output.scale rounded up, draw at that integer, and the renderer
// downscales. Every size sent to clients (configures, xdg-output, layer
// configures, pointer coordinates) stays logical.

// scaleState is what clients were last told about the output.
type scaleState struct {
	scale         float64
	width, height int // logical
	outputs       []*wayland.Output
	xdgOutputs    []*xdgoutput.ZxdgOutputV1
	fractions     map[*surface]*fractionalscale.WpFractionalScaleV1
}

// integerScale is wl_output.scale: the fractional scale rounded up.
func (st *scaleState) integerScale() int32 { return int32(math.Ceil(st.scale - 1e-9)) }

func (s *Server) setOutputScale(c ports.SetOutputScale) {
	st := &s.scale
	scaleChanged := c.Scale != st.scale
	st.scale, st.width, st.height = c.Scale, c.Width, c.Height
	s.log.Info().Float64("scale", c.Scale).Int("w", c.Width).Int("h", c.Height).Msg("output scale")
	for _, x := range st.xdgOutputs {
		x.SendLogicalSize(int32(c.Width), int32(c.Height))
		if x.Version() < 3 {
			x.SendDone()
		}
	}
	for _, o := range st.outputs {
		if o.Version() >= 2 {
			o.SendScale(st.integerScale())
			o.SendDone()
		}
	}
	if scaleChanged {
		for _, surf := range s.surfaces {
			s.sendSurfaceScale(surf.wl)
		}
		for surf, f := range st.fractions {
			if !surf.destroyed {
				f.SendPreferredScale(uint32(math.Round(st.scale * 120)))
			}
		}
	}
	// Layer surfaces sized from the output follow its new logical size.
	for _, l := range s.layers {
		// Uses committed state only: pending requests wait for the client's commit.
		if l.configured && (l.current.width == 0 || l.current.height == 0) {
			l.sendConfigure()
		}
	}
}

// sendSurfaceScale tells a wl_surface v6+ which integer buffer scale to use.
func (s *Server) sendSurfaceScale(w *wayland.Surface) {
	if w != nil && w.Version() >= 6 {
		w.SendPreferredBufferScale(s.scale.integerScale())
	}
}

func registerScale(d *server.Display, s *Server) error {
	if err := fractionalscale.NewWpFractionalScaleManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = fractionalscale.NewWpFractionalScaleManagerV1(c, int32(v), id, fractionManager{s})
	}); err != nil {
		return err
	}
	return viewporter.NewWpViewporterGlobal(d, 1, func(c server.Client, v, id uint32) {
		_, _ = viewporter.NewWpViewporter(c, int32(v), id, viewporterHandler{s})
	})
}

type fractionManager struct{ server *Server }

func (fractionManager) Destroy(*fractionalscale.WpFractionalScaleManagerV1) {}
func (m fractionManager) GetFractionalScale(r *fractionalscale.WpFractionalScaleManagerV1, id uint32, w *wayland.Surface) {
	s := m.server
	surf := s.surfaces[w.GetResource()]
	if surf == nil {
		return
	}
	if _, dup := s.scale.fractions[surf]; dup {
		r.PostError(uint32(fractionalscale.WpFractionalScaleManagerV1ErrorFractionalScaleExists), "fractional scale already exists")
		return
	}
	f, err := fractionalscale.NewWpFractionalScaleV1(r.Client(), r.Version(), id, fraction{})
	if err != nil {
		return
	}
	s.scale.fractions[surf] = f
	f.OnDestroy = func() { delete(s.scale.fractions, surf) }
	f.SendPreferredScale(uint32(math.Round(s.scale.scale * 120)))
}

type fraction struct{}

func (fraction) Destroy(*fractionalscale.WpFractionalScaleV1) {}

type viewporterHandler struct{ server *Server }

func (viewporterHandler) Destroy(*viewporter.WpViewporter) {}
func (h viewporterHandler) GetViewport(r *viewporter.WpViewporter, id uint32, w *wayland.Surface) {
	surf := h.server.surfaces[w.GetResource()]
	if surf == nil {
		return
	}
	if surf.viewport != nil {
		r.PostError(uint32(viewporter.WpViewporterErrorViewportExists), "viewport already exists")
		return
	}
	vp := &viewport{surface: surf}
	res, err := viewporter.NewWpViewport(r.Client(), r.Version(), id, vp)
	if err != nil {
		return
	}
	surf.viewport = vp
	res.OnDestroy = func() {
		// Destroying the viewport resets the size on the next commit.
		if surf.viewport == vp {
			surf.viewport = nil
		}
	}
}

// viewport holds the pending and committed wp_viewport destination; the
// source crop is accepted and ignored (clients use it for video crops).
type viewport struct {
	surface          *surface
	pendingW, destW  int32
	pendingH, destH  int32
	pendingSet, dest bool
}

func (*viewport) Destroy(*viewporter.WpViewport) {}
func (*viewport) SetSource(*viewporter.WpViewport, server.Fixed, server.Fixed, server.Fixed, server.Fixed) {
}
func (v *viewport) SetDestination(r *viewporter.WpViewport, w, h int32) {
	if v.surface.destroyed {
		r.PostError(uint32(viewporter.WpViewportErrorNoSurface), "surface destroyed")
		return
	}
	if (w == -1) != (h == -1) || (w != -1 && (w <= 0 || h <= 0)) {
		r.PostError(uint32(viewporter.WpViewportErrorBadValue), "invalid destination size")
		return
	}
	v.pendingW, v.pendingH, v.pendingSet = w, h, w != -1
}
func (v *viewport) commit() { v.destW, v.destH, v.dest = v.pendingW, v.pendingH, v.pendingSet }

// logicalSize is the surface size in logical pixels: the viewport
// destination, else the buffer divided by its integer buffer scale.
func (s *surface) logicalSize(bw, bh int) (int, int) {
	if s.viewport != nil && s.viewport.dest {
		return int(s.viewport.destW), int(s.viewport.destH)
	}
	scale := max(s.bufferScale, 1)
	return bw / scale, bh / scale
}
