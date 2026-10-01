package wayland

import (
	"encoding/binary"
	"io"
	"os"
	"slices"

	"github.com/bnema/go-wayland-bindings/server/linuxdmabuf"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// linux-dmabuf v4: clients hand GPU buffers to the renderer without copies.
// The formats are those the renderer imports (ports.DMABufSupport); without
// any, the global is not advertised and clients use wl_shm. Surface
// feedback also offers, first, a scanout tranche while the surface's
// window is fullscreen on an output that reported scanout formats
// (ports.OutputFormats): a buffer allocated from it can be flipped to the
// display with no composition.

const dmabufVersion = 4

// maxPlanes is the protocol limit (DRM planes).
const maxPlanes = 4

func registerDMABuf(d *server.Display, s *Server, sup ports.DMABufSupport) error {
	if len(sup.Formats) == 0 {
		return nil
	}
	table, err := formatTable(sup.Formats)
	if err != nil {
		return err
	}
	s.dmabuf = &dmabufGlobal{server: s, support: sup, table: table, scanout: map[string]ports.OutputFormats{}, feedbacks: map[*linuxdmabuf.ZwpLinuxDmabufFeedbackV1]*surface{}}
	return linuxdmabuf.NewZwpLinuxDmabufV1Global(d, dmabufVersion, func(c server.Client, v, id uint32) {
		r, err := linuxdmabuf.NewZwpLinuxDmabufV1(c, int32(v), id, s.dmabuf)
		if err != nil || v >= 4 {
			return
		}
		// Before v4 formats come as events on bind.
		for _, f := range sup.Formats {
			if v >= 3 {
				r.SendModifier(f.Format, uint32(f.Modifier>>32), uint32(f.Modifier))
			} else if f.Modifier == 0 {
				r.SendFormat(f.Format)
			}
		}
	})
}

// formatTable is the shared memfd of 16-byte entries (u32 format, 4 bytes
// padding, u64 modifier) feedback clients map read-only.
func formatTable(formats []ports.DMABufFormat) (*os.File, error) {
	data := make([]byte, 16*len(formats))
	for i, f := range formats {
		binary.NativeEndian.PutUint32(data[i*16:], f.Format)
		binary.NativeEndian.PutUint64(data[i*16+8:], f.Modifier)
	}
	fd, err := unix.MemfdCreate("neferwl-dmabuf-formats", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "dmabuf-formats")
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	// Clients map it: it must never shrink or change.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

type dmabufGlobal struct {
	server  *Server
	support ports.DMABufSupport
	table   *os.File
	nextID  uint64
	// scanout are the outputs' direct scanout formats by output name.
	scanout map[string]ports.OutputFormats
	// feedbacks are the live feedback objects and their surface (nil:
	// default feedback).
	feedbacks map[*linuxdmabuf.ZwpLinuxDmabufFeedbackV1]*surface
}

func (g *dmabufGlobal) close() {
	if g != nil && g.table != nil {
		g.table.Close()
	}
}

func (*dmabufGlobal) Destroy(*linuxdmabuf.ZwpLinuxDmabufV1) {}

func (g *dmabufGlobal) CreateParams(r *linuxdmabuf.ZwpLinuxDmabufV1, id uint32) {
	p := &params{global: g}
	res, err := linuxdmabuf.NewZwpLinuxBufferParamsV1(r.Client(), r.Version(), id, p)
	if err != nil {
		return
	}
	p.resource = res
	res.OnDestroy = p.closeFiles
}

func (g *dmabufGlobal) GetDefaultFeedback(r *linuxdmabuf.ZwpLinuxDmabufV1, id uint32) {
	g.feedback(r, id, nil)
}

// GetSurfaceFeedback follows the surface: a scanout tranche comes first
// while it can be scanned out (see scanoutFor).
func (g *dmabufGlobal) GetSurfaceFeedback(r *linuxdmabuf.ZwpLinuxDmabufV1, id uint32, surf *wayland.Surface) {
	g.feedback(r, id, g.server.surfaceOf(surf))
}

func (g *dmabufGlobal) feedback(r *linuxdmabuf.ZwpLinuxDmabufV1, id uint32, surf *surface) {
	fb, err := linuxdmabuf.NewZwpLinuxDmabufFeedbackV1(r.Client(), r.Version(), id, feedback{})
	if err != nil {
		return
	}
	g.feedbacks[fb] = surf
	fb.OnDestroy = func() { delete(g.feedbacks, fb) }
	g.send(fb, surf)
}

// devBytes is a dev_t as the protocol's array.
func devBytes(dev uint64) []byte {
	b := make([]byte, 8)
	binary.NativeEndian.PutUint64(b, dev)
	return b
}

// send sends the whole feedback: table, main device, the scanout tranche
// when the surface has one, then the renderer tranche.
func (g *dmabufGlobal) send(fb *linuxdmabuf.ZwpLinuxDmabufFeedbackV1, surf *surface) {
	fb.SendFormatTable(int(g.table.Fd()), uint32(16*len(g.support.Formats)))
	main := devBytes(g.support.Device)
	fb.SendMainDevice(main)
	if out, ok := g.scanoutFor(surf); ok {
		var indices []byte
		for _, f := range out.Formats {
			if i := slices.Index(g.support.Formats, f); i >= 0 {
				indices = binary.NativeEndian.AppendUint16(indices, uint16(i))
			}
		}
		if len(indices) > 0 {
			fb.SendTrancheTargetDevice(devBytes(out.Device))
			fb.SendTrancheFlags(uint32(linuxdmabuf.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout))
			fb.SendTrancheFormats(indices)
			fb.SendTrancheDone()
		}
	}
	indices := make([]byte, 2*len(g.support.Formats))
	for i := range g.support.Formats {
		binary.NativeEndian.PutUint16(indices[i*2:], uint16(i))
	}
	fb.SendTrancheTargetDevice(main)
	fb.SendTrancheFlags(0)
	fb.SendTrancheFormats(indices)
	fb.SendTrancheDone()
	fb.SendDone()
}

// scanoutFor is the scanout offer for a surface: its window is fullscreen
// on an output that reported scanout formats.
func (g *dmabufGlobal) scanoutFor(surf *surface) (ports.OutputFormats, bool) {
	if surf == nil || surf.destroyed {
		return ports.OutputFormats{}, false
	}
	root := toplevelRoot(surf)
	if g.server.invisible(surf) || root.xdg == nil || root.xdg.window == nil || !root.xdg.window.hasLast || !root.xdg.window.last.Fullscreen {
		return ports.OutputFormats{}, false
	}
	out, ok := g.scanout[root.xdg.window.last.Output]
	return out, ok && len(out.Formats) > 0
}

// setOutputFormats records an output's scanout formats and sends the
// feedback of the surfaces fullscreen on it again. Outputs offer only
// formats the renderer samples (a refused buffer is composed), so they
// are all in the table.
func (g *dmabufGlobal) setOutputFormats(f ports.OutputFormats) {
	if old, ok := g.scanout[f.Output]; ok && old.Device == f.Device && slices.Equal(old.Formats, f.Formats) {
		return
	}
	if len(f.Formats) == 0 {
		delete(g.scanout, f.Output)
	} else {
		g.scanout[f.Output] = f
	}
	for fb, surf := range g.feedbacks {
		if surf != nil && !surf.destroyed {
			if root := toplevelRoot(surf); root.xdg != nil && root.xdg.window != nil && root.xdg.window.last.Fullscreen && root.xdg.window.last.Output == f.Output {
				g.send(fb, surf)
			}
		}
	}
}

// resendSurface sends the feedback of a window's surfaces again, when its
// fullscreen state or output changed.
func (g *dmabufGlobal) resendSurface(root *surface) {
	if g == nil {
		return
	}
	for fb, surf := range g.feedbacks {
		// Popups follow their toplevel's offer (scanoutFor).
		if surf != nil && toplevelRoot(surf) == root {
			g.send(fb, surf)
		}
	}
}

type feedback struct{}

func (feedback) Destroy(*linuxdmabuf.ZwpLinuxDmabufFeedbackV1) {}

// params collects the planes of one buffer.
type params struct {
	global   *dmabufGlobal
	resource *linuxdmabuf.ZwpLinuxBufferParamsV1
	planes   [maxPlanes]*plane
	modifier uint64
	used     bool
}

type plane struct {
	file           *os.File
	offset, stride uint32
}

func (p *params) closeFiles() {
	for i, pl := range p.planes {
		if pl != nil {
			pl.file.Close()
			p.planes[i] = nil
		}
	}
}

func (p *params) Destroy(*linuxdmabuf.ZwpLinuxBufferParamsV1) {}

func (p *params) Add(r *linuxdmabuf.ZwpLinuxBufferParamsV1, fd int, idx, offset, stride, modHi, modLo uint32) {
	f := os.NewFile(uintptr(fd), "dmabuf")
	switch {
	case p.used:
		f.Close()
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorAlreadyUsed), "params already used")
	case idx >= maxPlanes:
		f.Close()
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorPlaneIdx), "plane index out of bounds")
	case p.planes[idx] != nil:
		f.Close()
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorPlaneSet), "plane already set")
	default:
		mod := uint64(modHi)<<32 | uint64(modLo)
		if p.anyPlane() && mod != p.modifier {
			f.Close()
			r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidFormat), "planes with different modifiers")
			return
		}
		p.modifier = mod
		p.planes[idx] = &plane{file: f, offset: offset, stride: stride}
	}
}

func (p *params) anyPlane() bool {
	return slices.ContainsFunc(p.planes[:], func(pl *plane) bool { return pl != nil })
}

const (
	fourccNV12 = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
	fourccP010 = 'P' | '0'<<8 | '1'<<16 | '0'<<24
	fourccAB4H = 'A' | 'B'<<8 | '4'<<16 | 'H'<<24
	fourccXB4H = 'X' | 'B'<<8 | '4'<<16 | 'H'<<24
)

// build validates the planes and turns them into a buffer; ok is false after
// a protocol error, failed means the client gets `failed`.
func (p *params) build(width, height int32, format, flags uint32) (buf *dmabufBuffer, failed, ok bool) {
	r := p.resource
	if p.used {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorAlreadyUsed), "params already used")
		return nil, false, false
	}
	p.used = true
	n := 0
	for n < maxPlanes && p.planes[n] != nil {
		n++
	}
	if n == 0 || slices.ContainsFunc(p.planes[n:], func(pl *plane) bool { return pl != nil }) {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorIncomplete), "missing planes")
		return nil, false, false
	}
	if width <= 0 || height <= 0 {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidDimensions), "invalid dimensions")
		return nil, false, false
	}
	// Unsupported formats, y-inverted or interlaced buffers: the client
	// falls back to another format or to wl_shm.
	want := ports.DMABufFormat{Format: format, Modifier: p.modifier}
	if flags != 0 || !slices.Contains(p.global.support.Formats, want) {
		return nil, true, true
	}
	// NV12/P010 are 4:2:0 two-plane images. The same modifier must
	// describe both planes; each plane may have its own fd and offset.
	yuv := isYUV(format)
	wantPlanes := 1
	if yuv {
		wantPlanes = 2
	}
	if n != wantPlanes {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorIncomplete), "wrong plane count")
		return nil, false, false
	}
	if yuv && (width%2 != 0 || height%2 != 0) {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidDimensions), "4:2:0 requires even dimensions")
		return nil, false, false
	}
	for i, pl := range p.planes[:n] {
		row, rows := uint64(width)*4, uint64(height)
		if yuv {
			bytesPerSample := uint64(1)
			if format == fourccP010 {
				bytesPerSample = 2
			}
			row = uint64(width) * bytesPerSample
			if i == 1 {
				rows /= 2
			}
		} else if format == fourccAB4H || format == fourccXB4H {
			row = uint64(width) * 8
		}
		end := uint64(pl.offset) + uint64(pl.stride)*(rows-1) + row
		size, err := pl.file.Seek(0, io.SeekEnd)
		if uint64(pl.stride) < row || (err == nil && end > uint64(size)) {
			r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorOutOfBounds), "plane out of bounds")
			return nil, false, false
		}
	}
	p.global.nextID++
	d := &ports.DMABuf{ID: p.global.nextID, Width: int(width), Height: int(height), Format: format, Modifier: p.modifier}
	for _, pl := range p.planes[:n] {
		d.Planes = append(d.Planes, ports.DMABufPlane{File: pl.file, Offset: pl.offset, Stride: pl.stride})
	}
	// The buffer owns the files now.
	p.planes = [maxPlanes]*plane{}
	return &dmabufBuffer{buf: d}, false, true
}

func (p *params) Create(r *linuxdmabuf.ZwpLinuxBufferParamsV1, width, height int32, format, flags uint32) {
	b, failed, ok := p.build(width, height, format, flags)
	if !ok {
		return
	}
	if failed {
		r.SendFailed()
		return
	}
	// A server-side wl_buffer: id 0 asks libwayland to allocate one.
	res, err := wayland.NewBuffer(r.Client(), 1, 0, b)
	if err != nil {
		b.close()
		r.SendFailed()
		return
	}
	p.global.server.addBuffer(res, b)
	r.SendCreated(res)
}

func (p *params) CreateImmed(r *linuxdmabuf.ZwpLinuxBufferParamsV1, id uint32, width, height int32, format, flags uint32) {
	b, failed, ok := p.build(width, height, format, flags)
	if !ok {
		return
	}
	if failed {
		// Immediate creation has no failure event: the protocol says to
		// raise invalid_wl_buffer.
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidWlBuffer), "unsupported buffer")
		return
	}
	res, err := wayland.NewBuffer(r.Client(), 1, id, b)
	if err != nil {
		b.close()
		return
	}
	p.global.server.addBuffer(res, b)
}

func (*params) SetSamplingDevice(*linuxdmabuf.ZwpLinuxBufferParamsV1, []byte) {}

// dmabufBuffer is a wl_buffer backed by client GPU memory.
type dmabufBuffer struct{ buf *ports.DMABuf }

func (*dmabufBuffer) Destroy(*wayland.Buffer) {}

func (b *dmabufBuffer) close() {
	for _, pl := range b.buf.Planes {
		pl.File.Close()
	}
}

func (b *dmabufBuffer) size() (int, int) { return b.buf.Width, b.buf.Height }

// content hands the renderer the buffer itself: nothing is copied.
func (b *dmabufBuffer) content(id ports.WindowID) (ports.SurfaceContent, bool) {
	opaque := b.buf.Format == fourccXRGB || b.buf.Format == fourccXBGR || b.buf.Format == fourccXR30 || b.buf.Format == fourccXB30
	return ports.SurfaceContent{ID: id, Width: b.buf.Width, Height: b.buf.Height, Opaque: opaque, DMABuf: b.buf}, true
}

const (
	fourccXRGB = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	fourccXBGR = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
	fourccXR30 = 'X' | 'R'<<8 | '3'<<16 | '0'<<24
	fourccXB30 = 'X' | 'B'<<8 | '3'<<16 | '0'<<24
)
