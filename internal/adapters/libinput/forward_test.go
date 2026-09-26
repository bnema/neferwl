package libinput

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"
)

// A slow consumer must not block push, and stale motion must collapse into
// the latest position while keys and buttons keep their order.
func TestForwarderCoalescesMotionWithoutBlocking(t *testing.T) {
	f := newForwarder(zerowrap.Default())
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
	f := newForwarder(zerowrap.Default())
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

// Coalesced motions keep the sum of their relative deltas.
func TestForwarderSumsDeltas(t *testing.T) {
	f := newForwarder(zerowrap.Default())
	f.push(ports.PointerMotion{X: 1, DX: 1, DY: 2, UnaccelDX: 3, UnaccelDY: 4})
	f.push(ports.PointerMotion{X: 2, DX: 10, DY: 20, UnaccelDX: 30, UnaccelDY: 40})
	ev, _ := f.pop()
	if want := (ports.PointerMotion{X: 2, DX: 11, DY: 22, UnaccelDX: 33, UnaccelDY: 44}); ev != want {
		t.Fatalf("got %+v, want %+v", ev, want)
	}
}

// 10 000 motions with core stalled are one event with exact delta sums.
func TestForwarderManyMotionsOneEvent(t *testing.T) {
	f := newForwarder(zerowrap.Default())
	for i := range 10000 {
		f.push(ports.PointerMotion{X: float64(i), DX: 0.5, UnaccelDY: 1})
	}
	if len(f.queue) != 1 {
		t.Fatalf("%d queued", len(f.queue))
	}
	ev, _ := f.pop()
	if m := ev.(ports.PointerMotion); m.X != 9999 || m.DX != 5000 || m.UnaccelDY != 10000 {
		t.Fatalf("%+v", m)
	}
}

// Past forwardWarn queued events the forwarder warns once, and once more
// when the queue drains below it; nothing is dropped.
func TestForwarderWarnsOnceEachWay(t *testing.T) {
	var buf lockedBuffer
	f := newForwarder(zerowrap.Logger{Logger: zerolog.New(&buf)})
	for range forwardWarn + 100 {
		f.push(ports.PointerButton{Button: 272})
	}
	if n := strings.Count(buf.String(), "events queue up"); n != 1 {
		t.Fatalf("%d warnings", n)
	}
	for range 200 {
		f.pop()
	}
	if n := strings.Count(buf.String(), "input queue drained"); n != 1 {
		t.Fatalf("%d drained logs", n)
	}
	if len(f.queue) != forwardWarn-100 {
		t.Fatalf("dropped: %d left", len(f.queue))
	}
}

// lockedBuffer is an io.Writer for a logger.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
