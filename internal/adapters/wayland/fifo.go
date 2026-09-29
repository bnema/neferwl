package wayland

import (
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/committiming"
	"github.com/bnema/purego-libwayland/protocol/contenttype"
	"github.com/bnema/purego-libwayland/protocol/fifo"
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
// Every commit captures its pending state into an update; the pacer (frames.go)
// applies queued updates in order when they are ready. Requests arriving after
// capture stay in live pending state. wp_content_type_v1 is logged only.

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
		h.surface.next.barrier = true
	}
}
func (h *fifoHandler) WaitBarrier(r *fifo.WpFifoV1) {
	if h.live(r) {
		h.surface.next.wait = true
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
	case !h.surface.next.at.IsZero():
		r.PostError(uint32(committiming.WpCommitTimerV1ErrorTimestampExists), "timestamp already set")
	default:
		h.surface.next.at = monotonicTime(uint64(secHi)<<32|uint64(secLo), int64(nsec))
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
			state.next.kind = 0
		}
	}
}

type contentTypeHandler struct{ surface *surface }

func (h *contentTypeHandler) SetContentType(_ *contenttype.WpContentTypeV1, kind uint32) {
	if h.surface != nil && !h.surface.destroyed && h.surface.contentType == h {
		h.surface.next.kind = kind
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

// update owns a captured commit until it applies or is discarded.
type update struct {
	// pendingCommit is the wl_surface state captured by the commit.
	pendingCommit
	vp              *viewport
	vpW, vpH        int32
	vpSet           bool
	vpSrc           [4]server.Fixed
	vpCrop          bool
	layout          []childLayout
	deps            []*update
	owner           *surface
	implicit        [4]*syncWait
	prev            *update
	synced, bound   bool
	xdg             *xdgSurface
	geometry        ports.Rect
	cons            *constraint
	region          *ports.Rect
	layer           *layerSurface
	layerNext       layerState
	readyGeneration uint64
	readyResult     bool
	// refs counts incoming deps and prev links. Retired updates remain readable
	// until their last incoming link goes away, even after dropQueue.
	refs    int
	retired bool
}

// updatePool contains only retired updates with no incoming graph links. A
// popped update is NOT reusable just because it left its owner's queue: a
// parent's queued deps can still point to it (including after role teardown).
// The display owner goroutine changes graph links; sync.Pool only shares dead,
// zeroed storage between independent displays.
var updatePool sync.Pool

func (u *update) releaseRef() {
	u.refs--
	if u.refs == 0 && u.retired {
		deps := u.deps[:0]
		*u = update{deps: deps}
		updatePool.Put(u)
	}
}

// retireUpdate severs outgoing links only after the update is no longer used
// by applyUpdate/dropQueue. Incoming links keep its identity until consumed.
func (u *update) retireUpdate() {
	u.retired = true
	prev, deps := u.prev, u.deps
	u.prev, u.deps = nil, deps[:0]
	if prev != nil {
		prev.releaseRef()
	}
	for i, dep := range deps {
		deps[i] = nil // the pooled backing array must not retain old dependencies
		dep.releaseRef()
	}
	if u.refs == 0 {
		*u = update{deps: u.deps}
		updatePool.Put(u)
	}
}

type childLayout struct {
	child *surface
	x, y  int
	below bool
}

func removeLayout(layout []childLayout, child *surface) []childLayout {
	layout = append([]childLayout(nil), layout...)
	for i := 0; i < len(layout); i++ {
		if layout[i].child == child {
			layout = append(layout[:i:i], layout[i+1:]...)
			i--
		}
	}
	return layout
}

func sameLayout(a, b []childLayout) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// takePending moves the pending state into an update: one-shot state is
// cleared, sticky state (scale, hints, viewport, geometry, layer) kept.
func (s *surface) takePending() update {
	u := update{pendingCommit: s.next}
	// One-shot requests apply once; sticky ones carry over.
	s.next = pendingCommit{scale: s.next.scale, transform: s.next.transform, async: s.next.async, color: s.next.color, representation: s.next.representation, kind: s.next.kind}
	if v := s.viewport; v != nil {
		u.vp, u.vpW, u.vpH, u.vpSet, u.vpSrc, u.vpCrop = v, v.pendingW, v.pendingH, v.pendingSet, v.pendingSrc, v.pendingCrop
	}
	u.layout = s.sub.pendingLayout
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
	u, _ := updatePool.Get().(*update)
	if u == nil {
		u = new(update)
	}
	deps := u.deps[:0]
	*u = s.takePending()
	u.deps = deps
	u.owner, u.synced = s, s.effectivelySynced()
	if s.server.syncWait != nil && u.attached && u.buffer != nil && u.sync == nil {
		if b, ok := s.server.buffers[u.buffer.Resource].(*dmabufBuffer); ok {
			err := s.server.syncWait.watchImplicit(b.buf, &u.implicit)
			if err != nil {
				s.server.log.Warn().Str("component", "wayland").Err(err).Msg("implicit buffer wait")
			}
		}
	}
	for _, ch := range s.sub.children {
		for i := len(ch.queue) - 1; i >= 0; i-- {
			candidate := ch.queue[i]
			if candidate.synced && !candidate.bound {
				candidate.bound = true
				u.deps = append(u.deps, candidate)
				candidate.refs++
				break
			}
		}
	}
	if len(s.queue) > 0 {
		u.prev = s.queue[len(s.queue)-1]
		u.prev.refs++
	}
	s.queue = append(s.queue, u)
	s.server.fifoSurfaces[s] = struct{}{}
	s.server.wakePacer()
}

// applyUpdate applies only the captured commit; later requests remain pending.
func (s *surface) applyUpdate(u *update) {
	// A buffer destroyed while queued (a swapchain resize) is skipped: the
	// surface keeps its content, and is not unmapped behind the client.
	s.commitSkipped = false
	s.queuedScale = u.scale
	s.queuedBuffer = 0
	if u.buffer != nil {
		s.queuedBuffer = u.buffer.ID()
	}
	if u.buffer != nil && !u.buffer.Resource.Alive() {
		s.commitSkipped = true
		u.buffer, u.attached = nil, false
		// Scale, transform and crop describe the same retained buffer.
		u.scale = s.bufferScale
		u.transform = s.transform
		// The viewport change was made for that buffer: keep the crop that
		// matches the retained content instead of validating a mismatch.
		if u.vp != nil && u.vp.resource != nil && u.vp.resource.Resource.Alive() {
			v := &s.committedViewport
			u.vpW, u.vpH, u.vpSet, u.vpSrc, u.vpCrop = v.destW, v.destH, v.dest, v.src, v.crop
		}
	}
	s.applyCommit(u)
	s.commitSkipped = false
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
			s.server.queueFrames(s, u.callbacks)
		}
		for _, fb := range u.feedback {
			discard(fb)
		}
		s.dropSync(u.sync)
		for _, w := range u.implicit {
			s.server.syncWait.cancel(w)
		}
		if u.sync != nil {
			continue // explicit sync: the release point replaced release
		}
		if u.buffer != nil && !sameBuffer(u.buffer, s.current) && !released[u.buffer.Resource] && u.buffer.Resource.Alive() && !s.bufferQueuedElsewhere(u.buffer) {
			released[u.buffer.Resource] = true
			u.buffer.SendRelease()
		}
	}
	for i, u := range s.queue {
		s.queue[i] = nil // do not retain retired objects in the queue backing array
		u.retireUpdate()
	}
	s.queue = s.queue[:0]
	delete(s.server.fifoSurfaces, s)
}

func (s *surface) bufferQueuedElsewhere(b *wayland.Buffer) bool {
	for _, other := range s.server.surfaces {
		if other == s {
			continue
		}
		if sameBuffer(other.current, b) {
			return true
		}
		for _, u := range other.queue {
			if sameBuffer(u.buffer, b) {
				return true
			}
		}
	}
	return false
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
	if name != "" && name != suspendedFrameQueue && s.outputByNameExact(name) == nil {
		return ""
	}
	return name
}

// outputPeriod is one refresh of an output ("": 60 Hz).
func (s *Server) outputPeriod(name string) time.Duration {
	if name == suspendedFrameQueue {
		return suspendedFramePeriod
	}
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
	wait := suspendedFramePeriod
	s.readinessGeneration++
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
			if u.synced || !u.graphReady(now) {
				break
			}
			s.applyingGraph = true
			s.graphDrawn = false
			s.graphFeedback = s.graphFeedback[:0]
			u.applyGraph()
			s.applyingGraph = false
			// Applying a graph changes queues and readiness within this tick.
			s.readinessGeneration++
			if s.graphDrawn {
				surf.redraw()
			}
			// Only the last commit per surface can be sampled from this
			// publication. Earlier callbacks were already queued on apply.
			for i, fb := range s.graphFeedback {
				superseded := false
				for j := i + 1; j < len(s.graphFeedback); j++ {
					if s.graphFeedback[j].surf == fb.surf && s.graphFeedback[j].fresh {
						superseded = true
						break
					}
				}
				if superseded {
					for _, pending := range fb.pending {
						discard(pending)
					}
					continue
				}
				fb.surf.commitFeedback(fb.pending, fb.fresh)
			}
			s.graphFeedback = s.graphFeedback[:0]
		}
		if surf.barrier {
			wait = min(wait, deadline.Sub(now))
		}
		if len(surf.queue) > 0 {
			wait = min(wait, p)
		}
		if !surf.barrier && len(surf.queue) == 0 {
			delete(s.fifoSurfaces, surf)
		}
	}
	return max(wait, time.Millisecond), len(s.fifoSurfaces) > 0
}

func (s *surface) effectivelySynced() bool {
	for p := s; p != nil; p = p.sub.parent {
		if p.sub.parent != nil && p.sub.synced {
			return true
		}
	}
	return false
}

func (s *surface) flushDesync() {
	if !s.effectivelySynced() {
		for _, u := range s.queue {
			if u.synced && !u.bound {
				u.synced = false
			}
		}
	}
	for _, ch := range s.sub.children {
		ch.flushDesync()
	}
	s.server.wakePacer()
}

func (u *update) graphReady(now time.Time) bool {
	if u.retired {
		return true // applied or dropped; a queued parent cannot replay it
	}
	s := u.owner
	gen := s.server.readinessGeneration
	if gen != 0 && u.readyGeneration == gen {
		return u.readyResult
	}
	if gen != 0 {
		u.readyGeneration, u.readyResult = gen, false
	}
	if u.prev != nil && !u.prev.graphReady(now) {
		return false
	}
	if u.wait && s.barrier || !s.syncReady(u.sync) || s.tooEarly(u.at, now) {
		return false
	}
	for _, w := range u.implicit {
		if w != nil && !s.server.syncWait.fired(w) {
			return false
		}
	}
	for _, dep := range u.deps {
		if !dep.owner.destroyed && !dep.graphReady(now) {
			return false
		}
	}
	u.readyResult = true
	return true
}

func (u *update) applyGraph() {
	if u.retired {
		return
	}
	s := u.owner
	for len(s.queue) > 0 && s.queue[0] != u {
		s.queue[0].applyGraph()
	}
	for _, dep := range u.deps {
		if !dep.owner.destroyed {
			dep.applyGraph()
		}
	}
	if len(s.queue) == 0 || s.queue[0] != u {
		return
	}
	s.queue[0] = nil
	if len(s.queue) == 1 {
		s.queue = s.queue[:0] // retain capacity for the next commit
	} else {
		s.queue = s.queue[1:]
	}
	if len(s.queue) > 0 {
		next := s.queue[0]
		next.prev = nil
		u.releaseRef() // next's prev no longer points to u
	}
	s.applyUpdate(u)
	u.retireUpdate()
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
