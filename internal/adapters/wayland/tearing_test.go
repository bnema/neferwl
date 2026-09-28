package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
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

// A subsurface carries its own hint in the window's content, even when a
// commit changes only the hint after the tree was cached (Wine presents
// the game in a subsurface that the output scans out).
func TestTearingHintOnSubsurface(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	childAsync := func(want bool) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case got := <-contents:
				if got.ID == w.ID && len(got.Children) == 1 && got.Children[0].Async == want {
					if got.Async {
						t.Fatal("child hint leaked to the root")
					}
					return
				}
			case <-deadline:
				t.Fatalf("no content with child async=%v", want)
			}
		}
	}
	childAsync(false)

	manager := bindProtocol(t, c, "wp_tearing_control_manager_v1")
	control := c.AllocateID()
	registerProtocol(t, c, control)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, control, child)
	requestProtocol(t, c, control, tearingcontrol.WpTearingControlV1RequestSetPresentationHint, uint32(tearingcontrol.WpTearingControlV1PresentationHintAsync))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	childAsync(true)
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
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// Double-buffered: nothing changes before the commit.
	select {
	case got := <-contents:
		if got.Async {
			t.Fatal("hint applied before commit")
		}
	case <-time.After(50 * time.Millisecond):
	}
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

	// A control outliving its surface is inert.
	inert := c.AllocateID()
	registerProtocol(t, c, inert)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, inert, surf)
	requestProtocol(t, c, surf, wayland.SurfaceRequestDestroy)
	requestProtocol(t, c, inert, tearingcontrol.WpTearingControlV1RequestSetPresentationHint, uint32(tearingcontrol.WpTearingControlV1PresentationHintAsync))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	unmapped(t, events, w.ID)

	// A surface has at most one control.
	_, other, _ := surfaceMapper(t, c, events)()
	first, second := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, first)
	registerProtocol(t, c, second)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, first, other)
	requestProtocol(t, c, manager, tearingcontrol.WpTearingControlManagerV1RequestGetTearingControl, second, other)
	expectProtocolError(t, c, manager, uint32(tearingcontrol.WpTearingControlManagerV1ErrorTearingControlExists))
}
