package vulkan

import (
	"time"

	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// idleTTL is how long a client buffer cache survives undrawn in wall-clock
// time. An idle output renders no frame, so importTTL alone never frees
// the pools, GPU copies and imports of windows that left the screen.
const idleTTL = 20 * time.Second

// trimMark is the last frame recorded when Trim ran at a time.
type trimMark struct {
	frame uint64
	at    time.Time
}

// Trim frees the pool mappings, GPU copies and dmabuf imports not drawn
// for idleTTL, and the objects of frames the GPU finished, without a new
// frame. What the last frame drew stays: it is on screen and a redraw
// would copy it again. The output calls it periodically on its goroutine.
func (r *Renderer) Trim(now time.Time) error {
	// An idle output keeps its oldest mark: the frame is the same.
	if n := len(r.marks); n == 0 || r.marks[n-1].frame != r.frame {
		r.marks = append(r.marks, trimMark{frame: r.frame, at: now})
	}
	// cutoff is the newest mark at least idleTTL old: everything last
	// drawn at or before it has been undrawn for idleTTL.
	i := -1
	for j, m := range r.marks {
		if now.Sub(m.at) >= idleTTL {
			i = j
		}
	}
	if i >= 0 {
		cutoff := r.marks[i].frame
		r.marks = append(r.marks[:0], r.marks[i:]...)
		stale := func(last uint64) bool { return last <= cutoff && last < r.frame }
		// The GPU reads the copies, never a mapping, and no frame is being
		// recorded: unmapping now is safe.
		for id, m := range r.pools {
			if stale(m.last) {
				_ = unix.Munmap(m.data)
				delete(r.pools, id)
			}
		}
		for key, s := range r.shm {
			if stale(s.last) {
				r.retireShm(s)
				delete(r.shm, key)
			}
		}
		for id, im := range r.imports {
			if stale(im.last) {
				r.retire(im.last, func() { r.release(im) })
				delete(r.imports, id)
			}
		}
	}
	return r.poll()
}

// poll records the frames the GPU finished, without waiting, and frees
// what they held.
func (r *Renderer) poll() error {
	var pending uint64 // oldest frame still running, 0: none
	for i := range r.slots {
		s := &r.slots[i]
		if !s.busy {
			continue
		}
		res := r.dd.WaitForFences(r.device, 1, &s.fence, 1, 0)
		if res == vk.Timeout {
			if pending == 0 || s.frame < pending {
				pending = s.frame
			}
			continue
		}
		if err := checked("vkWaitForFences", res); err != nil {
			return err
		}
		if err := checked("vkResetFences", r.dd.ResetFences(r.device, 1, &s.fence)); err != nil {
			return err
		}
		s.busy = false
	}
	if pending == 0 {
		// Nothing runs: frames recorded but never submitted have no GPU
		// work either (like idle).
		r.completed = r.frame
	} else {
		r.completed = max(r.completed, pending-1)
	}
	r.runDeferred()
	return nil
}
