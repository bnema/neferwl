package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
)

// paceFixture is a server whose surface waits on an output whose next
// frame is an hour away: a commit queues its callback and nothing fires.
func paceFixture(timer bool) (*Server, *surface) {
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{
		outputs:      []*output{out},
		awaiting:     map[string][]*wayland.Callback{},
		frameDue:     map[string]time.Time{"DP-2": time.Now().Add(time.Hour)},
		fifoSurfaces: map[*surface]struct{}{},
		lastFlip:     map[string]time.Time{},
		flips:        map[string]ports.FlipInfo{},
		reports:      map[string]ports.OutputPresented{},
		frameReady:   make(chan struct{}, 1),
	}
	if timer {
		s.paceTimer = time.NewTimer(time.Hour)
		s.paceTimer.Stop()
	}
	w := &window{hasLast: true, last: ports.ConfigureWindow{Output: "DP-2", Visible: true}}
	surf := &surface{server: s, xdg: &xdgSurface{window: w}}
	surf.next.callbacks = []*wayland.Callback{new(wayland.Callback)}
	return s, surf
}

// A commit on the display goroutine ticks the pacer inline: it arms the
// pacer's timer itself and does not wake the pacer goroutine, which would
// only call display.Do to get back to the display goroutine.
func TestCommitTicksPacerInline(t *testing.T) {
	s, surf := paceFixture(true)
	surf.queueAndTick()
	if len(s.awaiting["DP-2"]) != 1 {
		t.Fatalf("callback not queued: %+v", s.awaiting)
	}
	if len(s.frameReady) != 0 {
		t.Fatal("commit woke the pacer goroutine")
	}
	if !s.paceTimer.Stop() {
		t.Fatal("inline tick did not arm the pacer timer for the next deadline")
	}
	if s.paceDepth != 0 || s.paceDirty {
		t.Fatalf("pace state leaked: depth %d dirty %v", s.paceDepth, s.paceDirty)
	}
}

// Without a running pacer (no timer) the wake falls back to the channel.
func TestCommitWakesPacerWithoutTimer(t *testing.T) {
	s, surf := paceFixture(false)
	surf.queueAndTick()
	if len(s.frameReady) != 1 {
		t.Fatal("pacer not woken")
	}
}

// Wakes inside a tick coalesce into one more pass, not one channel wake each.
func TestWakePacerDefersInsideTick(t *testing.T) {
	s, _ := paceFixture(true)
	s.paceDepth = 1
	s.wakePacer()
	s.wakePacer()
	if len(s.frameReady) != 0 || !s.paceDirty {
		t.Fatalf("wake inside a tick: queued %d dirty %v", len(s.frameReady), s.paceDirty)
	}
	s.paceDepth = 0
	s.wakePacer()
	if len(s.frameReady) != 1 {
		t.Fatal("wake outside a tick lost")
	}
}

// A commit that waits (a fifo barrier) is timed by the inline tick.
func TestCommitWaitingOnBarrierArmsTimer(t *testing.T) {
	s, surf := paceFixture(true)
	surf.next.callbacks = nil
	surf.barrier, surf.barrierAt = true, time.Now()
	surf.next.wait = true
	surf.queueAndTick()
	if len(surf.queue) != 1 || len(s.frameReady) != 0 {
		t.Fatalf("queued %d, pacer wakes %d", len(surf.queue), len(s.frameReady))
	}
	if !s.paceTimer.Stop() {
		t.Fatal("timer not armed for the barrier deadline")
	}
}
