package wayland

import (
	"math"

	"github.com/bnema/go-wayland-bindings/server/singlepixelbuffer"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// wp_single_pixel_buffer_v1: a 1x1 wl_buffer of one color. Clients use it
// for solid backgrounds and letterbox bars; the renderer draws it as a fill
// (ports.SurfaceContent.Solid) and no pixels are ever read or imported.

func registerSinglePixelBuffer(d *server.Display, s *Server) error {
	return singlepixelbuffer.NewWpSinglePixelBufferManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = singlepixelbuffer.NewWpSinglePixelBufferManagerV1(c, int32(v), id, singlePixelManager{s})
	})
}

type singlePixelManager struct{ server *Server }

func (singlePixelManager) Destroy(*singlepixelbuffer.WpSinglePixelBufferManagerV1) {}

// CreateU32RgbaBuffer takes 32-bit components already premultiplied by
// alpha, like client pixels; they become unit floats.
func (m singlePixelManager) CreateU32RgbaBuffer(r *singlepixelbuffer.WpSinglePixelBufferManagerV1, id, red, green, blue, alpha uint32) {
	unit := func(v uint32) float32 { return float32(float64(v) / math.MaxUint32) }
	buf := &singlePixelBuffer{
		color:  ports.SolidColor{R: unit(red), G: unit(green), B: unit(blue), A: unit(alpha)},
		opaque: alpha == math.MaxUint32,
	}
	res, err := wayland.NewBuffer(r.Client(), 1, id, buf)
	if err != nil {
		return
	}
	m.server.addBuffer(res, buf)
}

// singlePixelBuffer is a wl_buffer of one color. The color is never mutated,
// so content shares it between commits.
type singlePixelBuffer struct {
	color  ports.SolidColor
	opaque bool
}

func (*singlePixelBuffer) Destroy(*wayland.Buffer) {}

func (*singlePixelBuffer) size() (int, int) { return 1, 1 }

func (b *singlePixelBuffer) content(id ports.WindowID) (ports.SurfaceContent, bool) {
	return ports.SurfaceContent{ID: id, Width: 1, Height: 1, Opaque: b.opaque, Solid: &b.color}, true
}
