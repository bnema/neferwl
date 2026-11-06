package screencast

import (
	"errors"
	"os"
	"sync"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// CurrentBuffers resolves the current render device for formats and each new
// allocation. Releases remain bound to the device that created the buffer,
// including when PipeWire retires a pool after render-device recovery.
func CurrentBuffers(current func() ports.CastBufferAllocator) ports.CastBufferAllocator {
	return &currentBuffers{current: current, owners: make(map[*os.File]ports.CastBufferAllocator)}
}

type currentBuffers struct {
	mu      sync.Mutex
	current func() ports.CastBufferAllocator
	owners  map[*os.File]ports.CastBufferAllocator
}

func (a *currentBuffers) CastFormats() []domain.DMABufFormat {
	if dev := a.current(); dev != nil {
		return dev.CastFormats()
	}
	return nil
}

func (a *currentBuffers) AllocCast(format uint32, w, h int, modifier uint64) (domain.DMABuf, error) {
	dev := a.current()
	if dev == nil {
		return domain.DMABuf{}, errors.New("screencast: no render device")
	}
	buf, err := dev.AllocCast(format, w, h, modifier)
	if err != nil {
		return buf, err
	}
	if len(buf.Planes) > 0 {
		a.mu.Lock()
		a.owners[buf.Planes[0].File] = dev
		a.mu.Unlock()
	}
	return buf, nil
}

func (a *currentBuffers) ReleaseCast(buf domain.DMABuf) {
	if len(buf.Planes) == 0 {
		return
	}
	a.mu.Lock()
	dev := a.owners[buf.Planes[0].File]
	delete(a.owners, buf.Planes[0].File)
	a.mu.Unlock()
	if dev != nil {
		dev.ReleaseCast(buf)
	}
}
