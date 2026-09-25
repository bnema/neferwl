package wayland

import (
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// Buffer release. Outputs read client buffers in place: the renderer
// samples them when it draws, and an output scanning out a client buffer
// (ADR 006) shows it until the next flip. A replaced wl_buffer therefore
// goes back to the client (release) only when the output showing its
// window reports (ports.OutputPresented) that it has a later content of
// that window and neither shows nor queues the buffer. Outputs keep only
// the latest content of a window, so they never draw an older buffer
// again. heldTimeout guards against an output that stopped reporting
// (switched away, stalled): a buffer it does not scan out is released then.

// heldTimeout releases held buffers of an output that stopped reporting.
const heldTimeout = 100 * time.Millisecond

type heldBuffer struct {
	res    *wayland.Buffer
	window ports.WindowID
	id     uint64 // DMABuf ID, 0 for wl_shm
	after  uint64 // the window's content Seq when the buffer was replaced
	at     time.Time
}

// releaseBuffer releases a replaced buffer, or holds it until the output
// showing its window no longer reads it.
func (s *Server) releaseBuffer(surf *surface, b *wayland.Buffer) {
	if !b.Resource.Alive() {
		return
	}
	window, name := surf.root().windowID(), s.frameOutput(surf)
	if window == 0 || name == "" {
		// Not drawn (unmapped, hidden, a cursor): nothing reads it.
		b.SendRelease()
		return
	}
	h := heldBuffer{res: b, window: window, at: time.Now()}
	if d, ok := s.buffers[b.Resource].(*dmabufBuffer); ok {
		h.id = d.buf.ID
	}
	s.contentMu.Lock()
	h.after = s.contentSeq[window]
	s.contentMu.Unlock()
	s.held = append(s.held, h)
	// Wake the pacer: it releases the buffer on the next output report.
	select {
	case s.frameReady <- struct{}{}:
	default:
	}
}

// releaseHeld records the output reports and releases the buffers no
// output reads. Every output reads every window's content, so a buffer
// waits until no live output shows or queues it and all of them have seen
// a later content of its window (or heldTimeout passed). It reports
// whether buffers are still held.
func (s *Server) releaseHeld(now time.Time, reports []ports.OutputPresented) bool {
	for _, r := range reports {
		s.reports[r.Output] = r
	}
	live := make([]ports.OutputPresented, 0, len(s.outputs))
	for name := range s.reports {
		if s.outputByNameExact(name) == nil {
			delete(s.reports, name)
		}
	}
	for _, r := range s.reports {
		live = append(live, r)
	}
	kept := s.held[:0]
	for _, h := range s.held {
		if keepHeld(h, live, now) {
			kept = append(kept, h)
			continue
		}
		if h.res.Resource.Alive() {
			h.res.SendRelease()
		}
	}
	clear(s.held[len(kept):])
	s.held = kept
	return len(s.held) > 0
}

// keepHeld reports whether an output may still read a held buffer: one
// scans it out or queued it, or one has not got a later content of its
// window yet and heldTimeout has not passed.
func keepHeld(h heldBuffer, reports []ports.OutputPresented, now time.Time) bool {
	seen := true
	for _, r := range reports {
		if h.id != 0 && (r.Shown == h.id || r.Queued == h.id) {
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
