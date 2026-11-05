package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
)

// committedThroughBarrier drains the event stream through an owner-loop marker.
//
//testpolicy:realtime event forwarding from the real Wayland display owner
func committedThroughBarrier(t *testing.T, s *Server, events chan ports.ClientEvent) []ports.WindowCommitted {
	t.Helper()
	if !s.display.Do(func() { s.emit(ports.WindowTitle{Title: "commit-barrier"}) }) {
		t.Fatal("server stopped")
	}
	var got []ports.WindowCommitted
	deadline := waylandDeadline(2 * time.Second)
	for {
		select {
		case ev := <-events:
			switch v := ev.(type) {
			case ports.WindowCommitted:
				got = append(got, v)
			case ports.WindowTitle:
				if v.Title == "commit-barrier" {
					return got
				}
			}
		case <-deadline:
			t.Fatal("event barrier timeout")
		}
	}
}

func TestXDGConfigureCommitted(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	buffer := shmBufferSized(t, c, 32, 24)
	surf, xdg, serials := xdgWindow(t, c, comp, wm, buffer, func(xdg uint32) {
		top := c.AllocateID()
		registerProtocol(t, c, top)
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	})
	ackAndAttach(t, c, surf, xdg, buffer, serials)
	w := mapped(t, events, 2*time.Second)
	configure := func(serial uint64) {
		t.Helper()
		if !s.display.Do(func() { s.apply(ports.ConfigureWindow{ID: w.ID, Serial: serial, Width: 32, Height: 24, Visible: true}) }) {
			t.Fatal("server stopped")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() {
		t.Helper()
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	configure(10)
	commit()
	if got := committedThroughBarrier(t, s, events); len(got) != 0 {
		t.Fatalf("without ack: %+v", got)
	}
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	commit()
	got := committedThroughBarrier(t, s, events)
	if len(got) != 1 || got[0] != (ports.WindowCommitted{ID: w.ID, Serial: 10, Width: 32, Height: 24}) {
		t.Fatalf("acked commit: %+v", got)
	}
	commit()
	if got := committedThroughBarrier(t, s, events); len(got) != 0 {
		t.Fatalf("duplicate: %+v", got)
	}
	// Several configures may be acknowledged before one commit: only the newest
	// acknowledged core identity belongs to that applied commit.
	configure(11)
	configure(12)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	commit()
	got = committedThroughBarrier(t, s, events)
	if len(got) != 1 || got[0].Serial != 12 {
		t.Fatalf("coalesced acks: %+v", got)
	}
}

func TestX11ConfigureCommitted(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	r.x11Window(win)
	surf := r.pair(win, x11Serial)
	mapped := awaitEvent[ports.WindowMapped](t, r.events)
	buffer := shmBufferSized(t, r.xc, 32, 24)
	configure := func(serial uint64, width int) {
		t.Helper()
		height := 24
		if width == 0 {
			height = 0
		}
		if !r.s.display.Do(func() {
			r.s.apply(ports.ConfigureWindow{ID: mapped.ID, Serial: serial, Width: width, Height: height, Visible: true, Floating: width == 0})
		}) {
			t.Fatal("server stopped")
		}
	}
	commit := func(attach bool) {
		t.Helper()
		if attach {
			requestProtocol(t, r.xc, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
		}
		requestProtocol(t, r.xc, surf, wayland.SurfaceRequestCommit)
		if err := r.xc.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	configure(20, 33)
	commit(true)
	if got := committedThroughBarrier(t, r.s, r.events); len(got) != 0 {
		t.Fatalf("wrong size: %+v", got)
	}
	configure(21, 32)
	commit(false)
	got := committedThroughBarrier(t, r.s, r.events)
	if len(got) != 1 || got[0] != (ports.WindowCommitted{ID: mapped.ID, Serial: 21, Width: 32, Height: 24}) {
		t.Fatalf("matching commit: %+v", got)
	}
	commit(false)
	if got := committedThroughBarrier(t, r.s, r.events); len(got) != 0 {
		t.Fatalf("duplicate: %+v", got)
	}
	configure(22, 32)
	commit(false)
	got = committedThroughBarrier(t, r.s, r.events)
	if len(got) != 1 || got[0].Serial != 22 {
		t.Fatalf("same-size configure: %+v", got)
	}
	// A non-imposed float gets 0x0 and keeps the size it draws: its next
	// commit answers the configure.
	configure(23, 0)
	commit(false)
	got = committedThroughBarrier(t, r.s, r.events)
	if len(got) != 1 || got[0] != (ports.WindowCommitted{ID: mapped.ID, Serial: 23, Width: 32, Height: 24}) {
		t.Fatalf("unsized float configure: %+v", got)
	}
}
