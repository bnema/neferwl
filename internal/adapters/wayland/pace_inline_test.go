package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// paceFixture is a server whose surface waits on an output whose next
// frame is an hour away: a commit queues its callback and nothing fires. It
// has what New gives a server for pacing: a display (never run, so posted
// ticks stay queued) and the registered pace timer.
func paceFixture(t *testing.T) (*Server, *surface) {
	t.Helper()
	display, err := server.NewDisplay()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(display.Close)
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{
		display:      display,
		outputs:      []*output{out},
		awaiting:     map[string][]*wayland.Callback{},
		frameDue:     map[string]time.Time{"DP-2": time.Now().Add(time.Hour)},
		fifoSurfaces: map[*surface]struct{}{},
		lastFlip:     map[string]time.Time{},
		flips:        map[string]ports.FlipInfo{},
		reports:      map[string]ports.OutputPresented{},
	}
	s.paceSignalFn = s.paceSignalled
	if err := s.openPaceTimer(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(s.paceFD) })
	w := &window{hasLast: true, last: ports.ConfigureWindow{Output: "DP-2", Visible: true}}
	surf := &surface{server: s, xdg: &xdgSurface{window: w}}
	surf.next.callbacks = []*wayland.Callback{new(wayland.Callback)}
	return s, surf
}

// A commit on the display goroutine ticks the pacer inline: it arms the
// pace timer itself and posts no tick, which would only run later on the
// display goroutine it is already on.
func TestCommitTicksPacerInline(t *testing.T) {
	s, surf := paceFixture(t)
	surf.queueAndTick()
	if len(s.awaiting["DP-2"]) != 1 {
		t.Fatalf("callback not queued: %+v", s.awaiting)
	}
	if s.paceSignal.Load() {
		t.Fatal("commit posted a tick")
	}
	waitArmed(t, s, "inline tick did not arm the pacer timer for the next deadline")
	if s.paceDepth != 0 || s.paceDirty {
		t.Fatalf("pace state leaked: depth %d dirty %v", s.paceDepth, s.paceDirty)
	}
}

// Wakes inside a tick coalesce into one more pass, not one posted tick each.
func TestWakePacerDefersInsideTick(t *testing.T) {
	s, _ := paceFixture(t)
	s.paceDepth = 1
	s.wakePacer()
	s.wakePacer()
	if s.paceSignal.Load() || !s.paceDirty {
		t.Fatalf("wake inside a tick: signalled %v dirty %v", s.paceSignal.Load(), s.paceDirty)
	}
	s.paceDepth = 0
	s.wakePacer()
	if !s.paceSignal.Load() {
		t.Fatal("wake outside a tick lost")
	}
}

// A commit that waits (a fifo barrier) is timed by the inline tick.
func TestCommitWaitingOnBarrierArmsTimer(t *testing.T) {
	s, surf := paceFixture(t)
	surf.next.callbacks = nil
	surf.barrier, surf.barrierAt = true, time.Now()
	surf.next.wait = true
	surf.queueAndTick()
	if len(surf.queue) != 1 || s.paceSignal.Load() {
		t.Fatalf("queued %d, tick signalled %v", len(surf.queue), s.paceSignal.Load())
	}
	waitArmed(t, s, "timer not armed for the barrier deadline")
}

// waitArmed asserts the timerfd was armed: it is pending, or it already
// fired and holds its expiry.
func waitArmed(t *testing.T, s *Server, msg string) {
	t.Helper()
	var cur unix.ItimerSpec
	if err := unix.TimerfdGettime(s.paceFD, &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Value != (unix.Timespec{}) {
		return
	}
	var buf [8]byte
	if n, _ := unix.Read(s.paceFD, buf[:]); n != 8 {
		t.Fatal(msg)
	}
}

// A synchronized subsurface commit waits for its parent's commit: it does
// not tick the pacer, so a burst of child commits costs no ticks.
func TestSyncedCommitDoesNotTick(t *testing.T) {
	s, surf := paceFixture(t)
	surf.sub.parent = &surface{server: s}
	surf.sub.synced = true
	surf.queueAndTick()
	if len(surf.queue) != 1 || !surf.queue[0].synced {
		t.Fatalf("synced commit not queued: %d", len(surf.queue))
	}
	var cur unix.ItimerSpec
	if err := unix.TimerfdGettime(s.paceFD, &cur); err != nil {
		t.Fatal(err)
	}
	if s.paceSignal.Load() || cur.Value != (unix.Timespec{}) {
		t.Fatal("synced commit ticked the pacer")
	}
}

// An arm for the deadline already armed (within paceTolerance) keeps the
// timer; another deadline re-arms it; an expiry consumed by onPaceTimer, or a
// stop, leaves it disarmed, and a stop of a disarmed timer does nothing.
func TestArmPaceSkipsUnchangedDeadline(t *testing.T) {
	s, _ := paceFixture(t)
	remaining := func() time.Duration {
		var cur unix.ItimerSpec
		if err := unix.TimerfdGettime(s.paceFD, &cur); err != nil {
			t.Fatal(err)
		}
		return time.Duration(cur.Value.Nano())
	}
	s.stopPace()
	if s.paceArmed {
		t.Fatal("stop of a never armed timer armed it")
	}
	now := time.Now()
	s.armPaceFrom(now, time.Hour)
	first := s.paceDeadline
	if !s.paceArmed || remaining() <= 59*time.Minute {
		t.Fatalf("not armed: %v", remaining())
	}
	// A deadline a little before the armed one keeps the timer.
	s.armPaceFrom(now, time.Hour-paceTolerance/2)
	if !s.paceDeadline.Equal(first) {
		t.Fatalf("an earlier deadline within tolerance re-armed the timer: %v -> %v", first, s.paceDeadline)
	}
	// A later deadline, however close, re-arms: the timer must never fire
	// before the requested deadline.
	s.armPaceFrom(now, time.Hour+time.Nanosecond)
	if !s.paceDeadline.After(first) {
		t.Fatalf("a later deadline kept the earlier timer: %v -> %v", first, s.paceDeadline)
	}
	first = s.paceDeadline
	s.armPace(time.Hour - time.Minute)
	if s.paceDeadline.Equal(first) || remaining() > 59*time.Minute {
		t.Fatalf("a new deadline kept the old timer: remaining %v", remaining())
	}
	s.stopPace()
	if s.paceArmed || remaining() != 0 {
		t.Fatalf("stop left the timer armed: %v", remaining())
	}
	// One-shot expiry: the fired timer is disarmed, so a stop is a no-op and
	// the next arm sets the timer again even for the same deadline.
	s.armPace(time.Nanosecond)
	fds := []unix.PollFd{{Fd: int32(s.paceFD), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 5000); n != 1 {
		t.Fatalf("the timer did not fire: %d, %v", n, err)
	}
	s.onPaceTimer()
	if s.paceArmed {
		t.Fatal("expiry did not disarm")
	}
	s.stopPace() // no syscall to observe: it must not re-arm anything
	if remaining() != 0 {
		t.Fatalf("stop after the expiry: %v", remaining())
	}
	s.armPace(time.Hour)
	if !s.paceArmed || remaining() <= 59*time.Minute {
		t.Fatalf("not re-armed after the expiry: %v", remaining())
	}
}
