package capture

import (
	"errors"
	"image"
	"os"

	"github.com/bnema/neferwl/internal/domain"
)

// pixelsFrame is a capture read back on the CPU (Renderer.Pixels): the
// fallback of a pipeline with CPUReadback when the renderer cannot track a
// GPU copy (no sync-file export, e.g. lavapipe). It holds no renderer lease:
// Recycle does not end it. The image is never written after creation, so the
// worker reads it without synchronization.
type pixelsFrame struct{ img *image.RGBA }

var errPixelsDMABuf = errors.New("capture: CPU readback cannot write dmabufs")

// Done is nil: Pixels waited for the GPU before the frame existed.
func (f *pixelsFrame) Done() *os.File { return nil }

// Read writes region as opaque BGRA rows, like a GPU capture.
func (f *pixelsFrame) Read(region image.Rectangle, dst []byte, stride int) error {
	w, h := region.Dx(), region.Dy()
	need := w * 4
	if region.Empty() || !region.In(f.img.Rect) || stride < need || len(dst) < need {
		return errors.New("invalid capture region or destination")
	}
	if h > 1 && stride > (len(dst)-need)/(h-1) {
		return errors.New("capture destination too small for stride")
	}
	for y := range h {
		src := f.img.Pix[f.img.PixOffset(region.Min.X, region.Min.Y+y):][:need]
		row := dst[y*stride:][:need]
		for x := 0; x < need; x += 4 {
			row[x], row[x+1], row[x+2], row[x+3] = src[x+2], src[x+1], src[x], 255
		}
	}
	return nil
}

// CopyTo refuses: a CPU readback has no GPU image to copy from.
func (f *pixelsFrame) CopyTo(image.Rectangle, domain.DMABuf) (*os.File, error) {
	return nil, errPixelsDMABuf
}
