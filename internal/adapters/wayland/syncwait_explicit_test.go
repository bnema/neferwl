package wayland

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// explicitWaiter is a waiter whose explicit waits register with a mocked
// display-loop source; the callback and fd are captured.
type explicitWaiter struct {
	sw    *syncWaiter
	src   *mockacquireSource
	rm    *mockacquireRemover
	paced int
	fd    int
	fire  func()
	point syncPoint
}

func newExplicitWaiter(t *testing.T, addErr error) *explicitWaiter {
	t.Helper()
	e := &explicitWaiter{src: newMockacquireSource(t), rm: newMockacquireRemover(t)}
	dev := newMocksyncobjDevice(t)
	dev.EXPECT().signalled(uint32(7), uint64(4)).Return(false, nil).Once()
	if addErr == nil {
		dev.EXPECT().eventfd(uint32(7), uint64(4), mock.Anything).Return(nil).Once()
	}
	e.point = syncPoint{tl: &timeline{dev: dev, handle: 7}, point: 4}
	sw, err := newSyncWaiter(func() { t.Error("explicit wait posted a tick") })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sw.discard(); sw.close() })
	sw.src, sw.pace = e.src, func() { e.paced++ }
	e.sw = sw
	e.src.EXPECT().add(mock.Anything, mock.Anything).RunAndReturn(func(fd int, f func()) (acquireRemover, error) {
		e.fd, e.fire = fd, f
		if addErr != nil {
			return nil, addErr
		}
		return e.rm, nil
	}).Once()
	return e
}

func fdClosed(fd int) bool {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	return err == unix.EBADF
}

// A fired eventfd retains the source and drained fd and wakes the pacer
// inline, never through a posted tick.
func TestExplicitWaitFiresOnDisplayLoop(t *testing.T) {
	e := newExplicitWaiter(t, nil)
	w, err := e.sw.watch(e.point)
	if err != nil || e.sw.fired(w) {
		t.Fatalf("watch: %v fired=%v", err, e.sw.fired(w))
	}
	e.rm.EXPECT().Remove().Once()
	one := [8]byte{1}
	if _, err := unix.Write(e.fd, one[:]); err != nil {
		t.Fatal(err)
	}
	fd := e.fd
	e.fire()
	if !e.sw.fired(w) || w.failed || e.paced != 1 || fdClosed(fd) {
		t.Fatalf("fired=%v failed=%v paced=%d closed=%v", e.sw.fired(w), w.failed, e.paced, fdClosed(fd))
	}
	e.sw.cancel(w) // applySync cancels after the fire: nothing more to release
	e.sw.releasePending()
	if !fdClosed(fd) {
		t.Fatal("pooled fd leaked at shutdown")
	}
}

// Cancelling before the fire removes the source, then closes the fd.
func TestExplicitWaitCancelRemovesThenCloses(t *testing.T) {
	e := newExplicitWaiter(t, nil)
	w, err := e.sw.watch(e.point)
	if err != nil {
		t.Fatal(err)
	}
	e.rm.EXPECT().Remove().RunAndReturn(func() {
		if fdClosed(e.fd) {
			t.Error("fd closed before Remove")
		}
	}).Once()
	e.sw.cancel(w)
	if !e.sw.fired(w) || !fdClosed(e.fd) || e.paced != 0 {
		t.Fatal("cancel left the wait registered")
	}
	e.sw.cancel(w)
}

func TestExplicitWaitAddFailureFails(t *testing.T) {
	e := newExplicitWaiter(t, unix.ENOMEM)
	w, err := e.sw.watch(e.point)
	if err == nil || !e.sw.fired(w) || !w.failed || !fdClosed(e.fd) {
		t.Fatalf("add failure: err=%v", err)
	}
}

// A callback with nothing to read keeps the wait registered.
func TestExplicitWaitSpuriousCallbackKeepsWaiting(t *testing.T) {
	e := newExplicitWaiter(t, nil)
	w, err := e.sw.watch(e.point)
	if err != nil {
		t.Fatal(err)
	}
	e.fire()
	if e.sw.fired(w) || e.paced != 0 || fdClosed(e.fd) {
		t.Fatal("spurious callback ended the wait")
	}
	e.rm.EXPECT().Remove().Once()
	e.sw.cancel(w)
}

// Shutdown releases waits still registered, Remove before Close.
func TestExplicitWaitReleasePending(t *testing.T) {
	e := newExplicitWaiter(t, nil)
	if _, err := e.sw.watch(e.point); err != nil {
		t.Fatal(err)
	}
	e.rm.EXPECT().Remove().Once()
	e.sw.releasePending()
	if !fdClosed(e.fd) {
		t.Fatal("pending wait leaked its eventfd")
	}
}

// End to end: a queued commit stays queued until the callback runs, then
// is applied by that same tick.
func TestSyncobjCommitAppliedByAcquireCallback(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	src, rm := newMockacquireSource(t), newMockacquireRemover(t)
	var fire func()
	src.EXPECT().add(mock.Anything, mock.Anything).RunAndReturn(func(_ int, f func()) (acquireRemover, error) {
		fire = f
		return rm, nil
	}).Once()
	rm.EXPECT().Remove().Maybe()
	h.s.display.Do(func() { h.s.syncWait.src = src })
	a := h.dmabuf()
	h.commit(a, 1, 2)
	if _, ok := h.content(50 * time.Millisecond); ok {
		t.Fatal("content before the acquire point fired")
	}
	h.fire(1) // the kernel signals: efd readable (display loop is mocked, so nothing reads it)
	var applied, posted bool
	h.s.display.Do(func() {
		fire()
		// Same tick: the commit left the queue without posting a pace tick.
		applied = len(h.s.windows[h.win].xdg.surface.queue) == 0
		posted = h.s.paceSignal.Load()
	})
	if !applied || posted {
		t.Fatalf("acquire callback: applied=%v posted tick=%v, want applied inline", applied, posted)
	}
	got, ok := h.content(2 * time.Second)
	if !ok || got.Acquire == nil {
		t.Fatalf("content %+v", got)
	}
}

// The kernel retains the old eventfd object even if its numeric fd is reused.
func TestExplicitWaitLateCancelledWrite(t *testing.T) {
	e := newExplicitWaiter(t, nil)
	w, err := e.sw.watch(e.point)
	if err != nil {
		t.Fatal(err)
	}
	late, err := unix.Dup(e.fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(late) }()
	e.rm.EXPECT().Remove().Once()
	e.sw.cancel(w)
	if !fdClosed(e.fd) {
		t.Fatal("cancelled fd open")
	}
	next, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(next) }()
	one := [8]byte{1}
	if _, err := unix.Write(late, one[:]); err != nil {
		t.Fatal(err)
	}
	e.fire() // even a stale callback must be harmless
	var b [8]byte
	if _, err := unix.Read(next, b[:]); err != unix.EAGAIN {
		t.Fatalf("late notification reached new fd: %v", err)
	}
	if e.paced != 0 {
		t.Fatal("cancelled callback paced")
	}
}

func TestExplicitSyncWatchPool(t *testing.T) {
	e := newExplicitWaiter(t, nil)
	dev := e.point.tl.dev.(*mocksyncobjDevice)
	dev.EXPECT().signalled(uint32(7), uint64(4)).Return(false, nil).Times(99)
	dev.EXPECT().eventfd(uint32(7), uint64(4), mock.Anything).Return(nil).Times(99)
	e.rm.EXPECT().Remove().Once()
	var first *acquireSlot
	for range 100 {
		w, err := e.sw.watch(e.point)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = w.slot
		} else if first != w.slot {
			t.Fatal("slot not reused")
		}
		one := [8]byte{1}
		if _, err := unix.Write(w.efd, one[:]); err != nil {
			t.Fatal(err)
		}
		e.fire()
		e.sw.cancel(w)
	}
	if e.paced != 100 || len(e.sw.slots) != 1 {
		t.Fatal("pool cycle failed")
	}
	e.sw.releasePending()
}

// Subtract the identical generated mock calls: testify bookkeeping is not
// production acquire-path allocation. The sole remaining allocation is w.
func TestExplicitSyncWatchAllocations(t *testing.T) {
	if raceEnabled {
		// AllocsPerRun counts every goroutine, and the race runtime
		// allocates on its own now and then. perf-check runs it without.
		t.Skip("the race detector allocates in the background")
	}
	e := newExplicitWaiter(t, nil)
	dev := e.point.tl.dev.(*mocksyncobjDevice)
	dev.EXPECT().signalled(uint32(7), uint64(4)).Return(false, nil)
	dev.EXPECT().eventfd(uint32(7), uint64(4), mock.Anything).Return(nil)
	e.rm.EXPECT().Remove().Once()
	cycle := func() {
		w, err := e.sw.watch(e.point)
		if err != nil {
			t.Fatal(err)
		}
		one := [8]byte{1}
		if _, err := unix.Write(w.efd, one[:]); err != nil {
			t.Fatal(err)
		}
		e.fire()
	}
	cycle()
	baseline := testing.AllocsPerRun(100, func() {
		explicitWatchMockCalls(dev, e.fd)
	})
	total := testing.AllocsPerRun(100, cycle)
	t.Logf("total=%.0f mock=%.0f acquire=%.0f allocs/cycle", total, baseline, total-baseline)
	if total-baseline > 1 {
		t.Fatalf("acquire allocations %.0f > 1", total-baseline)
	}
	e.sw.releasePending()
}

//go:noinline
func explicitWatchMockCalls(dev syncobjDevice, fd int) {
	_, _ = dev.signalled(7, 4)
	_ = dev.eventfd(7, 4, fd)
}
