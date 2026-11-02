package wayland

import (
	"testing"
	"time"

	"bnema.dev/wlturbo"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// releaseOrderProxy records protocol events in their client dispatch order.
// Both wl_buffer.release and wl_callback.done have opcode zero.
type releaseOrderProxy struct {
	wlturbo.BaseProxy
	label  string
	events chan string
}

func (p *releaseOrderProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == 0 {
		p.events <- p.label
	}
}

// A completion that makes the replaced buffer safe must release it before
// inviting a two-buffer client to redraw. The current buffer stays owned by
// the output; only the report proving newer content permits the old release.
func TestFrameCompletionReleasesReplacedBufferBeforeCallback(t *testing.T) {
	s, events, commands, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, xdg := surfaceMapper(t, c, events)()
	registerProtocol(t, c, xdg)
	placeWindow(t, s, commands, w, "HEADLESS-1")
	drainContents(t, s, contents)
	wire := make(chan string, 8)
	register := func(id uint32, label string) {
		p := &releaseOrderProxy{label: label, events: wire}
		p.SetID(id)
		registerWireProxy(c, p)
	}
	first, current := shmBuffer(t, c), shmBuffer(t, c)
	register(first, "release replaced")
	register(current, "release current")
	// Prevent the fallback pacer from answering the callback before the
	// controlled completion. This is display-owned scheduling state.
	s.display.Do(func() { s.frameDue["HEADLESS-1"] = time.Now().Add(time.Hour) })
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, first, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	cb := c.AllocateID()
	register(cb, "frame done")
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, current, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestFrame, cb)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-wire:
		t.Fatalf("event before completion: %s", event)
	default:
	}
	s.display.Do(func() {
		held := false
		for _, h := range s.held {
			if h.res != nil && h.res.ID() == first {
				held = true
			}
		}
		if !held {
			t.Error("replaced buffer not held before completion")
		}
		// No scanout or child read remains: the output has completed reading
		// the current content, strictly newer than the replaced buffer's hold.
		seq := s.contentSeq[w.ID]
		report := ports.OutputPresented{Output: "HEADLESS-1", Seen: map[domain.WindowID]uint64{w.ID: seq}}
		s.paceTick(map[string]bool{"HEADLESS-1": true}, []ports.OutputPresented{report})
	})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for len(wire) > 0 {
		got = append(got, <-wire)
	}
	if len(got) != 2 || got[0] != "release replaced" || got[1] != "frame done" {
		t.Fatalf("completion wire order = %v, want [release replaced frame done] (current buffer must remain owned)", got)
	}
}
