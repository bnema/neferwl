package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// outputRouter feeds the running outputs their scenes, cursor and capture
// requests. It runs in its own goroutine and never blocks: every mailbox is
// latest-wins (scenes, cursor) or fails fast when full (captures). Output
// lifecycle (scan, modeset, start and stop, security, leases) runs on the
// backend loop, so a hotplug or a configuration on one output never delays
// another output's frames or cursor.
//
// The backend loop registers an output before it runs and unregisters it
// once it stopped, over ops; registration is lossless and ordered, and an
// output's instance keys it so a late unregister never removes its
// successor.
type outputRouter struct {
	outs map[string]*mailbox
	// off is the latest Scene.Off per output name, kept after the output
	// goes: it reconnects off until core's first scene (drm StartOff).
	off      map[string]bool
	cursor   ports.CursorChange
	captured chan<- ports.CaptureDone
	ctx      context.Context

	in   outputRoutes
	ops  chan routerOp
	done chan struct{} // closed when run returns

	log zerowrap.Logger
	// stats counts since the last report; only run's goroutine touches it.
	stats routerStats
	// eventWait is the longest time (ns) the backend loop blocked sending an
	// OutputEvent to core since the last report. The backend loop writes it,
	// so it is atomic.
	eventWait atomic.Int64
}

// routerStatsEvery is the period of the "routing stats" log entry.
const routerStatsEvery = 10 * time.Second

// routerStats tells whether outputs keep up: a scene or cursor replaced
// unread means an output did not consume the previous one before the next.
type routerStats struct {
	scenes, scenesReplaced uint64
	cursor, cursorReplaced uint64
	capturesFull           uint64 // captures failed because the queue was full
}

// outputRoutes are the router's inputs: core's scenes, wayland's cursor and
// capture requests.
type outputRoutes struct {
	scenes   <-chan []ports.Scene
	cursor   <-chan ports.CursorChange
	captures <-chan ports.CaptureRequest
}

// mailbox is what the router feeds one output lifetime.
type mailbox struct {
	instance domain.OutputInstance
	scenes   chan ports.Scene
	cursor   chan ports.CursorChange
	captures chan ports.CaptureRequest
	// ctx ends and done closes with the output; stop cancels it.
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}
}

func newMailbox(instance domain.OutputInstance, ctx context.Context, stop context.CancelFunc, done chan struct{}) *mailbox {
	return &mailbox{instance: instance, scenes: make(chan ports.Scene, 1), cursor: make(chan ports.CursorChange, 1), captures: make(chan ports.CaptureRequest, 16), ctx: ctx, stop: stop, done: done}
}

// routerOp registers (add set) or unregisters an output. reply carries the
// registration's start-off state; it is closed after an unregister.
type routerOp struct {
	name  string
	add   *mailbox
	gone  domain.OutputInstance
	reply chan bool
}

func newOutputRouter(ctx context.Context, captured chan<- ports.CaptureDone, in outputRoutes) *outputRouter {
	return &outputRouter{outs: map[string]*mailbox{}, off: map[string]bool{}, captured: captured, ctx: ctx, in: in, ops: make(chan routerOp), done: make(chan struct{}), log: logging.For(ctx, "router")}
}

// run routes until ctx ends.
func (r *outputRouter) run(ctx context.Context) {
	defer close(r.done)
	report := time.NewTicker(routerStatsEvery)
	defer report.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-report.C:
			r.report()
		case op := <-r.ops:
			r.apply(op)
		case set := <-r.in.scenes:
			r.scenes(set)
		case c := <-r.in.cursor:
			r.setCursor(c)
		case q := <-r.in.captures:
			r.routeCapture(q)
		}
	}
}

// report logs and resets the counters; it stays quiet when nothing happened.
func (r *outputRouter) report() {
	st, wait := r.stats, time.Duration(r.eventWait.Swap(0))
	r.stats = routerStats{}
	if st == (routerStats{}) && wait == 0 {
		return
	}
	r.log.Info().
		Uint64("scenes", st.scenes).
		Uint64("scenes_replaced", st.scenesReplaced).
		Uint64("cursor", st.cursor).
		Uint64("cursor_replaced", st.cursorReplaced).
		Uint64("captures_full", st.capturesFull).
		Float64("max_event_send_ms", float64(wait)/float64(time.Millisecond)).
		Msg("routing stats")
}

// noteEventSend records how long the backend loop waited for core to take an
// event. Backend loop only; lock-free so it never adds to the wait.
func (r *outputRouter) noteEventSend(d time.Duration) {
	n := int64(d)
	for {
		cur := r.eventWait.Load()
		if n <= cur || r.eventWait.CompareAndSwap(cur, n) {
			return
		}
	}
}

func (r *outputRouter) apply(op routerOp) {
	if op.add != nil {
		op.reply <- r.register(op.name, op.add)
		return
	}
	r.unregister(op.name, op.gone)
	close(op.reply)
}

// register feeds m from now on, starting with the current cursor, and
// reports whether the output's latest scene turned it off.
func (r *outputRouter) register(name string, m *mailbox) bool {
	r.outs[name] = m
	m.cursor <- r.cursor
	return r.off[name]
}

// unregister stops feeding the output lifetime instance of name.
func (r *outputRouter) unregister(name string, instance domain.OutputInstance) {
	if m := r.outs[name]; m != nil && m.instance == instance {
		delete(r.outs, name)
	}
}

// setCursor gives every output the latest cursor, replacing an unread one.
func (r *outputRouter) setCursor(c ports.CursorChange) {
	r.cursor = c
	r.stats.cursor++
	for _, m := range r.outs {
		if latest(m.cursor, c) {
			r.stats.cursorReplaced++
		}
	}
}

// scenes routes each scene to its output, replacing an unread one.
func (r *outputRouter) scenes(set []ports.Scene) {
	for _, sc := range set {
		if sc.Off {
			r.off[sc.Output] = true
		} else {
			delete(r.off, sc.Output)
		}
		if m := r.outs[sc.Output]; m != nil {
			r.stats.scenes++
			if latest(m.scenes, sc) {
				r.stats.scenesReplaced++
			}
		}
	}
}

// routeCapture hands the request to its output or fails it at once: a full
// or stopped output never holds up the router.
func (r *outputRouter) routeCapture(req ports.CaptureRequest) {
	m := r.outs[req.Output]
	if m == nil {
		capture.Fail(r.ctx, req, fmt.Errorf("output %q unavailable", req.Output), r.captured)
		return
	}
	select {
	case m.captures <- req:
	case <-m.done:
		m.stop()
		capture.Fail(r.ctx, req, fmt.Errorf("output %q stopped", req.Output), r.captured)
	case <-m.ctx.Done():
		capture.Fail(r.ctx, req, fmt.Errorf("output %q stopped", req.Output), r.captured)
	default:
		r.stats.capturesFull++
		capture.Fail(r.ctx, req, fmt.Errorf("output %q capture queue full", req.Output), r.captured)
	}
}

// add registers m for name and returns its start-off state. Backend loop
// only; false once the router has stopped.
func (r *outputRouter) add(name string, m *mailbox) (off, ok bool) {
	reply := make(chan bool, 1)
	select {
	case r.ops <- routerOp{name: name, add: m, reply: reply}:
		return <-reply, true
	case <-r.done:
		return false, false
	}
}

// remove unregisters the lifetime instance of name and returns once the
// router no longer feeds it, so its queued captures can be failed. Backend
// loop only.
func (r *outputRouter) remove(name string, instance domain.OutputInstance) {
	reply := make(chan bool)
	select {
	case r.ops <- routerOp{name: name, gone: instance, reply: reply}:
		<-reply
	case <-r.done:
	}
}
