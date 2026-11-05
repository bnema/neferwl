package screencast

import (
	"errors"
	"fmt"
	"slices"

	"bnema.dev/purego-pipewire/pipewire"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Casts are DMA-BUF only: the PipeWire buffers are GPU images the device
// exports (ports.CastBufferAllocator), and frames are copied into them on
// the GPU. No memfd buffer is offered (VideoDMABuf.Strict), so a consumer
// that cannot take DMA-BUF ends the cast instead of getting CPU frames.

// maxCastBuffers is the size of the source's buffer table (videoMaxBuffers
// in purego-pipewire): the indexes Alloc may be called with.
const maxCastBuffers = 16

// errNoCastFormats: the GPU exports no cast buffer.
var errNoCastFormats = errors.New("screencast: the GPU exports no DMA-BUF cast format")

// castAllocator is the pipewire.VideoAllocator of one stream. PipeWire calls
// it on the loop thread, which is the owner goroutine (inside Iterate).
type castAllocator struct {
	dev  ports.CastBufferAllocator
	bufs [maxCastBuffers]domain.DMABuf
}

// fourccOf is the cast format of a video format; ok is false for others.
func fourccOf(f pipewire.VideoFormat) (uint32, bool) {
	switch f {
	case pipewire.VideoFormatBGRx:
		return domain.CastXRGB8888, true
	case pipewire.VideoFormatBGRA:
		return domain.CastARGB8888, true
	case pipewire.VideoFormatXBGR210LE:
		return domain.CastXBGR2101010, true
	}
	return 0, false
}

// videoFormat is the video format of a cast format.
func videoFormat(f uint32) pipewire.VideoFormat {
	switch f {
	case domain.CastARGB8888:
		return pipewire.VideoFormatBGRA
	case domain.CastXBGR2101010:
		return pipewire.VideoFormatXBGR210LE
	}
	return pipewire.VideoFormatBGRx
}

// dmabufFormats groups the device's cast formats by format, in the order
// of domain.CastFormats(true) (every cast format), for VideoDMABuf.Formats.
func dmabufFormats(listed []domain.DMABufFormat) []pipewire.VideoDMABufFormat {
	var out []pipewire.VideoDMABufFormat
	for _, fc := range domain.CastFormats(true) {
		var mods []uint64
		for _, f := range listed {
			if f.Format == fc && !slices.Contains(mods, f.Modifier) {
				mods = append(mods, f.Modifier)
			}
		}
		if len(mods) > 0 {
			out = append(out, pipewire.VideoDMABufFormat{Format: videoFormat(fc), Modifiers: mods})
		}
	}
	return out
}

// Fixate picks the first candidate the device exports for f, and reports
// its plane count from a trial buffer of the stream's size.
func (a *castAllocator) Fixate(f pipewire.VideoFormat, w, h int, candidates []uint64) (uint64, int, error) {
	fc, ok := fourccOf(f)
	if !ok {
		return 0, 0, fmt.Errorf("screencast: video format %d is not a cast format", f)
	}
	listed := a.dev.CastFormats()
	var last error
	for _, m := range candidates {
		if !slices.Contains(listed, domain.DMABufFormat{Format: fc, Modifier: m}) {
			continue
		}
		buf, err := a.dev.AllocCast(fc, w, h, m)
		if err != nil {
			last = err
			continue
		}
		n := len(buf.Planes)
		a.release(&buf)
		return m, n, nil
	}
	if last == nil {
		last = fmt.Errorf("screencast: no modifier the consumer takes for format %#x", fc)
	}
	return 0, 0, last
}

// Alloc exports buffer idx. A failure leaves nothing allocated.
func (a *castAllocator) Alloc(idx int, f pipewire.VideoFormat, w, h int, modifier uint64, out *pipewire.VideoPlanes) error {
	if idx < 0 || idx >= len(a.bufs) {
		return fmt.Errorf("screencast: buffer index %d out of range", idx)
	}
	fc, ok := fourccOf(f)
	if !ok {
		return fmt.Errorf("screencast: video format %d is not a cast format", f)
	}
	a.release(&a.bufs[idx])
	buf, err := a.dev.AllocCast(fc, w, h, modifier)
	if err != nil {
		return err
	}
	if len(buf.Planes) < 1 || len(buf.Planes) > len(out.P) {
		a.release(&buf)
		return fmt.Errorf("screencast: cast buffer with %d planes", len(buf.Planes))
	}
	// The planes share one object: its size bounds every plane.
	size, err := unix.Seek(int(buf.Planes[0].File.Fd()), 0, unix.SEEK_END)
	if err != nil || size <= 0 || size > 1<<32-1 {
		a.release(&buf)
		return fmt.Errorf("screencast: cast buffer size %d: %v", size, err)
	}
	*out = pipewire.VideoPlanes{N: len(buf.Planes), Size: uint32(size)}
	for i, p := range buf.Planes {
		if int64(p.Offset) >= size {
			a.release(&buf)
			*out = pipewire.VideoPlanes{}
			return fmt.Errorf("screencast: plane %d offset %d past the buffer", i, p.Offset)
		}
		out.P[i] = pipewire.VideoPlane{FD: int(p.File.Fd()), Offset: p.Offset, Stride: p.Stride, Size: uint32(size) - p.Offset}
	}
	a.bufs[idx] = buf
	return nil
}

// Free releases buffer idx's retained allocation before closing its files.
func (a *castAllocator) Free(idx int) {
	if idx >= 0 && idx < len(a.bufs) {
		a.release(&a.bufs[idx])
	}
}

// buffer is the dmabuf of a dequeued buffer, nil when it has none.
func (a *castAllocator) buffer(b *pipewire.VideoBuffer) *domain.DMABuf {
	if a == nil || !b.DMABuf || b.Index < 0 || b.Index >= len(a.bufs) || len(a.bufs[b.Index].Planes) == 0 {
		return nil
	}
	return &a.bufs[b.Index]
}

// freeAll closes every buffer still held: the source is gone.
func (a *castAllocator) freeAll() {
	for i := range a.bufs {
		a.release(&a.bufs[i])
	}
}

// closePlanes closes b's files and clears it.
func closePlanes(b *domain.DMABuf) {
	for _, p := range b.Planes {
		if p.File != nil {
			_ = p.File.Close()
		}
	}
	*b = domain.DMABuf{}
}

// release runs for remove-buffer, replacement, failed allocation and shutdown.
func (a *castAllocator) release(b *domain.DMABuf) {
	if len(b.Planes) > 0 {
		a.dev.ReleaseCast(*b)
	}
	closePlanes(b)
}
