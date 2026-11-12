package wayland

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/domain"
)

// Buffer release. Outputs read client buffers in place: the renderer
// samples them when it draws, and an output scanning out a client buffer
// (ADR 006) shows it until the next flip. A replaced wl_buffer therefore
// goes back to the client (release) only when the output showing its
// window reports (ports.OutputPresented) that it has a later content of
// that window and neither shows nor queues the buffer. Outputs keep only
// the latest content of a window, so they never draw an older buffer
// again. heldTimeout guards against an output that stopped reporting
// (switched away, stalled): a buffer it does not scan out or read on a
// separate child device is released then.

// heldTimeout releases held buffers of an output that stopped reporting.
const heldTimeout = 100 * time.Millisecond

type heldBuffer struct {
	res *wayland.Buffer
	// sync is the buffer's explicit-sync state: its release point is
	// signalled instead of wl_buffer.release.
	sync   syncHold
	window domain.WindowID
	id     uint64 // DMABuf ID, 0 for wl_shm
	after  uint64 // the window's content Seq when the buffer was replaced
	at     time.Time
}

// releaseBuffer releases a replaced buffer, or holds it until the output
// showing its window no longer reads it.
func (s *Server) releaseBuffer(surf *surface, b *wayland.Buffer, sync syncHold) {
	// An invisible window is held like a shown one: a scanout plane may
	// still read its buffer until the flip that hides it.
	window, name := surf.root().windowID(), s.frameOutput(surf)
	if !b.Alive() || window == 0 || name == "" {
		// Not drawn (unmapped, a cursor): nothing reads it.
		s.release(b, sync)
		return
	}
	h := heldBuffer{res: b, sync: sync, window: window, at: time.Now()}
	if d, ok := s.buffers[b.Resource].(*dmabufBuffer); ok {
		h.id = d.buf.ID
	}
	h.after = s.router.seq(window)
	s.router.hold(h)
	// Wake the pacer: it releases the buffer on the next output report.
	s.wakePacer()
}

// release gives a buffer back: its release point is signalled when it
// has one (explicit sync), else wl_buffer.release.
func (s *Server) release(b *wayland.Buffer, sync syncHold) {
	if sync.release.set() || sync.acquire != nil {
		s.releaseSync(sync)
		return
	}
	if b != nil && b.Alive() {
		b.SendRelease()
	}
}

// releaseBufferSync holds only a buffer's explicit-sync points: the same
// buffer committed again with new points (clients reuse one buffer).
func (s *Server) releaseBufferSync(surf *surface, sync syncHold) {
	if !sync.release.set() && sync.acquire == nil {
		return
	}
	window, name := surf.root().windowID(), s.frameOutput(surf)
	if window == 0 || name == "" {
		s.releaseSync(sync)
		return
	}
	after := s.router.seq(window)
	s.router.hold(heldBuffer{sync: sync, window: window, after: after, at: time.Now()})
	s.wakePacer()
}

// bufferReferenced checks queued, cached and pending commits on the owner
// goroutine. Never return a buffer still promised to a later commit.
func (s *Server) bufferReferenced(b *wayland.Buffer) bool {
	if b == nil {
		return false
	}
	for _, surf := range s.surfaces {
		if surf.next.attached && sameBuffer(surf.next.buffer, b) {
			return true
		}
		for _, u := range surf.queue {
			if u.attached && sameBuffer(u.buffer, b) {
				return true
			}
		}
	}
	return false
}

// keepHeld reports whether an output may still read a held buffer: one
// scans it out or queued it, a child renderer still reads its content, or
// one has not got a later content of its window yet and heldTimeout has
// not passed. Child reads are exempt from the timeout until the render
// finishes or its device shuts down.
func keepHeld(h heldBuffer, reports []ports.OutputPresented, now time.Time) bool {
	seen := true
	for _, r := range reports {
		if h.id != 0 && (r.Shown == h.id || r.Queued == h.id) {
			return true
		}
		if seq, ok := r.ChildReads[h.window]; ok && seq <= h.after {
			return true
		}
		if r.Seen[h.window] <= h.after {
			seen = false
		}
	}
	if len(reports) == 0 {
		seen = false
	}
	return !seen && now.Sub(h.at) < heldTimeout
}
