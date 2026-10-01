package wayland

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/wlrlayershell"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

type layerProxy struct {
	wlturbo.BaseProxy
	configured chan [3]uint32
}

func (p *layerProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wlrlayershell.ZwlrLayerSurfaceV1EventConfigure) {
		p.configured <- [3]uint32{e.Uint32(), e.Uint32(), e.Uint32()}
	}
}
func layerProtocol(t *testing.T, c *wlturbo.Display) (uint32, uint32) {
	t.Helper()
	comp := bindProtocol(t, c, "wl_compositor")
	shell := bindProtocol(t, c, "zwlr_layer_shell_v1")
	surf := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	layer := c.AllocateID()
	requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, layer, surf, uint32(0), uint32(ports.LayerTop), "test")
	return surf, layer
}
func TestLayerInvalidSize(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	surf, layer := layerProtocol(t, c)
	registerProtocol(t, c, layer)
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(0), uint32(30))
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetAnchor, uint32(ports.AnchorTop))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	expectProtocolError(t, c, layer, uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSize))
}
func TestLayerLifecycle(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	surf, layer := layerProtocol(t, c)
	proxy := &layerProxy{configured: make(chan [3]uint32, 2)}
	proxy.SetID(layer)
	registerWireProxy(c, proxy)
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetAnchor, uint32(13))
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(0), uint32(30))
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetExclusiveZone, int32(30))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var config [3]uint32
	select {
	case config = <-proxy.configured:
	case <-time.After(2 * time.Second):
		t.Fatal("configure timeout")
	}
	if config[1] != 1920 || config[2] != 30 {
		t.Fatalf("configure: %v", config)
	}
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestAckConfigure, config[0])
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("layer-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err = unix.Ftruncate(fd, 1920*30*4); err != nil {
		t.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err = wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(1920*30*4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(1920), int32(30), int32(1920*4), uint32(wayland.ShmFormatArgb8888))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err = c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		v, ok := ev.(ports.LayerChanged)
		if !ok || len(v.Layers) != 1 || v.Layers[0].Anchor != 13 || v.Layers[0].ExclusiveZone != 30 || v.Layers[0].Width != 1920 || v.Layers[0].Height != 30 {
			t.Fatalf("mapped: %#v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("map timeout")
	}
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestDestroy)
	if err = c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		v, ok := ev.(ports.LayerChanged)
		if !ok || len(v.Layers) != 0 {
			t.Fatalf("destroy: %#v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("destroy timeout")
	}
}
func TestWaybarLayer(t *testing.T) {
	tool, err := exec.LookPath("waybar")
	if err != nil {
		t.Skip("waybar missing")
	}
	s, events, _, dir := keyboardServer(t)
	home := t.TempDir()
	config := home + "/config.json"
	style := home + "/style.css"
	if err = os.WriteFile(config, []byte(`{"layer":"top","position":"top","height":30,"modules-left":["clock"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(style, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// waybar (gio) aborts without a session bus; the user bus is the default.
	bus := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if bus == "" {
		sock := os.Getenv("XDG_RUNTIME_DIR") + "/bus"
		if _, err := os.Stat(sock); err != nil {
			t.Skip("no D-Bus session bus")
		}
		bus = "unix:path=" + sock
	}
	_, done, output := lifecycleClient(t, tool, s, dir, []string{"HOME=" + home, "DBUS_SESSION_BUS_ADDRESS=" + bus}, "-c", config, "-s", style)
	select {
	case ev := <-events:
		v, ok := ev.(ports.LayerChanged)
		if !ok || len(v.Layers) != 1 || v.Layers[0].Height != 30 {
			t.Fatalf("waybar event: %#v stderr: %s", ev, output.String())
		}
	case err := <-done:
		t.Fatalf("waybar exited: %v stderr: %s", err, output.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("waybar timeout stderr: %s", output.String())
	}
}

func TestLayerAlreadyConstructed(t *testing.T) {
	for _, scenario := range []string{"role"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			comp := bindProtocol(t, c, "wl_compositor")
			shell := bindProtocol(t, c, "zwlr_layer_shell_v1")
			surf := c.AllocateID()
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
			registerProtocol(t, c, surf)
			switch scenario {
			case "role":
				sub := bindProtocol(t, c, "wl_subcompositor")
				parent := c.AllocateID()
				requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, parent)
				registerProtocol(t, c, parent)
				id := c.AllocateID()
				requestProtocol(t, c, sub, wayland.SubcompositorRequestGetSubsurface, id, surf, parent)
			}
			id := c.AllocateID()
			requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, id, surf, uint32(0), uint32(ports.LayerTop), "test")
			expectProtocolError(t, c, shell, uint32(wlrlayershell.ZwlrLayerShellV1ErrorAlreadyConstructed))
		})
	}
}

type doneProxy struct {
	wlturbo.BaseProxy
	done   int
	opcode uint16
}

func (p *doneProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == p.opcode {
		p.done++
	}
}
func TestXDGOutputDone(t *testing.T) {
	for _, version := range []uint32{1, 2} {
		t.Run(fmt.Sprintf("wl_output_v%d", version), func(t *testing.T) {
			s, _, _, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			global, ok := c.Registry().FindGlobal("wl_output")
			if !ok {
				t.Fatal("missing wl_output")
			}
			output, err := bindWireID(c, global.Name, global.Interface, version)
			if err != nil {
				t.Fatal(err)
			}
			op := &doneProxy{opcode: uint16(wayland.OutputEventDone)}
			op.SetID(output)
			registerWireProxy(c, op)
			global, ok = c.Registry().FindGlobal("zxdg_output_manager_v1")
			if !ok {
				t.Fatal("missing xdg output manager")
			}
			manager, err := bindWireID(c, global.Name, global.Interface, 3)
			if err != nil {
				t.Fatal(err)
			}
			registerProtocol(t, c, manager)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			op.done = 0
			xdg := c.AllocateID()
			xp := &doneProxy{opcode: 2}
			xp.SetID(xdg)
			registerWireProxy(c, xp)
			requestProtocol(t, c, manager, 1, xdg, output)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if version == 1 && (xp.done != 1 || op.done != 0) || version == 2 && (xp.done != 0 || op.done != 1) {
				t.Fatalf("wl_output v%d: xdg done=%d, wl_output done=%d", version, xp.done, op.done)
			}
		})
	}
}

type layerPointerProxy struct {
	wlturbo.BaseProxy
	enters  chan [2]float64
	buttons chan uint32 // serials
}

func (p *layerPointerProxy) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.PointerEventEnter):
		_, _ = e.Uint32(), e.Uint32()
		p.enters <- [2]float64{e.Fixed().Float64(), e.Fixed().Float64()}
	case uint16(wayland.PointerEventButton):
		p.buttons <- e.Uint32()
	}
}

// A bar gets the pointer, and a menu it opens on a click through
// get_popup reaches core with the bar as its parent and a grab.
func TestLayerPointerAndPopup(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindVersion(t, c, "wl_seat", 8)
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	pp := &layerPointerProxy{enters: make(chan [2]float64, 1), buttons: make(chan uint32, 1)}
	pp.SetID(pointer)
	registerWireProxy(c, pp)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("layer-popup-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err = unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buf)
	if err = wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(1), int32(1), int32(4), uint32(0))

	surf, layer := layerProtocol(t, c)
	lp := &layerProxy{configured: make(chan [3]uint32, 2)}
	lp.SetID(layer)
	registerWireProxy(c, lp)
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestSetSize, uint32(1), uint32(1))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err = c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var config [3]uint32
	select {
	case config = <-lp.configured:
	case <-time.After(2 * time.Second):
		t.Fatal("configure timeout")
	}
	requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestAckConfigure, config[0])
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err = c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	lc, ok := waitEvent(t, events, 2*time.Second).(ports.LayerChanged)
	if !ok || len(lc.Layers) != 1 {
		t.Fatalf("layer mapped %+v", lc)
	}
	id := lc.Layers[0].ID

	commands <- ports.PointerFocus{ID: id, X: 0.5, Y: 0.25}
	commands <- ports.PointerButtonTo{ID: id, Button: 0x110, Pressed: true}
	var press uint32
	for deadline := time.Now().Add(2 * time.Second); len(pp.buttons) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("pointer events timeout")
		}
		if err = c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if at := <-pp.enters; at != [2]float64{0.5, 0.25} {
		t.Fatalf("enter at %v", at)
	}
	press = <-pp.buttons

	positioner := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, positioner)
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetSize, int32(1), int32(1))
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetAnchorRect, int32(0), int32(0), int32(1), int32(1))
	done := make(chan struct{}, 1)
	xdgWindow(t, c, comp, wm, buf, func(pxdg uint32) {
		popup := c.AllocateID()
		proxy := &popupDoneProxy{done: done}
		proxy.SetID(popup)
		registerWireProxy(c, proxy)
		requestProtocol(t, c, pxdg, xdgshell.SurfaceRequestGetPopup, popup, uint32(0), positioner)
		requestProtocol(t, c, layer, wlrlayershell.ZwlrLayerSurfaceV1RequestGetPopup, popup)
		requestProtocol(t, c, popup, xdgshell.PopupRequestGrab, seat, press)
	})
	req, ok := waitEvent(t, events, 2*time.Second).(ports.PopupRequest)
	if !ok || req.Parent != id || !req.Grab {
		t.Fatalf("popup request %+v, want parent %d with a grab", req, id)
	}
	if len(done) != 0 {
		t.Fatal("layer popup dismissed")
	}
}
