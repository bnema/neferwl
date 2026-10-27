package routing

import (
	"context"
	"os"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/mock"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// routeInputs are the router's input channels, as wired in production.
type routeInputs struct {
	scenes   chan []domain.Scene
	cursor   chan domain.CursorChange
	captures chan domain.CaptureRequest
}

func newRouteInputs() routeInputs {
	return routeInputs{scenes: make(chan []domain.Scene), cursor: make(chan domain.CursorChange), captures: make(chan domain.CaptureRequest)}
}

func (in routeInputs) routes() Routes {
	return Routes{Scenes: in.scenes, Cursor: in.cursor, Captures: in.captures}
}

// idleRun is an output that runs until stopped.
func idleRun(ctx context.Context, _ Feed) error {
	<-ctx.Done()
	return nil
}

func TestOutputRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replies := make(chan domain.CaptureDone, 2)
	in := newRouteInputs()
	set := NewSet(ctx, replies, in.routes())
	for _, name := range []string{"A", "B"} {
		set.Start(ctx, name, idleRun)
		<-set.Outs[name].Cursor // initial cursor
	}
	in.scenes <- []domain.Scene{{Output: "A", Seq: 1}, {Output: "B", Seq: 2}}
	in.scenes <- []domain.Scene{{Output: "A", Seq: 3}}
	in.cursor <- domain.CursorChange{} // the router took both scene sets
	if got := <-set.Outs["A"].Scenes; got.Seq != 3 {
		t.Fatalf("A scene: %+v", got)
	}
	if got := <-set.Outs["B"].Scenes; got.Seq != 2 {
		t.Fatalf("B scene: %+v", got)
	}
	in.cursor <- domain.CursorChange{Shape: "text"}
	in.cursor <- domain.CursorChange{Shape: "pointer"}
	in.scenes <- nil // the router took both cursors
	for _, name := range []string{"A", "B"} {
		if got := <-set.Outs[name].Cursor; got.Shape != "pointer" {
			t.Fatalf("%s cursor: %+v", name, got)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	in.captures <- domain.CaptureRequest{ID: 7, Output: "B", Dst: domain.SHMBuffer{File: f}}
	if got := <-set.Outs["B"].Captures; got.ID != 7 {
		t.Fatalf("B capture: %+v", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a := set.Outs["A"]
	a.Stop()
	if err := set.Finish("A"); err != nil {
		t.Fatal(err)
	}
	in.scenes <- []domain.Scene{{Output: "A", Seq: 4}}
	in.captures <- domain.CaptureRequest{ID: 8, Output: "A"}
	if got := <-replies; got.ID != 8 || got.Err == nil {
		t.Fatalf("removed output capture: %+v", got)
	}
	select {
	case sc := <-a.Scenes:
		t.Fatalf("removed output fed a scene: %+v", sc)
	default:
	}
	if err := set.Wait(); err != nil {
		t.Fatal(err)
	}
}

// An output started after a scene turned its name off starts off; one
// turned back on starts on.
func TestOutputStartsOffAfterOffScene(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in := newRouteInputs()
		set := NewSet(ctx, nil, in.routes())
		startOff := make(chan bool, 1)
		run := func(ctx context.Context, feed Feed) error {
			startOff <- feed.StartOff
			<-ctx.Done()
			return nil
		}
		in.scenes <- []domain.Scene{{Output: "A", Off: true}, {Output: "B"}}
		for _, tc := range []struct {
			name string
			want bool
		}{{"A", true}, {"B", false}} {
			set.Start(ctx, tc.name, run)
			if got := <-startOff; got != tc.want {
				t.Fatalf("%s start off %v, want %v", tc.name, got, tc.want)
			}
		}
		in.scenes <- []domain.Scene{{Output: "A"}}
		set.Outs["A"].Stop()
		if err := set.Finish("A"); err != nil {
			t.Fatal(err)
		}
		set.Start(ctx, "A", run)
		if <-startOff {
			t.Fatal("A starts off after a scene turned it on")
		}
		if err := set.Wait(); err != nil {
			t.Fatal(err)
		}

	})
}

// A late unregister of a previous lifetime never removes its successor.
func TestRouterUnregisterKeepsNewerLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRouter(context.Background(), nil, Routes{})
		newer := &Mailbox{Instance: 2, Scenes: make(chan domain.Scene, 1), Cursor: make(chan domain.CursorChange, 1)}
		r.Register("A", newer)
		r.Unregister("A", 1)
		if r.outs["A"] != newer {
			t.Fatal("stale unregister removed the newer lifetime")
		}
		r.Unregister("A", 2)
		if _, ok := r.outs["A"]; ok {
			t.Fatal("unregister kept the lifetime")
		}

	})
}

// Lifecycle work on the backend loop never holds up routing: while a new
// output's content attach blocks (it waits for the display goroutine), the
// running output still gets its scenes and cursor.
func TestBlockedStartDoesNotDelayRouting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in := newRouteInputs()
		set := NewSet(ctx, nil, in.routes())
		set.Start(ctx, "A", idleRun)
		<-set.Outs["A"].Cursor
		attacher := newMockcontentAttacher(t)
		set.Attach = attacher
		release := make(chan struct{})
		attaching := make(chan struct{})
		ret := portsmocks.NewMockContentReturn(t)
		attacher.EXPECT().AttachOutput("B").RunAndReturn(func(string) (<-chan *domain.ContentBatch, ports.ContentReturn) {
			close(attaching)
			<-release
			return nil, ret
		}).Once()
		attacher.EXPECT().DetachOutput(mock.Anything).Maybe()
		releaseB := sync.OnceFunc(func() { close(release) })
		wait := sync.OnceValue(set.Wait)
		started := make(chan bool, 1)
		startDone := make(chan struct{})
		go func() {
			defer close(startDone)
			started <- set.Start(ctx, "B", idleRun)
		}()
		// A failed assertion still unblocks and joins B's start.
		t.Cleanup(func() {
			releaseB()
			cancel()
			<-startDone
			_ = wait()
		})
		<-attaching
		a := set.Outs["A"]
		for seq := uint64(1); seq <= 3; seq++ {
			synctest.Wait()
			select {
			case in.scenes <- []domain.Scene{{Output: "A", Seq: seq}}:
			default:
				t.Fatal("router blocked behind a starting output")
			}
			synctest.Wait()
			select {
			case got := <-a.Scenes:
				if got.Seq != seq {
					t.Fatalf("A scene %d, want %d", got.Seq, seq)
				}
			default:
				t.Fatal("scene not routed while another output starts")
			}
		}
		synctest.Wait()
		select {
		case in.cursor <- domain.CursorChange{Shape: "text"}:
		default:
			t.Fatal("cursor blocked behind a starting output")
		}
		if got := <-a.Cursor; got.Shape != "text" {
			t.Fatalf("A cursor %+v", got)
		}
		releaseB()
		if !<-started {
			t.Fatal("B did not start")
		}
		if err := wait(); err != nil {
			t.Fatal(err)
		}

	})
}

func TestOutputRoutingAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRouter(context.Background(), nil, Routes{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := NewMailbox(1, ctx, cancel, make(chan struct{}))
		r.Register("A", m)
		<-m.Cursor
		scenes := []domain.Scene{{Output: "A"}}
		cursor := domain.CursorChange{Shape: "pointer"}
		capture := domain.CaptureRequest{Output: "A"}
		if got := testing.AllocsPerRun(100, func() {
			r.Scenes(scenes)
			r.SetCursor(cursor)
			r.RouteCapture(capture)
			<-m.Captures
		}); got != 0 {
			t.Fatalf("routing: %g allocations per run, want 0", got)
		}

	})
}
