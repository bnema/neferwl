package wayland

import (
	wl "github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
)

func TestToplevelDestroyedAfterUnmap(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	mapped, surf, xdg := surfaceMapper(t, c, events)()
	requestProtocol(t, c, surf, wl.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, surf, wl.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := clientEvent[ports.WindowUnmapped](t, events); got.ID != mapped.ID {
		t.Fatal(got)
	}
	requestProtocol(t, c, xdg+1, xdgshell.ToplevelRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := clientEvent[ports.WindowDestroyed](t, events); got.ID != mapped.ID {
		t.Fatal(got)
	}
}
