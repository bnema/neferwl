package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/neferwl/internal/ports"
)

// statsRegistered returns a router with output A registered and its initial
// cursor drained.
func statsRegistered(ctx context.Context, in outputRoutes) (*outputRouter, *mailbox) {
	r := newOutputRouter(ctx, nil, in)
	mctx, stop := context.WithCancel(ctx)
	m := newMailbox(1, mctx, stop, make(chan struct{}))
	r.register("A", m)
	<-m.cursor
	return r, m
}

// A scene or cursor sent before the output read the previous one counts as
// replaced: the output is behind.
func TestRouterStatsCountReplaced(t *testing.T) {
	r, m := statsRegistered(t.Context(), outputRoutes{})
	r.scenes([]ports.Scene{{Output: "A", Seq: 1}})
	if r.stats.scenes != 1 || r.stats.scenesReplaced != 0 {
		t.Fatalf("one scene: %+v", r.stats)
	}
	r.scenes([]ports.Scene{{Output: "A", Seq: 2}, {Output: "missing"}})
	if r.stats.scenes != 2 || r.stats.scenesReplaced != 1 {
		t.Fatalf("two scenes unread: %+v", r.stats)
	}
	<-m.scenes
	r.scenes([]ports.Scene{{Output: "A", Seq: 3}})
	if r.stats.scenesReplaced != 1 {
		t.Fatalf("a drained mailbox is not behind: %+v", r.stats)
	}

	r.setCursor(ports.CursorChange{Shape: "text"})
	r.setCursor(ports.CursorChange{Shape: "pointer"})
	if r.stats.cursor != 2 || r.stats.cursorReplaced != 1 {
		t.Fatalf("cursor: %+v", r.stats)
	}
	if got := <-m.cursor; got.Shape != "pointer" {
		t.Fatalf("cursor %+v", got)
	}
}

// A full capture queue fails the request fast and counts it.
func TestRouterStatsCapturesFull(t *testing.T) {
	replies := make(chan ports.CaptureDone, 2)
	r, m := statsRegistered(t.Context(), outputRoutes{})
	r.captured = replies
	for id := range uint64(cap(m.captures)) {
		r.routeCapture(ports.CaptureRequest{ID: id, Output: "A"})
	}
	if r.stats.capturesFull != 0 {
		t.Fatalf("queue not full yet: %+v", r.stats)
	}
	r.routeCapture(ports.CaptureRequest{ID: 99, Output: "A"})
	r.routeCapture(ports.CaptureRequest{ID: 100, Output: "missing"})
	if r.stats.capturesFull != 1 {
		t.Fatalf("captures full: %+v", r.stats)
	}
	if got := <-replies; got.ID != 99 || got.Err == nil {
		t.Fatalf("rejected capture: %+v", got)
	}
}

// The backend loop reports its longest wait on core; shorter ones keep it.
func TestRouterNoteEventSendKeepsMax(t *testing.T) {
	r := newOutputRouter(t.Context(), nil, outputRoutes{})
	for _, d := range []time.Duration{3 * time.Millisecond, 12 * time.Millisecond, time.Millisecond} {
		r.noteEventSend(d)
	}
	if got := time.Duration(r.eventWait.Load()); got != 12*time.Millisecond {
		t.Fatalf("max wait %v, want 12ms", got)
	}
}

// Every 10 s the router logs and resets its counters, and stays quiet when
// nothing happened.
func TestRouterLogsStatsEvery10s(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out lockedBuffer
		log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out})
		ctx, cancel := context.WithCancel(zerowrap.WithCtx(context.Background(), log))
		in := newRouteInputs()
		r := newOutputRouter(ctx, nil, in.routes())
		go r.run(ctx)
		m := newMailbox(1, ctx, func() {}, make(chan struct{}))
		if _, ok := r.add("A", m); !ok {
			t.Fatal("router stopped")
		}
		<-m.cursor

		time.Sleep(routerStatsEvery)
		synctest.Wait()
		if got := out.String(); got != "" {
			t.Fatalf("idle router logged: %s", got)
		}

		in.scenes <- []ports.Scene{{Output: "A", Seq: 1}}
		in.scenes <- []ports.Scene{{Output: "A", Seq: 2}}
		in.cursor <- ports.CursorChange{Shape: "text"}
		in.cursor <- ports.CursorChange{Shape: "pointer"}
		in.scenes <- nil // the router took both cursors
		r.noteEventSend(1500 * time.Microsecond)
		time.Sleep(routerStatsEvery)
		synctest.Wait()

		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 1 {
			t.Fatalf("log lines: %q", lines)
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"message": "routing stats", "component": "router", "scenes": 2.0, "scenes_replaced": 1.0, "cursor": 2.0, "cursor_replaced": 1.0, "captures_full": 0.0, "max_event_send_ms": 1.5}
		for k, v := range want {
			if entry[k] != v {
				t.Errorf("%s = %v, want %v (%s)", k, entry[k], v, lines[0])
			}
		}

		time.Sleep(routerStatsEvery)
		synctest.Wait()
		if n := strings.Count(out.String(), "routing stats"); n != 1 {
			t.Fatalf("counters were not reset: %d entries", n)
		}
		cancel()
		<-r.done
	})
}
