package wayland

import (
	"context"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bnema/neferwl/internal/ports"

	"github.com/bnema/go-wayland-bindings/server/wayland"
)

// Frame callbacks (wl_surface.frame) tell a client when to draw its next
// frame. They are paced per output: on its page flip when it flips, else
// at its refresh rate, so a 165 Hz output lets clients draw 165 frames a
// second and a V-Sync game follows the screen. Cursors and unplaced
// surfaces are paced at 60 Hz; invisible toplevel trees at 1 Hz. The
// pacer sleeps while no callback waits.

// defaultFramePeriod paces surfaces without an output (60 Hz).
const defaultFramePeriod = time.Second / 60
const suspendedFramePeriod = time.Second
const suspendedFrameQueue = "\x00suspended" // never a wl_output name

// pace fires the due frame callbacks on page flips and deadlines.
func (s *Server) pace(ctx context.Context) {
	timer := time.NewTimer(defaultFramePeriod)
	defer timer.Stop()
	presented := s.channels.Presented
	if s.flipped == nil {
		s.flipped = make(map[string]bool)
	}
	for {
		clear(s.flipped)
		flipped := s.flipped
		s.frameReports = s.frameReports[:0]
		reports := s.frameReports
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case p := <-presented:
			flipped[p.Output] = flipped[p.Output] || paces(p)
			reports = append(reports, p)
		case <-s.frameReady:
		case <-timer.C:
		}
		// Coalesce flips queued while the display was busy: one burst of
		// callbacks per output, not one per stale flip.
	drain:
		for {
			select {
			case p := <-presented:
				flipped[p.Output] = flipped[p.Output] || paces(p)
				reports = append(reports, p)
			default:
				break drain
			}
		}
		s.frameReports = reports
		wait, idle := defaultFramePeriod, false
		if !s.display.Do(func() {
			if ctx.Err() == nil {
				// Each flip answers its own presentation feedbacks, in
				// order: flips are never merged here.
				for _, p := range reports {
					if p.Flip != nil {
						s.flips[p.Output] = *p.Flip
						s.presentFlip(p.Output, p.Flip)
					}
				}
				s.dropFeedbacks(time.Now())
				if len(s.feedbacks) > 0 {
					wait = feedbackTimeout
				}
				var due []string
				var dueWait time.Duration
				due, dueWait, idle = s.dueFrames(time.Now(), flipped)
				if len(s.feedbacks) > 0 {
					// Unanswered feedbacks time out: keep ticking.
					dueWait, idle = min(dueWait, feedbackTimeout), false
				}
				wait = dueWait
				for _, name := range due {
					s.sendFrames(name, flipped[name])
				}
				if s.releaseHeld(time.Now(), reports) && idle {
					// Held buffers wait for a flip or heldTimeout.
					wait, idle = heldTimeout, false
				}
				// Fifo barriers and queued commits wait for a refresh.
				if fw, waiting := s.tickFifo(time.Now(), flipped); waiting {
					wait, idle = min(wait, fw), false
				}
			}
		}) {
			return
		}
		if idle {
			// Nothing waits: sleep until a commit queues a callback
			// (frameReady) or a flip.
			timer.Stop()
			continue
		}
		timer.Reset(wait)
	}
}

// toplevelRoot resolves subsurfaces and popups to their parent toplevel.
func toplevelRoot(surf *surface) *surface {
	surf = surf.root()
	for surf.xdg != nil && surf.xdg.window != nil && surf.xdg.window.popup != nil && surf.xdg.window.popup.parent != nil {
		surf = surf.xdg.window.popup.parent.xdg.surface.root()
	}
	return surf
}

// invisible reports that no display shows the toplevel tree: the physical
// truth, used for scanout and presentation feedback. A tree rendered only
// for a capture session is invisible.
func (s *Server) invisible(surf *surface) bool {
	root := toplevelRoot(surf)
	return root.xdg != nil && root.xdg.window != nil && root.xdg.window.hasLast && !root.xdg.window.last.Visible
}

// suspended reports that nothing draws the toplevel tree, not even a capture
// session: it is throttled and marked suspended. A captured tree keeps the
// frame callbacks of its output so the capture keeps moving.
func (s *Server) suspended(surf *surface) bool {
	root := toplevelRoot(surf)
	if root.xdg == nil || root.xdg.window == nil || !root.xdg.window.hasLast {
		return false
	}
	return !root.xdg.window.last.Visible && !root.xdg.window.last.Captured
}

// frameOutput is the pacing class for a surface; "" is the 60 Hz
// outputless class, not the invisible toplevel class.
func (s *Server) frameOutput(surf *surface) string {
	surf = toplevelRoot(surf)
	switch {
	case surf.xdg != nil && surf.xdg.window != nil:
		if s.suspended(surf) {
			return suspendedFrameQueue
		}
		return surf.xdg.window.last.Output
	case surf.layer != nil && surf.layer.output != nil:
		return surf.layer.output.name()
	case surf.lock != nil && surf.lock.output != nil && !surf.lock.closed:
		return surf.lock.output.name()
	}
	return ""
}

// queueFrames adds a committed surface's callbacks to its output's queue
// ("" when it has none) and wakes the pacer.
func (s *Server) queueFrames(surf *surface, callbacks []*wayland.Callback) {
	name := s.frameOutput(surf)
	s.awaiting[name] = append(s.awaiting[name], callbacks...)
	if s.frameOwners == nil {
		s.frameOwners = make(map[*wayland.Callback]*surface)
	}
	for _, cb := range callbacks {
		s.frameOwners[cb] = surf
	}
	s.wakePacer()
}

// relocateCallbacks reclassifies callbacks committed before a visibility change.
func (s *Server) relocateCallbacks(root *surface) {
	moved := make(map[string][]*wayland.Callback)
	for name, callbacks := range s.awaiting {
		kept := callbacks[:0]
		for _, cb := range callbacks {
			owner := s.frameOwners[cb]
			if owner == nil || toplevelRoot(owner) != root || s.frameOutput(owner) == name {
				kept = append(kept, cb)
				continue
			}
			dest := s.frameOutput(owner)
			moved[dest] = append(moved[dest], cb)
		}
		if len(kept) == 0 {
			delete(s.awaiting, name)
		} else {
			s.awaiting[name] = kept
		}
	}
	for name, callbacks := range moved {
		s.awaiting[name] = append(s.awaiting[name], callbacks...)
		if name != suspendedFrameQueue {
			delete(s.frameDue, name) // resume without waiting for the old phase
		}
	}
	s.wakePacer()
}

// dueFrames returns the queues to fire now: flipped outputs and those past
// their deadline. wait is the time to the next deadline; idle is set when
// no callback waits after firing.
func (s *Server) dueFrames(now time.Time, flipped map[string]bool) (fire []string, wait time.Duration, idle bool) {
	if s.framePeriods == nil {
		s.framePeriods = make(map[string]time.Duration, len(s.outputs)+1)
	}
	periods := s.framePeriods
	clear(periods)
	for _, o := range s.outputs {
		periods[o.name()] = framePeriod(o.place.Info.RefreshMilli)
	}
	// Callbacks of surfaces whose output is gone go with the outputless ones.
	for name, callbacks := range s.awaiting {
		if _, ok := periods[name]; !ok && name != "" && name != suspendedFrameQueue {
			delete(s.awaiting, name)
			s.awaiting[""] = append(s.awaiting[""], callbacks...)
		}
	}
	periods[""] = defaultFramePeriod
	periods[suspendedFrameQueue] = suspendedFramePeriod
	for name := range s.frameDue {
		if _, ok := periods[name]; !ok {
			delete(s.frameDue, name)
		}
	}
	for name, p := range periods {
		due, ok := s.frameDue[name]
		switch {
		case flipped[name] && name != "":
			// A flipping output is paced by its flips; its deadline only
			// catches a missed one.
			fire = append(fire, name)
			s.frameDue[name] = now.Add(p + p/2)
		case !ok || !now.Before(due):
			if len(s.awaiting[name]) == 0 {
				// Idle: the next callback goes out as soon as it comes.
				delete(s.frameDue, name)
				continue
			}
			fire = append(fire, name)
			// Keep the phase; after a stall, restart from now.
			next := due.Add(p)
			if !ok || !next.After(now) {
				next = now.Add(p)
			}
			s.frameDue[name] = next
		}
	}
	waiting := false
	wait = suspendedFramePeriod
	for name, callbacks := range s.awaiting {
		if len(callbacks) == 0 || slices.Contains(fire, name) {
			continue
		}
		waiting = true
		if due, ok := s.frameDue[name]; ok {
			wait = min(wait, due.Sub(now))
		} else {
			wait = 0
		}
	}
	return fire, max(wait, time.Millisecond), !waiting
}

// paces reports whether a report paces frame callbacks: a flip of a real
// display. Software flips (headless) answer presentation feedback, but
// callbacks keep the output's refresh period.
func paces(p ports.OutputPresented) bool { return p.Flip != nil && p.Flip.HardwareClock }

// sendFrames fires an output's callbacks; flipped: a flip of it fired them.
func (s *Server) sendFrames(name string, flipped bool) {
	callbacks := s.awaiting[name]
	delete(s.awaiting, name)
	if len(callbacks) == 0 {
		return
	}
	// done carries the flip's time when the output flips (the frame the
	// client drew for is on screen), else now.
	ms := uint32(time.Since(s.started).Milliseconds())
	if f, ok := s.flips[name]; ok && flipped && f.When > 0 {
		ms = uint32(max(f.When-monotonic(s.started), 0).Milliseconds())
	}
	for _, cb := range callbacks {
		delete(s.frameOwners, cb)
		if !cb.Resource.Alive() {
			continue
		}
		cb.SendDone(ms)
		cb.Destroy()
		s.frames++
	}
}

// framePeriod is one refresh at refreshMilli mHz, 60 Hz when unknown.
func framePeriod(refreshMilli int) time.Duration {
	if refreshMilli <= 0 {
		return defaultFramePeriod
	}
	return time.Duration(int64(time.Second) * 1000 / int64(refreshMilli))
}

// monotonic is t on CLOCK_MONOTONIC, the clock of flip timestamps.
func monotonic(t time.Time) time.Duration {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
		return 0
	}
	return time.Duration(ts.Nano()) - time.Since(t)
}
