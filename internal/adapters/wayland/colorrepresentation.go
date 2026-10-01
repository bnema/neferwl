package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/colorrepresentation"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// The absent YUV representation defaults to BT.709 limited, type 0 chroma.
// RGB without a representation retains premultiplied electrical samples.
type surfaceRepresentation struct{ coefficients, rangeValue, chroma, alpha uint8 }

const (
	coefficientIdentity = uint8(colorrepresentation.WpColorRepresentationSurfaceV1CoefficientsIdentity)
	coefficient709      = uint8(colorrepresentation.WpColorRepresentationSurfaceV1CoefficientsBt709)
	coefficient601      = uint8(colorrepresentation.WpColorRepresentationSurfaceV1CoefficientsBt601)
	coefficient2020     = uint8(colorrepresentation.WpColorRepresentationSurfaceV1CoefficientsBt2020)
	rangeFull           = uint8(colorrepresentation.WpColorRepresentationSurfaceV1RangeFull)
	rangeLimited        = uint8(colorrepresentation.WpColorRepresentationSurfaceV1RangeLimited)
)

func isYUV(format uint32) bool { return format == fourccNV12 || format == fourccP010 }

func registerColorRepresentation(d *server.Display, s *Server) error {
	return colorrepresentation.NewWpColorRepresentationManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		r, err := colorrepresentation.NewWpColorRepresentationManagerV1(c, int32(v), id, representationManager{s})
		if err != nil {
			return
		}
		r.SendSupportedAlphaMode(uint32(colorrepresentation.WpColorRepresentationSurfaceV1AlphaModePremultipliedElectrical))
		r.SendSupportedCoefficientsAndRanges(uint32(coefficientIdentity), uint32(rangeFull))
		for _, coeff := range []uint8{coefficient601, coefficient709, coefficient2020} {
			for _, ran := range []uint8{rangeLimited, rangeFull} {
				r.SendSupportedCoefficientsAndRanges(uint32(coeff), uint32(ran))
			}
		}
		r.SendDone()
	})
}

type representationManager struct{ server *Server }

func (representationManager) Destroy(*colorrepresentation.WpColorRepresentationManagerV1) {}
func (m representationManager) GetSurface(r *colorrepresentation.WpColorRepresentationManagerV1, id uint32, wl *wayland.Surface) {
	surf := m.server.surfaceOf(wl)
	if surf != nil && surf.representationControl != nil {
		r.PostError(uint32(colorrepresentation.WpColorRepresentationManagerV1ErrorSurfaceExists), "surface already has color representation")
		return
	}
	h := &representationSurface{surf: surf}
	res, err := colorrepresentation.NewWpColorRepresentationSurfaceV1(r.Client(), r.Version(), id, h)
	if err != nil {
		return
	}
	h.res = res
	if surf != nil {
		surf.representationControl = h
		res.OnDestroy = func() {
			if surf.representationControl == h {
				surf.representationControl = nil
				surf.next.representation = surfaceRepresentation{}
			}
		}
	}
}

type representationSurface struct {
	surf *surface
	res  *colorrepresentation.WpColorRepresentationSurfaceV1
}

func (h *representationSurface) Destroy(*colorrepresentation.WpColorRepresentationSurfaceV1) {
	if h.surf != nil && !h.surf.destroyed {
		h.surf.next.representation = surfaceRepresentation{}
	}
}
func (h *representationSurface) live(r *colorrepresentation.WpColorRepresentationSurfaceV1) bool {
	if h.surf == nil || h.surf.destroyed {
		r.PostError(uint32(colorrepresentation.WpColorRepresentationSurfaceV1ErrorInert), "surface destroyed")
		return false
	}
	return true
}
func (h *representationSurface) SetAlphaMode(r *colorrepresentation.WpColorRepresentationSurfaceV1, mode uint32) {
	if !h.live(r) {
		return
	}
	if mode != uint32(colorrepresentation.WpColorRepresentationSurfaceV1AlphaModePremultipliedElectrical) {
		r.PostError(uint32(colorrepresentation.WpColorRepresentationSurfaceV1ErrorAlphaMode), "unsupported alpha mode")
		return
	}
	h.surf.next.representation.alpha = uint8(mode)
}
func (h *representationSurface) SetCoefficientsAndRange(r *colorrepresentation.WpColorRepresentationSurfaceV1, coeff, ran uint32) {
	if !h.live(r) {
		return
	}
	valid := coeff == uint32(coefficientIdentity) && ran == uint32(rangeFull) ||
		(coeff == uint32(coefficient601) || coeff == uint32(coefficient709) || coeff == uint32(coefficient2020)) && (ran == uint32(rangeFull) || ran == uint32(rangeLimited))
	if !valid {
		r.PostError(uint32(colorrepresentation.WpColorRepresentationSurfaceV1ErrorCoefficients), "unsupported coefficients or range")
		return
	}
	h.surf.next.representation.coefficients, h.surf.next.representation.rangeValue = uint8(coeff), uint8(ran)
}
func (h *representationSurface) SetChromaLocation(r *colorrepresentation.WpColorRepresentationSurfaceV1, chroma uint32) {
	if !h.live(r) {
		return
	}
	if chroma != uint32(colorrepresentation.WpColorRepresentationSurfaceV1ChromaLocationType0) {
		r.PostError(uint32(colorrepresentation.WpColorRepresentationSurfaceV1ErrorChromaLocation), "unsupported chroma location")
		return
	}
	h.surf.next.representation.chroma = uint8(chroma)
}

// The metadata is checked against the buffer the commit will show,
// including when a client changes representation without attaching a new
// buffer: then the one of the last queued commit that attached, or the
// current one.
func (s *surface) checkRepresentationCommit() bool {
	rep := s.next.representation
	if rep.coefficients == 0 && rep.chroma == 0 {
		return true
	}
	buffer := s.current
	for i := len(s.queue) - 1; i >= 0; i-- {
		if s.queue[i].attached {
			buffer = s.queue[i].buffer
			break
		}
	}
	if s.next.attached {
		buffer = s.next.buffer
	}
	if buffer == nil {
		return true
	}
	b, ok := s.server.buffers[buffer.Resource]
	if !ok {
		return true
	}
	d, ok := b.(*dmabufBuffer)
	yuv := ok && isYUV(d.buf.Format)
	bad := rep.coefficients != 0 && (rep.coefficients == coefficientIdentity) == yuv || rep.chroma != 0 && !yuv
	if bad && s.representationControl != nil && s.representationControl.res != nil {
		s.representationControl.res.PostError(uint32(colorrepresentation.WpColorRepresentationSurfaceV1ErrorPixelFormat), "color representation incompatible with pixel format")
		return false
	}
	return true
}
