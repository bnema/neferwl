package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestWindowStatsRecording(t *testing.T) {
	var w windowStats
	c := ports.SurfaceContent{Width: 640, Height: 480}
	now := time.Unix(1, 0)
	w.commit(now, c, false, false)
	w.flip(&ports.FlipInfo{Shows: map[ports.WindowID]uint64{1: 1}})
	if w.client.n != 0 || w.shown.n != 0 || w.facts != (ports.WindowStats{}) {
		t.Fatal("unwatched recording")
	}
	w.watched = 1
	for i := range 4 {
		w.commit(now.Add(time.Duration(i)*20*time.Millisecond), c, false, true)
	}
	if w.client.fps() != 50 || w.facts.Width != 640 || w.facts.Height != 480 || w.facts.DMABuf || !w.facts.Tearing {
		t.Fatalf("commit stats: %+v", w.facts)
	}
	for i := range 3 {
		w.flip(&ports.FlipInfo{When: time.Duration(i) * 10 * time.Millisecond, Shows: map[ports.WindowID]uint64{1: uint64(i + 1)}, ZeroCopy: 1, HardwareClock: true})
	}
	w.flip(&ports.FlipInfo{When: time.Second, Shows: map[ports.WindowID]uint64{1: 3}, ScanoutReason: "composition", OverlayReason: "busy"})
	if w.shown.n != 2 || w.shown.fps() != 100 {
		t.Fatalf("shown samples %d FPS %v", w.shown.n, w.shown.fps())
	}
	if w.facts.ScanoutReason != "composition" || w.facts.OverlayReason != "busy" {
		t.Fatal("missing reasons")
	}
	w.flip(&ports.FlipInfo{Shows: map[ports.WindowID]uint64{1: 3}, ZeroCopy: 1, HardwareClock: true})
	if !w.facts.Scanout || !w.facts.VRR {
		t.Fatal("missing scanout/VRR")
	}
}

func TestWatchStatsTimer(t *testing.T) {
	events := make(chan ports.ClientEvent, 16)
	s, dir, timers := idleClockServerWith(t, Channels{Events: events})
	c := protocolClient(t, s, dir)
	mapWindow := surfaceMapper(t, c, events)
	a, _, _ := mapWindow()
	b, _, _ := mapWindow()
	s.display.Do(func() { s.apply(ports.WatchStats{Window: a.ID}) })
	first := nextArmed(t, timers)
	if first.d != ports.StatsInterval {
		t.Fatalf("interval %v", first.d)
	}
	s.display.Do(func() { s.apply(ports.WatchStats{Window: a.ID}) })
	first.fire()
	next := nextArmed(t, timers)
	report := clientEvent[ports.WindowStats](t, events)
	if report.Window != a.ID {
		t.Fatalf("window %d", report.Window)
	}
	s.display.Do(func() {
		timer := s.stats.timer.(*portsmocks.MockTimer)
		timer.ExpectedCalls = nil
		timer.EXPECT().Stop().Return(true).Once()
		s.apply(ports.WatchStats{})
	})
	next.fire()
	s.display.Do(func() {
		if s.stats.watched != 0 || s.stats.timer != nil {
			t.Error("watch not reset")
		}
	})
	select {
	case ev := <-events:
		t.Fatalf("stale event %T", ev)
	default:
	}
	select {
	case <-timers:
		t.Fatal("unexpected timer")
	default:
	}
	s.display.Do(func() { s.watchStats(b.ID) })
	stale := nextArmed(t, timers)
	s.display.Do(func() { s.emit(ports.WindowUnmapped{ID: b.ID}) })
	_ = clientEvent[ports.WindowUnmapped](t, events)
	stale.fire()
	s.display.Do(func() {
		if s.stats.watched != 0 {
			t.Error("unmap did not stop watch")
		}
	})
	select {
	case ev := <-events:
		t.Fatalf("stale unmap event %T", ev)
	default:
	}
}

// TestWatchStatsCommits uses a real client and a generated clock for commit timestamps.
func TestWatchStatsCommits(t *testing.T) {
	events := make(chan ports.ClientEvent, 16)
	s, dir, timers := idleClockServerWith(t, Channels{Events: events})
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	clock := s.clock.(*portsmocks.MockClock)
	s.display.Do(func() { s.watchStats(w.ID) })
	first := nextArmed(t, timers)
	now := time.Unix(1, 0)
	clock.EXPECT().Now().RunAndReturn(func() time.Time { result := now; now = now.Add(20 * time.Millisecond); return result }).Times(3)
	// Reattaching the retained buffer is a fresh commit, unlike damage-only commits.
	var buffer uint32
	s.display.Do(func() { buffer = s.windows[w.ID].xdg.surface.current.ID() })
	for range 3 {
		requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	first.fire()
	_ = nextArmed(t, timers)
	report := clientEvent[ports.WindowStats](t, events)
	if report.ClientFPS != 50 || report.Width != 1 || report.Height != 1 || report.DMABuf {
		t.Fatalf("report %+v", report)
	}
	s.display.Do(func() { s.watchStats(0) })
}

func TestWatchStatsFlips(t *testing.T) {
	events := make(chan ports.ClientEvent, 16)
	s, dir, timers := idleClockServerWith(t, Channels{Events: events})
	c := protocolClient(t, s, dir)
	w, _, _ := surfaceMapper(t, c, events)()
	sink := s.Presentation()
	push := func(seq uint64, when time.Duration) {
		sink.Push(ports.OutputPresented{Output: "HEADLESS-1", Flip: &ports.FlipInfo{When: when, Shows: map[ports.WindowID]uint64{w.ID: seq}, ZeroCopy: w.ID, HardwareClock: true, OverlayReason: "occupied"}})
		s.display.Do(func() {})
	}
	push(1, 0)
	s.display.Do(func() {
		if s.stats.shown.n != 0 {
			t.Error("unwatched sample")
		}
	})
	select {
	case <-timers:
		t.Fatal("unwatched timer")
	default:
	}
	s.display.Do(func() { s.watchStats(w.ID) })
	_ = nextArmed(t, timers)
	push(1, 0)
	push(2, 10*time.Millisecond)
	push(2, 15*time.Millisecond)
	push(3, 20*time.Millisecond)
	s.display.Do(func() {
		report := s.stats.snapshot()
		if s.stats.shown.n != 2 || report.ShownFPS != 100 || !report.Scanout || !report.VRR || report.OverlayReason != "occupied" {
			t.Errorf("flip report %+v", report)
		}
		s.watchStats(0)
	})
}

// TestWatchStatsMappedIdle seeds facts and rejects stale callbacks when switching windows.
func TestWatchStatsMappedIdle(t *testing.T) {
	events := make(chan ports.ClientEvent, 16)
	s, dir, timers := idleClockServerWith(t, Channels{Events: events})
	c := protocolClient(t, s, dir)
	mapWindow := surfaceMapper(t, c, events)
	a, surf, _ := mapWindow()
	b, _, _ := mapWindow()
	s.display.Do(func() { s.watchStats(a.ID) })
	first := nextArmed(t, timers)
	first.fire()
	stale := nextArmed(t, timers)
	report := clientEvent[ports.WindowStats](t, events)
	if report.Window != a.ID || report.Width != 1 || report.Height != 1 || report.ClientFPS != 0 {
		t.Fatalf("idle report %+v", report)
	}
	s.display.Do(func() { s.watchStats(b.ID) })
	current := nextArmed(t, timers)
	stale.fire()
	s.display.Do(func() {})
	select {
	case ev := <-events:
		t.Fatalf("stale event %T", ev)
	default:
	}
	current.fire()
	_ = nextArmed(t, timers)
	report = clientEvent[ports.WindowStats](t, events)
	if report.Window != b.ID {
		t.Fatalf("switched report %+v", report)
	}
	s.display.Do(func() { s.watchStats(0) })
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	_ = clientEvent[ports.WindowUnmapped](t, events)
	s.display.Do(func() { s.watchStats(a.ID); s.watchStats(ports.WindowID(999999)) })
	select {
	case <-timers:
		t.Fatal("unmapped or unknown watch armed a timer")
	default:
	}
}
