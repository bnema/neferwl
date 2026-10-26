package wayland

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// castLayout: two small outputs, so frames are cheap and a flip of the other
// output can be told from a flip of the cast's.
var castLayout = ports.Layout{
	{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 64, Height: 32}, Width: 64, Height: 32, Scale: 1},
	{Info: ports.OutputInfo{Name: "HEADLESS-2", Width: 32, Height: 32}, X: 64, Width: 32, Height: 32, Scale: 1},
}

// castHarness runs a server with the cast channels wired: the commands go in
// through casts, the events and inventories the server publishes are recorded
// by the generated CastSink mock into got and invs, and the capture requests
// and replies are the test's.
type castHarness struct {
	t        *testing.T
	s        *Server
	dir      string
	events   chan ports.ClientEvent
	commands chan ports.ClientCommand
	requests chan ports.CaptureRequest
	replies  chan ports.CaptureDone
	casts    chan ports.CastCommand
	got      chan ports.CastEvent
	invs     chan ports.CastInventory
	changes  chan ports.SecurityState
}

// newCastHarness starts the server; withSink false leaves Options.Casts nil
// (the allocation test measures the server alone).
func newCastHarness(t *testing.T, withSink bool) *castHarness {
	t.Helper()
	h := &castHarness{
		t:        t,
		dir:      t.TempDir(),
		events:   make(chan ports.ClientEvent, 256),
		commands: make(chan ports.ClientCommand, 16),
		requests: make(chan ports.CaptureRequest, maxCaptureInflight),
		replies:  make(chan ports.CaptureDone, maxCaptureInflight+2),
		casts:    make(chan ports.CastCommand, 16),
		got:      make(chan ports.CastEvent, 4096),
		invs:     make(chan ports.CastInventory, 64),
		changes:  make(chan ports.SecurityState, 16),
	}
	opts := Options{RuntimeDir: h.dir, Outputs: castLayout, Security: &sessionsecurity.Gate{}}
	if withSink {
		sink := portsmocks.NewMockCastSink(t)
		sink.EXPECT().Push(mock.Anything).RunAndReturn(func(ev ports.CastEvent) { h.got <- ev }).Maybe()
		sink.EXPECT().Inventory(mock.Anything).RunAndReturn(func(inv ports.CastInventory) { h.invs <- inv }).Maybe()
		opts.Casts = sink
	}
	ch := Channels{
		Events: h.events, Commands: h.commands, Captures: h.requests, Captured: h.replies, CastCommands: h.casts,
		SecurityChanges: h.changes, SecurityEvents: make(chan ports.SecurityBackendEvent, 16),
	}
	s, err := New(opts, ch, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	relayContents(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-waylandDeadline(3 * time.Second):
			t.Fatal("server did not stop")
		}
	})
	return h
}

// sync returns once the display goroutine ran everything posted before.
func (h *castHarness) sync() {
	h.t.Helper()
	ran := make(chan struct{})
	h.s.display.Post(func() { close(ran) })
	select {
	case <-ran:
	case <-waylandDeadline(3 * time.Second):
		h.t.Fatal("display goroutine stalled")
	}
}

// next is the next event pushed to the sink, which must have that kind.
func (h *castHarness) next(kind ports.CastEventKind) ports.CastEvent {
	h.t.Helper()
	select {
	case ev := <-h.got:
		if ev.Kind != kind {
			h.t.Fatalf("event %+v, want kind %d", ev, kind)
		}
		return ev
	case <-waylandDeadline(3 * time.Second):
		h.t.Fatalf("no cast event of kind %d", kind)
		return ports.CastEvent{}
	}
}

//testpolicy:realtime capture replies arrive through a real Wayland display event loop
func (h *castHarness) nextCastFrameDone() ports.CastEvent {
	h.t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-h.got:
			if ev.Kind == ports.CastFrameDone {
				return ev
			}
			if ev.Kind != ports.CastDamage {
				h.t.Fatalf("unexpected event %+v", ev)
			}
		case <-timer.C:
			h.t.Fatal("no frame completion")
			return ports.CastEvent{}
		}
	}
}

// none says that no event is pending once the display goroutine caught up.
func (h *castHarness) none() {
	h.t.Helper()
	h.sync()
	select {
	case ev := <-h.got:
		h.t.Fatalf("unexpected cast event %+v", ev)
	default:
	}
}

func (h *castHarness) request() ports.CaptureRequest {
	h.t.Helper()
	select {
	case r := <-h.requests:
		return r
	case <-waylandDeadline(3 * time.Second):
		h.t.Fatal("no capture request")
		return ports.CaptureRequest{}
	}
}

// noRequest says that no capture was requested.
func (h *castHarness) noRequest(wait time.Duration) {
	h.t.Helper()
	select {
	case r := <-h.requests:
		h.t.Fatalf("unexpected capture request for cast session %d on %s", r.Session, r.Output)
	case <-waylandDeadline(wait):
	}
}

// castClientEvent waits for the next ClientEvent of type T, skipping others.
func castClientEvent[T ports.ClientEvent](h *castHarness) T {
	h.t.Helper()
	for {
		select {
		case ev := <-h.events:
			if v, ok := ev.(T); ok {
				return v
			}
		case <-waylandDeadline(3 * time.Second):
			var zero T
			h.t.Fatalf("no %T", zero)
			return zero
		}
	}
}

// complete answers a capture request the way an output does.
func (h *castHarness) complete(req ports.CaptureRequest, err error) {
	h.replies <- ports.CaptureDone{ID: req.ID, Output: req.Output, Err: err, Time: time.Unix(5, 6)}
}

func (h *castHarness) open(id uint64, output string) (ports.CastEvent, uint64) {
	h.t.Helper()
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: id, Output: output, Cursor: true}
	size := h.next(ports.CastSize)
	return size, castClientEvent[ports.CaptureSessionOpen](h).ID
}

// state sends core's decision and waits until the display goroutine took it.
func (h *castHarness) state(core uint64, st ports.CaptureSessionState) {
	h.t.Helper()
	h.commands <- st
	flushCommands(h.t, h.s, h.commands)
	var seen bool
	h.s.display.Do(func() { seen = h.s.castByCore[core].haveState })
	if !seen {
		h.t.Fatal("state not applied")
	}
}

func castFrame(id uint64, buf int, data []byte, w, hgt int) ports.CastCommand {
	return ports.CastCommand{Kind: ports.CastFrame, ID: id, Buf: buf, Data: data, Stride: w * 4, Width: w, Height: hgt, Format: 1}
}

func (h *castHarness) flip(output string) {
	h.t.Helper()
	h.s.Presentation().Push(ports.OutputPresented{Output: output, Flip: &ports.FlipInfo{When: 3 * time.Second, Seq: 1}})
	h.sync()
}

// An output cast: open registers it with core and reports its size; a frame is
// captured into the lent mapping, indicated once, and reported done.
func TestCastOpenSizeFrameDone(t *testing.T) {
	h := newCastHarness(t, true)
	size, core := h.open(7, "HEADLESS-1")
	if size.ID != 7 || size.Width != 64 || size.Height != 32 {
		t.Fatalf("size %+v", size)
	}
	if core == 0 {
		t.Fatal("cast not registered with core")
	}
	data := make([]byte, 64*32*4)
	h.casts <- castFrame(7, 3, data, 64, 32)
	req := h.request()
	if req.Output != "HEADLESS-1" || req.Session != core || !req.Indicate || !req.Cursor || req.Dst.File != nil ||
		len(req.Mapped) != len(data) || &req.Mapped[0] != &data[0] || req.Stride != 256 || req.Format != 1 || req.Region.Dx() != 64 || req.Region.Dy() != 32 {
		t.Fatalf("request %+v", req)
	}
	taken := castClientEvent[ports.CaptureFrameTaken](h)
	if taken.Session != core || taken.Output != "HEADLESS-1" {
		t.Fatalf("taken %+v", taken)
	}
	h.complete(req, nil)
	done := h.next(ports.CastFrameDone)
	if done.ID != 7 || done.Buf != 3 || done.Err != nil || !done.Time.Equal(time.Unix(5, 6)) {
		t.Fatalf("done %+v", done)
	}
	// Later frames are not indicated again: the cast keeps the indicator on.
	h.casts <- castFrame(7, 4, data, 64, 32)
	h.complete(h.request(), nil)
	if done = h.next(ports.CastFrameDone); done.Buf != 4 || done.Err != nil {
		t.Fatalf("second done %+v", done)
	}
	h.sync()
	for len(h.events) > 0 {
		if ev := <-h.events; ev != nil {
			if _, again := ev.(ports.CaptureFrameTaken); again {
				t.Fatalf("second frame indicated again: %+v", ev)
			}
		}
	}
	// CastClose ends the cast without a reply and tells core.
	h.casts <- ports.CastCommand{Kind: ports.CastClose, ID: 7}
	if c := castClientEvent[ports.CaptureSessionClose](h); c.ID != core {
		t.Fatalf("close %+v", c)
	}
	h.none()
	// A frame for a closed cast gives its buffer back.
	h.casts <- castFrame(7, 5, data, 64, 32)
	if done = h.next(ports.CastFrameDone); done.Buf != 5 || done.Err == nil {
		t.Fatalf("frame of a closed cast: %+v", done)
	}
}

// A target that is missing refuses the cast.
func TestCastOpenRefused(t *testing.T) {
	h := newCastHarness(t, true)
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: 1, Output: "NOPE-1"}
	if ev := h.next(ports.CastStopped); ev.ID != 1 || ev.Err == nil {
		t.Fatalf("stopped %+v", ev)
	}
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: 2, Window: 99}
	if ev := h.next(ports.CastStopped); ev.ID != 2 || ev.Reason != ports.CaptureReasonWindowGone {
		t.Fatalf("stopped %+v", ev)
	}
	for id := uint64(10); id < 10+maxCasts+1; id++ {
		h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: id, Output: "HEADLESS-1"}
	}
	for range maxCasts {
		h.next(ports.CastSize)
	}
	if ev := h.next(ports.CastStopped); ev.ID != 10+maxCasts || !errors.Is(ev.Err, errCastLimit) {
		t.Fatalf("over the limit: %+v", ev)
	}
}

// A window cast follows the window and stops when it closes.
func TestCastWindowCloseStops(t *testing.T) {
	h := newCastHarness(t, true)
	c := protocolClient(t, h.s, h.dir)
	w, _, xdg := surfaceMapper(t, c, h.events)()
	registerProtocol(t, c, xdg)
	top := xdg + 1
	placeWindow(t, h.s, h.commands, w, "HEADLESS-1")
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: 4, Window: w.ID}
	if size := h.next(ports.CastSize); size.ID != 4 || size.Width != 100 || size.Height != 100 {
		t.Fatalf("size %+v", size)
	}
	core := castClientEvent[ports.CaptureSessionOpen](h)
	if core.Window != w.ID {
		t.Fatalf("open %+v", core)
	}
	requestProtocol(t, c, top, xdgshell.ToplevelRequestDestroy)
	roundtrip(t, c)
	ev := h.next(ports.CastStopped)
	if ev.ID != 4 || ev.Reason != ports.CaptureReasonWindowGone {
		t.Fatalf("stopped %+v", ev)
	}
	if cl := castClientEvent[ports.CaptureSessionClose](h); cl.ID != core.ID {
		t.Fatalf("close %+v", cl)
	}
	h.none()
}

// A session lock ends every cast and refuses new ones.
func TestCastSessionLockStops(t *testing.T) {
	h := newCastHarness(t, true)
	h.open(1, "HEADLESS-1")
	data := make([]byte, 64*32*4)
	h.casts <- castFrame(1, 0, data, 64, 32)
	req := h.request()
	c := protocolClient(t, h.s, h.dir)
	if _, err := bindLockManager(t, c).Lock(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, c)
	// The capture still writes into the lent buffer: its reply comes first.
	h.none()
	h.complete(req, nil)
	if done := h.next(ports.CastFrameDone); done.Buf != 0 || !errors.Is(done.Err, errCastStopped) {
		t.Fatalf("done %+v", done)
	}
	if ev := h.next(ports.CastStopped); ev.ID != 1 || !errors.Is(ev.Err, errCastProtected) {
		t.Fatalf("stopped %+v", ev)
	}
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: 2, Output: "HEADLESS-1"}
	if ev := h.next(ports.CastStopped); ev.ID != 2 || !errors.Is(ev.Err, errCastProtected) {
		t.Fatalf("open under lock: %+v", ev)
	}
}

// An unplugged output ends the casts on it, after the capture in flight.
func TestCastOutputUnplugStops(t *testing.T) {
	h := newCastHarness(t, true)
	h.open(1, "HEADLESS-1")
	h.open(2, "HEADLESS-2")
	data := make([]byte, 64*32*4)
	h.casts <- castFrame(1, 9, data, 64, 32)
	req := h.request()
	// Apply on the owner before completing the capture. Posting a barrier
	// does not fence the independent Commands forwarding goroutine.
	if !h.s.display.Do(func() {
		h.s.apply(ports.SetOutputs{Outputs: castLayout[1:], Focused: "HEADLESS-2"})
	}) {
		t.Fatal("display stopped")
	}
	h.none()
	h.complete(req, nil)
	if done := h.next(ports.CastFrameDone); done.ID != 1 || done.Buf != 9 || done.Err == nil {
		t.Fatalf("a frame written for a dead cast must not be queued: %+v", done)
	}
	if ev := h.next(ports.CastStopped); ev.ID != 1 || ev.Reason != ports.CaptureReasonOutputGone {
		t.Fatalf("stopped %+v", ev)
	}
	// The cast on the other output lives.
	h.casts <- castFrame(2, 1, make([]byte, 32*32*4), 32, 32)
	h.complete(h.request(), nil)
	if done := h.next(ports.CastFrameDone); done.ID != 2 || done.Err != nil {
		t.Fatalf("done %+v", done)
	}
}

// A new mode renegotiates (CastSize); a frame of the old size is refused.
func TestCastSizeChange(t *testing.T) {
	h := newCastHarness(t, true)
	h.open(1, "HEADLESS-1")
	bigger := ports.Layout{castLayout[0], castLayout[1]}
	bigger[0].Info.Width, bigger[0].Info.Height, bigger[0].Width, bigger[0].Height = 128, 64, 128, 64
	h.commands <- ports.SetOutputs{Outputs: bigger, Focused: "HEADLESS-1"}
	if size := h.next(ports.CastSize); size.Width != 128 || size.Height != 64 {
		t.Fatalf("size %+v", size)
	}
	h.casts <- castFrame(1, 2, make([]byte, 64*32*4), 64, 32)
	if done := h.next(ports.CastFrameDone); done.Buf != 2 || !errors.Is(done.Err, errCastSize) {
		t.Fatalf("done %+v", done)
	}
	h.noRequest(20 * time.Millisecond)
	h.casts <- castFrame(1, 2, make([]byte, 128*64*4), 128, 64)
	h.complete(h.request(), nil)
	if done := h.next(ports.CastFrameDone); done.Err != nil {
		t.Fatalf("done %+v", done)
	}
}

// A transient failure is retried on the cadence of an ext frame; past the
// deadline the frame fails with the error.
func TestCastTransientRetry(t *testing.T) {
	h := newCastHarness(t, true)
	h.open(1, "HEADLESS-1")
	data := make([]byte, 64*32*4)
	h.casts <- castFrame(1, 0, data, 64, 32)
	first := h.request()
	h.complete(first, ports.ErrCaptureBusy)
	start := time.Now()
	retried := h.request()
	if retried.ID == first.ID || time.Since(start) < captureRetryEvery/2 {
		t.Fatalf("retry %+v after %v", retried, time.Since(start))
	}
	h.complete(retried, nil)
	if done := h.next(ports.CastFrameDone); done.Err != nil {
		t.Fatalf("done %+v", done)
	}

	// Never ready: retried until the deadline, then reported.
	h.casts <- castFrame(1, 1, data, 64, 32)
	attempts := 0
	for {
		select {
		case req := <-h.requests:
			attempts++
			h.complete(req, ports.ErrCaptureBusy)
			continue
		case ev := <-h.got:
			if ev.Kind != ports.CastFrameDone || ev.Buf != 1 || !errors.Is(ev.Err, ports.ErrCaptureTransient) {
				t.Fatalf("event %+v", ev)
			}
		case <-waylandDeadline(5 * time.Second):
			t.Fatal("frame neither retried nor failed")
		}
		break
	}
	if attempts < 10 {
		t.Fatalf("%d attempts in %v", attempts, captureRetryFor)
	}
}

// Until core says the cast is active the frame waits, then goes out.
func TestCastWaitsForActiveState(t *testing.T) {
	h := newCastHarness(t, true)
	_, core := h.open(1, "HEADLESS-1")
	h.state(core, ports.CaptureSessionState{ID: core, Output: "HEADLESS-1"})
	h.casts <- castFrame(1, 0, make([]byte, 64*32*4), 64, 32)
	h.noRequest(60 * time.Millisecond)
	h.commands <- ports.CaptureSessionState{ID: core, Output: "HEADLESS-1", Active: true}
	h.complete(h.request(), nil)
	if done := h.next(ports.CastFrameDone); done.Err != nil {
		t.Fatalf("done %+v", done)
	}
	// A terminal decision of core ends the cast without telling core.
	h.commands <- ports.CaptureSessionState{ID: core, Reason: ports.CaptureReasonOutputGone}
	if ev := h.next(ports.CastStopped); ev.Reason != ports.CaptureReasonOutputGone {
		t.Fatalf("stopped %+v", ev)
	}
	h.sync()
	for len(h.events) > 0 {
		if _, closed := (<-h.events).(ports.CaptureSessionClose); closed {
			t.Fatal("core told of an end it decided")
		}
	}
}

// A flip of the cast's output damages an output cast, and only that flip.
func TestCastFlipDamagesOutputCast(t *testing.T) {
	h := newCastHarness(t, true)
	h.open(1, "HEADLESS-1")
	h.open(2, "HEADLESS-2")
	h.flip("HEADLESS-1")
	ev := h.next(ports.CastDamage)
	if ev.ID != 1 || !ev.Time.Equal(time.Unix(3, 0)) {
		t.Fatalf("damage %+v", ev)
	}
	h.none()
	h.flip("HEADLESS-2")
	if ev = h.next(ports.CastDamage); ev.ID != 2 {
		t.Fatalf("damage %+v", ev)
	}
	// Every flip damages an output cast, committed or not.
	h.flip("HEADLESS-1")
	h.flip("HEADLESS-1")
	h.next(ports.CastDamage)
	h.next(ports.CastDamage)
	h.none()
}

// A window cast is damaged by a flip of its output only after the window
// committed since the last damage.
func TestCastFlipDamagesWindowCastAfterCommit(t *testing.T) {
	h := newCastHarness(t, true)
	c := protocolClient(t, h.s, h.dir)
	w, _, _ := surfaceMapper(t, c, h.events)()
	placeWindow(t, h.s, h.commands, w, "HEADLESS-1")
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: 1, Window: w.ID}
	h.next(ports.CastSize)

	h.flip("HEADLESS-1")
	h.none() // nothing committed since the cast began
	commit := func() {
		if !h.s.display.Do(func() { h.s.emitContent(ports.SurfaceContent{ID: w.ID}, damage{full: true}) }) {
			t.Fatal("display stopped")
		}
	}
	commit()
	h.flip("HEADLESS-2")
	h.none() // not the window's output
	h.flip("HEADLESS-1")
	if ev := h.next(ports.CastDamage); ev.ID != 1 {
		t.Fatalf("damage %+v", ev)
	}
	h.flip("HEADLESS-1")
	h.none() // the damage consumed the commit
	commit()
	commit()
	h.flip("HEADLESS-1")
	h.next(ports.CastDamage)
	h.none() // two commits, one damage
}

// A popup is drawn with its toplevel: a static toplevel whose popup (or a
// popup of that popup) keeps committing, mapping, unmapping or moving still
// damages the window cast. The popups are windows of the server, as an
// xdg_popup makes them.
func TestCastWindowDamagedByPopupChanges(t *testing.T) {
	h := newCastHarness(t, true)
	c := protocolClient(t, h.s, h.dir)
	w, _, _ := surfaceMapper(t, c, h.events)()
	placeWindow(t, h.s, h.commands, w, "HEADLESS-1")
	h.casts <- ports.CastCommand{Kind: ports.CastOpen, ID: 1, Window: w.ID}
	h.next(ports.CastSize)

	var menu, sub ports.WindowID
	if !h.s.display.Do(func() {
		top := h.s.windows[w.ID]
		m := &window{id: h.s.nextWindow, xdg: top.xdg, mapped: true}
		m.popup = &popup{w: m, parent: top}
		h.s.windows[m.id] = m
		s2 := &window{id: m.id + 1, xdg: top.xdg, mapped: true}
		s2.popup = &popup{w: s2, parent: m}
		h.s.windows[s2.id] = s2
		h.s.nextWindow += 2
		menu, sub = m.id, s2.id
	}) {
		t.Fatal("display stopped")
	}
	t.Cleanup(func() {
		h.s.display.Do(func() { delete(h.s.windows, menu); delete(h.s.windows, sub) })
	})
	h.flip("HEADLESS-1")
	h.none() // nothing changed yet
	change := func(name string, f func()) {
		t.Helper()
		if !h.s.display.Do(f) {
			t.Fatal("display stopped")
		}
		h.flip("HEADLESS-1")
		if ev := h.next(ports.CastDamage); ev.ID != 1 {
			t.Fatalf("%s: damage %+v", name, ev)
		}
		h.flip("HEADLESS-1")
		h.none() // the damage consumed the change
	}
	content := func(id ports.WindowID) func() {
		return func() { h.s.emitContent(ports.SurfaceContent{ID: id}, damage{full: true}) }
	}
	change("popup commit", content(menu))
	change("popup commit again", content(menu))
	change("nested popup commit", content(sub))
	change("popup unmap", func() { h.s.windows[menu].mapped = false; content(menu)() })
	change("popup map", func() { h.s.windows[menu].mapped = true; h.s.castCommitted(menu) })
	change("popup reposition", func() { h.s.castCommitted(menu) })
	// A window nobody casts leaves the cast alone.
	if !h.s.display.Do(func() { h.s.emitContent(ports.SurfaceContent{ID: w.ID + 1000}, damage{full: true}) }) {
		t.Fatal("display stopped")
	}
	h.flip("HEADLESS-1")
	h.none()
}

// The inventory is published once per change, not once per frame or flip, and
// changes of one display loop turn share one publication.
func TestCastInventoryPublishedOncePerChange(t *testing.T) {
	h := newCastHarness(t, true)
	inv := func() ports.CastInventory {
		t.Helper()
		select {
		case v := <-h.invs:
			return v
		case <-waylandDeadline(3 * time.Second):
			t.Fatal("no inventory")
			return ports.CastInventory{}
		}
	}
	noInv := func() {
		t.Helper()
		h.sync()
		h.sync()
		select {
		case v := <-h.invs:
			t.Fatalf("unexpected inventory %+v", v)
		default:
		}
	}
	first := inv()
	if len(first.Outputs) != 2 || len(first.Windows) != 0 || first.Outputs[0].Info.Name != "HEADLESS-1" || first.Outputs[0].Width != 64 {
		t.Fatalf("first inventory %+v", first)
	}
	for _, o := range first.Outputs {
		if o.Current < 0 || o.Current >= len(o.Modes) || o.Modes[o.Current].Width != o.Info.Width {
			t.Fatalf("current mode of %s: %+v", o.Info.Name, o)
		}
	}
	noInv()

	c := protocolClient(t, h.s, h.dir)
	w, _, xdg := surfaceMapper(t, c, h.events)()
	registerProtocol(t, c, xdg)
	top := xdg + 1
	if got := inv(); len(got.Windows) != 1 || got.Windows[0].ID != w.ID {
		t.Fatalf("mapped inventory %+v", got)
	}
	noInv()
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetTitle, "editor")
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetAppId, "app.editor")
	roundtrip(t, c)
	var got ports.CastInventory
	for got = inv(); got.Windows[0].Title != "editor" || got.Windows[0].AppID != "app.editor"; got = inv() {
	}
	noInv()
	// The same title again changes nothing.
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetTitle, "editor")
	roundtrip(t, c)
	noInv()

	// Frames and flips do not publish.
	h.open(1, "HEADLESS-1")
	data := make([]byte, 64*32*4)
	for i := range 20 {
		h.flip("HEADLESS-1")
		h.casts <- castFrame(1, i%3, data, 64, 32)
		h.complete(h.request(), nil)
		// Flips may also publish damage; settle this buffer before lending
		// the next one, independently of the reply-forwarding goroutine.
		if ev := h.nextCastFrameDone(); ev.Err != nil {
			t.Fatalf("frame %d: %+v", i, ev)
		}
	}
	noInv()

	// Several changes in one turn: one publication, of the last state. An
	// unchanged inventory is not published at all.
	h.s.display.Do(func() { h.s.castInventoryChanged(); h.s.castInventoryChanged() })
	noInv()
	h.s.display.Do(func() {
		for _, title := range []string{"a", "b", "c"} {
			h.s.windows[w.ID].title = title
			h.s.castInventoryChanged()
		}
	})
	if got = inv(); len(got.Windows) != 1 || got.Windows[0].Title != "c" {
		t.Fatalf("inventory %+v", got)
	}
	noInv()

	// An output moving is a change; the focused output too.
	moved := slices.Clone(castLayout)
	moved[1].X = 200
	h.commands <- ports.SetOutputs{Outputs: moved, Focused: "HEADLESS-2"}
	if got = inv(); got.Outputs[1].X != 200 || got.Focused != "HEADLESS-2" {
		t.Fatalf("inventory %+v", got)
	}
	noInv()

	// Under a session lock the windows are not listed.
	lc := protocolClient(t, h.s, h.dir)
	if _, err := bindLockManager(t, lc).Lock(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, lc)
	for got = inv(); len(got.Windows) != 0; got = inv() {
	}
}

// Pushing a cast frame, capturing it and answering the flip allocates nothing
// in steady state: the cast command rides the post ring, the request and the
// reply are plain values, the reply function is bound once. The count
// includes the display goroutine (AllocsPerRun reads the global malloc
// counter). Nothing in the closure is a testify mock; the sink is left out so
// the measured code is the server's.
func TestCastFrameAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes the purego wake callback allocate")
	}
	h := newCastHarness(t, false)
	s := h.s
	s.castRing.Push(ports.CastCommand{Kind: ports.CastOpen, ID: 1, Output: "HEADLESS-1"})
	core := castClientEvent[ports.CaptureSessionOpen](h).ID
	data := make([]byte, 64*32*4)
	frame := castFrame(1, 0, data, 64, 32)
	flip := ports.OutputPresented{Output: "HEADLESS-1", Flip: &ports.FlipInfo{When: time.Second, Seq: 1}}
	ran := make(chan struct{}, 1)
	marker := func() { ran <- struct{}{} }
	at := time.Unix(1, 2)
	cycle := func() {
		s.castRing.Push(frame)
		req := <-h.requests
		s.capturedRing.Push(ports.CaptureDone{ID: req.ID, Output: req.Output, Time: at})
		s.Presentation().Push(flip)
		s.display.Post(marker)
		<-ran
	}
	for range 4 {
		cycle()
	}
	if taken := castClientEvent[ports.CaptureFrameTaken](h); taken.Session != core {
		t.Fatalf("taken %+v", taken)
	}
	var live int
	s.display.Do(func() { live = len(s.castSessions) + len(s.captureInflight) + len(s.captureReplies) })
	if live != 1 {
		t.Fatalf("cast %d, inflight and replies must be empty after a cycle", live)
	}
	if allocs := testing.AllocsPerRun(200, cycle); allocs != 0 {
		t.Errorf("cast frame cycle: %.1f allocs, want 0", allocs)
	}
}
