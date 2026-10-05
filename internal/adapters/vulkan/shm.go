package vulkan

import (
	"fmt"
	"runtime/debug"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// wl_shm buffers: each client pool is mapped once, read only. A surface's
// pixels are copied once per new content into a persistently mapped,
// host-visible GPU buffer (device-local when the GPU exposes it, ReBAR)
// that the fragment shader reads directly: one memcpy per row, no staging
// copy, no resample, no alpha fix-up on the CPU. Each surface has two GPU
// buffers used in turn, so the CPU never writes one a frame in flight
// reads. Mappings and buffers are dropped after importTTL frames unused,
// or idleTTL of wall-clock time (Trim).

// mapping is a client pool mapped read-only.
type mapping struct {
	data []byte
	last uint64
}

// shmPixels returns the pool bytes of a buffer, mapping the pool once and
// again when it grew past the mapping.
func (r *Renderer) shmPixels(b *ports.SHMBuffer, end int) ([]byte, error) {
	if m := r.pools[b.Pool]; m != nil {
		if end <= len(m.data) {
			m.last = r.frame
			return m.data, nil
		}
		// Draws collected earlier this frame may still read the old
		// mapping: it is unmapped once the frame is recorded.
		r.retired = append(r.retired, m.data)
		delete(r.pools, b.Pool)
	}
	fd, err := dupFile(b.File)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if int64(end) > st.Size {
		return nil, fmt.Errorf("shm pool of %d bytes, buffer ends at %d", st.Size, end)
	}
	data, err := unix.Mmap(fd, 0, int(st.Size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	r.pools[b.Pool] = &mapping{data: data, last: r.frame}
	return data, nil
}

// dropPools unmaps replaced mappings and pools not drawn for importTTL
// frames. The GPU never reads a mapping: this runs once the CPU copies of
// a frame are done.
func (r *Renderer) dropPools() {
	for _, data := range r.retired {
		_ = unix.Munmap(data)
	}
	r.retired = r.retired[:0]
	for id, m := range r.pools {
		if r.frame-m.last > importTTL {
			_ = unix.Munmap(m.data)
			delete(r.pools, id)
		}
	}
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
	d := r.dd
	ok := false
	defer func() {
		if !ok {
			r.freeGPUBuffer(b)
		}
	}()
	info := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: vk.DeviceSize(size), Usage: vk.BufferUsageStorageBufferBit, SharingMode: vk.SharingModeExclusive}
	if err := checked("vkCreateBuffer(shm)", d.CreateBuffer(r.device, &info, nil, &b.buffer)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetBufferMemoryRequirements(r.device, b.buffer, &req)
	host := vk.MemoryPropertyFlags(vk.MemoryPropertyHostVisibleBit | vk.MemoryPropertyHostCoherentBit)
	kind, err := r.findMemoryType(req.MemoryTypeBits, host|vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		kind, err = r.findMemoryType(req.MemoryTypeBits, host)
	}
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(shm)", d.AllocateMemory(r.device, &alloc, nil, &b.memory)); err != nil {
		return err
	}
	if err := checked("vkBindBufferMemory(shm)", d.BindBufferMemory(r.device, b.buffer, b.memory, 0)); err != nil {
		return err
	}
	if err := checked("vkMapMemory(shm)", d.MapMemory(r.device, b.memory, 0, vk.DeviceSize(size), 0, &b.mapped)); err != nil {
		return err
	}
	b.size, ok = size, true
	return nil
}

func (r *Renderer) freeGPUBuffer(b *gpuBuffer) {
	d := r.dd
	if b.mapped != nil {
		d.UnmapMemory(r.device, b.memory)
	}
	if b.buffer != 0 {
		d.DestroyBuffer(r.device, b.buffer, nil)
	}
	if b.memory != 0 {
		d.FreeMemory(r.device, b.memory, nil)
	}
	*b = gpuBuffer{}
}

// shmKey identifies a surface independently of its position in a tree.
type shmKey struct {
	win     ports.WindowID
	surface uint64
}

// shmState is what a GPU buffer holds: a w×h client buffer at a content
// Seq.
type shmState struct {
	w, h      int
	seq       uint64
	windowSeq uint64 // root damage history uses window publications
}

// shmCopy is one of a surface's two GPU buffers and the set binding it.
type shmCopy struct {
	gpu   gpuBuffer
	pool  vk.DescriptorPool
	set   vk.DescriptorSet
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

// shmCopyFor copies a surface's content st from the client pixels (stride
// bytes per row from offset) into a GPU buffer; the caller found none
// holding it (shmCached). A buffer holding an older content of the same
// size gets only what changed since (damage, in buffer pixels; false:
// everything).
func (r *Renderer) shmCopyFor(key shmKey, st shmState, pixels []byte, offset, stride int, damage func(since uint64) ([]ports.Rect, bool)) (*shmCopy, error) {
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
	if size > r.maxRange {
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
		var err error
		if c.pool, c.set, err = r.newSet(r.compose.dummyView, c.gpu.buffer); err != nil {
			r.freeGPUBuffer(&c.gpu)
			return nil, err
		}
		s.bufs[i] = c
	}
	if err := r.waitFrame(c.gpu.last); err != nil {
		return nil, err
	}
	dst := unsafe.Slice((*byte)(c.gpu.mapped), size)
	row := st.w * 4
	rects := []ports.Rect{{W: st.w, H: st.h}}
	if c.valid && c.holds.w == st.w && c.holds.h == st.h && damage != nil {
		if d, ok := damage(c.holds.windowSeq); ok {
			rects = d
		}
	}
	// The client may shrink its pool under the mapping: its window then
	// shows garbage for this frame, never a crash, and is copied again.
	c.valid = copyGuarded(func() {
		for _, rc := range rects {
			x0, y0 := max(rc.X, 0), max(rc.Y, 0)
			x1, y1 := min(rc.X+rc.W, st.w), min(rc.Y+rc.H, st.h)
			for y := y0; y < y1; y++ {
				d := y*row + x0*4
				s := offset + y*stride + x0*4
				n := (x1 - x0) * 4
				copy(dst[d:d+n], pixels[s:s+n])
				r.copied += n
			}
		}
	})
	c.holds, c.gpu.last = st, r.frame
	return c, nil
}

func (r *Renderer) freeShmCopy(c *shmCopy) {
	if c.pool != 0 {
		r.dd.DestroyDescriptorPool(r.device, c.pool, nil)
		c.pool = 0
	}
	r.freeGPUBuffer(&c.gpu)
}

// dropShm frees the GPU buffers of surfaces not drawn for importTTL frames.
func (r *Renderer) dropShm() {
	for key, s := range r.shm {
		if r.frame-s.last > importTTL {
			r.retireShm(s)
			delete(r.shm, key)
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
