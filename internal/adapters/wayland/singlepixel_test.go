package wayland

import (
	"math"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/singlepixelbuffer"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
)

// A single-pixel buffer reaches the renderer as a 1x1 solid color, opaque
// only when its alpha is full.
func TestSinglePixelBuffer(t *testing.T) {
	s, events, contents, dir := dmabufServer(t, ports.DMABufSupport{})
	c := protocolClient(t, s, dir)
	g, ok := c.Registry().FindGlobal("wp_single_pixel_buffer_manager_v1")
	if !ok || g.Version != 1 {
		t.Fatalf("global %+v", g)
	}
	mgr, err := bindWireID(c, g.Name, g.Interface, 1)
	if err != nil {
		t.Fatal(err)
	}
	w, surf, _ := surfaceMapper(t, c, events)()
	solid := func(alpha uint32, red uint32) ports.SurfaceContent {
		t.Helper()
		buf := c.AllocateID()
		registerProtocol(t, c, buf)
		requestProtocol(t, c, mgr, singlepixelbuffer.WpSinglePixelBufferManagerV1RequestCreateU32RgbaBuffer, buf, red, uint32(0), uint32(0), alpha)
		requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		timeout := time.After(2 * time.Second)
		for {
			select {
			case got := <-contents:
				if got.ID == w.ID && got.Solid != nil {
					return got
				}
			case <-timeout:
				t.Fatal("no solid content")
			}
		}
	}
	got := solid(math.MaxUint32, 0)
	if got.Width != 1 || got.Height != 1 || !got.Opaque || *got.Solid != (ports.SolidColor{A: 1}) {
		t.Fatalf("opaque black = %+v solid %+v", got, got.Solid)
	}
	got = solid(math.MaxUint32/2, math.MaxUint32/2)
	if got.Width != 1 || got.Height != 1 || got.Opaque {
		t.Fatalf("translucent = %+v", got)
	}
	if want := float32(0.5); got.Solid.R != want || got.Solid.A != want || got.Solid.G != 0 || got.Solid.B != 0 {
		t.Fatalf("translucent color = %+v", got.Solid)
	}
}
