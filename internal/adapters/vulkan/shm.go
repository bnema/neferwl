package vulkan

import (
	"errors"
	"fmt"
	"image"
	"io"
	"runtime/debug"
	"unsafe"

	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
	"github.com/bnema/neferwl/internal/domain"

	vk "bnema.dev/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// wl_shm pixels are read directly into persistently mapped GPU buffers.
// Only drivers rejecting pread into GPU memory use a transient client mapping.
// Each surface has two GPU buffers, so the CPU never writes one in flight.

// shmFD holds the pool open across validation and copying, even if the client
// concurrently closes its file.
func shmFD(b *domain.SHMBuffer, end int) (int, error) {
	fd, err := dupFile(b.File)
	if err != nil {
		return -1, err
	}
	if err := shmSize(fd, end); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func shmSize(fd, end int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if end < 0 || int64(end) > st.Size {
		return fmt.Errorf("shm pool of %d bytes, buffer ends at %d", st.Size, end)
	}
	return nil
}

// preadPixels reads exactly one contiguous region. A short read is invalid
// content, not a reason to retry and combine different client publications.
func preadPixels(fd int, dst []byte, offset int) error {
	for {
		n, err := unix.Pread(fd, dst, int64(offset))
		if err == unix.EINTR && n == 0 {
			continue
		}
		if err != nil {
			return err
		}
		if n != len(dst) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
}

// readSHMRect uses one syscall for full contiguous rows, otherwise one per row.
func readSHMRect(fd int, dst []byte, offset, stride, row int, rc image.Rectangle) error {
	if stride == row && rc.Min.X == 0 && rc.Dx()*4 == row {
		return preadPixels(fd, dst[rc.Min.Y*row:rc.Max.Y*row], offset+rc.Min.Y*stride)
	}
	for y := rc.Min.Y; y < rc.Max.Y; y++ {
		d, n := y*row+rc.Min.X*4, rc.Dx()*4
		if err := preadPixels(fd, dst[d:d+n], offset+y*stride+rc.Min.X*4); err != nil {
			return err
		}
	}
	return nil
}

// copySHMRect is only for GPU mappings which reject kernel writes. The
// mapping never survives this copy; SIGBUS from concurrent truncation is guarded.
func copySHMRect(fd int, dst []byte, offset, stride, row int, rc image.Rectangle) error {
	end := offset + (rc.Max.Y-1)*stride + rc.Max.X*4
	if err := shmSize(fd, end); err != nil {
		return err
	}
	data, err := unix.Mmap(fd, 0, end, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	defer unix.Munmap(data)
	if !copyGuarded(func() {
		for y := rc.Min.Y; y < rc.Max.Y; y++ {
			d, src, n := y*row+rc.Min.X*4, offset+y*stride+rc.Min.X*4, rc.Dx()*4
			copy(dst[d:d+n], data[src:src+n])
		}
	}) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// copyGuarded runs copy with page faults turned into a recoverable panic:
// a client may shrink its pool file under the mapping (SIGBUS). It reports
// false when the client broke its buffer.
func copyGuarded(copy func()) (ok bool) {
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	copy()
	return true
}

// gpuBuffer is a host-visible storage buffer, mapped for its whole life.
type gpuBuffer struct {
	buffer vk.Buffer
	memory vk.DeviceMemory
	mapped unsafe.Pointer
	size   int
	// last is the frame that last read it: it is written or freed only
	// once that frame completed.
	last uint64
}

func (r *Renderer) newGPUBuffer(b *gpuBuffer, size int) error {
	d := r.gpu.dd
	ok := false
	defer func() {
		if !ok {
			r.freeGPUBuffer(b)
		}
	}()
	info := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: vk.DeviceSize(size), Usage: vk.BufferUsageStorageBufferBit, SharingMode: vk.SharingModeExclusive}
	if err := checked("vkCreateBuffer(shm)", d.CreateBuffer(r.gpu.device, &info, nil, &b.buffer)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetBufferMemoryRequirements(r.gpu.device, b.buffer, &req)
	host := vk.MemoryPropertyFlags(vk.MemoryPropertyHostVisibleBit | vk.MemoryPropertyHostCoherentBit)
	kind, err := r.findMemoryType(req.MemoryTypeBits, host|vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		kind, err = r.findMemoryType(req.MemoryTypeBits, host)
	}
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(shm)", d.AllocateMemory(r.gpu.device, &alloc, nil, &b.memory)); err != nil {
		return err
	}
	if err := checked("vkBindBufferMemory(shm)", d.BindBufferMemory(r.gpu.device, b.buffer, b.memory, 0)); err != nil {
		return err
	}
	if err := checked("vkMapMemory(shm)", d.MapMemory(r.gpu.device, b.memory, 0, vk.DeviceSize(size), 0, &b.mapped)); err != nil {
		return err
	}
	b.size, ok = size, true
	return nil
}

func (r *Renderer) freeGPUBuffer(b *gpuBuffer) {
	if b.mapped == nil && b.buffer == 0 && b.memory == 0 {
		*b = gpuBuffer{}
		return
	}
	d := r.gpu.dd
	if b.mapped != nil {
		d.UnmapMemory(r.gpu.device, b.memory)
	}
	if b.buffer != 0 {
		d.DestroyBuffer(r.gpu.device, b.buffer, nil)
	}
	if b.memory != 0 {
		d.FreeMemory(r.gpu.device, b.memory, nil)
	}
	*b = gpuBuffer{}
}

// shmKey identifies a surface independently of its position in a tree.
type shmKey struct {
	win     domain.WindowID
	surface uint64
	// overlay identifies a compositor-drawn overlay (overlays.go). Its ID is
	// never zero, and win and surface are zero, so client keys cannot collide.
	overlay  uint64
	scale    float64
	style    overlaydraw.Style
	maxWidth int // physical panel width limit; zero for labels
}

// shmState is what a GPU buffer holds: a w×h client buffer at a content
// Seq.
type shmState struct {
	w, h      int
	seq       uint64
	windowSeq uint64 // root damage history uses window publications
}

// shmCopy is one of a surface's two GPU buffers.
type shmCopy struct {
	gpu   gpuBuffer
	holds shmState
	valid bool
}

// shmSurface is the GPU copy of one surface's wl_shm content.
type shmSurface struct {
	bufs [2]*shmCopy
	last uint64 // frame that drew it
}

// shmCached returns the GPU buffer already holding a surface's content st,
// or nil. It needs no client pixels: a kept window whose pool is closed
// still draws from it.
func (r *Renderer) shmCached(key shmKey, st shmState) *shmCopy {
	s := r.shm[key]
	if s == nil {
		return nil
	}
	s.last = r.frame
	for _, c := range s.bufs {
		if c != nil && c.valid && c.holds.w == st.w && c.holds.h == st.h && c.holds.seq == st.seq {
			c.gpu.last = r.frame
			return c
		}
	}
	return nil
}

// shmCopyFor copies a surface's content st from b, or CPU-owned pixels when
// b is nil (compositor overlays), using stride
// bytes per row from offset into a GPU buffer; the caller found none
// holding it (shmCached). A buffer holding an older content of the same
// size gets only what changed since (damage, in buffer pixels; false:
// everything).
func (r *Renderer) shmCopyFor(key shmKey, st shmState, pixels []byte, b *domain.SHMBuffer, offset, stride int, damage func(since uint64) ([]domain.Rect, bool)) (*shmCopy, error) {
	s := r.shm[key]
	if s == nil {
		s = &shmSurface{}
		r.shm[key] = s
	}
	s.last = r.frame
	// The older buffer takes the new content.
	i := 0
	if s.bufs[0] != nil && (s.bufs[1] == nil || s.bufs[1].gpu.last < s.bufs[0].gpu.last) {
		i = 1
	}
	size := st.w * st.h * 4
	if size > r.gpu.maxRange {
		return nil, fmt.Errorf("shm buffer of %d bytes past the storage buffer range", size)
	}
	c := s.bufs[i]
	if c != nil && c.gpu.size != size {
		r.retire(c.gpu.last, func() { r.freeShmCopy(c) })
		s.bufs[i], c = nil, nil
	}
	if c == nil {
		c = &shmCopy{}
		if err := r.newGPUBuffer(&c.gpu, size); err != nil {
			return nil, err
		}
		s.bufs[i] = c
	}
	if err := r.waitFrame(c.gpu.last); err != nil {
		return nil, err
	}
	dst := unsafe.Slice((*byte)(c.gpu.mapped), size)
	row := st.w * 4
	rects := []domain.Rect{{W: st.w, H: st.h}}
	if c.valid && c.holds.w == st.w && c.holds.h == st.h && damage != nil {
		if d, ok := damage(c.holds.windowSeq); ok {
			rects = d
		}
	}
	// Damage can overlap: each pixel is copied once.
	r.shmRects = disjointRects(r.shmRects[:0], rects, st.w, st.h)
	c.valid = false
	fd := -1
	if b != nil {
		var err error
		fd, err = shmFD(b, offset+(st.h-1)*stride+row)
		if err != nil {
			c.holds, c.gpu.last = st, r.frame
			return c, nil
		}
		defer unix.Close(fd)
	}
	c.valid = true
	for _, rc := range r.shmRects {
		if b != nil {
			err := readSHMRect(fd, dst, offset, stride, row, rc)
			if errors.Is(err, unix.EFAULT) {
				if !r.shmPreadFallback {
					r.shmPreadFallback = true
					r.log.Debug().Msg("pread into GPU memory rejected; using transient shm mappings")
				}
				err = copySHMRect(fd, dst, offset, stride, row, rc)
			}
			if err != nil {
				c.valid = false
				break
			}
		} else {
			for y := rc.Min.Y; y < rc.Max.Y; y++ {
				d, src, n := y*row+rc.Min.X*4, offset+y*stride+rc.Min.X*4, rc.Dx()*4
				copy(dst[d:d+n], pixels[src:src+n])
			}
		}
		r.copied += rc.Dx() * rc.Dy() * 4
	}
	c.holds, c.gpu.last = st, r.frame
	return c, nil
}

func (r *Renderer) freeShmCopy(c *shmCopy) {
	r.invalidateCompose(resourceKey{shm: c})
	r.freeGPUBuffer(&c.gpu)
}

// dropShm frees the GPU buffers of surfaces not drawn for importTTL frames.
func (r *Renderer) dropShm() {
	for key, s := range r.shm {
		if r.frame-s.last > importTTL {
			r.retireShm(s)
			r.removeShm(key)
		}
	}
}

// retireShm frees a surface's GPU buffers once the frames reading them
// completed.
func (r *Renderer) retireShm(s *shmSurface) {
	for _, c := range s.bufs {
		if c != nil {
			r.retire(c.gpu.last, func() { r.freeShmCopy(c) })
		}
	}
}

// maxDisjointRects bounds the pieces disjointRects cuts the damage into: the
// rects come from the client, and each cut can add three pieces.
const maxDisjointRects = 64

// disjointRects appends to dst the parts of rects inside a w×h buffer, with
// no pixel in two of them. Past maxDisjointRects pieces it appends their
// bounding box instead: some undamaged pixels are copied again. It reuses
// dst's capacity.
func disjointRects(dst []image.Rectangle, rects []domain.Rect, w, h int) []image.Rectangle {
	bounds := image.Rect(0, 0, w, h)
	start := len(dst)
	for _, rc := range rects {
		if len(dst)-start > maxDisjointRects {
			var box image.Rectangle
			for _, rc := range rects {
				box = box.Union(image.Rect(rc.X, rc.Y, rc.X+rc.W, rc.Y+rc.H).Intersect(bounds))
			}
			return append(dst[:start], box)
		}
		pending := len(dst)
		dst = append(dst, image.Rect(rc.X, rc.Y, rc.X+rc.W, rc.Y+rc.H).Intersect(bounds))
		// Cut the new rect by each earlier one; the pieces it leaves are
		// appended after it and cut in turn.
		for i := pending; i < len(dst); i++ {
			if dst[i].Empty() {
				dst[i] = dst[len(dst)-1]
				dst = dst[:len(dst)-1]
				i--
				continue
			}
			for _, o := range dst[:pending] {
				in := dst[i].Intersect(o)
				if in.Empty() {
					continue
				}
				r := dst[i]
				dst[i] = image.Rect(r.Min.X, r.Min.Y, r.Max.X, in.Min.Y)
				dst = append(dst,
					image.Rect(r.Min.X, in.Max.Y, r.Max.X, r.Max.Y),
					image.Rect(r.Min.X, in.Min.Y, in.Min.X, in.Max.Y),
					image.Rect(in.Max.X, in.Min.Y, r.Max.X, in.Max.Y))
				i--
				break
			}
		}
	}
	return dst
}

// removeShm releases the CPU panel only after its last scale/style/width key.
func (r *Renderer) removeShm(key shmKey) {
	delete(r.shm, key)
	if key.overlay == 0 {
		return
	}
	for other := range r.shm {
		if other.overlay == key.overlay {
			return
		}
	}
	delete(r.panels, key.overlay)
}
