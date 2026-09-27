package wayland

import (
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrforeigntoplevel"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
)

// toplevelManagerEvents registers a handle proxy for each announced toplevel.
type toplevelManagerEvents struct {
	wlturbo.BaseProxy
	client  *wlturbo.Display
	handles chan uint32
	events  chan string
}

func (p *toplevelManagerEvents) Dispatch(e *wlturbo.Event) {
	if e.Opcode != uint16(wlr.ZwlrForeignToplevelManagerV1EventToplevel) {
		return
	}
	id := e.Uint32()
	h := &toplevelHandleEvents{events: p.events}
	h.SetID(id)
	p.client.Context().Register(h)
	p.handles <- id
}

// toplevelHandleEvents records handle events as text.
type toplevelHandleEvents struct {
	wlturbo.BaseProxy
	events chan string
}

func (p *toplevelHandleEvents) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case wlr.ZwlrForeignToplevelHandleV1EventTitle:
		p.events <- "title " + e.String()
	case wlr.ZwlrForeignToplevelHandleV1EventAppId:
		p.events <- "app_id " + e.String()
	case wlr.ZwlrForeignToplevelHandleV1EventOutputEnter:
		p.events <- "output_enter"
	case wlr.ZwlrForeignToplevelHandleV1EventOutputLeave:
		p.events <- "output_leave"
	case wlr.ZwlrForeignToplevelHandleV1EventState:
		var states []uint32
		for b := e.Array(); len(b) >= 4; b = b[4:] {
			states = append(states, binary.NativeEndian.Uint32(b))
		}
		p.events <- fmt.Sprint("state ", states)
	case wlr.ZwlrForeignToplevelHandleV1EventDone:
		p.events <- "done"
	case wlr.ZwlrForeignToplevelHandleV1EventClosed:
		p.events <- "closed"
	}
}

// takeEvents returns the handle events received up to the next done or
// closed, after a roundtrip.
func takeEvents(t *testing.T, c *wlturbo.Display, events chan string) []string {
	t.Helper()
	var got []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(events) > 0 {
			ev := <-events
			got = append(got, ev)
			if ev == "done" || ev == "closed" {
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no done, got %q", got)
	return nil
}

func TestForeignToplevel(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	out := bindVersion(t, c, "wl_output", 4)
	registerProtocol(t, c, out)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	mapWindow := surfaceMapper(t, c, events)
	w, surf, xdg := mapWindow()
	// Core configures below are not acked: drop them.
	registerProtocol(t, c, xdg)
	// surfaceMapper allocates the wl_surface, xdg_surface and xdg_toplevel in order.
	top := xdg + 1
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetTitle, "editor")
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetAppId, "app.editor")

	// A window mapped before the bind is announced.
	manager := bindVersion(t, c, "zwlr_foreign_toplevel_manager_v1", 3)
	p := &toplevelManagerEvents{client: c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	p.SetID(manager)
	c.Context().Register(p)
	if got, want := takeEvents(t, c, p.events), []string{"title editor", "app_id app.editor", "state []", "done"}; !slices.Equal(got, want) {
		t.Fatalf("announce = %q, want %q", got, want)
	}
	handle := <-p.handles

	// Core configures it fullscreen and focused on the output.
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Activated: true, Output: "HEADLESS-1"}
	want := []string{fmt.Sprint("state ", []uint32{uint32(wlr.ZwlrForeignToplevelHandleV1StateActivated), uint32(wlr.ZwlrForeignToplevelHandleV1StateFullscreen)}), "output_enter", "done"}
	if got := takeEvents(t, c, p.events); !slices.Equal(got, want) {
		t.Fatalf("fullscreen = %q, want %q", got, want)
	}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 960, Height: 1080, Output: "HEADLESS-1"}
	want = []string{fmt.Sprint("state ", []uint32{uint32(wlr.ZwlrForeignToplevelHandleV1StateMaximized)}), "done"}
	if got := takeEvents(t, c, p.events); !slices.Equal(got, want) {
		t.Fatalf("tiled = %q, want %q", got, want)
	}
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetTitle, "editor - file")
	if got, want := takeEvents(t, c, p.events), []string{"title editor - file", "done"}; !slices.Equal(got, want) {
		t.Fatalf("title = %q, want %q", got, want)
	}

	// Requests reach core as the xdg-shell and xdg-activation messages.
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestSetFullscreen, uint32(0))
	if ev := clientEvent[ports.WindowFullscreenRequest](t, events); ev != (ports.WindowFullscreenRequest{ID: w.ID, Fullscreen: true}) {
		t.Fatalf("set_fullscreen = %#v", ev)
	}
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestUnsetFullscreen)
	if ev := clientEvent[ports.WindowFullscreenRequest](t, events); ev != (ports.WindowFullscreenRequest{ID: w.ID}) {
		t.Fatalf("unset_fullscreen = %#v", ev)
	}
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestActivate, seat)
	if ev := clientEvent[ports.WindowActivate](t, events); ev.ID != w.ID {
		t.Fatalf("activate = %#v", ev)
	}

	// Unmapping closes the handle; requests on it are then ignored.
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if got, want := takeEvents(t, c, p.events), []string{"closed"}; !slices.Equal(got, want) {
		t.Fatalf("unmap = %q, want %q", got, want)
	}
	unmapped(t, events, w.ID)
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestActivate, seat)
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("event after close: %#v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	// A window mapped after the bind is announced too.
	mapWindow()
	if got := takeEvents(t, c, p.events); len(got) == 0 || got[len(got)-1] != "done" {
		t.Fatalf("new window = %q", got)
	}
}

func TestForeignToplevelInvalidRectangle(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	_, surf, _ := surfaceMapper(t, c, events)()
	manager := bindVersion(t, c, "zwlr_foreign_toplevel_manager_v1", 3)
	p := &toplevelManagerEvents{client: c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	p.SetID(manager)
	c.Context().Register(p)
	takeEvents(t, c, p.events)
	handle := <-p.handles
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestSetRectangle, surf, int32(0), int32(0), int32(-1), int32(1))
	expectProtocolError(t, c, handle, uint32(wlr.ZwlrForeignToplevelHandleV1ErrorInvalidRectangle))
}
