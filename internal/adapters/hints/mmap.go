package hints

import "syscall"

// mapping is a byte region that lives outside the Go heap: anonymous memory
// from mmap, returned to the system by unmap. When mmap fails it falls back
// to heap memory, which unmap then just drops.
type mapping struct {
	b      []byte
	mapped bool
	live   *int
}

// alloc returns a zeroed region of n bytes; w.live counts the regions not
// yet released.
func (w *worker) alloc(n int) *mapping {
	m := &mapping{live: &w.live}
	b, err := syscall.Mmap(-1, 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		b = make([]byte, n)
	} else {
		m.mapped = true
	}
	m.b = b
	w.live++
	return m
}

// unmap releases the region; it is safe to call twice and on nil. The
// region must not be used afterwards, nor still be written by the output.
func (m *mapping) unmap() {
	if m == nil || m.b == nil {
		return
	}
	if m.mapped {
		_ = syscall.Munmap(m.b)
	}
	m.b = nil
	*m.live--
}
