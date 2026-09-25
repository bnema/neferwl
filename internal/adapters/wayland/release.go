package wayland

import (
	"time"

	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// Buffer release. A replaced wl_buffer goes back to the client (release)
// when nothing reads it any more. Composed outputs copy buffers into their
// own image, so buffers are released at once. While an output scans out a
// client buffer directly (ADR 006), a replaced GPU buffer of a surface on
// it may be on screen or queued for a page flip: it is held until the
// output presents a frame that scans out another buffer, or heldTimeout
// passes without a flip. The first buffer of a switch to scanout is not
// held (known limitation: it may be released while its flip is queued).

// heldTimeout releases held buffers of an output that stopped flipping; a
// queued flip completes well within it.
const heldTimeout = 100 * time.Millisecond

type heldBuffer struct {
	res *wayland.Buffer
	id  uint64
	at  time.Time
}

// releaseBuffer releases a replaced buffer, or holds a GPU buffer until
// its output flipped away from it.
func (s *Server) releaseBuffer(surf *surface, b *wayland.Buffer) {
	if !b.Resource.Alive() {
		return
	}
	d, ok := s.buffers[b.Resource].(*dmabufBuffer)
	name := s.frameOutput(surf)
	// Only an output scanning out a client buffer reads it after the
	// frame: composed outputs copied it already.
	if !ok || name == "" || s.scanned[name] == 0 {
		b.SendRelease()
		return
	}
	s.held[name] = append(s.held[name], heldBuffer{res: b, id: d.buf.ID, at: time.Now()})
	// Wake the pacer: it releases the buffer after the next flip.
	select {
	case s.frameReady <- struct{}{}:
	default:
	}
}

// releaseHeld records the buffers scanned out by the outputs that flipped
// and releases held buffers no output shows. It reports whether buffers
// are still held.
func (s *Server) releaseHeld(now time.Time, scanned map[string]uint64) bool {
	for name, id := range scanned {
		if id == 0 {
			delete(s.scanned, name)
		} else {
			s.scanned[name] = id
		}
	}
	live := map[string]bool{}
	for _, o := range s.outputs {
		live[o.name()] = true
	}
	for name := range s.scanned {
		if !live[name] {
			delete(s.scanned, name)
		}
	}
	for name, list := range s.held {
		_, flipped := scanned[name]
		kept := list[:0]
		for _, h := range list {
			if keepHeld(h, s.scanned[name], live[name], flipped, now) {
				kept = append(kept, h)
				continue
			}
			if h.res.Resource.Alive() {
				h.res.SendRelease()
			}
		}
		if len(kept) == 0 {
			delete(s.held, name)
		} else {
			s.held[name] = kept
		}
	}
	return len(s.held) > 0
}

// keepHeld reports whether a held buffer must stay with the client's
// compositor: its output scans it out, or has not flipped since it was
// replaced (a queued flip may still show it) and heldTimeout has not
// passed. A buffer of an unplugged output is released.
func keepHeld(h heldBuffer, scanned uint64, live, flipped bool, now time.Time) bool {
	if scanned == h.id {
		return true
	}
	return live && !flipped && now.Sub(h.at) < heldTimeout
}
