package vulkan

import (
	"fmt"
	"runtime/debug"

	"github.com/bnema/nefertty/internal/ports"
	"golang.org/x/sys/unix"
)

// wl_shm buffers are read in place: each client pool is mapped once, read
// only, and the visible pixels are copied from it into staging when a frame
// is drawn. Mappings follow the dmabuf import rules: kept while drawn,
// dropped after importTTL frames without use.

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
		// Uploads collected earlier this frame may still slice the old
		// mapping: it is unmapped once staging is done.
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
// frames.
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
