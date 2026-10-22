package wayland

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/pointerconstraints"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/go-wayland-bindings/server/xwaylandshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

var x11Serial = uint64(0x1234_0000_0042)

// x11Rig is a server with an Xwayland client (a real wlturbo client on the
// adopted descriptor), a scripted X peer on the WM descriptor and the XWM
// between them.
type x11Rig struct {
	t        *testing.T
	s        *Server
	events   chan ports.ClientEvent
	commands chan ports.ClientCommand
	dir      string
	xc       *wlturbo.Display
	peer     *x11Peer
	stop     func()
	shell    uint32
	comp     uint32
}

func newX11Rig(t *testing.T) *x11Rig {
	t.Helper()
	s, events, commands, dir := lifecycleServer(t)
	r := &x11Rig{t: t, s: s, events: events, commands: commands, dir: dir}
	wl := xwaylandPair(t)
	s.AttachXwayland(wl[0])
	f := os.NewFile(uintptr(wl[1]), "xwayland client")
	wl[1] = -1
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if r.xc, err = wlturbo.ConnectFromConn(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.xc.Close(); forgetWireClient(r.xc) })
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	wm, peer := startX11Peer(t)
	r.peer = peer
	r.stop = s.AttachXWM(context.Background(), wm)
	t.Cleanup(func() { r.stop() })
	r.shell = bindProtocol(t, r.xc, "xwayland_shell_v1")
	r.comp = bindProtocol(t, r.xc, "wl_compositor")
	return r
}

// x11Window announces and maps X window win with the properties of an
// ordinary client, and waits for the XWM to be ready for it.
func (r *x11Rig) x11Window(win uint32) {
	r.t.Helper()
	p := r.peer
	str := uint32(31)                                       // XA_STRING
	p.setProp(win, 67, str, 8, []byte("inst\x00Class\x00")) // WM_CLASS
	p.setProp(win, p.atom("_NET_WM_NAME"), p.atom("UTF8_STRING"), 8, []byte("net title"))
	p.setProp(win, p.atom("_NET_WM_PID"), 6, 32, x11le32(4242))
	p.createNotify(win)
	skip := len(p.requests())
	p.mapRequest(win)
	p.waitReq(skip, func(q x11Req) bool { return q.major == 8 && q.u32(4) == win }) // MapWindow
}

// pair sends the serial from both sides and commits a buffer on the surface.
// It returns the wl_surface id of the Xwayland client.
func (r *x11Rig) pair(win uint32, serial uint64) uint32 {
	r.t.Helper()
	r.peer.clientMessage(win, "WL_SURFACE_SERIAL", uint32(serial), uint32(serial>>32))
	surf, xs := r.xc.AllocateID(), r.xc.AllocateID()
	requestProtocol(r.t, r.xc, r.comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(r.t, r.xc, surf)
	requestProtocol(r.t, r.xc, r.shell, xwaylandshell.XwaylandShellV1RequestGetXwaylandSurface, xs, surf)
	registerProtocol(r.t, r.xc, xs)
	requestProtocol(r.t, r.xc, xs, xwaylandshell.XwaylandSurfaceV1RequestSetSerial, uint32(serial), uint32(serial>>32))
	requestProtocol(r.t, r.xc, surf, wayland.SurfaceRequestAttach, shmBuffer(r.t, r.xc), int32(0), int32(0))
	requestProtocol(r.t, r.xc, surf, wayland.SurfaceRequestCommit)
	if err := r.xc.Roundtrip(); err != nil {
		r.t.Fatal(err)
	}
	return surf
}

func awaitEvent[T ports.ClientEvent](t *testing.T, events <-chan ports.ClientEvent) T {
	t.Helper()
	for deadline := time.After(3 * time.Second); ; {
		select {
		case ev := <-events:
			if v, ok := ev.(T); ok {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T event", zero)
		}
	}
}

func TestXwaylandGlobalFilter(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	other := protocolClient(t, s, dir)
	for _, iface := range []string{"xwayland_shell_v1", "zwp_xwayland_keyboard_grab_manager_v1"} {
		if _, ok := other.Registry().FindGlobal(iface); ok {
			t.Errorf("ordinary client sees %s", iface)
		}
	}
	if _, ok := other.Registry().FindGlobal("xdg_wm_base"); !ok {
		t.Error("ordinary globals filtered out")
	}
	r := newX11Rig(t)
	if _, ok := r.xc.Registry().FindGlobal("xwayland_shell_v1"); !ok {
		t.Error("Xwayland client misses xwayland_shell_v1")
	}
	// The ordinary client of the same server still does not see it.
	late := protocolClient(t, r.s, r.dir)
	if _, ok := late.Registry().FindGlobal("xwayland_shell_v1"); ok {
		t.Error("ordinary client sees xwayland_shell_v1 while Xwayland is attached")
	}
}

func TestXwaylandShellRoleErrors(t *testing.T) {
	for _, scenario := range []string{"xdg role first", "serial twice", "zero serial"} {
		t.Run(scenario, func(t *testing.T) {
			r := newX11Rig(t)
			c := r.xc
			wmBase := bindProtocol(t, c, "xdg_wm_base")
			surf := c.AllocateID()
			requestProtocol(t, c, r.comp, wayland.CompositorRequestCreateSurface, surf)
			registerProtocol(t, c, surf)
			xs := c.AllocateID()
			switch scenario {
			case "xdg role first":
				xdg := xs
				requestProtocol(t, c, wmBase, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
				requestProtocol(t, c, r.shell, xwaylandshell.XwaylandShellV1RequestGetXwaylandSurface, c.AllocateID(), surf)
				expectProtocolError(t, c, r.shell, uint32(xwaylandshell.XwaylandShellV1ErrorRole))
			case "serial twice":
				requestProtocol(t, c, r.shell, xwaylandshell.XwaylandShellV1RequestGetXwaylandSurface, xs, surf)
				registerProtocol(t, c, xs)
				requestProtocol(t, c, xs, xwaylandshell.XwaylandSurfaceV1RequestSetSerial, uint32(1), uint32(0))
				requestProtocol(t, c, xs, xwaylandshell.XwaylandSurfaceV1RequestSetSerial, uint32(2), uint32(0))
				expectProtocolError(t, c, xs, uint32(xwaylandshell.XwaylandSurfaceV1ErrorAlreadyAssociated))
			case "zero serial":
				requestProtocol(t, c, r.shell, xwaylandshell.XwaylandShellV1RequestGetXwaylandSurface, xs, surf)
				registerProtocol(t, c, xs)
				requestProtocol(t, c, xs, xwaylandshell.XwaylandSurfaceV1RequestSetSerial, uint32(0), uint32(0))
				expectProtocolError(t, c, xs, uint32(xwaylandshell.XwaylandSurfaceV1ErrorInvalidSerial))
			}
		})
	}
}

// A window announced by the X server and a surface carrying the same serial
// map as one neferwl window with the X11 info; core commands reach the X side.
func TestX11WindowLifecycle(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	r.x11Window(win)
	surf := r.pair(win, x11Serial)

	m := awaitEvent[ports.WindowMapped](t, r.events)
	if m.AppID != "Class" || m.PID != 4242 || !m.Positioned || m.Floating || m.Slot != "" {
		t.Fatalf("mapped %+v", m)
	}

	// Title and app id changes.
	p := r.peer
	p.setProp(win, p.atom("_NET_WM_NAME"), p.atom("UTF8_STRING"), 8, []byte("renamed"))
	p.propertyNotify(win, p.atom("_NET_WM_NAME"))
	p.setProp(win, 67, 31, 8, []byte("inst\x00Other\x00"))
	p.propertyNotify(win, 67)
	if a := awaitEvent[ports.WindowAppID](t, r.events); a.ID != m.ID || a.AppID != "Other" {
		t.Fatalf("app id %+v", a)
	}
	var title string
	waitTrue(t, "title", func() bool {
		r.s.display.Do(func() {
			if w := r.s.windows[m.ID]; w != nil {
				title = w.title
			}
		})
		return title == "renamed"
	})

	// Fullscreen request from the client.
	p.clientMessage(win, "_NET_WM_STATE", 1, p.atom("_NET_WM_STATE_FULLSCREEN"))
	if f := awaitEvent[ports.WindowFullscreenRequest](t, r.events); f.ID != m.ID || !f.Fullscreen || f.External {
		t.Fatalf("fullscreen %+v", f)
	}

	// Configure and Place reach the X server as ConfigureWindow.
	skip := len(p.requests())
	r.commands <- ports.ConfigureWindow{ID: m.ID, Width: 640, Height: 360, Visible: true}
	cfg, skip := p.waitReq(skip, func(q x11Req) bool { return q.major == x11OpConfigureWindow && q.u32(4) == win })
	if w := binary.LittleEndian.Uint32(cfg.raw[20:]); w != 640 {
		t.Fatalf("configured width %d, want 640 (%x)", w, cfg.raw)
	}
	r.commands <- ports.PlaceWindow{ID: m.ID, X: 10, Y: 20}
	cfg, _ = p.waitReq(skip, func(q x11Req) bool { return q.major == x11OpConfigureWindow && q.u32(4) == win })
	if x, y := int32(binary.LittleEndian.Uint32(cfg.raw[12:])), int32(binary.LittleEndian.Uint32(cfg.raw[16:])); x != 10 || y != 20 {
		t.Fatalf("placed at %d,%d, want 10,20", x, y)
	}

	// Focus.
	skip = len(p.requests())
	r.commands <- ports.FocusWindow{ID: m.ID}
	if f, _ := p.waitReq(skip, func(q x11Req) bool { return q.major == x11OpSetInputFocus }); f.u32(4) != win {
		t.Fatalf("focus on %#x, want %#x", f.u32(4), win)
	}

	// Close: without WM_DELETE_WINDOW the client is killed.
	skip = len(p.requests())
	r.commands <- ports.CloseWindow{ID: m.ID}
	if k, _ := p.waitReq(skip, func(q x11Req) bool { return q.major == x11OpKillClient }); k.u32(4) != win {
		t.Fatalf("killed %#x, want %#x", k.u32(4), win)
	}

	// Destroying the surface unmaps the window.
	requestProtocol(t, r.xc, surf, wayland.SurfaceRequestDestroy)
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if u := awaitEvent[ports.WindowUnmapped](t, r.events); u.ID != m.ID {
		t.Fatalf("unmapped %+v", u)
	}
}

// The serial may arrive on the Wayland side before the X side knows it, and
// an UnmapNotify unmaps; a remap pairs again.
func TestX11WindowSurfaceFirstAndRemap(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	p := r.peer
	surf, xs := r.xc.AllocateID(), r.xc.AllocateID()
	requestProtocol(t, r.xc, r.comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, r.xc, surf)
	requestProtocol(t, r.xc, r.shell, xwaylandshell.XwaylandShellV1RequestGetXwaylandSurface, xs, surf)
	registerProtocol(t, r.xc, xs)
	requestProtocol(t, r.xc, xs, xwaylandshell.XwaylandSurfaceV1RequestSetSerial, uint32(x11Serial), uint32(x11Serial>>32))
	requestProtocol(t, r.xc, surf, wayland.SurfaceRequestAttach, shmBuffer(t, r.xc), int32(0), int32(0))
	requestProtocol(t, r.xc, surf, wayland.SurfaceRequestCommit)
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	r.x11Window(win)
	p.clientMessage(win, "WL_SURFACE_SERIAL", uint32(x11Serial), uint32(x11Serial>>32))
	m := awaitEvent[ports.WindowMapped](t, r.events)
	if m.Remap {
		t.Fatalf("first map flagged as remap: %+v", m)
	}
	p.unmapNotify(win)
	if u := awaitEvent[ports.WindowUnmapped](t, r.events); u.ID != m.ID {
		t.Fatalf("unmapped %+v", u)
	}
	p.destroyNotify(win)
	waitTrue(t, "window forgotten", func() bool {
		gone := false
		r.s.display.Do(func() { gone = r.s.windows[m.ID] == nil })
		return gone
	})
}

// stop closes the XWM: its windows unmap, and the server keeps serving.
func TestX11StopUnmapsWindows(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	r.x11Window(win)
	r.pair(win, x11Serial)
	m := awaitEvent[ports.WindowMapped](t, r.events)
	r.stop()
	if u := awaitEvent[ports.WindowUnmapped](t, r.events); u.ID != m.ID {
		t.Fatalf("unmapped %+v", u)
	}
	r.s.display.Do(func() {
		if r.s.xwm != nil || r.s.windows[m.ID] != nil || len(r.s.x11Slots) != 0 {
			t.Error("XWM state left behind")
		}
	})
	r.stop() // idempotent
}

// Boot gives up as soon as its context ends, even against a silent server,
// and leaves the server serving.
func TestAttachXWMBootAbortsWithContext(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1]) // the silent server end
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan func(), 1)
	go func() { done <- s.AttachXWM(ctx, fds[0]) }()
	select {
	case <-done:
		t.Fatal("AttachXWM returned before the context ended")
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case stop := <-done:
		stop()
	case <-time.After(2 * time.Second):
		t.Fatal("AttachXWM did not abort with its context")
	}
	assertFDClosed(t, fds[0])
	protocolClient(t, s, dir) // still serving
}

// Xwayland locks the pointer for games: the constraint reaches core for the
// X11 window like for an xdg toplevel.
func TestX11WindowPointerLock(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	r.x11Window(win)
	surf := r.pair(win, x11Serial)
	m := awaitEvent[ports.WindowMapped](t, r.events)

	seat := bindProtocol(t, r.xc, "wl_seat")
	registerProtocol(t, r.xc, seat)
	pointer := r.xc.AllocateID()
	requestProtocol(t, r.xc, seat, wayland.SeatRequestGetPointer, pointer)
	registerProtocol(t, r.xc, pointer)
	constraints := bindProtocol(t, r.xc, "zwp_pointer_constraints_v1")
	lock := r.xc.AllocateID()
	requestProtocol(t, r.xc, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestLockPointer, lock, surf, pointer, uint32(0), uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent))
	registerProtocol(t, r.xc, lock)
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	r.commands <- ports.PointerFocus{ID: m.ID}
	r.commands <- ports.FocusWindow{ID: m.ID}
	if got := constrained(t, r.events); got.ID != m.ID || got.Mode != ports.ConstraintLock {
		t.Fatalf("constrained %+v", got)
	}
}
