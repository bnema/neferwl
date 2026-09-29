package wayland

import (
	"math"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/alphamodifier"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

// contentMatching waits for a content of the window that matches want.
func contentMatching(t *testing.T, contents <-chan ports.SurfaceContent, id ports.WindowID, want func(ports.SurfaceContent) bool) ports.SurfaceContent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-contents:
			if c.ID == id && want(c) {
				return c
			}
		case <-deadline:
			t.Fatal("no matching content")
		}
	}
}

// The multiplier is double-buffered, fades the content and makes it
// translucent; destroying the modifier restores the surface on commit.
func TestAlphaModifier(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	region := c.AllocateID()
	registerProtocol(t, c, region)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateRegion, region)
	requestProtocol(t, c, region, wayland.RegionRequestAdd, int32(0), int32(0), int32(1), int32(1))
	requestProtocol(t, c, surf, wayland.SurfaceRequestSetOpaqueRegion, region)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	contentMatching(t, contents, w.ID, func(c ports.SurfaceContent) bool { return c.Opaque })

	manager := bindProtocol(t, c, "wp_alpha_modifier_v1")
	mod := c.AllocateID()
	registerProtocol(t, c, mod)
	requestProtocol(t, c, manager, alphamodifier.WpAlphaModifierV1RequestGetSurface, mod, surf)
	requestProtocol(t, c, mod, alphamodifier.WpAlphaModifierSurfaceV1RequestSetMultiplier, uint32(math.MaxUint32/4))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	got := contentMatching(t, contents, w.ID, func(c ports.SurfaceContent) bool { return c.Fade > 0 })
	if got.Opaque || math.Abs(float64(got.Fade)-0.75) > 1e-6 {
		t.Fatalf("faded content %+v", got)
	}

	requestProtocol(t, c, mod, alphamodifier.WpAlphaModifierSurfaceV1RequestDestroy)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	got = contentMatching(t, contents, w.ID, func(c ports.SurfaceContent) bool { return c.Fade == 0 })
	if !got.Opaque {
		t.Fatalf("restored content %+v", got)
	}

	// A second modifier on the same surface is a protocol error.
	mod2, mod3 := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, mod2)
	registerProtocol(t, c, mod3)
	requestProtocol(t, c, manager, alphamodifier.WpAlphaModifierV1RequestGetSurface, mod2, surf)
	requestProtocol(t, c, manager, alphamodifier.WpAlphaModifierV1RequestGetSurface, mod3, surf)
	if err := c.Roundtrip(); err == nil {
		t.Fatal("second modifier accepted")
	}
}

func TestFade(t *testing.T) {
	for factor, want := range map[uint32]float32{0: 1, math.MaxUint32: 0} {
		if got := fade(factor); got != want {
			t.Fatalf("fade(%d) = %v, want %v", factor, got, want)
		}
	}
}
