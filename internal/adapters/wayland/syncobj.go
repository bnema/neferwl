package wayland

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/linuxdrmsyncobj"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// wp_linux_drm_syncobj_v1: explicit sync. A client names, per commit, the
// timeline point its GPU signals when the buffer is drawn (acquire) and
// the point we signal once nothing reads the buffer any more (release).
// A commit waits (like a fifo barrier) until its acquire point has a
// fence; the fence then travels with the content as a sync file the
// renderer and the display wait on, so no CPU waits for the GPU. The
// release point replaces wl_buffer.release.

const syncobjVersion = 1

// timeline is an imported client timeline.
type timeline struct {
	dev    syncobjDevice
	handle uint32
	alive  bool
	// uses counts committed points still to signal or wait on.
	uses int
}

// syncPoint is a point on a timeline.
type syncPoint struct {
	tl    *timeline
	point uint64
}

func (p syncPoint) set() bool { return p.tl != nil }

// syncState is a surface's wp_linux_drm_syncobj_surface_v1 state.
type syncState struct {
	resource         *linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1
	acquire, release syncPoint // pending, for the next commit
}

func registerSyncobj(d *server.Display, s *Server) error {
	if s.syncDev == nil {
		return nil
	}
	return linuxdrmsyncobj.NewWpLinuxDrmSyncobjManagerV1Global(d, syncobjVersion, func(c server.Client, v, id uint32) {
		_, _ = linuxdrmsyncobj.NewWpLinuxDrmSyncobjManagerV1(c, int32(v), id, syncManager{s})
	})
}

type syncManager struct{ server *Server }

func (syncManager) Destroy(*linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1) {}

func (m syncManager) GetSurface(r *linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1, id uint32, surf *wayland.Surface) {
	state := m.server.surfaceOf(surf)
	if state == nil {
		return
	}
	if state.sync != nil {
		r.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1ErrorSurfaceExists), "surface already has a syncobj surface")
		return
	}
	st := &syncState{}
	res, err := linuxdrmsyncobj.NewWpLinuxDrmSyncobjSurfaceV1(r.Client(), r.Version(), id, syncSurface{state, st})
	if err != nil {
		return
	}
	st.resource = res
	state.sync = st
	res.OnDestroy = func() {
		if state.sync == st {
			state.sync = nil
		}
	}
}

func (m syncManager) ImportTimeline(r *linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1, id uint32, fd int) {
	defer unix.Close(fd)
	h, err := m.server.syncDev.fdToHandle(fd)
	if err != nil {
		r.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1ErrorInvalidTimeline), "invalid timeline fd")
		return
	}
	tl := &timeline{dev: m.server.syncDev, handle: h, alive: true}
	res, err := linuxdrmsyncobj.NewWpLinuxDrmSyncobjTimelineV1(r.Client(), r.Version(), id, timelineHandler{})
	if err != nil {
		_ = tl.dev.destroy(h)
		return
	}
	m.server.timelines[res.Resource] = tl
	res.OnDestroy = func() {
		delete(m.server.timelines, res.Resource)
		// Points already committed keep the syncobj: destroyed once no
		// held buffer or waiting commit uses it (timeline.drop).
		tl.alive = false
		m.server.dropTimeline(tl)
	}
}

type timelineHandler struct{}

func (timelineHandler) Destroy(*linuxdrmsyncobj.WpLinuxDrmSyncobjTimelineV1) {}

type syncSurface struct {
	surf *surface
	st   *syncState
}

func (syncSurface) Destroy(*linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1) {}

func (h syncSurface) point(r *linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1, tl *linuxdrmsyncobj.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) (syncPoint, bool) {
	if h.surf.destroyed {
		r.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoSurface), "surface destroyed")
		return syncPoint{}, false
	}
	t := h.surf.server.timelines[tl.Resource]
	if t == nil {
		return syncPoint{}, false
	}
	return syncPoint{tl: t, point: uint64(hi)<<32 | uint64(lo)}, true
}

func (h syncSurface) SetAcquirePoint(r *linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1, tl *linuxdrmsyncobj.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) {
	if p, ok := h.point(r, tl, hi, lo); ok {
		h.st.acquire = p
	}
}

func (h syncSurface) SetReleasePoint(r *linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1, tl *linuxdrmsyncobj.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) {
	if p, ok := h.point(r, tl, hi, lo); ok {
		h.st.release = p
	}
}

// checkSyncCommit applies the protocol rules to a commit with sync
// points; false means a protocol error was posted.
func (s *surface) checkSyncCommit() bool {
	st := s.sync
	if st == nil {
		return true
	}
	a, r := st.acquire, st.release
	// Points belong only to a commit that attaches a non-null buffer.
	hasBuffer := s.next.attached && s.next.buffer != nil
	res := st.resource
	switch {
	case !a.set() && !r.set():
		if hasBuffer {
			res.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoAcquirePoint), "buffer without acquire point")
			return false
		}
		return true
	case !hasBuffer:
		res.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoBuffer), "points without a buffer")
	case !a.set():
		res.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoAcquirePoint), "no acquire point")
	case !r.set():
		res.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoReleasePoint), "no release point")
	case a.tl == r.tl && a.point >= r.point:
		res.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorConflictingPoints), "release point not after acquire point")
	case s.pendingIsSHM():
		res.PostError(uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorUnsupportedBuffer), "explicit sync on a wl_shm buffer")
	default:
		return true
	}
	return false
}

// pendingIsSHM reports whether the buffer of the next commit is wl_shm.
func (s *surface) pendingIsSHM() bool {
	b := s.next.buffer
	if !s.next.attached {
		b = s.current
	}
	if b == nil {
		return false
	}
	_, dma := s.server.buffers[b.Resource].(*dmabufBuffer)
	return !dma
}

// syncWaiter watches acquire points: each gets an eventfd the kernel
// writes once the point is signalled, and the pacer is woken to apply the
// commits that waited for it.
type acquirePoller interface {
	poll([]unix.PollFd, int) (int, error)
}

type unixAcquirePoller struct{}

func (unixAcquirePoller) poll(fds []unix.PollFd, timeout int) (int, error) {
	return unix.Poll(fds, timeout)
}

type syncWaiter struct {
	poller    acquirePoller
	terminal  bool // polling ended in failure; protected by mu
	mu        sync.Mutex
	ready     map[*syncWait]bool
	wake      func()
	pipeR     *os.File
	pipeW     *os.File
	added     []*syncWait // new waits for run (under mu)
	log       zerowrap.Logger
	pollError sync.Once
}

// syncWait is one acquire point a queued commit waits for.
type syncWait struct {
	efd       int
	done      bool        // the eventfd fired or the wait was cancelled (pacer, under Do)
	failed    bool        // observation failed; done must not authorize publication
	cancelled atomic.Bool // run stops polling it and closes efd
}

func newSyncWaiter(wake func()) (*syncWaiter, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &syncWaiter{poller: unixAcquirePoller{}, ready: map[*syncWait]bool{}, wake: wake, pipeR: r, pipeW: w, log: zerowrap.Default()}, nil
}

// watch registers an eventfd for p; the wait is ready once it fires.
func (sw *syncWaiter) watch(p syncPoint) (*syncWait, error) {
	if sw.stopped() {
		return &syncWait{efd: -1, done: true, failed: true}, unix.EIO
	}
	ready, err := p.tl.dev.signalled(p.tl.handle, p.point)
	if err != nil || ready {
		return &syncWait{efd: -1, done: true, failed: err != nil}, err
	}
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return &syncWait{efd: -1, done: true, failed: true}, err
	}
	if err := p.tl.dev.eventfd(p.tl.handle, p.point, efd); err != nil {
		unix.Close(efd)
		return &syncWait{efd: -1, done: true, failed: true}, err
	}
	w := sw.add(efd)
	if w.failed {
		return w, unix.EIO
	}
	return w, nil
}

// watchImplicit waits for a dma-buf's exclusive fences without blocking dispatch.
func (sw *syncWaiter) watchImplicit(b *ports.DMABuf, waits *[4]*syncWait) error {
	if sw.stopped() {
		return unix.EIO
	}
	var fds [4]unix.PollFd
	for i, p := range b.Planes {
		if i >= len(fds) {
			break
		}
		fds[i] = unix.PollFd{Fd: int32(p.File.Fd()), Events: unix.POLLIN}
	}
	planes := fds[:min(len(b.Planes), len(fds))]
	_, err := unix.Poll(planes, 0)
	for errors.Is(err, unix.EINTR) { // a signal, not a fence error
		_, err = unix.Poll(planes, 0)
	}
	if err != nil {
		return err
	}
	for i, fd := range planes {
		if fd.Revents&unix.POLLIN != 0 {
			continue
		}
		if fd.Revents&(unix.POLLERR|unix.POLLNVAL|unix.POLLHUP) != 0 {
			sw.warnPollError()
			return unix.EIO
		}
		dup, err := unix.FcntlInt(uintptr(fd.Fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			for j, w := range waits {
				sw.cancel(w)
				waits[j] = nil
			}
			return err
		}
		waits[i] = sw.add(dup)
		if waits[i].failed {
			for j, w := range waits {
				if w != nil {
					sw.cancel(w)
					waits[j] = nil
				}
			}
			return unix.EIO
		}
	}
	return nil
}

func (sw *syncWaiter) warnPollError() {
	sw.pollError.Do(func() {
		sw.log.Warn().Str("component", "wayland").Msg("implicit fence poll error")
	})
}

func (sw *syncWaiter) add(fd int) *syncWait {
	w := &syncWait{efd: fd}
	sw.mu.Lock()
	if sw.terminal {
		sw.mu.Unlock()
		unix.Close(fd)
		return &syncWait{efd: -1, done: true, failed: true}
	}
	sw.added = append(sw.added, w)
	sw.mu.Unlock()
	_, _ = sw.pipeW.Write([]byte{0})
	return w
}

// fired reports whether w's point is signalled, and forgets w once it has.
func (sw *syncWaiter) fired(w *syncWait) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if ready, ok := sw.ready[w]; ok {
		delete(sw.ready, w)
		w.done, w.failed = true, !ready
	}
	return w.done
}

// cancel stops watching w (the commit was dropped).
func (sw *syncWaiter) cancel(w *syncWait) {
	if w == nil || w.efd < 0 {
		return
	}
	w.cancelled.Store(true)
	sw.mu.Lock()
	delete(sw.ready, w)
	sw.mu.Unlock()
	w.done = true
	_, _ = sw.pipeW.Write([]byte{0}) // run closes its eventfd
}

func (sw *syncWaiter) stopped() bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.terminal
}

// fail publishes terminal observations before run closes its descriptors.
// add checks the same mutex, so no new wait can miss this failure handoff.
func (sw *syncWaiter) fail(waits []*syncWait) {
	sw.mu.Lock()
	sw.terminal = true
	for _, group := range [][]*syncWait{waits, sw.added} {
		for _, w := range group {
			if !w.cancelled.Load() {
				sw.ready[w] = false
			}
		}
	}
	sw.mu.Unlock()
	sw.wake()
}

// run polls every watched eventfd; it owns and closes them.
func (sw *syncWaiter) run(ctx context.Context) {
	defer sw.pipeR.Close()
	var waits []*syncWait
	defer func() {
		sw.mu.Lock()
		waits = append(waits, sw.added...)
		sw.added = nil
		sw.mu.Unlock()
		for _, w := range waits {
			unix.Close(w.efd)
		}
	}()
	for ctx.Err() == nil {
		live := waits[:0]
		for _, w := range waits {
			if w.cancelled.Load() {
				unix.Close(w.efd)
				continue
			}
			live = append(live, w)
		}
		waits = live
		fds := []unix.PollFd{{Fd: int32(sw.pipeR.Fd()), Events: unix.POLLIN}}
		for _, w := range waits {
			fds = append(fds, unix.PollFd{Fd: int32(w.efd), Events: unix.POLLIN})
		}
		n, err := sw.poller.poll(fds, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			sw.fail(waits)
			return
		}
		if n <= 0 {
			continue
		}
		fired := false
		kept := waits[:0]
		for i, w := range waits {
			if w.cancelled.Load() {
				unix.Close(w.efd)
				continue
			}
			revents := fds[i+1].Revents
			if revents&unix.POLLIN == 0 && revents&(unix.POLLERR|unix.POLLNVAL|unix.POLLHUP) == 0 {
				kept = append(kept, w)
				continue
			}
			if revents&unix.POLLIN == 0 {
				sw.warnPollError()
			}
			unix.Close(w.efd)
			sw.mu.Lock()
			if !w.cancelled.Load() {
				sw.ready[w] = revents&unix.POLLIN != 0
				fired = true
			}
			sw.mu.Unlock()
		}
		waits = kept
		// New waits join the next poll.
		if fds[0].Revents != 0 {
			var b [64]byte
			_, _ = sw.pipeR.Read(b[:])
			sw.mu.Lock()
			waits = append(waits, sw.added...)
			sw.added = nil
			sw.mu.Unlock()
		}
		if fired {
			sw.wake()
		}
	}
}

// close stops the waiter's input side.
func (sw *syncWaiter) close() { sw.pipeW.Close() }

// syncHold is a buffer's explicit-sync state while it is on screen: the
// acquire fence handed to readers and the release point to signal.
type syncHold struct {
	acquire *os.File
	release syncPoint
}

// releaseSync signals the release point once nothing reads the buffer,
// and closes the acquire fence.
func (s *Server) releaseSync(h syncHold) {
	if h.acquire != nil {
		h.acquire.Close()
	}
	if h.release.set() {
		if err := h.release.tl.dev.signal(h.release.tl.handle, h.release.point); err != nil {
			s.log.Warn().Str("component", "wayland").Err(err).Msg("release point")
		}
		h.release.tl.uses--
		s.dropTimeline(h.release.tl)
	}
}

// dropTimeline destroys a timeline the client destroyed once no held
// buffer still needs to signal it.
func (s *Server) dropTimeline(tl *timeline) {
	if !tl.alive && tl.uses <= 0 && tl.handle != 0 {
		_ = tl.dev.destroy(tl.handle)
		tl.handle = 0
	}
}

// takeSyncPoints moves the surface's requested points to the commit and
// starts waiting for the acquire point's fence.
func (s *surface) takeSyncPoints() {
	st := s.sync
	if st == nil || !st.acquire.set() {
		return
	}
	cs := &commitSync{acquire: st.acquire, release: st.release}
	st.acquire, st.release = syncPoint{}, syncPoint{}
	cs.acquire.tl.uses++
	cs.release.tl.uses++
	w, err := s.server.syncWait.watch(cs.acquire)
	if err != nil {
		// Keep points owned by the captured commit for safe discard. An
		// unobservable acquire must never reach native rendering.
		s.server.log.Warn().Str("component", "wayland").Err(err).Msg("acquire point")
	}
	cs.wait = w
	s.next.sync = cs
}

// syncReady reports whether a commit's acquire point has its fence.
func (s *surface) syncReady(cs *commitSync) bool {
	return cs == nil || cs.wait == nil || s.server.syncWait.fired(cs.wait)
}

// dropSync forgets a commit that will never apply. Its release point is
// signalled only once the acquire point has a fence: signalling earlier
// could move a shared timeline past a point the client has not submitted.
func (s *surface) dropSync(cs *commitSync) {
	if cs == nil {
		return
	}
	if cs.fence != nil {
		cs.fence.Close()
		cs.fence = nil
	}
	ready := cs.wait != nil && s.server.syncWait.fired(cs.wait) && !cs.wait.failed
	if cs.wait != nil {
		s.server.syncWait.cancel(cs.wait)
	}
	cs.acquire.tl.uses--
	s.server.dropTimeline(cs.acquire.tl)
	if ready {
		s.server.releaseSync(syncHold{release: cs.release})
		return
	}
	cs.release.tl.uses--
	s.server.dropTimeline(cs.release.tl)
}

// prepareSync exports only after observed readiness, before any current state
// changes. Export failure must not fall back to native implicit synchronization.
func (s *surface) prepareSync(cs *commitSync) bool {
	if cs == nil || cs.fence != nil {
		return true
	}
	f, err := cs.acquire.tl.dev.exportSyncFile(cs.acquire.tl.handle, cs.acquire.point)
	if err != nil || f == nil {
		if f != nil {
			f.Close()
		}
		s.server.log.Warn().Str("component", "wayland").Err(err).Msg("acquire fence export rejected")
		return false
	}
	cs.fence = f
	return true
}

// applySync makes a commit's points the current buffer's: its acquire
// fence goes with the content, its release point waits for release.
func (s *surface) applySync(cs *commitSync) {
	if cs == nil {
		return
	}
	if cs.wait != nil {
		s.server.syncWait.cancel(cs.wait)
	}
	f := cs.fence
	cs.fence = nil
	cs.acquire.tl.uses--
	s.server.dropTimeline(cs.acquire.tl)
	s.hold = syncHold{acquire: f, release: cs.release}
}
