package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/wlturbo"
)

var second = ports.OutputPlacement{Info: ports.OutputInfo{Name: "HEADLESS-2", Width: 1280, Height: 1024}, X: 1920, Width: 1280, Height: 1024, Scale: 1}

// outputCount counts wl_output globals the client sees.
func outputCount(c *wlturbo.Display) int {
	n := 0
	for _, g := range c.Registry().GetGlobals() {
		if g.Interface == "wl_output" {
			n++
		}
	}
	return n
}

// waitOutputs round-trips until the client sees n wl_output globals.
func waitOutputs(t *testing.T, c *wlturbo.Display, n int) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if outputCount(c) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wl_output globals: %d, want %d", outputCount(c), n)
}

type closedProxy struct {
	wlturbo.BaseProxy
	configured chan [3]uint32
	closed     chan struct{}
}

func (p *closedProxy) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wlrlayershell.ZwlrLayerSurfaceV1EventConfigure):
		p.configured <- [3]uint32{e.Uint32(), e.Uint32(), e.Uint32()}
	case uint16(wlrlayershell.ZwlrLayerSurfaceV1EventClosed):
		close(p.closed)
	}
}

func TestOutputHotplugAndLayerOutput(t *testing.T) {
	s, _, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	if outputCount(c) != 1 {
		t.Fatal(outputCount(c))
	}
	commands <- ports.SetOutputs{Outputs: ports.Layout{testOutputs[0], second}, Focused: "HEADLESS-1"}
	waitOutputs(t, c, 2)
	// A layer surface on the second output is sized from it.
	var name uint32
	for n, g := range c.Registry().GetGlobals() {
		if g.Interface == "wl_output" && (name == 0 || n > name) {
			name = n
		}
	}
	out, err := c.Registry().BindID(name, "wl_output", 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	comp := bindProtocol(t, c, "wl_compositor")
	shell := bindProtocol(t, c, "zwlr_layer_shell_v1")
	surf := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	layer := c.AllocateID()
	proxy := &closedProxy{configured: make(chan [3]uint32, 2), closed: make(chan struct{})}
	proxy.SetID(layer)
	c.Context().Register(proxy)
	requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, layer, surf, out, uint32(ports.LayerTop), "bar")
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetAnchor, uint32(13))
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(0), uint32(30))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case cfg := <-proxy.configured:
		if cfg[1] != 1280 {
			t.Fatalf("configure %v", cfg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("configure timeout")
	}
	// Unplugged: the global goes away and the layer surface is closed.
	commands <- ports.SetOutputs{Outputs: ports.Layout{testOutputs[0]}, Focused: "HEADLESS-1"}
	waitOutputs(t, c, 1)
	select {
	case <-proxy.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("layer not closed")
	}
}
