package drm

import (
	"github.com/bnema/neferwl/internal/adapters/drm/gbm"
	"github.com/bnema/neferwl/internal/ports"
)

// imageAllocator makes the output images with the display driver's own
// allocator (GBM), which knows the layouts the driver scans out correctly:
// images allocated by the renderer show corrupted on NVIDIA. One per
// output, used by its goroutine only.
type imageAllocator interface {
	// create allocates an image whose modifier is one of modifiers.
	create(width, height int, format uint32, modifiers []uint64) (imageBuffer, error)
	close()
}

// imageBuffer is one allocated image, kept alive while it is shown.
type imageBuffer interface {
	// dmabuf returns the image with fresh descriptors the caller closes.
	dmabuf() (ports.DMABuf, error)
	close()
}

// gbmAllocator allocates scanout-capable, renderable images with libgbm.
type gbmAllocator struct{ dev *gbm.Device }

// openGBM opens a GBM allocator on a duplicate of the card fd.
func openGBM(fd int) (imageAllocator, error) {
	dev, err := gbm.Open(fd)
	if err != nil {
		return nil, err
	}
	return gbmAllocator{dev}, nil
}

func (a gbmAllocator) create(width, height int, format uint32, modifiers []uint64) (imageBuffer, error) {
	b, err := a.dev.Create(width, height, format, modifiers, gbm.UseScanout|gbm.UseRendering)
	if err != nil {
		return nil, err
	}
	return gbmImage{b}, nil
}

func (a gbmAllocator) close() { a.dev.Close() }

type gbmImage struct{ b *gbm.Buffer }

func (i gbmImage) dmabuf() (ports.DMABuf, error) { return i.b.DMABuf() }
func (i gbmImage) close()                        { i.b.Close() }
