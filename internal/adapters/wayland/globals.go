package wayland

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"os"
)

func registerGlobals(d *server.Display, o Options, s *Server) error {
	for _, register := range []func() error{
		func() error { return registerXDG(d, s) },
		func() error { return registerLayer(d, s) },
		func() error { return registerXDGOutput(d, s) },
		func() error { return registerOutputManagement(d, s) },
		func() error { return registerWorkspaces(d, s) },
		func() error { return registerForeignToplevel(d, s) },
		func() error { return registerDecoration(d, s) },
		func() error { return registerScale(d, s) },
		func() error { return registerCursorShape(d, s) },
		func() error { return registerDMABuf(d, s, o.DMABuf) },
		func() error { return registerPointerConstraints(d, s) },
		func() error { return registerTearing(d, s) },
		func() error { return registerColorManagement(d, s) },
		func() error { return registerColorRepresentation(d, s) },
		func() error { return registerPresentationConstraints(d, s) },
		func() error { return registerPresentation(d, s) },
		func() error { return registerSyncobj(d, s) },
		func() error { return registerInhibit(d, s) },
		func() error { return registerIdle(d, s) },
		func() error { return registerCapture(d, s) },
		func() error { return registerActivation(d, s) },
		func() error { return registerVirtualKeyboard(d, s) },
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
			return wayland.NewSeatGlobal(d, 8, func(c server.Client, v, id uint32) {
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
		func() error { return registerClipboard(d, s) },
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

type compositor struct{ server *Server }

func (c compositor) CreateSurface(r *wayland.Compositor, id uint32) {
	state := &surface{server: c.server, bufferScale: 1, inputAll: true}
	if w, err := wayland.NewSurface(r.Client(), r.Version(), id, state); err == nil {
		c.server.nextSurface++
		state.identity = c.server.nextSurface
		state.wl = w
		c.server.surfaces[w.Resource] = state
		state.sendScale()
		w.OnDestroy = func() { delete(c.server.surfaces, w.Resource); state.Destroy(w) }
	}
}
func (c compositor) CreateRegion(r *wayland.Compositor, id uint32) {
	state := &region{}
	if w, err := wayland.NewRegion(r.Client(), r.Version(), id, state); err == nil {
		c.server.regions[w.Resource] = state
		w.OnDestroy = func() { delete(c.server.regions, w.Resource) }
	}
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
	state.sub = subState{role: sub, parent: up, children: state.sub.children, layout: state.sub.layout, pendingLayout: state.sub.pendingLayout, synced: true}
	up.sub.children = append(up.sub.children, state)
	up.sub.pendingLayout = append(append([]childLayout(nil), up.sub.pendingLayout...), childLayout{child: state})
	// The child follows its root window's output and scale.
	state.sendScale()
	up.emitInput()
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
	p := s.surface.sub.parent
	if p == nil {
		return
	}
	p.sub.pendingLayout = append([]childLayout(nil), p.sub.pendingLayout...)
	for i := range p.sub.pendingLayout {
		item := &p.sub.pendingLayout[i]
		if item.child == s.surface {
			item.x, item.y = int(x), int(y)
			break
		}
	}
}
func (s subsurface) PlaceAbove(r *wayland.Subsurface, sibling *wayland.Surface) {
	s.restack(r, sibling, true)
}
func (s subsurface) PlaceBelow(r *wayland.Subsurface, sibling *wayland.Surface) {
	s.restack(r, sibling, false)
}
func (s subsurface) SetSync(*wayland.Subsurface) { s.surface.sub.synced = true }
func (s subsurface) SetDesync(*wayland.Subsurface) {
	was := s.surface.effectivelySynced()
	s.surface.sub.synced = false
	if was && !s.surface.effectivelySynced() {
		s.surface.flushDesync()
	}
}

// restack changes only the pending layout of the parent.
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
	list := append([]childLayout(nil), p.sub.pendingLayout...)
	var item childLayout
	for _, entry := range list {
		if entry.child == me {
			item = entry
			break
		}
	}
	list = removeLayout(list, me)
	at := len(list)
	if other == p {
		item.below = !above
		if above {
			for i, entry := range list {
				if !entry.below {
					at = i
					break
				}
			}
		}
	} else {
		for i, entry := range list {
			if entry.child == other {
				at, item.below = i, entry.below
				if above {
					at++
				}
				break
			}
		}
	}
	list = append(list, childLayout{})
	copy(list[at+1:], list[at:])
	list[at] = item
	p.sub.pendingLayout = list
}

// region stores the exact union of added rectangles.
type region struct{ rects []ports.Rect }

func (*region) Destroy(*wayland.Region) {}
func (g *region) Add(_ *wayland.Region, x, y, w, h int32) {
	if w > 0 && h > 0 {
		g.rects = append(g.rects, ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)})
	}
}
func (g *region) Subtract(_ *wayland.Region, x, y, w, h int32) {
	if w <= 0 || h <= 0 {
		return
	}
	cut := ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}
	var out []ports.Rect
	for _, r := range g.rects {
		i := intersectRect(r, cut)
		if i.W == 0 {
			out = append(out, r)
			continue
		}
		for _, piece := range []ports.Rect{
			{X: r.X, Y: r.Y, W: r.W, H: i.Y - r.Y},
			{X: r.X, Y: i.Y + i.H, W: r.W, H: r.Y + r.H - i.Y - i.H},
			{X: r.X, Y: i.Y, W: i.X - r.X, H: i.H},
			{X: i.X + i.W, Y: i.Y, W: r.X + r.W - i.X - i.W, H: i.H},
		} {
			if piece.W > 0 && piece.H > 0 {
				out = append(out, piece)
			}
		}
	}
	g.rects = out
}

type shm struct{ server *Server }

func (h shm) CreatePool(r *wayland.Shm, id uint32, fd int, size int32) {
	file := os.NewFile(uintptr(fd), "wl_shm_pool")
	if size <= 0 || !fileHolds(file, int64(size)) {
		file.Close()
		r.PostError(uint32(wayland.ShmErrorInvalidFd), "invalid pool size")
		return
	}
	h.server.nextPool++
	state := &pool{id: h.server.nextPool, file: file, size: size, shm: r, server: h.server}
	p, err := wayland.NewShmPool(r.Client(), 1, id, state)
	if err != nil {
		file.Close()
		return
	}
	p.OnDestroy = func() {
		state.destroyed = true
		if state.refs == 0 {
			state.file.Close()
		}
	}
}
func (shm) Release(*wayland.Shm) {}

// fileHolds reports whether the file has at least size bytes.
func fileHolds(f *os.File, size int64) bool {
	st, err := f.Stat()
	return err == nil && st.Size() >= size
}

// pool is a wl_shm pool. neferwl never maps it: renderers map the file
// and read the pixels when they draw (ports.SHMBuffer).
type pool struct {
	id        uint64
	file      *os.File
	size      int32
	shm       *wayland.Shm
	server    *Server
	refs      int
	destroyed bool
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
			p.file.Close()
		}
	}
}
func (p *pool) Destroy(*wayland.ShmPool) {}
func (p *pool) Resize(r *wayland.ShmPool, size int32) {
	if size <= p.size {
		p.shm.PostError(uint32(wayland.ShmErrorInvalidFd), "pool must grow")
		return
	}
	if !fileHolds(p.file, int64(size)) {
		p.shm.PostError(uint32(wayland.ShmErrorInvalidFd), "cannot resize pool")
		return
	}
	p.size = size
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

// content points the renderer at the pool; a file shrunk below the buffer
// is refused.
func (b *buffer) content(id ports.WindowID) (ports.SurfaceContent, bool) {
	if !fileHolds(b.pool.file, int64(b.offset+(b.height-1)*b.stride+b.width*4)) {
		return ports.SurfaceContent{}, false
	}
	shm := &ports.SHMBuffer{Pool: b.pool.id, File: b.pool.file, Offset: b.offset, Stride: b.stride}
	return ports.SurfaceContent{ID: id, Width: b.width, Height: b.height, Opaque: b.format == uint32(wayland.ShmFormatXrgb8888), SHM: shm}, true
}

type seat struct{ server *Server }

func (h seat) GetPointer(r *wayland.Seat, id uint32) {
	p, err := wayland.NewPointer(r.Client(), r.Version(), id, pointer(h))
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
	if s.hasPointerFocus(r.Client()) {
		if surf, _, x, y := s.pointerSurface(s.pointerFocus, s.pointerX, s.pointerY); surf != nil {
			s.serial++
			p.SendEnter(s.serial, surf, server.FixedFromFloat(x), server.FixedFromFloat(y))
			pointerFrame(p)
		}
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
	fd, size := s.currentKeymap()
	k.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), fd, size)
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

type keyboard struct{}

func (keyboard) Release(*wayland.Keyboard) {}

type touch struct{}

func (touch) Release(*wayland.Touch) {}
