package wayland

import (
	"context"
	"slices"
	"time"

	"github.com/bnema/nefertty/internal/ports"

	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// Frame callbacks (wl_surface.frame) tell a client when to draw its next
// frame. They are paced per output: on its page flip when it flips, else
// at its refresh rate, so a 165 Hz output lets clients draw 165 frames a
// second and a V-Sync game follows the screen. Surfaces with no output
// (hidden windows, cursors, unplaced surfaces) are paced at 60 Hz. The
// pacer sleeps while no callback waits.

// defaultFramePeriod paces surfaces without an output (60 Hz).
const defaultFramePeriod = time.Second / 60

// pace fires the due frame callbacks on page flips and deadlines.
func (s *Server) pace(ctx context.Context) {
	timer := time.NewTimer(defaultFramePeriod)
	defer timer.Stop()
	presented := s.channels.Presented
	for {
		flipped := map[string]bool{}
		var reports []ports.OutputPresented
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case p := <-presented:
			flipped[p.Output] = flipped[p.Output] || p.Flip != nil
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
				flipped[p.Output] = flipped[p.Output] || p.Flip != nil
				reports = append(reports, p)
			default:
				break drain
			}
		}
		wait, idle := defaultFramePeriod, false
		if !s.display.Do(func() {
			if ctx.Err() == nil {
				var due []string
				due, wait, idle = s.dueFrames(time.Now(), flipped)
				for _, name := range due {
					s.sendFrames(name)
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

// frameOutput is the output showing a surface, "" when none does (a hidden
// window, a cursor): unlike outputOfSurface, no fallback to the focused one.
func (s *Server) frameOutput(surf *surface) string {
	surf = surf.root()
	for surf.xdg != nil && surf.xdg.window != nil && surf.xdg.window.popup != nil && surf.xdg.window.popup.parent != nil {
		surf = surf.xdg.window.popup.parent.xdg.surface.root()
	}
	switch {
	case surf.xdg != nil && surf.xdg.window != nil:
		return surf.xdg.window.last.Output
	case surf.layer != nil && surf.layer.output != nil:
		return surf.layer.output.name()
	}
	return ""
}

// queueFrames adds a committed surface's callbacks to its output's queue
// ("" when it has none) and wakes the pacer.
func (s *Server) queueFrames(name string, callbacks []*wayland.Callback) {
	s.awaiting[name] = append(s.awaiting[name], callbacks...)
	select {
	case s.frameReady <- struct{}{}:
	default:
	}
}

// dueFrames returns the queues to fire now: flipped outputs and those past
// their deadline. wait is the time to the next deadline; idle is set when
// no callback waits after firing.
func (s *Server) dueFrames(now time.Time, flipped map[string]bool) (fire []string, wait time.Duration, idle bool) {
	periods := make(map[string]time.Duration, len(s.outputs)+1)
	for _, o := range s.outputs {
		periods[o.name()] = framePeriod(o.place.Info.RefreshMilli)
	}
	// Callbacks of surfaces whose output is gone go with the outputless ones.
	for name, callbacks := range s.awaiting {
		if _, ok := periods[name]; !ok && name != "" {
			delete(s.awaiting, name)
			s.awaiting[""] = append(s.awaiting[""], callbacks...)
		}
	}
	periods[""] = defaultFramePeriod
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
	wait = defaultFramePeriod
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

func (s *Server) sendFrames(name string) {
	callbacks := s.awaiting[name]
	delete(s.awaiting, name)
	if len(callbacks) == 0 {
		return
	}
	ms := uint32(time.Since(s.started).Milliseconds())
	for _, cb := range callbacks {
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
