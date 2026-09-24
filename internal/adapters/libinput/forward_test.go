package libinput

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

// A slow consumer must not block push, and stale motion must collapse into
// the latest position while keys and buttons keep their order.
func TestForwarderCoalescesMotionWithoutBlocking(t *testing.T) {
	f := newForwarder()
	input := make(chan ports.InputEvent) // unbuffered: core is busy
	start := time.Now()
	for i := range 1000 {
		f.push(ports.PointerMotion{X: float64(i)})
	}
	f.push(ports.PointerButton{Button: 272, Pressed: true})
	f.push(ports.PointerMotion{X: 2000})
	f.push(ports.PointerMotion{X: 2001})
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("push blocked for %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.run(ctx, input)
	want := []ports.InputEvent{
		ports.PointerMotion{X: 999},
		ports.PointerButton{Button: 272, Pressed: true},
		ports.PointerMotion{X: 2001},
	}
	for i, w := range want {
		select {
		case got := <-input:
			if got != w {
				t.Fatalf("event %d: got %#v, want %#v", i, got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d not delivered", i)
		}
	}
	select {
	case got := <-input:
		t.Fatalf("unexpected extra event %#v", got)
	case <-time.After(20 * time.Millisecond):
	}
	s := f.take()
	if s.Motions != 1002 || s.Coalesced != 1000 || s.Sent != 3 {
		t.Fatalf("stats %+v", s)
	}
	if s := f.take(); s != (forwardStats{}) {
		t.Fatalf("take did not reset: %+v", s)
	}
}

func TestForwarderStopsOnCancel(t *testing.T) {
	f := newForwarder()
	f.push(ports.PointerMotion{X: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx, make(chan ports.InputEvent)); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop while blocked on send")
	}
}
