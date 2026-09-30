package wayland

import (
	"context"
	"image"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/wayland/imagecapture"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	source "github.com/bnema/purego-libwayland/protocol/extimagecapturesource"
	ext "github.com/bnema/purego-libwayland/protocol/extimagecopycapture"
	extws "github.com/bnema/purego-libwayland/protocol/extworkspace"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrscreencopy"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// sessionMsg is one decoded event of the neferwl extension.
type sessionMsg struct {
	op      uint16
	token   string
	numbers []int64
}

// attachEvents decodes the events of a neferwl_capture_layer_v1.
type attachEvents struct {
	wlturbo.BaseProxy
	msgs chan sessionMsg
}

func (p *attachEvents) Dispatch(e *wlturbo.Event) {
	m := sessionMsg{op: e.Opcode}
	if uint32(e.Opcode) != imagecapture.NeferwlCaptureLayerV1EventAttached {
		m.numbers = append(m.numbers, int64(e.Uint32()))
	}
	p.msgs <- m
}

// exclusionEvents decodes the events of a neferwl_capture_exclusion_v1.
type exclusionEvents struct {
	wlturbo.BaseProxy
	msgs chan sessionMsg
}

func (p *exclusionEvents) Dispatch(e *wlturbo.Event) {
	m := sessionMsg{op: e.Opcode}
	switch uint32(e.Opcode) {
	case imagecapture.NeferwlCaptureExclusionV1EventToken:
		m.token = e.String()
	case imagecapture.NeferwlCaptureExclusionV1EventFailed:
		m.numbers = append(m.numbers, int64(e.Uint32()))
	}
	p.msgs <- m
}

// captureHarness is a server with one 4x4 output and every capture channel.
type captureHarness struct {
	s          *Server
	dir        string
	events     chan ports.ClientEvent
	commands   chan ports.ClientCommand
	workspaces chan ports.Workspaces
	captures   chan ports.CaptureRequest
	captured   chan ports.CaptureDone
}

var captureOutput = ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 4, Height: 4}, Width: 4, Height: 4, Scale: 1}

func newCaptureHarness(t *testing.T) *captureHarness {
	t.Helper()
	return newCaptureHarnessWith(t, nil)
}

// newCaptureHarnessWith lets a test adjust the options and channels (for
// instance to wire a session-lock gate) before the server starts.
func newCaptureHarnessWith(t *testing.T, tune func(*Options, *Channels)) *captureHarness {
	t.Helper()
	h := &captureHarness{
		dir:        t.TempDir(),
		events:     make(chan ports.ClientEvent, 64),
		commands:   make(chan ports.ClientCommand, 16),
		workspaces: make(chan ports.Workspaces, 4),
		captures:   make(chan ports.CaptureRequest, 8),
		captured:   make(chan ports.CaptureDone, 8),
	}
	opts := Options{RuntimeDir: h.dir, Outputs: ports.Layout{captureOutput}, CaptureAllow: allowStore(t, "*\n")}
	ch := Channels{Events: h.events, Commands: h.commands, Workspaces: h.workspaces, Captures: h.captures, Captured: h.captured}
	if tune != nil {
		tune(&opts, &ch)
	}
	s, err := New(opts, ch, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		for len(h.captures) > 0 {
			_ = (<-h.captures).Dst.File.Close()
		}
	})
	return h
}

// cclient is a client with every capture global bound.
type cclient struct {
	c       *wlturbo.Display
	output  uint32
	outSrc  uint32 // ext_output_image_capture_source_manager_v1
	extra   uint32 // neferwl_image_capture_source_manager_v1
	copyMgr uint32 // ext_image_copy_capture_manager_v1
	excl    uint32 // neferwl_capture_exclusion_manager_v1
}

func (h *captureHarness) client(t *testing.T) *cclient {
	t.Helper()
	c := protocolClient(t, h.s, h.dir)
	out := bindVersion(t, c, "wl_output", 4)
	registerProtocol(t, c, out)
	return &cclient{c: c, output: out,
		outSrc:  bindProtocol(t, c, "ext_output_image_capture_source_manager_v1"),
		extra:   bindProtocol(t, c, "neferwl_image_capture_source_manager_v1"),
		copyMgr: bindProtocol(t, c, "ext_image_copy_capture_manager_v1"),
		excl:    bindProtocol(t, c, "neferwl_capture_exclusion_manager_v1"),
	}
}

func (cc *cclient) roundtrip(t *testing.T) {
	t.Helper()
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// outputSource, regionSource and workspaceSource make a capture source.
func (cc *cclient) outputSource(t *testing.T) uint32 {
	t.Helper()
	src := cc.c.AllocateID()
	registerProtocol(t, cc.c, src)
	requestProtocol(t, cc.c, cc.outSrc, source.ExtOutputImageCaptureSourceManagerV1RequestCreateSource, src, cc.output)
	return src
}

func (cc *cclient) regionSource(t *testing.T, x, y, w, h int32) uint32 {
	t.Helper()
	src := cc.c.AllocateID()
	registerProtocol(t, cc.c, src)
	requestProtocol(t, cc.c, cc.extra, imagecapture.NeferwlImageCaptureSourceManagerV1RequestCreateOutputRegionSource, src, cc.output, x, y, w, h)
	return src
}

func (cc *cclient) workspaceSource(t *testing.T, handle uint32) uint32 {
	t.Helper()
	src := cc.c.AllocateID()
	registerProtocol(t, cc.c, src)
	requestProtocol(t, cc.c, cc.extra, imagecapture.NeferwlImageCaptureSourceManagerV1RequestCreateWorkspaceSource, src, handle)
	return src
}

// session opens an ext-image-copy-capture session on a source and returns its
// object and the decoded events (see captureDetails).
func (cc *cclient) session(t *testing.T, src uint32) (uint32, chan []uint32) {
	t.Helper()
	id := cc.c.AllocateID()
	p := &captureDetails{events: make(chan []uint32, 32)}
	p.SetID(id)
	registerWireProxy(cc.c, p)
	requestProtocol(t, cc.c, cc.copyMgr, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, id, src, uint32(0))
	cc.roundtrip(t)
	return id, p.events
}

// Session event opcodes as captureDetails reports them.
const (
	evBufferSize = uint32(ext.ExtImageCopyCaptureSessionV1EventBufferSize)
	evDone       = uint32(ext.ExtImageCopyCaptureSessionV1EventDone)
	evStopped    = uint32(ext.ExtImageCopyCaptureSessionV1EventStopped)
)

// constraints reads one complete batch (buffer_size, two formats, done) and
// returns the size.
func constraints(t *testing.T, cc *cclient, ev chan []uint32) (w, h uint32) {
	t.Helper()
	got := nextSession(t, cc, ev)
	if got[0] != evBufferSize || len(got) != 3 {
		t.Fatalf("expected buffer_size, got %v", got)
	}
	w, h = got[1], got[2]
	for _, want := range []uint32{uint32(ext.ExtImageCopyCaptureSessionV1EventShmFormat), uint32(ext.ExtImageCopyCaptureSessionV1EventShmFormat), evDone} {
		if g := nextSession(t, cc, ev); g[0] != want {
			t.Fatalf("batch event %v, want opcode %d", g, want)
		}
	}
	return w, h
}

// nextSession waits for the next decoded session event, dispatching the client.
func nextSession(t *testing.T, cc *cclient, ev chan []uint32) []uint32 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case v := <-ev:
			return v
		default:
		}
		cc.roundtrip(t)
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for a session event")
	return nil
}

func noSession(t *testing.T, cc *cclient, ev chan []uint32) {
	t.Helper()
	cc.roundtrip(t)
	select {
	case v := <-ev:
		t.Fatalf("unexpected session event %v", v)
	default:
	}
}

// settle waits until the server applied the commands sent so far and the
// client received what followed.
func (h *captureHarness) settle(t *testing.T, cc *cclient) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(h.commands) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !h.s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	cc.roundtrip(t)
}

func (h *captureHarness) send(cmd ports.ClientCommand) { h.commands <- cmd }

// captureShm makes a w x h XRGB8888 wl_buffer.
func captureShm(t *testing.T, c *wlturbo.Display, w, h int32) (buf uint32, fd int) {
	t.Helper()
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("ext-capture", 0)
	if err != nil {
		t.Fatal(err)
	}
	size := w * h * 4
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	buf = c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, size); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), w, h, w*4, uint32(wayland.ShmFormatXrgb8888))
	t.Cleanup(func() { _ = unix.Close(fd) })
	return buf, fd
}

// extFrame asks one frame of a session into a fresh w x h buffer and returns
// its events; the frame object is destroyed so the next one may follow.
func (cc *cclient) extFrame(t *testing.T, session uint32, w, h int32) *captureEvents {
	t.Helper()
	buf, _ := captureShm(t, cc.c, w, h)
	frame := cc.c.AllocateID()
	ev := &captureEvents{events: make(chan uint16, 16)}
	ev.SetID(frame)
	registerWireProxy(cc.c, ev)
	requestProtocol(t, cc.c, session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, frame)
	requestProtocol(t, cc.c, frame, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, buf)
	requestProtocol(t, cc.c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
	cc.roundtrip(t)
	requestProtocol(t, cc.c, frame, ext.ExtImageCopyCaptureFrameV1RequestDestroy)
	cc.roundtrip(t)
	return ev
}

// wlrFrame asks a wlr-screencopy capture of the whole output into a buffer.
func (cc *cclient) wlrFrame(t *testing.T) *captureEvents {
	t.Helper()
	buf, _ := captureShm(t, cc.c, 4, 4)
	mgr := bindVersion(t, cc.c, "zwlr_screencopy_manager_v1", 3)
	frame := cc.c.AllocateID()
	ev := &captureEvents{events: make(chan uint16, 16)}
	ev.SetID(frame)
	registerWireProxy(cc.c, ev)
	requestProtocol(t, cc.c, mgr, wlr.ZwlrScreencopyManagerV1RequestCaptureOutput, frame, int32(0), cc.output)
	requestProtocol(t, cc.c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
	cc.roundtrip(t)
	return ev
}

func receiveCapture(t *testing.T, ch chan ports.CaptureRequest) ports.CaptureRequest {
	t.Helper()
	select {
	case r := <-ch:
		_ = r.Dst.File.Close()
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no capture request")
		return ports.CaptureRequest{}
	}
}

func noCapture(t *testing.T, ch chan ports.CaptureRequest) {
	t.Helper()
	select {
	case r := <-ch:
		_ = r.Dst.File.Close()
		t.Fatalf("a capture reached the renderer: %+v", r)
	default:
	}
}

// eventsInclude drains the frame events received so far and reports op among them.
func eventsInclude(ev *captureEvents, op uint16) bool {
	found := false
	for len(ev.events) > 0 {
		if <-ev.events == op {
			found = true
		}
	}
	return found
}

const (
	frameReady  = uint16(ext.ExtImageCopyCaptureFrameV1EventReady)
	frameFailed = uint16(ext.ExtImageCopyCaptureFrameV1EventFailed)
)

func (h *captureHarness) open(t *testing.T) ports.CaptureSessionOpen {
	t.Helper()
	return nextEvent[ports.CaptureSessionOpen](t, h.events)
}

// active is the state core sends for a live session on the whole output.
func active(id uint64) ports.CaptureSessionState {
	return ports.CaptureSessionState{ID: id, Output: "HEADLESS-1", Rect: ports.Rect{W: 4, H: 4}, Active: true}
}

func TestTrustedPeer(t *testing.T) {
	for _, tc := range []struct {
		name string
		peer server.Credentials
		err  error
		want bool
	}{
		{"same user", server.Credentials{UID: 1000}, nil, true},
		{"other user", server.Credentials{UID: 1001}, nil, false},
		{"root is not the user", server.Credentials{UID: 0}, nil, false},
		{"overflow id", server.Credentials{UID: 65534}, nil, false},
		{"unreadable", server.Credentials{UID: 1000}, context.Canceled, false},
	} {
		if got := trustedPeer(1000, tc.peer, tc.err); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// A session on an output source registers with core and gets the output size.
func TestOutputSessionRegistersAndClose(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	session, ev := cc.session(t, cc.outputSource(t))
	if w, hh := constraints(t, cc, ev); w != 4 || hh != 4 {
		t.Fatalf("size %dx%d", w, hh)
	}
	open := h.open(t)
	if open.ID == 0 || open.Output != "HEADLESS-1" || open.Workspace != 0 || open.Region != (ports.Rect{}) {
		t.Fatalf("open %+v", open)
	}
	requestProtocol(t, cc.c, session, ext.ExtImageCopyCaptureSessionV1RequestDestroy)
	cc.roundtrip(t)
	if c := nextEvent[ports.CaptureSessionClose](t, h.events); c.ID != open.ID {
		t.Fatalf("close %+v", c)
	}
}

// A region source is clipped to the output, sized in physical pixels, and its
// buffer size is sent again when the scale changes.
func TestRegionSourceConstraintsFollowScale(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	session, ev := cc.session(t, cc.regionSource(t, 1, 1, 2, 2))
	if w, hh := constraints(t, cc, ev); w != 2 || hh != 2 {
		t.Fatalf("size %dx%d", w, hh)
	}
	open := h.open(t)
	if open.Output != "HEADLESS-1" || open.Region != (ports.Rect{X: 1, Y: 1, W: 2, H: 2}) {
		t.Fatalf("open %+v", open)
	}
	// Clipped: a rectangle past the output edge.
	_, ev2 := cc.session(t, cc.regionSource(t, 3, 3, 10, 10))
	if w, hh := constraints(t, cc, ev2); w != 1 || hh != 1 {
		t.Fatalf("clipped size %dx%d", w, hh)
	}
	h.open(t)
	// Doubling the scale of a bigger mode doubles the buffer.
	bigger := ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 8, Height: 8}, Width: 4, Height: 4, Scale: 2}
	applyOutputs(t, h.s, h.commands, ports.SetOutputs{Outputs: ports.Layout{bigger}})
	if w, hh := constraints(t, cc, ev); w != 4 || hh != 4 {
		t.Fatalf("size after the scale change %dx%d", w, hh)
	}
	noSession(t, cc, ev) // one batch only
	_ = session
	// The frame asked with the new size is served for the new region.
	r := func() ports.CaptureRequest {
		cc.extFrame(t, session, 4, 4)
		return receiveCapture(t, h.captures)
	}()
	if r.Region.Dx() != 4 || r.Region.Dy() != 4 || r.Workspace != 0 || r.Exclude {
		t.Fatalf("request %+v", r)
	}
}

// A frame whose buffer does not fit the geometry computed now, while the last
// constraints the client got are stale (the refresh has not run), fails with
// buffer_constraints after a new constraints batch: the client can retry.
func TestStaleConstraintsAreResentBeforeBufferConstraintsFailure(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	session, ev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, ev)
	h.open(t)
	// The client's last batch said 1x1 while the output is 4x4 now.
	if !h.s.display.Do(func() {
		for c := range h.s.captureSessions {
			c.sent = image.Pt(1, 1)
		}
	}) {
		t.Fatal("display stopped")
	}
	frame := cc.extFrame(t, session, 1, 1)
	if !eventsInclude(frame, frameFailed) {
		t.Fatal("a buffer of the stale size was served")
	}
	noCapture(t, h.captures)
	if w, hh := constraints(t, cc, ev); w != 4 || hh != 4 {
		t.Fatalf("resent constraints %dx%d", w, hh)
	}
	// Up to date: a wrong buffer fails without another batch.
	frame = cc.extFrame(t, session, 1, 1)
	if !eventsInclude(frame, frameFailed) {
		t.Fatal("a buffer of the wrong size was served")
	}
	noSession(t, cc, ev)
}

func TestRegionSourceInvalidSize(t *testing.T) {
	for _, size := range [][2]int32{{0, 4}, {4, 0}, {-1, 4}} {
		h := newCaptureHarness(t)
		cc := h.client(t)
		cc.regionSource(t, 0, 0, size[0], size[1])
		expectProtocolError(t, cc.c, cc.extra, uint32(imagecapture.NeferwlImageCaptureSourceManagerV1ErrorInvalidRegion))
	}
}

// A region with nothing left on the output stops at once and is not
// registered with core; so does a session on an output that is gone.
func TestRegionAndOutputSourcesThatAreEmptyStop(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	_, ev := cc.session(t, cc.regionSource(t, 100, 100, 5, 5))
	if g := nextSession(t, cc, ev); g[0] != evStopped {
		t.Fatalf("event %v", g)
	}
	select {
	case e := <-h.events:
		t.Fatalf("event for a stopped session: %+v", e)
	default:
	}
}

// Removing the output stops its sessions at once and tells core.
func TestOutputRemovalStopsRegionAndOutputSessions(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	_, ev1 := cc.session(t, cc.outputSource(t))
	_, ev2 := cc.session(t, cc.regionSource(t, 0, 0, 2, 2))
	constraints(t, cc, ev1)
	constraints(t, cc, ev2)
	a, b := h.open(t), h.open(t)
	applyOutputs(t, h.s, h.commands, ports.SetOutputs{})
	for _, ev := range []chan []uint32{ev1, ev2} {
		if g := nextSession(t, cc, ev); g[0] != evStopped {
			t.Fatalf("event %v", g)
		}
	}
	closed := map[uint64]bool{}
	for len(closed) < 2 {
		closed[nextEvent[ports.CaptureSessionClose](t, h.events).ID] = true
	}
	if !closed[a.ID] || !closed[b.ID] {
		t.Fatalf("closed %v", closed)
	}
}

// workspaceClient binds ext_workspace_manager_v1 on cc and returns the handle
// of each workspace announced, by order.
func (h *captureHarness) handles(t *testing.T, cc *cclient, want int) []uint32 {
	t.Helper()
	mgr := bindProtocol(t, cc.c, "ext_workspace_manager_v1")
	p := &workspaceEvents{client: cc.c, events: make(chan [2]uint32, 128), idEvents: make(chan string, 16), order: &workspaceEventOrder{}}
	p.SetID(mgr)
	registerWireProxy(cc.c, p)
	var out []uint32
	for i := 0; i < 16 && len(out) < want; i++ {
		cc.roundtrip(t)
		for len(p.events) > 0 {
			if v := <-p.events; v[0] == uint32(extws.ExtWorkspaceManagerV1EventWorkspace) {
				out = append(out, v[1])
			}
		}
	}
	if len(out) != want {
		t.Fatalf("%d workspace handles, want %d", len(out), want)
	}
	return out
}

func (h *captureHarness) snapshot(t *testing.T, ws ...ports.WorkspaceInfo) {
	t.Helper()
	h.workspaces <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: ws}}}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := 0
		h.s.display.Do(func() {
			for _, o := range h.s.workspaceSnapshot.Outputs {
				got += len(o.Workspaces)
			}
		})
		if got == len(ws) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("snapshot not applied")
}

// A workspace source registers the workspace ID, sizes buffers from the
// frame (off screen: the whole frame at the output scale), sends new
// constraints when core moves or resizes it, and stops at once when the
// handle is removed.
func TestWorkspaceSourceSessionLifecycle(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t,
		ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 4, H: 4}},
		ports.WorkspaceInfo{ID: 43, Name: "2", Hidden: true, Index: 1, Frame: ports.Rect{W: 3, H: 2}},
	)
	cc := h.client(t)
	handles := h.handles(t, cc, 2)
	// On screen: its frame on the output.
	_, evOn := cc.session(t, cc.workspaceSource(t, handles[0]))
	if w, hh := constraints(t, cc, evOn); w != 4 || hh != 4 {
		t.Fatalf("on-screen size %dx%d", w, hh)
	}
	if o := h.open(t); o.Workspace != 42 || o.Output != "" {
		t.Fatalf("open %+v", o)
	}
	// Off screen: the whole frame.
	sess, ev := cc.session(t, cc.workspaceSource(t, handles[1]))
	if w, hh := constraints(t, cc, ev); w != 3 || hh != 2 {
		t.Fatalf("off-screen size %dx%d", w, hh)
	}
	open := h.open(t)
	if open.Workspace != 43 {
		t.Fatalf("open %+v", open)
	}
	// Core resizes the frame: new constraints, once.
	h.send(ports.CaptureSessionState{ID: open.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 2, H: 2}, Workspace: 43, Hidden: true, Active: true})
	if w, hh := constraints(t, cc, ev); w != 2 || hh != 2 {
		t.Fatalf("resized %dx%d", w, hh)
	}
	h.send(ports.CaptureSessionState{ID: open.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 2, H: 2}, Workspace: 43, Hidden: true, Active: true, Revision: 0})
	h.settle(t, cc)
	noSession(t, cc, ev)
	// A frame asks for the off-screen workspace, whole.
	cc.extFrame(t, sess, 2, 2)
	r := receiveCapture(t, h.captures)
	if r.Workspace != 43 || !r.OffScreen || r.Session != open.ID || r.Region.Dx() != 2 || r.Region.Dy() != 2 {
		t.Fatalf("request %+v", r)
	}
	// Removing workspace 43 stops its session at once; the other lives on.
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	if g := nextSession(t, cc, ev); g[0] != evStopped {
		t.Fatalf("event %v", g)
	}
	if c := nextEvent[ports.CaptureSessionClose](t, h.events); c.ID != open.ID {
		t.Fatalf("close %+v", c)
	}
	noSession(t, cc, evOn)
}

// A handle that is already removed gives a source whose sessions stop at once.
func TestWorkspaceSourceOfRemovedHandleStops(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	cc := h.client(t)
	handles := h.handles(t, cc, 1)
	h.snapshot(t)
	cc.roundtrip(t)
	_, ev := cc.session(t, cc.workspaceSource(t, handles[0]))
	if g := nextSession(t, cc, ev); g[0] != evStopped {
		t.Fatalf("event %v", g)
	}
}

// The workspace ID survives a configured-name change: same handle, same
// decimal id, so a source made before stays valid.
func TestWorkspaceSourceSurvivesConfiguredRename(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	cc := h.client(t)
	handles := h.handles(t, cc, 1)
	_, ev := cc.session(t, cc.workspaceSource(t, handles[0]))
	constraints(t, cc, ev)
	h.open(t)
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "dev", Configured: "dev", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	h.settle(t, cc)
	noSession(t, cc, ev)
	select {
	case e := <-h.events:
		t.Fatalf("event %+v after a rename", e)
	default:
	}
}

// frameEvents decodes the events of a neferwl_workspace_frame_v1.
type frameEvents struct {
	wlturbo.BaseProxy
	rects chan [4]int32
}

func (p *frameEvents) Dispatch(e *wlturbo.Event) {
	p.rects <- [4]int32{e.Int32(), e.Int32(), e.Int32(), e.Int32()}
}

// workspaceFrame asks get_workspace_frame for a workspace handle.
func (cc *cclient) workspaceFrame(t *testing.T, handle uint32) (uint32, chan [4]int32) {
	t.Helper()
	id := cc.c.AllocateID()
	ev := &frameEvents{rects: make(chan [4]int32, 8)}
	ev.SetID(id)
	registerWireProxy(cc.c, ev)
	requestProtocol(t, cc.c, cc.extra, imagecapture.NeferwlImageCaptureSourceManagerV1RequestGetWorkspaceFrame, id, handle)
	cc.roundtrip(t)
	return id, ev.rects
}

func wantFrame(t *testing.T, ch chan [4]int32, want [4]int32) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("frame %v, want %v", got, want)
		}
	default:
		t.Fatalf("no frame, want %v", want)
	}
}

func noFrame(t *testing.T, ch chan [4]int32) {
	t.Helper()
	select {
	case got := <-ch:
		t.Fatalf("unexpected frame %v", got)
	default:
	}
}

// A workspace frame object reports the frame when it is created and each time
// it changes, for every workspace, whether or not it is on screen; it reports
// nothing for a removed handle and stops after a rename replaced the handle.
func TestWorkspaceFrameEvents(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t,
		ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{X: 1, Y: 0, W: 2, H: 4}},
		ports.WorkspaceInfo{ID: 43, Name: "2", Hidden: true, Index: 1, Frame: ports.Rect{W: 3, H: 2}},
	)
	cc := h.client(t)
	handles := h.handles(t, cc, 2)
	_, on := cc.workspaceFrame(t, handles[0])
	wantFrame(t, on, [4]int32{1, 0, 2, 4})
	_, off := cc.workspaceFrame(t, handles[1])
	wantFrame(t, off, [4]int32{0, 0, 3, 2})
	// Unchanged snapshot: nothing. A change: once, to the right object.
	h.snapshot(t,
		ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{X: 1, Y: 0, W: 2, H: 4}},
		ports.WorkspaceInfo{ID: 43, Name: "2", Hidden: true, Index: 1, Frame: ports.Rect{X: 1, Y: 1, W: 2, H: 2}},
	)
	h.settle(t, cc)
	noFrame(t, on)
	wantFrame(t, off, [4]int32{1, 1, 2, 2})
	noFrame(t, off)
	// Removal sends nothing; a handle removed before the request sends nothing.
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{X: 1, Y: 0, W: 2, H: 4}})
	h.settle(t, cc)
	noFrame(t, off)
	_, gone := cc.workspaceFrame(t, handles[1])
	noFrame(t, gone)
	// A configured rename replaces the handle: the old object goes quiet.
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "dev", Configured: "dev", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	h.settle(t, cc)
	noFrame(t, on)
}

// While the session is locked a frame object sends nothing, whether it is
// created before the lock or during it; the unlock sends the current frame
// once, and only if it changed.
func TestWorkspaceFrameSessionLock(t *testing.T) {
	// unlock leaves protection the way the session-lock unlock does.
	unlock := func(t *testing.T, h *captureHarness, state *atomic.Pointer[ports.SecurityState]) {
		t.Helper()
		protect(t, h.s, state, 2, false)
		if !h.s.display.Do(func() { h.s.updateWorkspaceManagers(h.s.workspaceSnapshot) }) {
			t.Fatal("display stopped")
		}
	}
	info := func(r ports.Rect) ports.WorkspaceInfo {
		return ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: r}
	}
	t.Run("change while locked", func(t *testing.T) {
		h := newCaptureHarness(t)
		h.snapshot(t, info(ports.Rect{W: 2, H: 2}))
		cc := h.client(t)
		handles := h.handles(t, cc, 1)
		_, ev := cc.workspaceFrame(t, handles[0])
		wantFrame(t, ev, [4]int32{0, 0, 2, 2})
		state := installSecurity(t, h.s)
		protect(t, h.s, state, 1, true)
		h.snapshot(t, info(ports.Rect{X: 1, Y: 1, W: 3, H: 3}))
		h.settle(t, cc)
		noFrame(t, ev)
		unlock(t, h, state)
		cc.roundtrip(t)
		wantFrame(t, ev, [4]int32{1, 1, 3, 3})
		noFrame(t, ev)
	})
	t.Run("no change while locked", func(t *testing.T) {
		h := newCaptureHarness(t)
		h.snapshot(t, info(ports.Rect{W: 2, H: 2}))
		cc := h.client(t)
		handles := h.handles(t, cc, 1)
		_, ev := cc.workspaceFrame(t, handles[0])
		wantFrame(t, ev, [4]int32{0, 0, 2, 2})
		state := installSecurity(t, h.s)
		protect(t, h.s, state, 1, true)
		h.snapshot(t, info(ports.Rect{X: 1, Y: 1, W: 3, H: 3}))
		h.snapshot(t, info(ports.Rect{W: 2, H: 2}))
		h.settle(t, cc)
		unlock(t, h, state)
		cc.roundtrip(t)
		noFrame(t, ev)
	})
	t.Run("get while locked", func(t *testing.T) {
		h := newCaptureHarness(t)
		h.snapshot(t, info(ports.Rect{W: 2, H: 2}))
		cc := h.client(t)
		handles := h.handles(t, cc, 1)
		state := installSecurity(t, h.s)
		protect(t, h.s, state, 1, true)
		_, ev := cc.workspaceFrame(t, handles[0])
		h.settle(t, cc)
		noFrame(t, ev)
		unlock(t, h, state)
		cc.roundtrip(t)
		wantFrame(t, ev, [4]int32{0, 0, 2, 2})
		noFrame(t, ev)
	})
}

// Destroying a frame object drops it from the server's list, and nothing is
// sent to it afterwards.
func TestWorkspaceFrameDestroyRemovesObject(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 2, H: 2}})
	cc := h.client(t)
	handles := h.handles(t, cc, 1)
	count := func() (n int) {
		h.s.display.Do(func() { n = len(h.s.workspaceFrames) })
		return n
	}
	id, ev := cc.workspaceFrame(t, handles[0])
	wantFrame(t, ev, [4]int32{0, 0, 2, 2})
	if n := count(); n != 1 {
		t.Fatalf("%d frame objects, want 1", n)
	}
	requestProtocol(t, cc.c, id, imagecapture.NeferwlWorkspaceFrameV1RequestDestroy)
	cc.roundtrip(t)
	if n := count(); n != 0 {
		t.Fatalf("%d frame objects after destroy, want 0", n)
	}
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 3, H: 3}})
	h.settle(t, cc)
	noFrame(t, ev)
}

// exclusion asks get_exclusion on a session and returns the event stream.
func (cc *cclient) exclusion(t *testing.T, session uint32) (uint32, chan sessionMsg) {
	t.Helper()
	id := cc.c.AllocateID()
	ev := &exclusionEvents{msgs: make(chan sessionMsg, 8)}
	ev.SetID(id)
	registerWireProxy(cc.c, ev)
	requestProtocol(t, cc.c, cc.excl, imagecapture.NeferwlCaptureExclusionManagerV1RequestGetExclusion, id, session)
	cc.roundtrip(t)
	return id, ev.msgs
}

// exclusionOwner opens a session and its exclusion, confirmed by core.
func (h *captureHarness) exclusionOwner(t *testing.T) (*cclient, uint32, ports.CaptureSessionOpen, chan sessionMsg, string) {
	t.Helper()
	cc := h.client(t)
	session, sev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, sev)
	open := h.open(t)
	_, msgs := cc.exclusion(t, session)
	m := waitMsg(t, cc.c, msgs)
	if uint32(m.op) != imagecapture.NeferwlCaptureExclusionV1EventToken || len(m.token) != 64 || strings.Trim(m.token, "0123456789abcdef") != "" {
		t.Fatalf("token %+v", m)
	}
	if b := nextEvent[ports.CaptureExclusionBegin](t, h.events); b.Session != open.ID {
		t.Fatalf("begin %+v", b)
	}
	return cc, session, open, msgs, m.token
}

// The exclusion answers with a token; a second one is busy, even from
// another connection, and is not reported to core.
func TestExclusionTokenAndBusy(t *testing.T) {
	h := newCaptureHarness(t)
	_, _, open, _, _ := h.exclusionOwner(t)
	other := h.client(t)
	session2, sev2 := other.session(t, other.outputSource(t))
	constraints(t, other, sev2)
	h.open(t)
	_, msgs := other.exclusion(t, session2)
	if m := waitMsg(t, other.c, msgs); uint32(m.op) != imagecapture.NeferwlCaptureExclusionV1EventFailed || m.numbers[0] != int64(imagecapture.NeferwlCaptureExclusionV1FailureBusy) {
		t.Fatalf("second exclusion %+v", m)
	}
	for len(h.events) > 0 {
		if b, ok := (<-h.events).(ports.CaptureExclusionBegin); ok {
			t.Fatalf("a refused exclusion reached core: %+v (owner %d)", b, open.ID)
		}
	}
}

// get_exclusion on a session that already stopped answers
// failed(session_stopped), whatever the reason it stopped.
func TestExclusionOnStoppedSession(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	// Stopped at creation (a region with nothing on the output).
	session, ev := cc.session(t, cc.regionSource(t, 100, 100, 5, 5))
	if g := nextSession(t, cc, ev); g[0] != evStopped {
		t.Fatalf("event %v", g)
	}
	_, msgs := cc.exclusion(t, session)
	if m := waitMsg(t, cc.c, msgs); uint32(m.op) != imagecapture.NeferwlCaptureExclusionV1EventFailed || m.numbers[0] != int64(imagecapture.NeferwlCaptureExclusionV1FailureSessionStopped) {
		t.Fatalf("exclusion %+v", m)
	}
	// Stopped by core after it was registered.
	session2, sev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, sev)
	open := h.open(t)
	h.send(ports.CaptureSessionState{ID: open.ID, Reason: ports.CaptureReasonOutputGone})
	if g := nextSession(t, cc, sev); g[0] != evStopped {
		t.Fatalf("event %v", g)
	}
	_, msgs = cc.exclusion(t, session2)
	if m := waitMsg(t, cc.c, msgs); uint32(m.op) != imagecapture.NeferwlCaptureExclusionV1EventFailed || m.numbers[0] != int64(imagecapture.NeferwlCaptureExclusionV1FailureSessionStopped) {
		t.Fatalf("exclusion %+v", m)
	}
	for len(h.events) > 0 {
		if b, ok := (<-h.events).(ports.CaptureExclusionBegin); ok {
			t.Fatalf("a refused exclusion reached core: %+v", b)
		}
	}
}

// One session gets one live exclusion object.
func TestExclusionAlreadyExcluded(t *testing.T) {
	h := newCaptureHarness(t)
	cc, session, _, _, _ := h.exclusionOwner(t)
	requestProtocol(t, cc.c, cc.excl, imagecapture.NeferwlCaptureExclusionManagerV1RequestGetExclusion, cc.c.AllocateID(), session)
	expectProtocolError(t, cc.c, cc.excl, uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorAlreadyExcluded))
}

// Destroying the exclusion ends it in core, detaches its layers and frees
// the slot; the session lives on.
func TestExclusionDestroyFreesTheSlot(t *testing.T) {
	h := newCaptureHarness(t)
	cc, session, open, _, _ := h.exclusionOwner(t)
	h.send(ports.CaptureSessionState{ID: open.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 4, H: 4}, Active: true, Exclusion: true, Revision: 1})
	h.settle(t, cc)
	var excl uint32
	h.s.display.Do(func() { excl = h.s.excl.res.ID() })
	requestProtocol(t, cc.c, excl, imagecapture.NeferwlCaptureExclusionV1RequestDestroy)
	cc.roundtrip(t)
	if e := nextEvent[ports.CaptureExclusionEnd](t, h.events); e.Session != open.ID {
		t.Fatalf("end %+v", e)
	}
	_, msgs := cc.exclusion(t, session)
	if m := waitMsg(t, cc.c, msgs); uint32(m.op) != imagecapture.NeferwlCaptureExclusionV1EventToken {
		t.Fatalf("new exclusion %+v", m)
	}
}

// Frames of the session that owns the exclusion are fenced: refused until
// core knows the exclusion, then tagged with its revision. Every other
// capture, wlr-screencopy included, carries no tag.
func TestExclusionFrameTagging(t *testing.T) {
	h := newCaptureHarness(t)
	owner, session, open, _, _ := h.exclusionOwner(t)
	// Core has not confirmed the exclusion: the frame fails.
	ev := owner.extFrame(t, session, 4, 4)
	if !eventsInclude(ev, frameFailed) {
		t.Fatal("a frame before core confirmed the exclusion was served")
	}
	noCapture(t, h.captures)
	h.send(ports.CaptureSessionState{ID: open.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 4, H: 4}, Active: true, Exclusion: true, Revision: 9})
	h.settle(t, owner)
	owner.extFrame(t, session, 4, 4)
	r := receiveCapture(t, h.captures)
	if !r.Exclude || r.Session != open.ID || r.CaptureRevision != 9 {
		t.Fatalf("owner frame %+v", r)
	}
	h.captured <- ports.CaptureDone{ID: r.ID, Output: r.Output, Time: time.Now()}
	// A wlr-screencopy capture, even of the owner's connection, is untagged.
	owner.wlrFrame(t)
	r = receiveCapture(t, h.captures)
	if r.Exclude || r.Session != 0 || r.CaptureRevision != 0 || r.Workspace != 0 {
		t.Fatalf("wlr capture tagged: %+v", r)
	}
	h.captured <- ports.CaptureDone{ID: r.ID, Output: r.Output, Time: time.Now()}
	// So is a plain ext session of another client.
	other := h.client(t)
	session2, sev := other.session(t, other.outputSource(t))
	constraints(t, other, sev)
	h.open(t)
	other.extFrame(t, session2, 4, 4)
	r = receiveCapture(t, h.captures)
	if r.Exclude || r.CaptureRevision != 0 {
		t.Fatalf("another session's frame tagged: %+v", r)
	}
}

// hud is a second connection: a layer surface it attaches with the token.
type hud struct {
	*cclient
	surface, layer uint32
}

func (h *captureHarness) hud(t *testing.T, layer ports.Layer, keyboard uint32) *hud {
	t.Helper()
	return h.hudVersion(t, layer, keyboard, 1)
}

func (h *captureHarness) hudVersion(t *testing.T, layer ports.Layer, keyboard, shellVersion uint32) *hud {
	t.Helper()
	pc := h.client(t)
	comp := bindProtocol(t, pc.c, "wl_compositor")
	shell := bindVersion(t, pc.c, "zwlr_layer_shell_v1", shellVersion)
	surf, l := pc.c.AllocateID(), pc.c.AllocateID()
	requestProtocol(t, pc.c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, pc.c, surf)
	requestProtocol(t, pc.c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, l, surf, pc.output, uint32(layer), "hud")
	registerProtocol(t, pc.c, l)
	if keyboard != 0 {
		requestProtocol(t, pc.c, l, wlrlayershell.ZwlrLayerSurfaceV1RequestSetKeyboardInteractivity, keyboard)
	}
	if err := pc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return &hud{cclient: pc, surface: surf, layer: l}
}

// attach sends attach_surface and returns the attachment's event stream.
func (hd *hud) attach(t *testing.T, token string) chan sessionMsg {
	t.Helper()
	return hd.attachSurface(t, token, hd.surface)
}

func (hd *hud) attachSurface(t *testing.T, token string, surface uint32) chan sessionMsg {
	t.Helper()
	id := hd.c.AllocateID()
	ev := &attachEvents{msgs: make(chan sessionMsg, 8)}
	ev.SetID(id)
	registerWireProxy(hd.c, ev)
	requestProtocol(t, hd.c, hd.excl, imagecapture.NeferwlCaptureExclusionManagerV1RequestAttachSurface, id, token, surface)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return ev.msgs
}

// expectAttachment waits for one attachment event of the given opcode and first argument.
func expectAttachment(t *testing.T, c *wlturbo.Display, ch chan sessionMsg, op uint32, arg ...int64) {
	t.Helper()
	m := waitMsg(t, c, ch)
	if uint32(m.op) != op || (len(arg) > 0 && m.numbers[0] != arg[0]) {
		t.Fatalf("attachment event %+v, want op %d %v", m, op, arg)
	}
}

// waitMsg waits for a message on ch, dispatching the client meanwhile.
func waitMsg(t *testing.T, c *wlturbo.Display, ch chan sessionMsg) sessionMsg {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case m := <-ch:
			return m
		default:
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for an event")
	return sessionMsg{}
}

func noMessage(t *testing.T, c *wlturbo.Display, ch chan sessionMsg) {
	t.Helper()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch:
		t.Fatalf("unexpected event %+v", m)
	default:
	}
}

func nextEvent[T ports.ClientEvent](t *testing.T, ch chan ports.ClientEvent) T {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if v, ok := ev.(T); ok {
				return v
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a client event")
		}
	}
}

// Attach works from another connection with the token, reaches core before
// any content, and is confirmed to the client only once core lists the layer.
func TestExclusionAttach(t *testing.T) {
	h := newCaptureHarness(t)
	_, _, open, _, token := h.exclusionOwner(t)
	hd := h.hud(t, ports.LayerOverlay, 2)
	// A wrong token fails that attachment without touching the exclusion.
	bad := hd.attach(t, strings.Repeat("0", 64))
	expectAttachment(t, hd.c, bad, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureUnknownToken))
	good := hd.attach(t, token)
	layer := nextEvent[ports.CaptureExclusionLayer](t, h.events)
	if layer.Session != open.ID || !layer.Attached || layer.Layer == 0 {
		t.Fatalf("layer %+v", layer)
	}
	noMessage(t, hd.c, good) // not confirmed yet
	st := active(open.ID)
	st.Exclusion, st.Revision = true, 2
	h.send(st)
	noMessage(t, hd.c, good) // a state that does not list the layer confirms nothing
	st.Revision, st.Layers = 3, []ports.WindowID{layer.Layer}
	h.send(st)
	expectAttachment(t, hd.c, good, imagecapture.NeferwlCaptureLayerV1EventAttached)
	// Attaching the same surface again fails on its own object.
	again := hd.attach(t, token)
	expectAttachment(t, hd.c, again, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureAlreadyAttached))
	// Destroying the layer detaches it: LayerChanged (the unmap) first, then
	// the detach, on the same ordered channel.
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestDestroy)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expectAttachment(t, hd.c, good, imagecapture.NeferwlCaptureLayerV1EventDetached, int64(imagecapture.NeferwlCaptureLayerV1DetachReasonLayerDestroyed))
	if l := nextEvent[ports.CaptureExclusionLayer](t, h.events); l.Attached || l.Layer != layer.Layer {
		t.Fatalf("detach %+v", l)
	}
}

// Once the exclusion ended the token is dead.
func TestExclusionTokenDeadAfterEnd(t *testing.T) {
	h := newCaptureHarness(t)
	owner, session, _, _, token := h.exclusionOwner(t)
	requestProtocol(t, owner.c, session, ext.ExtImageCopyCaptureSessionV1RequestDestroy)
	owner.roundtrip(t)
	hd := h.hud(t, ports.LayerOverlay, 0)
	att := hd.attach(t, token)
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureUnknownToken))
}

// Order on the event channel: a mapped attached layer that is destroyed
// queues its LayerChanged removal before the detach reaches core.
func TestExclusionDetachOrdering(t *testing.T) {
	h := newCaptureHarness(t)
	_, _, open, _, token := h.exclusionOwner(t)
	hd := h.hud(t, ports.LayerTop, 0)
	att := hd.attach(t, token)
	layer := nextEvent[ports.CaptureExclusionLayer](t, h.events)
	st := active(open.ID)
	st.Exclusion, st.Revision, st.Layers = true, 2, []ports.WindowID{layer.Layer}
	h.send(st)
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventAttached)
	mapLayer(t, hd)
	if l := nextEvent[ports.LayerChanged](t, h.events); len(l.Layers) != 1 {
		t.Fatalf("mapped %+v", l)
	}
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestDestroy)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var order []string
	for len(order) < 2 {
		switch v := (<-h.events).(type) {
		case ports.LayerChanged:
			if len(v.Layers) == 0 {
				order = append(order, "unmap")
			}
		case ports.CaptureExclusionLayer:
			if !v.Attached {
				order = append(order, "detach")
			}
		}
	}
	if order[0] != "unmap" || order[1] != "detach" {
		t.Fatalf("order %v: the exclusion must outlive the layer's last frame", order)
	}
}

// mapLayer configures, acks and commits a buffer on the hud's layer.
func mapLayer(t *testing.T, hd *hud) {
	t.Helper()
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(1), uint32(1))
	requestProtocol(t, hd.c, hd.surface, wayland.SurfaceRequestCommit)
	cfg := &layerProxy{configured: make(chan [3]uint32, 4)}
	cfg.SetID(hd.layer)
	registerWireProxy(hd.c, cfg)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	c := <-cfg.configured
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestAckConfigure, c[0])
	_, buf, fd := captureSmallBuffer(t, hd.c)
	t.Cleanup(func() { _ = unix.Close(fd) })
	requestProtocol(t, hd.c, hd.surface, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, hd.c, hd.surface, wayland.SurfaceRequestCommit)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// Ending the exclusion (here with its session) detaches every attachment,
// closes the layer surfaces (so none lingers into the next exclusion) and
// removes them before core is told the session closed.
func TestExclusionEndClosesLayers(t *testing.T) {
	h := newCaptureHarness(t)
	owner, session, open, _, token := h.exclusionOwner(t)
	hd := h.hud(t, ports.LayerOverlay, 0)
	closed := make(chan sessionMsg, 4)
	lp := &layerClosedProxy{msgs: closed}
	lp.SetID(hd.layer)
	registerWireProxy(hd.c, lp)
	confirmed := hd.attach(t, token)
	layer := nextEvent[ports.CaptureExclusionLayer](t, h.events)
	st := active(open.ID)
	st.Exclusion, st.Revision, st.Layers = true, 2, []ports.WindowID{layer.Layer}
	h.send(st)
	expectAttachment(t, hd.c, confirmed, imagecapture.NeferwlCaptureLayerV1EventAttached)
	pending := hd.attachSurfaceNew(t, token)
	requestProtocol(t, owner.c, session, ext.ExtImageCopyCaptureSessionV1RequestDestroy)
	owner.roundtrip(t)
	expectAttachment(t, hd.c, confirmed, imagecapture.NeferwlCaptureLayerV1EventDetached, int64(imagecapture.NeferwlCaptureLayerV1DetachReasonExclusionEnded))
	expectAttachment(t, hd.c, pending, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureExclusionEnded))
	if m := waitMsg(t, hd.c, closed); uint32(m.op) != uint32(wlrlayershell.ZwlrLayerSurfaceV1EventClosed) {
		t.Fatalf("layer event %+v", m)
	}
	if e := nextEvent[ports.CaptureSessionClose](t, h.events); e.ID != open.ID {
		t.Fatalf("close %+v", e)
	}
}

type layerClosedProxy struct {
	wlturbo.BaseProxy
	msgs chan sessionMsg
}

func (p *layerClosedProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wlrlayershell.ZwlrLayerSurfaceV1EventClosed) {
		p.msgs <- sessionMsg{op: e.Opcode}
	}
}

// attachSurfaceNew attaches a second, fresh layer surface of the hud and returns its stream.
func (hd *hud) attachSurfaceNew(t *testing.T, token string) chan sessionMsg {
	t.Helper()
	comp := bindProtocol(t, hd.c, "wl_compositor")
	shell := bindProtocol(t, hd.c, "zwlr_layer_shell_v1")
	surf, l := hd.c.AllocateID(), hd.c.AllocateID()
	requestProtocol(t, hd.c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, hd.c, surf)
	requestProtocol(t, hd.c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, l, surf, hd.output, uint32(ports.LayerTop), "hud2")
	registerProtocol(t, hd.c, l)
	return hd.attachSurface(t, token, surf)
}

func TestExclusionAttachRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		layer    ports.Layer
		keyboard uint32
		code     uint32
	}{
		{"background layer", ports.LayerBackground, 0, uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorInvalidLayer)},
		{"bottom layer", ports.LayerBottom, 0, uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorInvalidLayer)},
		{"exclusive keyboard", ports.LayerTop, 1, uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorExclusiveKeyboard)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCaptureHarness(t)
			_, _, _, _, token := h.exclusionOwner(t)
			hd := h.hud(t, tc.layer, tc.keyboard)
			requestProtocol(t, hd.c, hd.excl, imagecapture.NeferwlCaptureExclusionManagerV1RequestAttachSurface, hd.c.AllocateID(), token, hd.surface)
			expectProtocolError(t, hd.c, hd.excl, tc.code)
			for len(h.events) > 0 {
				if l, ok := (<-h.events).(ports.CaptureExclusionLayer); ok {
					t.Fatalf("a refused attach reached core: %+v", l)
				}
			}
		})
	}
}

func TestExclusionAttachLimit(t *testing.T) {
	h := newCaptureHarness(t)
	_, _, _, _, token := h.exclusionOwner(t)
	hd := h.hud(t, ports.LayerTop, 0)
	hd.attach(t, token)
	for i := 1; i < ports.MaxExclusionLayers; i++ {
		hd.attachSurfaceNew(t, token)
	}
	over := hd.attachSurfaceNew(t, token)
	expectAttachment(t, hd.c, over, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureTooManyLayers))
	n := 0
	for len(h.events) > 0 {
		if l, ok := (<-h.events).(ports.CaptureExclusionLayer); ok && l.Attached {
			n++
		}
	}
	if n != ports.MaxExclusionLayers {
		t.Fatalf("%d layers reached core, want %d", n, ports.MaxExclusionLayers)
	}
}

// An attached layer that commits a layer below top is closed and detached
// once, and its unmap reaches core before the detach.
func TestExclusionLayerMovedBelowTop(t *testing.T) {
	h := newCaptureHarness(t)
	_, _, open, _, token := h.exclusionOwner(t)
	hd := h.hudVersion(t, ports.LayerTop, 0, 2)
	att := hd.attach(t, token)
	layer := nextEvent[ports.CaptureExclusionLayer](t, h.events)
	st := active(open.ID)
	st.Exclusion, st.Revision, st.Layers = true, 2, []ports.WindowID{layer.Layer}
	h.send(st)
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventAttached)
	mapLayer(t, hd)
	if l := nextEvent[ports.LayerChanged](t, h.events); len(l.Layers) != 1 {
		t.Fatalf("mapped %+v", l)
	}
	closed := make(chan sessionMsg, 4)
	lp := &layerClosedProxy{msgs: closed}
	lp.SetID(hd.layer)
	registerWireProxy(hd.c, lp)
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetLayer, uint32(ports.LayerBottom))
	requestProtocol(t, hd.c, hd.surface, wayland.SurfaceRequestCommit)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventDetached, int64(imagecapture.NeferwlCaptureLayerV1DetachReasonLayerDestroyed))
	if m := waitMsg(t, hd.c, closed); uint32(m.op) != uint32(wlrlayershell.ZwlrLayerSurfaceV1EventClosed) {
		t.Fatalf("layer event %+v", m)
	}
	noMessage(t, hd.c, att) // detached exactly once
	var order []string
	for len(order) < 2 {
		switch v := (<-h.events).(type) {
		case ports.LayerChanged:
			if len(v.Layers) == 0 {
				order = append(order, "unmap")
			}
		case ports.CaptureExclusionLayer:
			if !v.Attached {
				order = append(order, "detach")
			}
		}
	}
	if order[0] != "unmap" || order[1] != "detach" {
		t.Fatalf("order %v", order)
	}
}

// An attached layer asking for keyboard interactivity, before or after the
// attach, is never reported with it to core.
func TestExclusionLayerKeyboardNone(t *testing.T) {
	h := newCaptureHarness(t)
	_, _, open, _, token := h.exclusionOwner(t)
	hd := h.hud(t, ports.LayerOverlay, 2) // on demand before the attach
	att := hd.attach(t, token)
	layer := nextEvent[ports.CaptureExclusionLayer](t, h.events)
	st := active(open.ID)
	st.Exclusion, st.Revision, st.Layers = true, 2, []ports.WindowID{layer.Layer}
	h.send(st)
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventAttached)
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetKeyboardInteractivity, uint32(2)) // and after
	mapLayer(t, hd)
	l := nextEvent[ports.LayerChanged](t, h.events)
	if len(l.Layers) != 1 || l.Layers[0].Keyboard != 0 {
		t.Fatalf("layer reported with keyboard: %+v", l.Layers)
	}
}

// frameTaken waits for the next CaptureFrameTaken core is told of.
func (h *captureHarness) frameTaken(t *testing.T) ports.CaptureFrameTaken {
	t.Helper()
	return nextEvent[ports.CaptureFrameTaken](t, h.events)
}

// Every capture is reported to core, which shows the indicator for it: a
// wlr-screencopy frame (no session) and a frame of each kind of ext source,
// with the target the client asked for.
func TestEveryCaptureFrameIsReportedToCore(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	cc := h.client(t)
	// wlr-screencopy, whole output.
	cc.wlrFrame(t)
	r := receiveCapture(t, h.captures)
	if got := h.frameTaken(t); got != (ports.CaptureFrameTaken{Output: "HEADLESS-1"}) {
		t.Fatalf("wlr frame reported %+v", got)
	}
	h.captured <- ports.CaptureDone{ID: r.ID, Output: r.Output, Time: time.Now()}
	if got := h.frameTaken(t); got != (ports.CaptureFrameTaken{Output: "HEADLESS-1"}) {
		t.Fatalf("wlr completion reported %+v", got)
	}
	// ext output source.
	session, ev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, ev)
	open := h.open(t)
	cc.extFrame(t, session, 4, 4)
	receiveCapture(t, h.captures)
	if got := h.frameTaken(t); got != (ports.CaptureFrameTaken{Session: open.ID, Output: "HEADLESS-1"}) {
		t.Fatalf("ext output frame reported %+v", got)
	}
	// ext region source: the logical rectangle as asked.
	session, ev = cc.session(t, cc.regionSource(t, 1, 1, 2, 2))
	constraints(t, cc, ev)
	open = h.open(t)
	cc.extFrame(t, session, 2, 2)
	receiveCapture(t, h.captures)
	if got := h.frameTaken(t); got != (ports.CaptureFrameTaken{Session: open.ID, Output: "HEADLESS-1", Region: ports.Rect{X: 1, Y: 1, W: 2, H: 2}}) {
		t.Fatalf("ext region frame reported %+v", got)
	}
	// ext workspace source.
	handles := h.handles(t, cc, 1)
	session, ev = cc.session(t, cc.workspaceSource(t, handles[0]))
	constraints(t, cc, ev)
	open = h.open(t)
	cc.extFrame(t, session, 4, 4)
	receiveCapture(t, h.captures)
	if got := h.frameTaken(t); got != (ports.CaptureFrameTaken{Session: open.ID, Workspace: 42}) {
		t.Fatalf("ext workspace frame reported %+v", got)
	}
}

// Every capture request asks the output owner to hold it until the indicator
// is on screen, whatever protocol or source made it.
func TestEveryCaptureRequestAsksForItsIndicator(t *testing.T) {
	h := newCaptureHarness(t)
	h.snapshot(t, ports.WorkspaceInfo{ID: 42, Name: "1", Active: true, Frame: ports.Rect{W: 4, H: 4}})
	cc := h.client(t)
	cc.wlrFrame(t)
	if r := receiveCapture(t, h.captures); !r.Indicate {
		t.Fatalf("wlr request %+v", r)
	}
	session, ev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, ev)
	h.open(t)
	cc.extFrame(t, session, 4, 4)
	if r := receiveCapture(t, h.captures); !r.Indicate {
		t.Fatalf("ext output request %+v", r)
	}
	handles := h.handles(t, cc, 1)
	session, ev = cc.session(t, cc.workspaceSource(t, handles[0]))
	constraints(t, cc, ev)
	h.open(t)
	cc.extFrame(t, session, 4, 4)
	if r := receiveCapture(t, h.captures); !r.Indicate || r.Workspace != 42 {
		t.Fatalf("ext workspace request %+v", r)
	}
}

// A capture the compositor refuses is not reported: nothing was captured.
func TestRefusedCaptureIsNotReported(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	session, ev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, ev)
	h.open(t)
	// A wrong-sized buffer is refused before it reaches the renderer.
	cc.extFrame(t, session, 2, 2)
	noCapture(t, h.captures)
	h.settle(t, cc)
	for len(h.events) > 0 {
		if v, ok := (<-h.events).(ports.CaptureFrameTaken); ok {
			t.Fatalf("refused capture reported: %+v", v)
		}
	}
}

// logicalRegion gives core the logical rectangle of a physical region.
func TestLogicalRegion(t *testing.T) {
	o := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Width: 8, Height: 8}, Scale: 2}}
	for _, tc := range []struct {
		phys image.Rectangle
		want ports.Rect
	}{
		{image.Rect(0, 0, 8, 8), ports.Rect{}},
		{image.Rect(2, 2, 6, 4), ports.Rect{X: 1, Y: 1, W: 2, H: 1}},
		{image.Rect(1, 1, 5, 3), ports.Rect{X: 0, Y: 0, W: 3, H: 2}}, // rounded outwards
	} {
		if got := logicalRegion(o, tc.phys); got != tc.want {
			t.Errorf("%v: %+v, want %+v", tc.phys, got, tc.want)
		}
	}
}
