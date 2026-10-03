package wayland

import (
	"slices"
	"testing"

	ext "github.com/bnema/go-wayland-bindings/server/extforeigntoplevellist"
	source "github.com/bnema/go-wayland-bindings/server/extimagecapturesource"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	xdgshell "github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

// extListEvents registers a handle proxy for each announced toplevel.
type extListEvents struct {
	wlturbo.BaseProxy
	client  *wlturbo.Display
	handles chan uint32
	events  chan string
}

func (p *extListEvents) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case ext.ExtForeignToplevelListV1EventToplevel:
		id := e.Uint32()
		h := &extHandleEvents{events: p.events}
		h.SetID(id)
		registerWireProxy(p.client, h)
		p.handles <- id
	case ext.ExtForeignToplevelListV1EventFinished:
		p.events <- "finished"
	}
}

// extHandleEvents records handle events as text.
type extHandleEvents struct {
	wlturbo.BaseProxy
	events chan string
}

func (p *extHandleEvents) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case ext.ExtForeignToplevelHandleV1EventIdentifier:
		p.events <- "identifier " + e.String()
	case ext.ExtForeignToplevelHandleV1EventTitle:
		p.events <- "title " + e.String()
	case ext.ExtForeignToplevelHandleV1EventAppId:
		p.events <- "app_id " + e.String()
	case ext.ExtForeignToplevelHandleV1EventDone:
		p.events <- "done"
	case ext.ExtForeignToplevelHandleV1EventClosed:
		p.events <- "closed"
	}
}

// The ext list announces mapped windows with a stable identifier, sends
// changes, and closed when a window unmaps.
func TestExtForeignToplevelList(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	w, _, xdg := surfaceMapper(t, c, events)()
	registerProtocol(t, c, xdg)
	top := xdg + 1
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetTitle, "editor")
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetAppId, "app.editor")

	list := bindProtocol(t, c, "ext_foreign_toplevel_list_v1")
	p := &extListEvents{client: c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	p.SetID(list)
	registerWireProxy(c, p)
	want := []string{"identifier " + toplevelIdentifier(w.ID, 1), "title editor", "app_id app.editor", "done"}
	if got := takeEvents(t, c, p.events); !slices.Equal(got, want) {
		t.Fatalf("announce = %q, want %q", got, want)
	}
	<-p.handles
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetTitle, "editor - file")
	if got, want := takeEvents(t, c, p.events), []string{"title editor - file", "done"}; !slices.Equal(got, want) {
		t.Fatalf("title = %q, want %q", got, want)
	}
	requestProtocol(t, c, top, xdgshell.ToplevelRequestDestroy)
	if got := takeEvents(t, c, p.events); !slices.Equal(got, []string{"closed"}) {
		t.Fatalf("close = %q", got)
	}
	requestProtocol(t, c, list, ext.ExtForeignToplevelListV1RequestStop)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if ev := <-p.events; ev != "finished" {
		t.Fatalf("stop = %q", ev)
	}
}

// A handle of the list makes a window capture source: its session opens on
// the window in core, its frames ask for that window off screen, and the
// session stops when the window goes.
func TestWindowCaptureSource(t *testing.T) {
	h := newCaptureHarness(t)
	cc := h.client(t)
	win, surf, xdg := surfaceMapper(t, cc.c, h.events)()
	registerProtocol(t, cc.c, xdg)
	h.send(ports.ConfigureWindow{ID: win.ID, Width: 3, Height: 2, Output: "HEADLESS-1", Visible: true})
	h.settle(t, cc)

	list := bindProtocol(t, cc.c, "ext_foreign_toplevel_list_v1")
	p := &extListEvents{client: cc.c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	p.SetID(list)
	registerWireProxy(cc.c, p)
	takeEvents(t, cc.c, p.events)
	handle := <-p.handles

	mgr := bindProtocol(t, cc.c, "ext_foreign_toplevel_image_capture_source_manager_v1")
	src := cc.c.AllocateID()
	registerProtocol(t, cc.c, src)
	requestProtocol(t, cc.c, mgr, source.ExtForeignToplevelImageCaptureSourceManagerV1RequestCreateSource, src, handle)
	session, ev := cc.session(t, src)
	if w, hh := constraints(t, cc, ev); w != 3 || hh != 2 {
		t.Fatalf("buffer %dx%d, want the window's 3x2", w, hh)
	}
	open := h.open(t)
	if open.Window != win.ID || open.Workspace != 0 || open.Output != "" {
		t.Fatalf("open %+v", open)
	}
	h.send(ports.CaptureSessionState{ID: open.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 3, H: 2}, Hidden: true, Active: true})
	h.settle(t, cc)
	cc.extFrame(t, session, 3, 2)
	r := receiveCapture(t, h.captures)
	if r.Window != win.ID || !r.OffScreen || r.Region.Dx() != 3 || r.Region.Dy() != 2 {
		t.Fatalf("request %+v", r)
	}

	// The window unmaps: the session stops at once.
	requestProtocol(t, cc.c, surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, cc.c, surf, wayland.SurfaceRequestCommit)
	for {
		if got := nextSession(t, cc, ev); got[0] == evStopped {
			break
		}
	}
	if ev := clientEvent[ports.WindowUnmapped](t, h.events); ev.ID != win.ID {
		t.Fatalf("unmapped %d, want %d", ev.ID, win.ID)
	}

	// The remap is a new toplevel: a new identifier, and the source of the
	// closed handle is dead.
	serials := make(chan uint32, 8)
	xp := &configureProxy{serial: serials}
	xp.SetID(xdg)
	registerWireProxy(cc.c, xp)
	requestProtocol(t, cc.c, surf, wayland.SurfaceRequestCommit)
	ackAndAttach(t, cc.c, surf, xdg, shmBuffer(t, cc.c), serials)
	clientEvent[ports.WindowMapped](t, h.events)
	got := takeEvents(t, cc.c, p.events)
	if slices.Equal(got, []string{"closed"}) {
		got = takeEvents(t, cc.c, p.events)
	}
	if !slices.Contains(got, "identifier "+toplevelIdentifier(win.ID, 2)) {
		t.Fatalf("remap = %q", got)
	}
	_, ev = cc.session(t, src)
	if got := nextSession(t, cc, ev); got[0] != evStopped {
		t.Fatalf("session on a stale source: %v", got)
	}
}
