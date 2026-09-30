package wayland

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/fractionalscale"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/wlturbo"
)

// layerScaleOrderProxy is a real protocol client recording fractional-scale
// and layer events in their wire order across the two protocol objects.
type layerScaleOrderProxy struct {
	wlturbo.BaseProxy
	layer  bool
	events chan [3]uint32
}

func (p *layerScaleOrderProxy) Dispatch(e *wlturbo.Event) {
	if !p.layer {
		p.events <- [3]uint32{0, e.Uint32()}
		return
	}
	switch e.Opcode {
	case uint16(wlrlayershell.ZwlrLayerSurfaceV1EventConfigure):
		_ = e.Uint32() // serial
		p.events <- [3]uint32{1, e.Uint32(), e.Uint32()}
	case uint16(wlrlayershell.ZwlrLayerSurfaceV1EventClosed):
		p.events <- [3]uint32{2}
	}
}

func TestLayerTargetScaleBeforeConfigure(t *testing.T) {
	for _, target := range []string{"fractional", "focused", "inferred", "removed"} {
		t.Run(target, func(t *testing.T) {
			s, _, commands, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			fractional := ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-2", Width: 900, Height: 600}, X: 1920, Width: 750, Height: 500, Scale: 1.2}
			commands <- ports.SetOutputs{Outputs: ports.Layout{testOutputs[0], fractional}, Focused: "HEADLESS-1"}
			waitOutputs(t, c, 2)
			var first, last uint32
			for n, g := range c.Registry().GetGlobals() {
				if g.Interface == "wl_output" {
					if first == 0 || n < first {
						first = n
					}
					last = max(last, n)
				}
			}
			var out uint32
			if target != "inferred" {
				name := last
				if target == "focused" {
					name = first
				}
				var err error
				out, err = c.Registry().BindID(name, "wl_output", 4)
				if err != nil {
					t.Fatal(err)
				}
				registerProtocol(t, c, out)
			}
			if target == "removed" {
				commands <- ports.SetOutputs{Outputs: testOutputs, Focused: "HEADLESS-1"}
				waitOutputs(t, c, 1)
			}
			comp := bindVersion(t, c, "wl_compositor", 6)
			shell := bindProtocol(t, c, "zwlr_layer_shell_v1")
			fm := bindProtocol(t, c, "wp_fractional_scale_manager_v1")
			surf := c.AllocateID()
			integers := make(chan int32, 4)
			sp := &surfaceScaleProxy{scales: integers}
			sp.SetID(surf)
			c.Context().Register(sp)
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
			frac := c.AllocateID()
			events := make(chan [3]uint32, 8)
			fp := &layerScaleOrderProxy{events: events}
			fp.SetID(frac)
			c.Context().Register(fp)
			requestProtocol(t, c, fm, fractionalscale.WpFractionalScaleManagerV1RequestGetFractionalScale, frac, surf)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || <-events != [3]uint32{0, 120} {
				t.Fatal("surface must initially use focused output scale 120")
			}
			if len(integers) != 1 || <-integers != 1 {
				t.Fatal("initial integer scale must be 1")
			}
			layer := c.AllocateID()
			lp := &layerScaleOrderProxy{layer: true, events: events}
			lp.SetID(layer)
			c.Context().Register(lp)
			requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, layer, surf, out, uint32(ports.LayerOverlay), "scale-test")
			requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetAnchor, uint32(15))
			requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(0), uint32(0))
			requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			want := [][3]uint32{{1, 1920, 1080}}
			switch target {
			case "fractional":
				want = [][3]uint32{{0, 144}, {1, 750, 500}}
				if len(integers) != 1 || <-integers != 2 {
					t.Fatal("target integer scale must update to 2")
				}
			case "removed":
				want = [][3]uint32{{2}}
			}
			if len(events) != len(want) {
				t.Fatalf("events: %d, want %v", len(events), want)
			}
			for _, expected := range want {
				if got := <-events; got != expected {
					t.Fatalf("event %v, want %v", got, expected)
				}
			}
			if len(integers) != 0 {
				t.Fatal("unexpected integer scale notification")
			}
		})
	}
}
