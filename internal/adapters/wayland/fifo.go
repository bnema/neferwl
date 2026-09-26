package wayland

import (
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/committiming"
	"github.com/bnema/purego-libwayland/protocol/contenttype"
	"github.com/bnema/purego-libwayland/protocol/fifo"
	"github.com/bnema/purego-libwayland/protocol/presentationtime"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// Presentation constraints on content updates (games, Vulkan FIFO):
//
//   - wp_fifo_v1: a commit with wait_barrier waits while the barrier set
//     by an earlier commit stands; the barrier clears at the output's
//     next refresh (its flip, or a deadline when it does not flip).
//   - wp_commit_timing_v1: a commit waits until the refresh that shows it
//     is not before its timestamp (CLOCK_MONOTONIC).
//
// A commit that must wait is taken out of the surface's pending state and
// queued; the pacer (frames.go) applies queued updates in order when they
// are ready. wp_content_type_v1 is recorded and logged only.

func registerPresentationConstraints(d *server.Display, s *Server) error {
	if err := fifo.NewWpFifoManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = fifo.NewWpFifoManagerV1(c, int32(v), id, fifoManager{s})
	}); err != nil {
		return err
	}
	if err := committiming.NewWpCommitTimingManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = committiming.NewWpCommitTimingManagerV1(c, int32(v), id, timingManager{s})
	}); err != nil {
		return err
	}
	return contenttype.NewWpContentTypeManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = contenttype.NewWpContentTypeManagerV1(c, int32(v), id, contentTypeManager{s})
	})
}

// fifo_v1

type fifoManager struct{ server *Server }

func (fifoManager) Destroy(*fifo.WpFifoManagerV1) {}

func (m fifoManager) GetFifo(r *fifo.WpFifoManagerV1, id uint32, surf *wayland.Surface) {
	state := m.server.surfaceOf(surf)
	if state != nil && state.fifo != nil {
		r.PostError(uint32(fifo.WpFifoManagerV1ErrorAlreadyExists), "surface already has a fifo")
		return
	}
	h := &fifoHandler{surface: state}
	res, err := fifo.NewWpFifoV1(r.Client(), r.Version(), id, h)
	if err != nil || state == nil {
		return
	}
	state.fifo = h
	res.OnDestroy = func() {
		if state.fifo == h {
			state.fifo = nil
		}
	}
}

type fifoHandler struct{ surface *surface }

func (h *fifoHandler) live(r *fifo.WpFifoV1) bool {
	if h.surface == nil || h.surface.destroyed {
		r.PostError(uint32(fifo.WpFifoV1ErrorSurfaceDestroyed), "surface destroyed")
		return false
	}
	return true
}
func (h *fifoHandler) SetBarrier(r *fifo.WpFifoV1) {
	if h.live(r) {
		h.surface.pendingBarrier = true
	}
}
func (h *fifoHandler) WaitBarrier(r *fifo.WpFifoV1) {
	if h.live(r) {
		h.surface.pendingWait = true
	}
}
func (*fifoHandler) Destroy(*fifo.WpFifoV1) {}

// commit_timing_v1

type timingManager struct{ server *Server }

func (timingManager) Destroy(*committiming.WpCommitTimingManagerV1) {}

func (m timingManager) GetTimer(r *committiming.WpCommitTimingManagerV1, id uint32, surf *wayland.Surface) {
	state := m.server.surfaceOf(surf)
	if state != nil && state.timer != nil {
		r.PostError(uint32(committiming.WpCommitTimingManagerV1ErrorCommitTimerExists), "surface already has a commit timer")
		return
	}
	h := &timerHandler{surface: state}
	res, err := committiming.NewWpCommitTimerV1(r.Client(), r.Version(), id, h)
	if err != nil || state == nil {
		return
	}
	state.timer = h
	res.OnDestroy = func() {
		if state.timer == h {
			state.timer = nil
		}
	}
}

type timerHandler struct{ surface *surface }

func (h *timerHandler) SetTimestamp(r *committiming.WpCommitTimerV1, secHi, secLo, nsec uint32) {
	switch {
	case h.surface == nil || h.surface.destroyed:
		r.PostError(uint32(committiming.WpCommitTimerV1ErrorSurfaceDestroyed), "surface destroyed")
	case nsec >= 1e9:
		r.PostError(uint32(committiming.WpCommitTimerV1ErrorInvalidTimestamp), "tv_nsec out of range")
	case !h.surface.pendingTime.IsZero():
		r.PostError(uint32(committiming.WpCommitTimerV1ErrorTimestampExists), "timestamp already set")
	default:
		h.surface.pendingTime = monotonicTime(uint64(secHi)<<32|uint64(secLo), int64(nsec))
	}
}
func (*timerHandler) Destroy(*committiming.WpCommitTimerV1) {}

// maxTimestampAhead bounds how far a commit timestamp can hold a commit;
// a later one applies early (a deviation from "not before").
const maxTimestampAhead = time.Second

// monotonicTime converts a CLOCK_MONOTONIC time to wall time, at most
// maxTimestampAhead from now: a far timestamp must not stall the surface.
func monotonicTime(sec uint64, nsec int64) time.Time {
	var now unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &now) != nil {
		return time.Now()
	}
	ahead := time.Duration(nsec - now.Nsec)
	switch n := uint64(now.Sec); {
	case sec >= n+2:
		ahead = maxTimestampAhead
	case sec+2 <= n:
		ahead = 0 // in the past: apply now
	default:
		ahead += time.Duration(int64(sec)-now.Sec) * time.Second
	}
	return time.Now().Add(min(max(ahead, 0), maxTimestampAhead))
}

// content_type_v1

type contentTypeManager struct{ server *Server }

func (contentTypeManager) Destroy(*contenttype.WpContentTypeManagerV1) {}

func (m contentTypeManager) GetSurfaceContentType(r *contenttype.WpContentTypeManagerV1, id uint32, surf *wayland.Surface) {
	state := m.server.surfaceOf(surf)
	if state != nil && state.contentType != nil {
		r.PostError(uint32(contenttype.WpContentTypeManagerV1ErrorAlreadyConstructed), "surface already has a content type")
		return
	}
	h := &contentTypeHandler{surface: state}
	res, err := contenttype.NewWpContentTypeV1(r.Client(), r.Version(), id, h)
	if err != nil || state == nil {
		return
	}
	state.contentType = h
	res.OnDestroy = func() {
		// Destroying it sets the type back to none, double-buffered.
		if state.contentType == h {
			state.contentType = nil
			state.pendingKind = 0
		}
	}
}

type contentTypeHandler struct{ surface *surface }

func (h *contentTypeHandler) SetContentType(_ *contenttype.WpContentTypeV1, kind uint32) {
	if h.surface != nil && !h.surface.destroyed && h.surface.contentType == h {
		h.surface.pendingKind = kind
	}
}
func (*contentTypeHandler) Destroy(*contenttype.WpContentTypeV1) {}

func (s *Server) surfaceOf(w *wayland.Surface) *surface {
	if w == nil {
		return nil
	}
	return s.surfaces[w.Resource]
}

// Content update queue

// update is a surface's pending state taken out by a commit that waits.
type update struct {
	attached  bool
	buffer    *wayland.Buffer
	scale     int
	async     bool
	kind      uint32
	callbacks []*wayland.Callback
	vp        *viewport
	vpW, vpH  int32
	vpSet     bool
	moves     []childMove
	xdg       *xdgSurface
	geometry  ports.Rect
	cons      *constraint
	region    *ports.Rect
	layer     *layerSurface
	layerNext layerState
	barrier   bool
	wait      bool
	at        time.Time
	feedback  []*presentationtime.WpPresentationFeedback
	damage    []ports.Rect
	bufDamage []ports.Rect
}

type childMove struct {
	child *surface
	x, y  int
}

// takePending moves the pending state into an update: one-shot state is
// cleared, sticky state (scale, hints, viewport, geometry, layer) kept.
func (s *surface) takePending() update {
	u := update{attached: s.attached, buffer: s.pending, scale: s.pendingScale, async: s.pendingAsync, kind: s.pendingKind, callbacks: s.callbacks, barrier: s.pendingBarrier, wait: s.pendingWait, at: s.pendingTime, damage: s.pendingDamage, bufDamage: s.pendingBufDamage, feedback: s.pendingFeedback}
	s.attached, s.pending, s.callbacks = false, nil, nil
	s.pendingFeedback = nil
	s.pendingDamage, s.pendingBufDamage = nil, nil
	s.pendingBarrier, s.pendingWait, s.pendingTime = false, false, time.Time{}
	if v := s.viewport; v != nil {
		u.vp, u.vpW, u.vpH, u.vpSet = v, v.pendingW, v.pendingH, v.pendingSet
	}
	for _, ch := range s.sub.children {
		if ch.sub.moved {
			u.moves = append(u.moves, childMove{ch, ch.sub.pendX, ch.sub.pendY})
			ch.sub.moved = false
		}
	}
	if s.xdg != nil {
		u.xdg, u.geometry = s.xdg, s.xdg.pendingGeometry
	}
	if c := s.server.constraints[s]; c != nil && c.pending != nil {
		u.cons, u.region, c.pending = c, c.pending, nil
	}
	if s.layer != nil {
		u.layer, u.layerNext = s.layer, s.layer.pending
	}
	return u
}

// putPending makes an update the surface's pending state again. Parts
// whose object is gone (viewport, child, role, constraint) are dropped.
func (s *surface) putPending(u update) {
	s.attached, s.pending, s.callbacks = u.attached, u.buffer, u.callbacks
	s.pendingScale, s.pendingAsync, s.pendingKind = u.scale, u.async, u.kind
	s.pendingBarrier, s.pendingWait, s.pendingTime = u.barrier, u.wait, u.at
	s.pendingDamage, s.pendingBufDamage = u.damage, u.bufDamage
	s.pendingFeedback = u.feedback
	if u.vp != nil && s.viewport == u.vp {
		u.vp.pendingW, u.vp.pendingH, u.vp.pendingSet = u.vpW, u.vpH, u.vpSet
	}
	for _, m := range u.moves {
		if m.child.sub.parent == s {
			m.child.sub.pendX, m.child.sub.pendY, m.child.sub.moved = m.x, m.y, true
		}
	}
	if u.xdg != nil && s.xdg == u.xdg {
		u.xdg.pendingGeometry = u.geometry
	}
	if u.cons != nil && s.server.constraints[s] == u.cons {
		u.cons.pending = u.region
	}
	if u.layer != nil && s.layer == u.layer {
		u.layer.pending = u.layerNext
	}
}

// mustWait reports whether the pending commit cannot apply now.
func (s *surface) mustWait(now time.Time) bool {
	return len(s.queue) > 0 || (s.pendingWait && s.barrier) || s.tooEarly(s.pendingTime, now)
}

// tooEarly reports whether content applied now would show before at:
// it reaches the screen one refresh later.
func (s *surface) tooEarly(at, now time.Time) bool {
	return !at.IsZero() && at.After(s.server.nextRefresh(s.server.fifoOutput(s), now))
}

// nextRefresh is when the output's next frame reaches the screen: the
// vblank after now counted from its last flip (kernel time, refresh
// period), or one period from now without flips or while VRR is on.
func (s *Server) nextRefresh(name string, now time.Time) time.Time {
	p := s.outputPeriod(name)
	f, ok := s.flips[name]
	if !ok || f.When <= 0 || f.Refresh <= 0 {
		return now.Add(p)
	}
	mono := monotonic(now)
	since := mono - f.When
	if since < 0 || since > time.Second {
		return now.Add(p)
	}
	n := since/f.Refresh + 1
	return now.Add(f.When + n*f.Refresh - mono)
}

// queueUpdate queues the pending commit until it can apply.
func (s *surface) queueUpdate() {
	s.queue = append(s.queue, s.takePending())
	s.server.fifoSurfaces[s] = struct{}{}
	s.server.wakePacer()
}

// applyUpdate applies a queued update, keeping the requests made since.
func (s *surface) applyUpdate(u update) {
	// A buffer destroyed while queued (a swapchain resize) is skipped: the
	// surface keeps its content, and is not unmapped behind the client.
	if u.buffer != nil && !u.buffer.Resource.Alive() {
		u.buffer, u.attached = nil, false
	}
	later := s.takePending()
	s.putPending(u)
	s.applyCommit()
	s.putPending(later)
}

// dropQueue discards the queued updates of a destroyed surface or role.
// A live surface still gets its queued frame callbacks.
func (s *surface) dropQueue() {
	released := map[*server.Resource]bool{}
	for _, u := range s.queue {
		if s.destroyed {
			for _, cb := range u.callbacks {
				cb.Destroy()
			}
		} else if len(u.callbacks) > 0 {
			s.server.queueFrames(s.server.frameOutput(s), u.callbacks)
		}
		for _, fb := range u.feedback {
			discard(fb)
		}
		if u.buffer != nil && !sameBuffer(u.buffer, s.current) && !released[u.buffer.Resource] && u.buffer.Resource.Alive() {
			released[u.buffer.Resource] = true
			u.buffer.SendRelease()
		}
	}
	s.queue = nil
	delete(s.server.fifoSurfaces, s)
}

// setBarrier applies a committed set_barrier.
func (s *surface) setBarrier(now time.Time) {
	s.barrier, s.barrierAt = true, now
	s.server.fifoSurfaces[s] = struct{}{}
	s.server.wakePacer()
}

// fifoOutput is the output pacing a surface, "" when none does.
func (s *Server) fifoOutput(surf *surface) string {
	name := s.frameOutput(surf)
	if name != "" && s.outputByNameExact(name) == nil {
		return ""
	}
	return name
}

// outputPeriod is one refresh of an output ("": 60 Hz).
func (s *Server) outputPeriod(name string) time.Duration {
	if o := s.outputByNameExact(name); o != nil && name != "" {
		return framePeriod(o.place.Info.RefreshMilli)
	}
	return defaultFramePeriod
}

// tickFifo clears the barriers whose latching deadline passed and applies
// the queued updates now ready. A flip of the surface's output is its
// deadline; outputs that do not flip (headless, switched away, no output)
// clear it after one refresh, or 1.5 when they usually flip. It returns
// the time to the next deadline and whether any surface still waits.
func (s *Server) tickFifo(now time.Time, flipped map[string]bool) (time.Duration, bool) {
	for name, f := range flipped {
		if f {
			s.lastFlip[name] = now
		}
	}
	for name := range s.lastFlip {
		if s.outputByNameExact(name) == nil {
			delete(s.lastFlip, name)
		}
	}
	wait := defaultFramePeriod
	for surf := range s.fifoSurfaces {
		name := s.fifoOutput(surf)
		p := s.outputPeriod(name)
		// The deadline of outputs that usually flip only catches a missed flip.
		deadline := surf.barrierAt.Add(p)
		if name != "" && now.Sub(s.lastFlip[name]) < time.Second {
			deadline = deadline.Add(p / 2)
		}
		if surf.barrier && ((flipped[name] && name != "" && s.lastFlip[name].After(surf.barrierAt)) || !now.Before(deadline)) {
			surf.barrier = false
		}
		for len(surf.queue) > 0 && !surf.destroyed {
			u := surf.queue[0]
			if u.wait && surf.barrier {
				break
			}
			if surf.tooEarly(u.at, now) {
				wait = min(wait, u.at.Sub(s.nextRefresh(name, now))+time.Millisecond)
				break
			}
			surf.queue = surf.queue[1:]
			surf.applyUpdate(u)
		}
		if surf.barrier {
			wait = min(wait, deadline.Sub(now))
		}
		if !surf.barrier && len(surf.queue) == 0 {
			delete(s.fifoSurfaces, surf)
		}
	}
	return max(wait, time.Millisecond), len(s.fifoSurfaces) > 0
}

// sameBuffer reports whether two wrappers are the same wl_buffer.
func sameBuffer(a, b *wayland.Buffer) bool {
	return a != nil && b != nil && a.Resource == b.Resource
}

// wakePacer makes the pacer look at its queues now.
func (s *Server) wakePacer() {
	select {
	case s.frameReady <- struct{}{}:
	default:
	}
}
