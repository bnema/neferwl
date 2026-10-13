package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// paceFixture is a server whose surface waits on an output whose next
// frame is an hour away: a commit queues its callback and nothing fires.
func paceFixture(t *testing.T, timer bool) (*Server, *surface) {
	t.Helper()
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{
		outputs:      []*output{out},
		awaiting:     map[string][]*wayland.Callback{},
		frameDue:     map[string]time.Time{"DP-2": time.Now().Add(time.Hour)},
		fifoSurfaces: map[*surface]struct{}{},
		lastFlip:     map[string]time.Time{},
		flips:        map[string]ports.FlipInfo{},
		reports:      map[string]ports.OutputPresented{},
	}
	if timer {
		fd, err := unix.TimerfdCreate(unix.CLOCK_MONOTONIC, unix.TFD_NONBLOCK|unix.TFD_CLOEXEC)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { unix.Close(fd) })
		s.paceFD, s.paceReady = fd, true
	}
	w := &window{hasLast: true, last: ports.ConfigureWindow{Output: "DP-2", Visible: true}}
	surf := &surface{server: s, xdg: &xdgSurface{window: w}}
	surf.next.callbacks = []*wayland.Callback{new(wayland.Callback)}
	return s, surf
}

// A commit on the display goroutine ticks the pacer inline: it arms the
// pace timer itself and posts no tick, which would only run later on the
// display goroutine it is already on.
func TestCommitTicksPacerInline(t *testing.T) {
	s, surf := paceFixture(t, true)
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

// Without a registered pace timer the wake falls back to a posted tick.
func TestCommitWakesPacerWithoutTimer(t *testing.T) {
	s, surf := paceFixture(t, false)
	surf.queueAndTick()
	if !s.paceSignal.Load() {
		t.Fatal("tick not signalled")
	}
}

// Wakes inside a tick coalesce into one more pass, not one posted tick each.
func TestWakePacerDefersInsideTick(t *testing.T) {
	s, _ := paceFixture(t, true)
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
	s, surf := paceFixture(t, true)
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
	s, surf := paceFixture(t, true)
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
