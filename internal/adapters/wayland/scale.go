package wayland

import (
	"math"

	"github.com/bnema/go-wayland-bindings/server/fractionalscale"
	"github.com/bnema/go-wayland-bindings/server/viewporter"
	"github.com/bnema/go-wayland-bindings/server/wayland"
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
	case surf.lock != nil:
		if !surf.lock.closed {
			return surf.lock.output
		}
		return nil
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
		for h := range s.colorFeedbacks {
			if h.surf == surf {
				h.last = o
				h.changed(s.outputColor(o).identity)
			}
		}
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
	vp.resource = res
	res.OnDestroy = func() {
		// Destroying the viewport resets the size on the next commit.
		if surf.viewport == vp {
			surf.viewport = nil
		}
	}
}

// viewport holds requests for the next commit; the committed crop and
// destination live by value on the surface so applying cannot allocate.
type viewport struct {
	surface            *surface
	resource           *viewporter.WpViewport
	pendingW, pendingH int32
	pendingSet         bool
	pendingSrc         [4]server.Fixed
	pendingCrop        bool
}

type viewportState struct {
	destW, destH int32
	dest         bool
	src          [4]server.Fixed
	crop         bool
}

func (*viewport) Destroy(*viewporter.WpViewport) {}
func (v *viewport) SetSource(r *viewporter.WpViewport, x, y, w, h server.Fixed) {
	if v.surface.destroyed {
		r.PostError(uint32(viewporter.WpViewportErrorNoSurface), "surface destroyed")
		return
	}
	unset := x == -256 && y == -256 && w == -256 && h == -256
	if !unset && (x < 0 || y < 0 || w <= 0 || h <= 0) {
		r.PostError(uint32(viewporter.WpViewportErrorBadValue), "invalid source rectangle")
		return
	}
	v.pendingSrc, v.pendingCrop = [4]server.Fixed{x, y, w, h}, !unset
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

// source returns the crop in untransformed buffer pixels, precomputed at
// commit. The viewport source is in transformed, scaled surface
// coordinates: its corners map back through the buffer transform.
func (s *surface) source(bw, bh int) ([4]float32, bool) {
	if !s.committedViewport.crop {
		return [4]float32{}, false
	}
	v := s.committedViewport.src
	scale := float64(max(s.bufferScale, 1))
	// Transformed buffer size, in the surface's axes.
	tw, th := float64(bw), float64(bh)
	if s.transform.Rotated() {
		tw, th = th, tw
	}
	x0, y0 := float64(v[0])*scale/256, float64(v[1])*scale/256
	x1, y1 := x0+float64(v[2])*scale/256, y0+float64(v[3])*scale/256
	// validateViewport tolerates a rounding overshoot: sample inside the buffer.
	x1, y1 = min(x1, tw), min(y1, th)
	ax, ay := s.transform.ToBuffer(x0, y0, tw, th)
	bx, by := s.transform.ToBuffer(x1, y1, tw, th)
	return [4]float32{float32(min(ax, bx)), float32(min(ay, by)), float32(math.Abs(bx - ax)), float32(math.Abs(by - ay))}, true
}

// transformedSize is the buffer size in the surface's axes: width and height
// swap for a 90° or 270° buffer transform.
func (s *surface) transformedSize(bw, bh int) (int, int) {
	if s.transform.Rotated() {
		return bh, bw
	}
	return bw, bh
}

func (s *surface) validateViewport(bw, bh int) bool {
	bw, bh = s.transformedSize(bw, bh)
	v := &s.committedViewport
	if !v.crop {
		return true
	}
	if !v.dest && (v.src[2]%256 != 0 || v.src[3]%256 != 0) {
		s.viewportError(viewporter.WpViewportErrorBadSize, "non-integer source size without destination")
		return false
	}
	// int64 prevents overflow when adding signed fixed coordinates.
	// Position and size are each rounded to 1/256 by the client, so an edge
	// computed in floating point can land up to two units past the buffer:
	// that rounding is tolerated, anything larger is a real overflow.
	const rounding = 2
	if int64(v.src[0])+int64(v.src[2]) > int64(bw)*256/int64(max(s.bufferScale, 1))+rounding ||
		int64(v.src[1])+int64(v.src[3]) > int64(bh)*256/int64(max(s.bufferScale, 1))+rounding {
		// The error disconnects the client: log every occurrence, with the
		// source edges and limits in pixels to show which bound failed.
		{
			limitW, limitH := float64(bw)/float64(max(s.bufferScale, 1)), float64(bh)/float64(max(s.bufferScale, 1))
			surfaceID, viewportID, bufferID := uint32(0), uint32(0), uint32(0)
			if s.wl != nil {
				surfaceID = s.wl.ID()
			}
			if s.viewport != nil && s.viewport.resource != nil {
				viewportID = s.viewport.resource.ID()
			}
			if s.current != nil {
				bufferID = s.current.ID()
			}
			s.server.log.Warn().Str("component", "wayland").Uint32("surface", surfaceID).Uint32("viewport", viewportID).
				Bool("fresh", s.commitFresh).Bool("retained", !s.commitFresh).Bool("skipped_destroyed", s.commitSkipped).
				Uint32("buffer", bufferID).Uint32("queued_buffer", s.queuedBuffer).Int("buffer_width", bw).Int("buffer_height", bh).
				Int("committed_scale", s.bufferScale).Int("queued_scale", s.queuedScale).
				Ints32("raw_source", []int32{int32(v.src[0]), int32(v.src[1]), int32(v.src[2]), int32(v.src[3])}).
				Float64("source_right", float64(int64(v.src[0])+int64(v.src[2]))/256).Float64("limit_w", limitW).
				Float64("source_bottom", float64(int64(v.src[1])+int64(v.src[3]))/256).Float64("limit_h", limitH).
				Bool("destination_set", v.dest).Msg("viewport source exceeds buffer")
		}
		s.viewportError(viewporter.WpViewportErrorOutOfBuffer, "source exceeds buffer")
		return false
	}
	return true
}

func (s *surface) viewportError(code viewporter.WpViewportError, message string) {
	if s.viewport != nil && s.viewport.resource != nil {
		s.viewport.resource.PostError(uint32(code), message)
	}
}

// logicalSize is the surface size in logical pixels: the viewport
// destination, else the transformed buffer divided by its buffer scale.
func (s *surface) logicalSize(bw, bh int) (int, int) {
	bw, bh = s.transformedSize(bw, bh)
	if s.committedViewport.dest {
		return int(s.committedViewport.destW), int(s.committedViewport.destH)
	}
	if s.committedViewport.crop {
		return int(s.committedViewport.src[2]) / 256, int(s.committedViewport.src[3]) / 256
	}
	scale := max(s.bufferScale, 1)
	return bw / scale, bh / scale
}
