package wayland

import (
	"math"

	"github.com/bnema/purego-libwayland/protocol/fractionalscale"
	"github.com/bnema/purego-libwayland/protocol/viewporter"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// Surfaces follow the output of their window or layer (the focused output
// until they are shown): wl_surface.enter/leave, preferred buffer scale and
// fractional scale all come from it.

// outputOfSurface is the output a surface is on: its window's configured
// output, its layer output, or the focused one.
func (s *Server) outputOfSurface(surf *surface) *output {
	// Subsurfaces are on their root's output.
	surf = surf.root()
	// Popups are on their parent's output.
	for surf.xdg != nil && surf.xdg.window != nil && surf.xdg.window.popup != nil && surf.xdg.window.popup.parent != nil {
		surf = surf.xdg.window.popup.parent.xdg.surface.root()
	}
	name := ""
	switch {
	case surf.xdg != nil && surf.xdg.window != nil:
		name = surf.xdg.window.last.Output
	case surf.layer != nil && surf.layer.output != nil:
		return surf.layer.output
	}
	return s.outputByName(name)
}

// sendTreeScale sends the scale to a surface and its subsurfaces.
func (surf *surface) sendTreeScale() {
	surf.sendScale()
	for _, ch := range surf.sub.children {
		ch.sendTreeScale()
	}
}

// sendScale tells the surface about its output: enter/leave, the integer
// buffer scale (wl_surface v6+) and the fractional scale.
func (surf *surface) sendScale() {
	s := surf.server
	if surf.destroyed || surf.wl == nil || !surf.wl.Resource.Alive() {
		return
	}
	o := s.outputOfSurface(surf)
	if o != surf.on {
		if surf.on != nil {
			surf.leave(surf.on)
		}
		surf.on = o
		if o != nil {
			for _, r := range o.resources {
				if r.Client() == surf.wl.Client() {
					surf.wl.SendEnter(r)
				}
			}
		}
	}
	scale := 1.0
	if o != nil {
		scale = o.place.Scale
	}
	if scale == surf.scale {
		return
	}
	surf.scale = scale
	if surf.wl.Version() >= 6 {
		surf.wl.SendPreferredBufferScale(integerScale(scale))
	}
	if f := s.fractions[surf]; f != nil {
		f.SendPreferredScale(uint32(math.Round(scale * 120)))
	}
}

// leave sends wl_surface.leave for o when the surface is on it.
func (surf *surface) leave(o *output) {
	if surf.on != o || o == nil {
		return
	}
	surf.on = nil
	if surf.destroyed || !surf.wl.Resource.Alive() {
		return
	}
	for _, r := range o.resources {
		if r.Client() == surf.wl.Client() && r.Resource.Alive() {
			surf.wl.SendLeave(r)
		}
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
	if _, dup := s.fractions[surf]; dup {
		r.PostError(uint32(fractionalscale.WpFractionalScaleManagerV1ErrorFractionalScaleExists), "fractional scale already exists")
		return
	}
	f, err := fractionalscale.NewWpFractionalScaleV1(r.Client(), r.Version(), id, fraction{})
	if err != nil {
		return
	}
	s.fractions[surf] = f
	f.OnDestroy = func() { delete(s.fractions, surf) }
	scale := 1.0
	if o := s.outputOfSurface(surf); o != nil {
		scale = o.place.Scale
	}
	f.SendPreferredScale(uint32(math.Round(scale * 120)))
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
