package wayland

import (
	"encoding/binary"
	"io"
	"os"
	"slices"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/linuxdmabuf"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// linux-dmabuf v4: clients hand GPU buffers to the renderer without copies.
// The formats are those the renderer imports (ports.DMABufSupport); without
// any, the global is not advertised and clients use wl_shm.

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
	s.dmabuf = &dmabufGlobal{server: s, support: sup, table: table}
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
	fd, err := unix.MemfdCreate("nefertty-dmabuf-formats", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
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
	g.feedback(r, id)
}

// GetSurfaceFeedback sends the default feedback: one renderer, one tranche.
// Scanout tranches come with direct scanout.
func (g *dmabufGlobal) GetSurfaceFeedback(r *linuxdmabuf.ZwpLinuxDmabufV1, id uint32, _ *wayland.Surface) {
	g.feedback(r, id)
}

func (g *dmabufGlobal) feedback(r *linuxdmabuf.ZwpLinuxDmabufV1, id uint32) {
	fb, err := linuxdmabuf.NewZwpLinuxDmabufFeedbackV1(r.Client(), r.Version(), id, feedback{})
	if err != nil {
		return
	}
	dev := make([]byte, 8)
	binary.NativeEndian.PutUint64(dev, g.support.Device)
	indices := make([]byte, 2*len(g.support.Formats))
	for i := range g.support.Formats {
		binary.NativeEndian.PutUint16(indices[i*2:], uint16(i))
	}
	fb.SendFormatTable(int(g.table.Fd()), uint32(16*len(g.support.Formats)))
	fb.SendMainDevice(dev)
	fb.SendTrancheTargetDevice(dev)
	fb.SendTrancheFlags(0)
	fb.SendTrancheFormats(indices)
	fb.SendTrancheDone()
	fb.SendDone()
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
	// Supported formats are single-plane, 4 bytes per pixel.
	if n != 1 {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorIncomplete), "format takes one plane")
		return nil, false, false
	}
	pl := p.planes[0]
	row := uint64(width) * 4
	end := uint64(pl.offset) + uint64(pl.stride)*uint64(height-1) + row
	size, err := pl.file.Seek(0, io.SeekEnd)
	if uint64(pl.stride) < row || (err == nil && end > uint64(size)) {
		r.PostError(uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorOutOfBounds), "plane out of bounds")
		return nil, false, false
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
	opaque := b.buf.Format == fourccXRGB || b.buf.Format == fourccXBGR
	return ports.SurfaceContent{ID: id, Width: b.buf.Width, Height: b.buf.Height, Opaque: opaque, DMABuf: b.buf}, true
}

const (
	fourccXRGB = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	fourccXBGR = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
)
