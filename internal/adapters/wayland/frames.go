package wayland

import (
	"context"
	"time"
)

// Frame callbacks (wl_surface.frame) tell a client when to draw its next
// frame. They are paced per output: on its page flip when it flips, else
// at its refresh rate, so a 165 Hz output lets clients draw 165 frames a
// second and a V-Sync game follows the screen.

// defaultFramePeriod paces surfaces without an output (60 Hz).
const defaultFramePeriod = time.Second / 60

// pace fires the due frame callbacks on each page flip and timer tick.
func (s *Server) pace(ctx context.Context) {
	timer := time.NewTimer(defaultFramePeriod)
	defer timer.Stop()
	presented := s.channels.Presented
	for {
		flipped, flip := "", false
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case p := <-presented:
			flipped, flip = p.Output, true
		case <-timer.C:
		}
		wait := defaultFramePeriod
		if !s.display.Do(func() {
			if ctx.Err() == nil {
				wait = s.fireFrames(time.Now(), flipped, flip)
			}
		}) {
			return
		}
		timer.Reset(wait)
	}
}

// fireFrames sends the frame callbacks due at now (all those of a flipped
// output) and returns the time to the next deadline.
func (s *Server) fireFrames(now time.Time, flipped string, flip bool) time.Duration {
	periods := make(map[string]time.Duration, len(s.outputs))
	for _, o := range s.outputs {
		periods[o.name()] = framePeriod(o.place.Info.RefreshMilli)
	}
	if flip {
		if p, ok := periods[flipped]; ok {
			s.sendFrames(flipped)
			// A flipping output is paced by its flips; the timer only
			// catches a missed one.
			s.frameDue[flipped] = now.Add(p + p/2)
		}
	}
	for name := range s.frameDue {
		if _, ok := periods[name]; !ok {
			delete(s.frameDue, name)
		}
	}
	for name, p := range periods {
		if due, ok := s.frameDue[name]; !ok || !now.Before(due) {
			s.sendFrames(name)
			s.frameDue[name] = now.Add(p)
		}
	}
	// Callbacks of surfaces without a known output go at the default rate.
	for name := range s.awaiting {
		if _, ok := periods[name]; !ok {
			s.sendFrames(name)
		}
	}
	wait := defaultFramePeriod
	for _, due := range s.frameDue {
		wait = min(wait, due.Sub(now))
	}
	return max(wait, time.Millisecond)
}

func (s *Server) sendFrames(name string) {
	callbacks := s.awaiting[name]
	if len(callbacks) == 0 {
		return
	}
	delete(s.awaiting, name)
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
