package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/tearingcontrol"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// asyncContent waits for a content of window id with the given hint.
func asyncContent(t *testing.T, contents <-chan ports.SurfaceContent, id ports.WindowID, async bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-contents:
			if c.ID == id && c.Async == async {
				return
			}
		case <-deadline:
			t.Fatalf("no content with async=%v", async)
		}
	}
}

func TestTearingHintAppliesOnCommit(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "wp_tearing_control_manager_v1")
	control := c.AllocateID()
	registerProtocol(t, c, control)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, control, surf)
	requestProtocol(t, c, control, tearingcontrol.WpTearingControlV1RequestSetPresentationHint, uint32(tearingcontrol.WpTearingControlV1PresentationHintAsync))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	asyncContent(t, contents, w.ID, true)

	// Destroying the control goes back to vsync on the next commit.
	requestProtocol(t, c, control, tearingcontrol.WpTearingControlV1RequestDestroy)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	asyncContent(t, contents, w.ID, false)

	// A surface has at most one control.
	first, second := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, first)
	registerProtocol(t, c, second)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, first, surf)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, second, surf)
	expectProtocolError(t, c, manager, uint32(tearingcontrol.WpTearingControlManagerV1ErrorTearingControlExists))
}
