package wayland

import (
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
	"runtime/debug"
)

func registerGlobals(d *server.Display, o Options, s *Server) error {
	for _, register := range []func() error{
		func() error { return registerXDG(d, s) },
		func() error { return registerLayer(d, s) },
		func() error { return registerXDGOutput(d, s) },
		func() error { return registerDecoration(d, s) },
		func() error { return registerScale(d, s) },
		func() error { return registerDMABuf(d, s, o.DMABuf) },
		func() error {
			return wayland.NewCompositorGlobal(d, 6, func(c server.Client, v, id uint32) { wayland.NewCompositor(c, int32(v), id, compositor{s}) })
		},
		func() error {
			return wayland.NewSubcompositorGlobal(d, 1, func(c server.Client, v, id uint32) {
				wayland.NewSubcompositor(c, int32(v), id, subcompositor{s})
			})
		},
		func() error {
			return wayland.NewShmGlobal(d, 1, func(c server.Client, v, id uint32) {
				r, e := wayland.NewShm(c, int32(v), id, shm{s})
				if e == nil {
					r.SendFormat(0)
					r.SendFormat(1)
				}
			})
		},
		func() error {
			return wayland.NewSeatGlobal(d, 7, func(c server.Client, v, id uint32) {
				r, e := wayland.NewSeat(c, int32(v), id, seat{s})
				if e == nil {
					capabilities := uint32(wayland.SeatCapabilityPointer)
					if s.keymapFD >= 0 {
						capabilities |= uint32(wayland.SeatCapabilityKeyboard)
					}
					r.SendCapabilities(capabilities)
					if v >= 2 {
						r.SendName("seat0")
					}
				}
			})
		},
		func() error {
			return wayland.NewDataDeviceManagerGlobal(d, 3, func(c server.Client, v, id uint32) { wayland.NewDataDeviceManager(c, int32(v), id, dataManager{}) })
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

type compositor struct{ server *Server }

func (c compositor) CreateSurface(r *wayland.Compositor, id uint32) {
	state := &surface{server: c.server, bufferScale: 1}
	if w, err := wayland.NewSurface(r.Client(), r.Version(), id, state); err == nil {
		state.wl = w
		c.server.surfaces[w.Resource] = state
		state.sendScale()
		w.OnDestroy = func() { delete(c.server.surfaces, w.Resource); state.Destroy(w) }
	}
}
func (compositor) CreateRegion(r *wayland.Compositor, id uint32) {
	wayland.NewRegion(r.Client(), r.Version(), id, region{})
}
func (compositor) Release(*wayland.Compositor) {}

type subcompositor struct{ server *Server }

func (subcompositor) Destroy(*wayland.Subcompositor) {}
func (c subcompositor) GetSubsurface(r *wayland.Subcompositor, id uint32, w, parent *wayland.Surface) {
	if w == nil || parent == nil {
		return
	}
	state, up := c.server.surfaces[w.Resource], c.server.surfaces[parent.Resource]
	if state == nil || up == nil {
		return
	}
	if state.kind != roleNone && state.kind != roleSubsurface || state.sub.role != nil {
		r.PostError(uint32(wayland.SubcompositorErrorBadSurface), "surface already has role")
		return
	}
	// A surface cannot be its own ancestor.
	for p := up; p != nil; p = p.sub.parent {
		if p == state {
			r.PostError(uint32(wayland.SubcompositorErrorBadParent), "parent is a descendant")
			return
		}
	}
	sub, err := wayland.NewSubsurface(r.Client(), 1, id, subsurface{state})
	if err != nil {
		return
	}
	state.kind = roleSubsurface
	state.role = func(bool) {}
	state.sub = subState{role: sub, parent: up, children: state.sub.children}
	up.sub.children = append(up.sub.children, state)
	// The child follows its root window's output and scale.
	state.sendScale()
	sub.OnDestroy = func() {
		if state.sub.role != sub {
			return
		}
		state.sub.role = nil
		state.role = nil
		state.detach()
	}
}

// Subsurfaces are drawn as their own commits arrive (desynchronized): the
// synchronized mode's cached state is not implemented.
type subsurface struct{ surface *surface }

func (subsurface) Destroy(*wayland.Subsurface) {}
func (s subsurface) SetPosition(_ *wayland.Subsurface, x, y int32) {
	s.surface.sub.pendX, s.surface.sub.pendY, s.surface.sub.moved = int(x), int(y), true
}
func (s subsurface) PlaceAbove(r *wayland.Subsurface, sibling *wayland.Surface) {
	s.restack(r, sibling, true)
}
func (s subsurface) PlaceBelow(r *wayland.Subsurface, sibling *wayland.Surface) {
	s.restack(r, sibling, false)
}
func (subsurface) SetSync(*wayland.Subsurface)   {}
func (subsurface) SetDesync(*wayland.Subsurface) {}

// restack moves the surface next to a sibling or its parent. The order
// applies at once rather than on the parent commit.
func (s subsurface) restack(r *wayland.Subsurface, sibling *wayland.Surface, above bool) {
	me := s.surface
	p := me.sub.parent
	if p == nil || sibling == nil {
		return
	}
	other := me.server.surfaces[sibling.Resource]
	if other == me || other == nil || other != p && other.sub.parent != p {
		r.PostError(uint32(wayland.SubsurfaceErrorBadSurface), "not a sibling or the parent")
		return
	}
	list := p.sub.children
	for i, ch := range list {
		if ch == me {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if other == p {
		// Relative to the parent: just above it or just below it.
		me.sub.below = !above
		if above {
			list = append([]*surface{me}, list...)
		} else {
			list = append(list, me)
		}
		// Keep below children first so the flattening stays ordered.
		sortBelowFirst(list)
	} else {
		me.sub.below = other.sub.below
		at := len(list)
		for i, ch := range list {
			if ch == other {
				at = i
				if above {
					at++
				}
				break
			}
		}
		list = append(list[:at:at], append([]*surface{me}, list[at:]...)...)
	}
	p.sub.children = list
	me.redraw()
}

// sortBelowFirst moves children below their parent ahead of those above,
// keeping each group's order.
func sortBelowFirst(list []*surface) {
	below := make([]*surface, 0, len(list))
	above := make([]*surface, 0, len(list))
	for _, ch := range list {
		if ch.sub.below {
			below = append(below, ch)
		} else {
			above = append(above, ch)
		}
	}
	copy(list, append(below, above...))
}

type region struct{}

func (region) Destroy(*wayland.Region)                              {}
func (region) Add(*wayland.Region, int32, int32, int32, int32)      {}
func (region) Subtract(*wayland.Region, int32, int32, int32, int32) {}

type shm struct{ server *Server }

func (h shm) CreatePool(r *wayland.Shm, id uint32, fd int, size int32) {
	if size <= 0 {
		unix.Close(fd)
		r.PostError(uint32(wayland.ShmErrorInvalidFd), "invalid pool size")
		return
	}
	data, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		unix.Close(fd)
		r.PostError(uint32(wayland.ShmErrorInvalidFd), "cannot map pool")
		return
	}
	state := &pool{fd: fd, size: size, data: data, shm: r, server: h.server}
	p, err := wayland.NewShmPool(r.Client(), 1, id, state)
	if err != nil {
		state.close()
		return
	}
	p.OnDestroy = func() {
		state.destroyed = true
		if state.refs == 0 {
			state.close()
		}
	}
}
func (shm) Release(*wayland.Shm) {}

type pool struct {
	fd        int
	size      int32
	data      []byte
	shm       *wayland.Shm
	server    *Server
	refs      int
	destroyed bool
}

func (p *pool) close() {
	if p.data != nil {
		_ = unix.Munmap(p.data)
		p.data = nil
	}
	_ = unix.Close(p.fd)
}
func (p *pool) CreateBuffer(r *wayland.ShmPool, id uint32, offset, width, height, stride int32, format uint32) {
	if format != uint32(wayland.ShmFormatArgb8888) && format != uint32(wayland.ShmFormatXrgb8888) {
		p.shm.PostError(uint32(wayland.ShmErrorInvalidFormat), "unsupported format")
		return
	}
	if width <= 0 || height <= 0 || int64(stride) < int64(width)*4 || offset < 0 || stride <= 0 || int64(offset)+int64(height-1)*int64(stride)+int64(width)*4 > int64(p.size) {
		p.shm.PostError(uint32(wayland.ShmErrorInvalidStride), "invalid buffer dimensions")
		return
	}
	state := &buffer{pool: p, offset: int(offset), width: int(width), height: int(height), stride: int(stride), format: format}
	b, err := wayland.NewBuffer(r.Client(), 1, id, state)
	if err != nil {
		return
	}
	p.refs++
	p.server.buffers[b.Resource] = state
	b.OnDestroy = func() {
		delete(p.server.buffers, b.Resource)
		p.refs--
		if p.destroyed && p.refs == 0 {
			p.close()
		}
	}
}
func (p *pool) Destroy(*wayland.ShmPool) {}
func (p *pool) Resize(r *wayland.ShmPool, size int32) {
	if size <= p.size {
		p.shm.PostError(uint32(wayland.ShmErrorInvalidFd), "pool must grow")
		return
	}
	data, err := unix.Mmap(p.fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		p.shm.PostError(uint32(wayland.ShmErrorInvalidFd), "cannot resize pool")
		return
	}
	_ = unix.Munmap(p.data)
	p.data, p.size = data, size
}

type buffer struct {
	pool                          *pool
	offset, width, height, stride int
	format                        uint32
}

func (*buffer) Destroy(*wayland.Buffer) {}

func (b *buffer) size() (int, int) { return b.width, b.height }

// clientBuffer is a wl_buffer's content source: wl_shm or linux-dmabuf.
type clientBuffer interface {
	// content returns what the renderer draws; false means the client
	// broke its buffer (a truncated shm file).
	content(ports.WindowID) (ports.SurfaceContent, bool)
	size() (int, int)
}

// addBuffer tracks a buffer until the client destroys it.
func (s *Server) addBuffer(r *wayland.Buffer, b *dmabufBuffer) {
	s.buffers[r.Resource] = b
	r.OnDestroy = func() {
		delete(s.buffers, r.Resource)
		b.close()
	}
}

// A malicious client may truncate its fd after mmap; SetPanicOnFault converts
// the resulting SIGBUS on this goroutine to a recoverable panic.
func (b *buffer) content(id ports.WindowID) (content ports.SurfaceContent, ok bool) {
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if recover() != nil {
			content = ports.SurfaceContent{}
			ok = false
		}
	}()
	size := (b.height-1)*b.stride + b.width*4
	pixels := make([]byte, size)
	for y := 0; y < b.height; y++ {
		start := b.offset + y*b.stride
		copy(pixels[y*b.stride:y*b.stride+b.width*4], b.pool.data[start:start+b.width*4])
	}
	return ports.SurfaceContent{ID: id, Width: b.width, Height: b.height, Stride: b.stride, Opaque: b.format == uint32(wayland.ShmFormatXrgb8888), Pixels: pixels}, true
}

type seat struct{ server *Server }

func (h seat) GetPointer(r *wayland.Seat, id uint32) {
	p, err := wayland.NewPointer(r.Client(), r.Version(), id, pointer{})
	if err != nil {
		return
	}
	s := h.server
	s.pointers[r.Client()] = append(s.pointers[r.Client()], p)
	p.OnDestroy = func() {
		list := s.pointers[r.Client()]
		for i, item := range list {
			if item == p {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(s.pointers, r.Client())
		} else {
			s.pointers[r.Client()] = list
		}
	}
	if w := s.windows[s.pointerFocus]; w != nil && w.mapped && w.xdg.resource.Client() == r.Client() {
		s.serial++
		x, y := w.surfacePoint(s.pointerX, s.pointerY)
		p.SendEnter(s.serial, w.xdg.surfaceResource(), server.FixedFromFloat(x), server.FixedFromFloat(y))
		pointerFrame(p)
	}
}
func (h seat) GetKeyboard(r *wayland.Seat, id uint32) {
	k, err := wayland.NewKeyboard(r.Client(), r.Version(), id, keyboard{})
	if err != nil {
		return
	}
	s := h.server
	k.OnDestroy = func() {
		list := s.keyboards[r.Client()]
		for i, item := range list {
			if item == k {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(s.keyboards, r.Client())
		} else {
			s.keyboards[r.Client()] = list
		}
	}
	if s.keymapFD < 0 {
		return
	}
	s.keyboards[r.Client()] = append(s.keyboards[r.Client()], k)
	k.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), s.keymapFD, s.keymapSize)
	if r.Version() >= 4 {
		k.SendRepeatInfo(int32(s.repeatRate), int32(s.repeatDelay))
	}
	if surf, _ := s.focusTarget(s.focused); surf != nil && surf.Client() == r.Client() {
		s.serial++
		k.SendEnter(s.serial, surf, []byte{})
		s.sendModifiers(k)
	}
}
func (seat) GetTouch(r *wayland.Seat, id uint32) {
	wayland.NewTouch(r.Client(), r.Version(), id, touch{})
}
func (seat) Release(*wayland.Seat) {}

type pointer struct{}

// Cursor surfaces are accepted but rendering cursors is not implemented yet.

func (pointer) SetCursor(*wayland.Pointer, uint32, *wayland.Surface, int32, int32) {}
func (pointer) Release(*wayland.Pointer)                                           {}

type keyboard struct{}

func (keyboard) Release(*wayland.Keyboard) {}

type touch struct{}

func (touch) Release(*wayland.Touch) {}

type dataManager struct{}

func (dataManager) CreateDataSource(r *wayland.DataDeviceManager, id uint32) {
	wayland.NewDataSource(r.Client(), r.Version(), id, dataSource{})
}
func (dataManager) GetDataDevice(r *wayland.DataDeviceManager, id uint32, _ *wayland.Seat) {
	wayland.NewDataDevice(r.Client(), r.Version(), id, dataDevice{})
}
func (dataManager) Release(*wayland.DataDeviceManager) {}

type dataSource struct{}

func (dataSource) Offer(*wayland.DataSource, string)      {}
func (dataSource) Destroy(*wayland.DataSource)            {}
func (dataSource) SetActions(*wayland.DataSource, uint32) {}

type dataDevice struct{}

func (dataDevice) StartDrag(*wayland.DataDevice, *wayland.DataSource, *wayland.Surface, *wayland.Surface, uint32) {
}
func (dataDevice) SetSelection(*wayland.DataDevice, *wayland.DataSource, uint32) {}
func (dataDevice) Release(*wayland.DataDevice)                                   {}
