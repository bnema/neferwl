package vulkan

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// idleTTL is how long a client buffer cache survives undrawn in wall-clock
// time. An idle output renders no frame, so importTTL alone never frees
// the pools, GPU copies and imports of windows that left the screen.
const idleTTL = 20 * time.Second

// hiddenTTL is idleTTL for the wl_shm copies and pool mappings of windows the
// last scene lists as Hidden (another workspace, a hidden stash): they are
// suspended, so their content will not change before they are shown again,
// and a window coming back is copied again in the frame that draws it. Trim
// dates frames at its own calls, so the real delay runs from the first call
// after the window left the screen.
const hiddenTTL = 5 * time.Second

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
	// A cutoff is the newest mark at least a TTL old: everything last drawn
	// at or before it has been undrawn for that TTL. h is the mark for
	// hiddenTTL, i the (older or equal) one for idleTTL.
	i, h := -1, -1
	for j, m := range r.marks {
		age := now.Sub(m.at)
		if age >= idleTTL {
			i = j
		}
		if age >= hiddenTTL {
			h = j
		}
	}
	if h >= 0 {
		hiddenCutoff := r.marks[h].frame
		var cutoff uint64 // frames start at 1: 0 frees nothing
		if i >= 0 {
			cutoff = r.marks[i].frame
			r.marks = append(r.marks[:0], r.marks[i:]...)
		}
		stale := func(last uint64, win ports.WindowID) bool {
			if last >= r.frame {
				return false
			}
			if _, hidden := r.hidden[win]; hidden {
				return last <= hiddenCutoff
			}
			return last <= cutoff
		}
		// The GPU reads the copies, never a mapping, and no frame is being
		// recorded: unmapping now is safe.
		for id, m := range r.pools {
			if stale(m.last, m.win) {
				_ = unix.Munmap(m.data)
				delete(r.pools, id)
			}
		}
		for key, s := range r.shm {
			if stale(s.last, key.win) {
				r.retireShm(s)
				delete(r.shm, key)
			}
		}
		for id, im := range r.imports {
			if stale(im.last, 0) {
				r.retire(im.last, func() { r.release(im) })
				delete(r.imports, id)
			}
		}
	}
	if err := r.poll(); err != nil {
		return err
	}
	if i >= 0 {
		r.trimCompose(r.marks[0].frame)
	}
	return nil
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
		res := r.gpu.dd.WaitForFences(r.gpu.device, 1, &s.fence, 1, 0)
		if res == vk.Timeout {
			if pending == 0 || s.frame < pending {
				pending = s.frame
			}
			continue
		}
		if err := checked("vkWaitForFences", res); err != nil {
			return err
		}
		if err := checked("vkResetFences", r.gpu.dd.ResetFences(r.gpu.device, 1, &s.fence)); err != nil {
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

// noteHidden records the windows s lists as Hidden: they are not drawn, so
// Trim frees their copies after hiddenTTL instead of idleTTL.
func (r *Renderer) noteHidden(s ports.Scene) {
	clear(r.hidden)
	for _, w := range s.Windows {
		if w.Hidden && !w.Popup {
			r.hidden[w.ID] = struct{}{}
		}
	}
}
