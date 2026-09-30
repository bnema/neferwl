package wayland

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/wayland/capturesession"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrscreencopy"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// sessionMsg is one decoded event of the private protocol.
type sessionMsg struct {
	op      uint16
	token   string
	name    string
	numbers []int64
}

// managerEvents and sessionEvents decode the events of the two interfaces.
type managerEvents struct {
	wlturbo.BaseProxy
	msgs chan sessionMsg
}

func (p *managerEvents) Dispatch(e *wlturbo.Event) {
	m := sessionMsg{op: e.Opcode}
	switch uint32(e.Opcode) {
	case capturesession.NeferwlCaptureManagerV1EventWorkspace:
		m.numbers = append(m.numbers, int64(e.Uint32()), int64(e.Uint32()))
		_ = e.String()
		m.name = e.String()
		m.numbers = append(m.numbers, int64(e.Int32()), int64(e.Int32()), int64(e.Int32()), int64(e.Int32()), int64(e.Uint32()))
	case capturesession.NeferwlCaptureManagerV1EventWorkspaceRemoved:
		m.numbers = append(m.numbers, int64(e.Uint32()), int64(e.Uint32()))
	}
	p.msgs <- m
}

// attachEvents decodes the events of a neferwl_capture_layer_v1.
type attachEvents struct {
	wlturbo.BaseProxy
	msgs chan sessionMsg
}

func (p *attachEvents) Dispatch(e *wlturbo.Event) {
	m := sessionMsg{op: e.Opcode}
	if uint32(e.Opcode) != capturesession.NeferwlCaptureLayerV1EventAttached {
		m.numbers = append(m.numbers, int64(e.Uint32()))
	}
	p.msgs <- m
}

type sessionEvents struct {
	wlturbo.BaseProxy
	msgs chan sessionMsg
}

func (p *sessionEvents) Dispatch(e *wlturbo.Event) {
	m := sessionMsg{op: e.Opcode}
	switch uint32(e.Opcode) {
	case capturesession.NeferwlCaptureSessionV1EventState:
		m.token = e.String()
		for i := 0; i < 9; i++ {
			m.numbers = append(m.numbers, int64(e.Int32()))
		}
	case capturesession.NeferwlCaptureSessionV1EventStopped:
		m.numbers = append(m.numbers, int64(e.Uint32()))
	}
	p.msgs <- m
}

type privateHarness struct {
	s          *Server
	dir        string
	events     chan ports.ClientEvent
	commands   chan ports.ClientCommand
	workspaces chan ports.Workspaces
	captures   chan ports.CaptureRequest
	captured   chan ports.CaptureDone
}

func newPrivateHarness(t *testing.T) *privateHarness {
	t.Helper()
	h := &privateHarness{
		dir:        t.TempDir(),
		events:     make(chan ports.ClientEvent, 64),
		commands:   make(chan ports.ClientCommand, 16),
		workspaces: make(chan ports.Workspaces, 4),
		captures:   make(chan ports.CaptureRequest, 4),
		captured:   make(chan ports.CaptureDone, 4),
	}
	out := ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 2, Height: 2}, Width: 2, Height: 2, Scale: 1}}
	s, err := New(Options{RuntimeDir: h.dir, Outputs: out}, Channels{Events: h.events, Commands: h.commands, Workspaces: h.workspaces, Captures: h.captures, Captured: h.captured}, logging.For(context.Background(), "wayland"))
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

type privateClient struct {
	c       *wlturbo.Display
	manager uint32
	output  uint32
	mgr     *managerEvents
}

func (h *privateHarness) client(t *testing.T) *privateClient {
	t.Helper()
	c := protocolClient(t, h.s, h.dir)
	g, ok := c.Registry().FindGlobal("wl_output")
	if !ok {
		t.Fatal("output missing")
	}
	out, err := bindWireID(c, g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	pc := &privateClient{c: c, output: out, manager: bindProtocol(t, c, "neferwl_capture_manager_v1"), mgr: &managerEvents{msgs: make(chan sessionMsg, 32)}}
	pc.mgr.SetID(pc.manager)
	registerWireProxy(c, pc.mgr)
	return pc
}

// begin opens a session and returns its event stream.
func (pc *privateClient) begin(t *testing.T, x, y, w, h int32, workspace uint64, record uint32) (uint32, chan sessionMsg) {
	t.Helper()
	id := pc.c.AllocateID()
	ev := &sessionEvents{msgs: make(chan sessionMsg, 32)}
	ev.SetID(id)
	registerWireProxy(pc.c, ev)
	requestProtocol(t, pc.c, pc.manager, capturesession.NeferwlCaptureManagerV1RequestBeginSession, id, pc.output, x, y, w, h, uint32(workspace>>32), uint32(workspace), record)
	if err := pc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return id, ev.msgs
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

func (h *privateHarness) send(cmd ports.ClientCommand) { h.commands <- cmd }

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

func TestPrivateCaptureBeginAndState(t *testing.T) {
	h := newPrivateHarness(t)
	pc := h.client(t)
	id, msgs := pc.begin(t, 1, 2, 3, 4, 1<<33|7, 1)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	if begin.Output != "HEADLESS-1" || begin.Workspace != 1<<33|7 || begin.Region != (ports.Rect{X: 1, Y: 2, W: 3, H: 4}) || !begin.Record || begin.ID == 0 {
		t.Fatalf("begin %+v", begin)
	}
	// Nothing is sent, and above all no token, until core confirmed.
	noMessage(t, pc.c, msgs)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Rect: ports.Rect{X: 1, Y: 2, W: 3, H: 4}, Workspace: 1<<33 | 7, Active: true, Revision: 1<<32 | 5})
	m := waitMsg(t, pc.c, msgs)
	if uint32(m.op) != capturesession.NeferwlCaptureSessionV1EventState || len(m.token) != 64 || strings.Trim(m.token, "0123456789abcdef") != "" {
		t.Fatalf("state %+v", m)
	}
	want := []int64{1, 1, 2, 3, 4, 2, 7, 1, 5}
	for i, v := range want {
		if m.numbers[i] != v {
			t.Fatalf("state numbers %v, want %v", m.numbers, want)
		}
	}
	// Ping reaches core, and a flood of them does not.
	for i := 0; i < 50; i++ {
		requestProtocol(t, pc.c, id, capturesession.NeferwlCaptureSessionV1RequestPing)
	}
	if err := pc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if p := nextEvent[ports.CaptureSessionPing](t, h.events); p.ID != begin.ID {
		t.Fatalf("ping %+v", p)
	}
	select {
	case ev := <-h.events:
		if _, ok := ev.(ports.CaptureSessionPing); ok {
			t.Fatalf("ping flood forwarded: %+v", ev)
		}
	default:
	}
	// Destroy ends the session in core.
	requestProtocol(t, pc.c, id, capturesession.NeferwlCaptureSessionV1RequestDestroy)
	if err := pc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if e := nextEvent[ports.CaptureSessionEnd](t, h.events); e.ID != begin.ID {
		t.Fatalf("end %+v", e)
	}
}

func TestPrivateCaptureBusyAndTerminal(t *testing.T) {
	h := newPrivateHarness(t)
	pc := h.client(t)
	_, first := pc.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	other := h.client(t)
	_, second := other.begin(t, 0, 0, 0, 0, 0, 0)
	if m := waitMsg(t, other.c, second); uint32(m.op) != capturesession.NeferwlCaptureSessionV1EventStopped || m.numbers[0] != int64(capturesession.NeferwlCaptureSessionV1StopReasonBusy) {
		t.Fatalf("second session: %+v", m)
	}
	// The refusal neither reaches core nor disturbs the first session.
	noMessage(t, pc.c, first)
	select {
	case ev := <-h.events:
		t.Fatalf("event for a refused session: %+v", ev)
	default:
	}
	// A terminal state from core stops the session with its reason.
	h.send(ports.CaptureSessionState{ID: begin.ID, Reason: ports.CaptureReasonWorkspaceGone})
	if m := waitMsg(t, pc.c, first); uint32(m.op) != capturesession.NeferwlCaptureSessionV1EventStopped || m.numbers[0] != int64(capturesession.NeferwlCaptureSessionV1StopReasonWorkspaceGone) {
		t.Fatalf("stopped: %+v", m)
	}
	// The slot is free again.
	_, third := other.begin(t, 0, 0, 0, 0, 0, 0)
	noMessage(t, other.c, third)
	nextEvent[ports.CaptureSessionBegin](t, h.events)
}

func TestPrivateCaptureInvalidRequests(t *testing.T) {
	h := newPrivateHarness(t)
	pc := h.client(t)
	requestProtocol(t, pc.c, pc.manager, capturesession.NeferwlCaptureManagerV1RequestBeginSession, pc.c.AllocateID(), pc.output, int32(0), int32(0), int32(-1), int32(4), uint32(0), uint32(0), uint32(0))
	expectProtocolError(t, pc.c, pc.manager, uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidRegion))
}

// hud is a second connection: a layer surface it attaches with the token.
type hud struct {
	*privateClient
	surface, layer uint32
}

func (h *privateHarness) hud(t *testing.T, layer ports.Layer, keyboard uint32) *hud {
	t.Helper()
	return h.hudVersion(t, layer, keyboard, 1)
}

func (h *privateHarness) hudVersion(t *testing.T, layer ports.Layer, keyboard, shellVersion uint32) *hud {
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
	return &hud{privateClient: pc, surface: surf, layer: l}
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
	requestProtocol(t, hd.c, hd.manager, capturesession.NeferwlCaptureManagerV1RequestAttachSurface, id, token, surface)
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

// Attach works from another connection with the token, reaches core before
// any content, and is confirmed to the client only once core lists the layer.
func TestPrivateCaptureAttach(t *testing.T) {
	h := newPrivateHarness(t)
	owner := h.client(t)
	_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 2, H: 2}, Active: true, Revision: 1})
	token := waitMsg(t, owner.c, msgs).token

	hd := h.hud(t, ports.LayerOverlay, 2)
	// A wrong token fails that attachment without touching the session.
	bad := hd.attach(t, strings.Repeat("0", 64))
	expectAttachment(t, hd.c, bad, capturesession.NeferwlCaptureLayerV1EventFailed, int64(capturesession.NeferwlCaptureLayerV1FailureUnknownToken))
	good := hd.attach(t, token)
	layer := nextEvent[ports.CaptureSessionLayer](t, h.events)
	if layer.ID != begin.ID || !layer.Attached || layer.Layer == 0 {
		t.Fatalf("layer %+v", layer)
	}
	noMessage(t, hd.c, good) // not confirmed yet
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 2})
	noMessage(t, hd.c, good) // a state that does not list the layer confirms nothing
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 3, Layers: []ports.WindowID{layer.Layer}})
	expectAttachment(t, hd.c, good, capturesession.NeferwlCaptureLayerV1EventAttached)
	// Attaching the same surface again fails on its own object.
	again := hd.attach(t, token)
	expectAttachment(t, hd.c, again, capturesession.NeferwlCaptureLayerV1EventFailed, int64(capturesession.NeferwlCaptureLayerV1FailureAlreadyAttached))
	// Destroying the layer detaches it: LayerChanged (the unmap) first, then
	// the detach, on the same ordered channel, so core never drops the
	// exclusion before the layer is gone.
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestDestroy)
	if err := hd.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expectAttachment(t, hd.c, good, capturesession.NeferwlCaptureLayerV1EventDetached, int64(capturesession.NeferwlCaptureLayerV1DetachReasonLayerDestroyed))
	if l := nextEvent[ports.CaptureSessionLayer](t, h.events); l.Attached || l.Layer != layer.Layer {
		t.Fatalf("detach %+v", l)
	}
}

// Order on the event channel: a mapped attached layer that is destroyed
// queues its LayerChanged removal before the detach reaches core.
func TestPrivateCaptureDetachOrdering(t *testing.T) {
	h := newPrivateHarness(t)
	owner := h.client(t)
	_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
	token := waitMsg(t, owner.c, msgs).token
	hd := h.hud(t, ports.LayerTop, 0)
	att := hd.attach(t, token)
	layer := nextEvent[ports.CaptureSessionLayer](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 2, Layers: []ports.WindowID{layer.Layer}})
	expectAttachment(t, hd.c, att, capturesession.NeferwlCaptureLayerV1EventAttached)
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
		case ports.CaptureSessionLayer:
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

// Stopping the session detaches every attachment, closes the layer surfaces
// (so none lingers into the next session) and removes them before core is told.
func TestPrivateCaptureStopClosesLayers(t *testing.T) {
	h := newPrivateHarness(t)
	owner := h.client(t)
	sid, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
	token := waitMsg(t, owner.c, msgs).token
	hd := h.hud(t, ports.LayerOverlay, 0)
	closed := make(chan sessionMsg, 4)
	lp := &layerClosedProxy{msgs: closed}
	lp.SetID(hd.layer)
	registerWireProxy(hd.c, lp)
	confirmed := hd.attach(t, token)
	layer := nextEvent[ports.CaptureSessionLayer](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 2, Layers: []ports.WindowID{layer.Layer}})
	expectAttachment(t, hd.c, confirmed, capturesession.NeferwlCaptureLayerV1EventAttached)
	pending := hd.attachSurfaceNew(t, token)
	_ = pending
	requestProtocol(t, owner.c, sid, capturesession.NeferwlCaptureSessionV1RequestDestroy)
	if err := owner.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	expectAttachment(t, hd.c, confirmed, capturesession.NeferwlCaptureLayerV1EventDetached, int64(capturesession.NeferwlCaptureLayerV1DetachReasonSessionEnded))
	if m := waitMsg(t, hd.c, closed); uint32(m.op) != uint32(wlrlayershell.ZwlrLayerSurfaceV1EventClosed) {
		t.Fatalf("layer event %+v", m)
	}
	if e := nextEvent[ports.CaptureSessionEnd](t, h.events); e.ID != begin.ID {
		t.Fatalf("end %+v", e)
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

func TestPrivateCaptureAttachRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		layer    ports.Layer
		keyboard uint32
		code     uint32
	}{
		{"background layer", ports.LayerBackground, 0, uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidLayer)},
		{"bottom layer", ports.LayerBottom, 0, uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidLayer)},
		{"exclusive keyboard", ports.LayerTop, 1, uint32(capturesession.NeferwlCaptureManagerV1ErrorExclusiveKeyboard)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPrivateHarness(t)
			owner := h.client(t)
			_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
			begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
			h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
			token := waitMsg(t, owner.c, msgs).token
			hd := h.hud(t, tc.layer, tc.keyboard)
			requestProtocol(t, hd.c, hd.manager, capturesession.NeferwlCaptureManagerV1RequestAttachSurface, hd.c.AllocateID(), token, hd.surface)
			expectProtocolError(t, hd.c, hd.manager, tc.code)
			select {
			case ev := <-h.events:
				if _, ok := ev.(ports.CaptureSessionLayer); ok {
					t.Fatalf("a refused attach reached core: %+v", ev)
				}
			default:
			}
		})
	}
}

func TestPrivateCaptureAttachLimit(t *testing.T) {
	h := newPrivateHarness(t)
	owner := h.client(t)
	_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
	token := waitMsg(t, owner.c, msgs).token
	hd := h.hud(t, ports.LayerTop, 0)
	hd.attach(t, token)
	for i := 1; i < ports.MaxCaptureSessionLayers; i++ {
		hd.attachSurfaceNew(t, token)
	}
	over := hd.attachSurfaceNew(t, token)
	expectAttachment(t, hd.c, over, capturesession.NeferwlCaptureLayerV1EventFailed, int64(capturesession.NeferwlCaptureLayerV1FailureTooManyLayers))
	n := 0
	for len(h.events) > 0 {
		if l, ok := (<-h.events).(ports.CaptureSessionLayer); ok && l.Attached {
			n++
		}
	}
	if n != ports.MaxCaptureSessionLayers {
		t.Fatalf("%d layers reached core, want %d", n, ports.MaxCaptureSessionLayers)
	}
}

func TestPrivateCaptureWorkspaceInventory(t *testing.T) {
	h := newPrivateHarness(t)
	pc := h.client(t)
	h.workspaces <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 1<<32 | 3, Name: "dev", Active: true, Frame: ports.Rect{X: 5, Y: 6, W: 7, H: 8}}, {ID: 4, Name: "2"}}}}}
	seen := map[uint64]sessionMsg{}
	for len(seen) < 2 {
		m := waitMsg(t, pc.c, pc.mgr.msgs)
		if uint32(m.op) != capturesession.NeferwlCaptureManagerV1EventWorkspace {
			t.Fatalf("event %+v", m)
		}
		seen[uint64(m.numbers[0])<<32|uint64(m.numbers[1])] = m
	}
	if m := seen[1<<32|3]; m.name != "dev" || m.numbers[2] != 5 || m.numbers[3] != 6 || m.numbers[4] != 7 || m.numbers[5] != 8 || m.numbers[6] != 1 {
		t.Fatalf("workspace %+v", m)
	}
	h.workspaces <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 4, Name: "2"}}}}}
	if m := waitMsg(t, pc.c, pc.mgr.msgs); uint32(m.op) != capturesession.NeferwlCaptureManagerV1EventWorkspaceRemoved || m.numbers[0] != 1 || m.numbers[1] != 3 {
		t.Fatalf("removed %+v", m)
	}
}

// captureOnce asks a wlr capture of the whole 2x2 output on client a.
func captureOnce(t *testing.T, h *privateHarness, pc *privateClient) (*captureEvents, uint32) {
	t.Helper()
	_, buf, fd := captureSmallBuffer(t, pc.c)
	t.Cleanup(func() { _ = unix.Close(fd) })
	mgr := bindVersion(t, pc.c, "zwlr_screencopy_manager_v1", 3)
	frame := pc.c.AllocateID()
	ev := &captureEvents{events: make(chan uint16, 16)}
	ev.SetID(frame)
	registerWireProxy(pc.c, ev)
	requestProtocol(t, pc.c, mgr, wlr.ZwlrScreencopyManagerV1RequestCaptureOutput, frame, int32(0), pc.output)
	requestProtocol(t, pc.c, frame, wlr.ZwlrScreencopyFrameV1RequestCopy, buf)
	if err := pc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return ev, frame
}

// The owner's captures are tagged clean and fenced by the confirmed
// revision; before confirmation or while paused they are refused, not served
// with the HUD in them. Every other connection is untouched.
func TestPrivateCaptureCleanTagging(t *testing.T) {
	h := newPrivateHarness(t)
	owner, other := h.client(t), h.client(t)
	_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)

	// Unconfirmed: the owner's capture fails and never reaches the renderer.
	ev, _ := captureOnce(t, h, owner)
	if !eventsInclude(ev, uint16(wlr.ZwlrScreencopyFrameV1EventFailed)) {
		t.Fatal("the capture of an unconfirmed session did not fail")
	}
	select {
	case r := <-h.captures:
		_ = r.Dst.File.Close()
		t.Fatalf("an unconfirmed session's capture reached the renderer: %+v", r)
	default:
	}

	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 9})
	waitMsg(t, owner.c, msgs)
	captureOnce(t, h, owner)
	r := receive1(t, h.captures)
	if !r.Clean || r.Session != begin.ID || r.CaptureRevision != 9 {
		t.Fatalf("owner capture %+v", r)
	}
	_ = r.Dst.File.Close()
	h.captured <- ports.CaptureDone{ID: r.ID, Output: r.Output, Time: time.Now()}

	captureOnce(t, h, other)
	r = receive1(t, h.captures)
	if r.Clean || r.Session != 0 || r.CaptureRevision != 0 {
		t.Fatalf("another connection's capture tagged: %+v", r)
	}
	_ = r.Dst.File.Close()
	h.captured <- ports.CaptureDone{ID: r.ID, Output: r.Output, Time: time.Now()}

	// Paused: the owner is refused again.
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: false, Revision: 10})
	waitMsg(t, owner.c, msgs)
	ev, _ = captureOnce(t, h, owner)
	if !eventsInclude(ev, uint16(wlr.ZwlrScreencopyFrameV1EventFailed)) {
		t.Fatal("the capture of a paused session did not fail")
	}
	select {
	case r := <-h.captures:
		_ = r.Dst.File.Close()
		t.Fatalf("a paused session's capture reached the renderer: %+v", r)
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

func receive1(t *testing.T, ch chan ports.CaptureRequest) ports.CaptureRequest {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no capture request")
		return ports.CaptureRequest{}
	}
}

// After the session ended and until the owner destroys the object, its
// captures of the output are refused, never served unfiltered; another
// connection is unaffected.
func TestPrivateCaptureOwnerRefusedAfterStop(t *testing.T) {
	h := newPrivateHarness(t)
	owner, other := h.client(t), h.client(t)
	sid, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
	waitMsg(t, owner.c, msgs)
	h.send(ports.CaptureSessionState{ID: begin.ID, Reason: ports.CaptureReasonOutputOff})
	if m := waitMsg(t, owner.c, msgs); uint32(m.op) != capturesession.NeferwlCaptureSessionV1EventStopped {
		t.Fatalf("stopped: %+v", m)
	}
	ev, _ := captureOnce(t, h, owner)
	if !eventsInclude(ev, uint16(wlr.ZwlrScreencopyFrameV1EventFailed)) {
		t.Fatal("the owner's capture after the stop did not fail")
	}
	select {
	case r := <-h.captures:
		_ = r.Dst.File.Close()
		t.Fatalf("a stopped session's capture reached the renderer: %+v", r)
	default:
	}
	captureOnce(t, h, other)
	r := receive1(t, h.captures)
	if r.Clean {
		t.Fatalf("another connection's capture is clean: %+v", r)
	}
	_ = r.Dst.File.Close()
	h.captured <- ports.CaptureDone{ID: r.ID, Output: r.Output, Time: time.Now()}
	// Destroying the object lifts the refusal.
	requestProtocol(t, owner.c, sid, capturesession.NeferwlCaptureSessionV1RequestDestroy)
	if err := owner.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	captureOnce(t, h, owner)
	r = receive1(t, h.captures)
	if r.Clean {
		t.Fatalf("capture after destroy: %+v", r)
	}
	_ = r.Dst.File.Close()
}

// An attached layer that commits a layer below top is closed and detached
// once, and its unmap reaches core before the detach.
func TestPrivateCaptureLayerMovedBelowTop(t *testing.T) {
	h := newPrivateHarness(t)
	owner := h.client(t)
	_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
	token := waitMsg(t, owner.c, msgs).token
	hd := h.hudVersion(t, ports.LayerTop, 0, 2)
	att := hd.attach(t, token)
	layer := nextEvent[ports.CaptureSessionLayer](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 2, Layers: []ports.WindowID{layer.Layer}})
	expectAttachment(t, hd.c, att, capturesession.NeferwlCaptureLayerV1EventAttached)
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
	expectAttachment(t, hd.c, att, capturesession.NeferwlCaptureLayerV1EventDetached, int64(capturesession.NeferwlCaptureLayerV1DetachReasonLayerDestroyed))
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
		case ports.CaptureSessionLayer:
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
func TestPrivateCaptureLayerKeyboardNone(t *testing.T) {
	h := newPrivateHarness(t)
	owner := h.client(t)
	_, msgs := owner.begin(t, 0, 0, 0, 0, 0, 0)
	begin := nextEvent[ports.CaptureSessionBegin](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 1})
	token := waitMsg(t, owner.c, msgs).token
	hd := h.hud(t, ports.LayerOverlay, 2) // on demand before the attach
	att := hd.attach(t, token)
	layer := nextEvent[ports.CaptureSessionLayer](t, h.events)
	h.send(ports.CaptureSessionState{ID: begin.ID, Output: "HEADLESS-1", Active: true, Revision: 2, Layers: []ports.WindowID{layer.Layer}})
	expectAttachment(t, hd.c, att, capturesession.NeferwlCaptureLayerV1EventAttached)
	requestProtocol(t, hd.c, hd.layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetKeyboardInteractivity, uint32(2)) // and after
	mapLayer(t, hd)
	l := nextEvent[ports.LayerChanged](t, h.events)
	if len(l.Layers) != 1 || l.Layers[0].Keyboard != 0 {
		t.Fatalf("layer reported with keyboard: %+v", l.Layers)
	}
}
