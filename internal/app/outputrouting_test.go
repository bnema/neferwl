package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// routeInputs are the router's input channels, as wired in production.
type routeInputs struct {
	scenes   chan []ports.Scene
	cursor   chan ports.CursorChange
	captures chan ports.CaptureRequest
}

func newRouteInputs() routeInputs {
	return routeInputs{scenes: make(chan []ports.Scene), cursor: make(chan ports.CursorChange), captures: make(chan ports.CaptureRequest)}
}

func (in routeInputs) routes() outputRoutes {
	return outputRoutes{scenes: in.scenes, cursor: in.cursor, captures: in.captures}
}

// idleRun is an output that runs until stopped.
func idleRun(ctx context.Context, _ outputFeed) error {
	<-ctx.Done()
	return nil
}

func TestOutputRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replies := make(chan ports.CaptureDone, 2)
	in := newRouteInputs()
	set := newOutputSet(ctx, replies, in.routes())
	for _, name := range []string{"A", "B"} {
		set.start(ctx, name, idleRun)
		<-set.outs[name].cursor // initial cursor
	}
	in.scenes <- []ports.Scene{{Output: "A", Seq: 1}, {Output: "B", Seq: 2}}
	in.scenes <- []ports.Scene{{Output: "A", Seq: 3}}
	in.cursor <- ports.CursorChange{} // the router took both scene sets
	if got := <-set.outs["A"].scenes; got.Seq != 3 {
		t.Fatalf("A scene: %+v", got)
	}
	if got := <-set.outs["B"].scenes; got.Seq != 2 {
		t.Fatalf("B scene: %+v", got)
	}
	in.cursor <- ports.CursorChange{Shape: "text"}
	in.cursor <- ports.CursorChange{Shape: "pointer"}
	in.scenes <- nil // the router took both cursors
	for _, name := range []string{"A", "B"} {
		if got := <-set.outs[name].cursor; got.Shape != "pointer" {
			t.Fatalf("%s cursor: %+v", name, got)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	in.captures <- ports.CaptureRequest{ID: 7, Output: "B", Dst: ports.SHMBuffer{File: f}}
	if got := <-set.outs["B"].captures; got.ID != 7 {
		t.Fatalf("B capture: %+v", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a := set.outs["A"]
	a.stop()
	if err := set.finish("A"); err != nil {
		t.Fatal(err)
	}
	in.scenes <- []ports.Scene{{Output: "A", Seq: 4}}
	in.captures <- ports.CaptureRequest{ID: 8, Output: "A"}
	if got := <-replies; got.ID != 8 || got.Err == nil {
		t.Fatalf("removed output capture: %+v", got)
	}
	select {
	case sc := <-a.scenes:
		t.Fatalf("removed output fed a scene: %+v", sc)
	default:
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}

// An output started after a scene turned its name off starts off; one
// turned back on starts on.
func TestOutputStartsOffAfterOffScene(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newRouteInputs()
	set := newOutputSet(ctx, nil, in.routes())
	startOff := make(chan bool, 1)
	run := func(ctx context.Context, feed outputFeed) error {
		startOff <- feed.StartOff
		<-ctx.Done()
		return nil
	}
	in.scenes <- []ports.Scene{{Output: "A", Off: true}, {Output: "B"}}
	for _, tc := range []struct {
		name string
		want bool
	}{{"A", true}, {"B", false}} {
		set.start(ctx, tc.name, run)
		if got := <-startOff; got != tc.want {
			t.Fatalf("%s start off %v, want %v", tc.name, got, tc.want)
		}
	}
	in.scenes <- []ports.Scene{{Output: "A"}}
	set.outs["A"].stop()
	if err := set.finish("A"); err != nil {
		t.Fatal(err)
	}
	set.start(ctx, "A", run)
	if <-startOff {
		t.Fatal("A starts off after a scene turned it on")
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}

// A late unregister of a previous lifetime never removes its successor.
func TestRouterUnregisterKeepsNewerLifetime(t *testing.T) {
	r := newOutputRouter(context.Background(), nil, outputRoutes{})
	newer := &mailbox{instance: 2, scenes: make(chan ports.Scene, 1), cursor: make(chan ports.CursorChange, 1)}
	r.register("A", newer)
	r.unregister("A", 1)
	if r.outs["A"] != newer {
		t.Fatal("stale unregister removed the newer lifetime")
	}
	r.unregister("A", 2)
	if _, ok := r.outs["A"]; ok {
		t.Fatal("unregister kept the lifetime")
	}
}

// Lifecycle work on the backend loop never holds up routing: while a new
// output's content attach blocks (it waits for the display goroutine), the
// running output still gets its scenes and cursor.
func TestBlockedStartDoesNotDelayRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := newRouteInputs()
	set := newOutputSet(ctx, nil, in.routes())
	set.start(ctx, "A", idleRun)
	<-set.outs["A"].cursor
	attacher := newMockcontentAttacher(t)
	set.attach = attacher
	release := make(chan struct{})
	attaching := make(chan struct{})
	ret := portsmocks.NewMockContentReturn(t)
	attacher.EXPECT().AttachOutput("B").RunAndReturn(func(string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
		close(attaching)
		<-release
		return nil, ret
	}).Once()
	attacher.EXPECT().DetachOutput(mock.Anything).Maybe()
	started := make(chan bool)
	go func() { started <- set.start(ctx, "B", idleRun) }()
	<-attaching
	a := set.outs["A"]
	deadline := time.After(2 * time.Second)
	for seq := uint64(1); seq <= 3; seq++ {
		select {
		case in.scenes <- []ports.Scene{{Output: "A", Seq: seq}}:
		case <-deadline:
			t.Fatal("router blocked behind a starting output")
		}
		select {
		case got := <-a.scenes:
			if got.Seq != seq {
				t.Fatalf("A scene %d, want %d", got.Seq, seq)
			}
		case <-deadline:
			t.Fatal("scene not routed while another output starts")
		}
	}
	select {
	case in.cursor <- ports.CursorChange{Shape: "text"}:
	case <-deadline:
		t.Fatal("cursor blocked behind a starting output")
	}
	if got := <-a.cursor; got.Shape != "text" {
		t.Fatalf("A cursor %+v", got)
	}
	close(release)
	if !<-started {
		t.Fatal("B did not start")
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestOutputRoutingAllocations(t *testing.T) {
	r := newOutputRouter(context.Background(), nil, outputRoutes{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMailbox(1, ctx, cancel, make(chan struct{}))
	r.register("A", m)
	<-m.cursor
	scenes := []ports.Scene{{Output: "A"}}
	cursor := ports.CursorChange{Shape: "pointer"}
	capture := ports.CaptureRequest{Output: "A"}
	if got := testing.AllocsPerRun(100, func() {
		r.scenes(scenes)
		r.setCursor(cursor)
		r.routeCapture(capture)
		<-m.captures
	}); got != 0 {
		t.Fatalf("routing: %g allocations per run, want 0", got)
	}
}
