package wayland

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/bnema/neferwl/internal/domain"
	"net"
	"os"
	"testing"
	"time"

	"bnema.dev/wlturbo"
	"github.com/bnema/go-wayland-bindings/server/pointerconstraints"
	"github.com/bnema/go-wayland-bindings/server/relativepointer"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/go-wayland-bindings/server/xwaylandkeyboardgrab"
	"github.com/bnema/go-wayland-bindings/server/xwaylandshell"
	"github.com/bnema/neferwl/internal/ports"
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
	// surfaceProxy, when set, observes the next paired wl_surface.
	surfaceProxy func(id uint32) wlturbo.Proxy
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
	if r.surfaceProxy != nil {
		registerWireProxy(r.xc, r.surfaceProxy(surf))
	} else {
		registerProtocol(r.t, r.xc, surf)
	}
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

//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
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
	r := newX11Rig(t)
	other := protocolClient(t, r.s, r.dir)
	for _, iface := range []string{"xwayland_shell_v1", "zwp_xwayland_keyboard_grab_manager_v1"} {
		if _, ok := other.Registry().FindGlobal(iface); ok {
			t.Errorf("ordinary client sees %s", iface)
		}
	}
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
	if m.AppID != "Class" || m.PID != 4242 || !m.Positioned || !m.Xwayland || m.PIDShared || m.Floating || m.Slot != "" {
		t.Fatalf("mapped %+v", m)
	}

	// Title and app id changes.
	p := r.peer
	p.setProp(win, p.atom("_NET_WM_NAME"), p.atom("UTF8_STRING"), 8, []byte("renamed"))
	p.propertyNotify(win, p.atom("_NET_WM_NAME"))
	if title := awaitEvent[ports.WindowTitle](t, r.events); title.ID != m.ID || title.Title != "renamed" {
		t.Fatal(title)
	}
	p.setProp(win, 67, 31, 8, []byte("inst\x00Other\x00"))
	p.propertyNotify(win, 67)
	if a := awaitEvent[ports.WindowAppID](t, r.events); a.ID != m.ID || a.AppID != "Other" {
		t.Fatalf("app id %+v", a)
	}
	var title string
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	r.s.display.Do(func() { title = r.s.windows[m.ID].title })
	if title != "renamed" {
		t.Fatalf("title %q", title)
	}

	// Modal changes after map reach the existing core message.
	for _, on := range []bool{true, false} {
		action := uint32(0)
		if on {
			action = 1
		}
		p.clientMessage(win, "_NET_WM_STATE", action, p.atom("_NET_WM_STATE_MODAL"))
		if ev := awaitEvent[ports.WindowModal](t, r.events); ev.ID != m.ID || ev.Modal != on {
			t.Fatalf("modal %+v, want %t", ev, on)
		}
		onDisplay(t, r.s, func() {
			if w := r.s.windows[m.ID]; w.modal != on || w.x11.info.Modal != on {
				t.Errorf("modal state not updated")
			}
		})
	}

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

	// Focus: X11 focus follows core's X11Active, not the keyboard.
	skip = len(p.requests())
	r.commands <- ports.FocusWindow{ID: m.ID}
	r.commands <- ports.X11Active{ID: m.ID}
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
	// An ordered X event after DestroyNotify is observed through a second mapped window.
	r.x11Window(win + 1)
	r.s.display.Do(func() {
		if r.s.windows[m.ID] != nil {
			t.Error("window not forgotten")
		}
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
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
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
	if got := constrained(t, r.events); got.ID != m.ID || got.Mode != domain.ConstraintLock {
		t.Fatalf("constrained %+v", got)
	}
}

// A late wl_pointer bind inherits the entered X11 surface, just as it does
// for an xdg window. Xwayland may bind additional pointer objects after focus.
func TestX11WindowLatePointerBind(t *testing.T) {
	r := newX11Rig(t)
	r.x11Window(x11Win)
	surf := r.pair(x11Win, x11Serial)
	m := awaitEvent[ports.WindowMapped](t, r.events)
	seat := bindProtocol(t, r.xc, "wl_seat")
	first := &surfacePointer{events: make(chan string, 32)}
	first.SetID(r.xc.AllocateID())
	registerWireProxy(r.xc, first)
	requestProtocol(t, r.xc, seat, wayland.SeatRequestGetPointer, first.ID())
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	r.commands <- ports.PointerFocus{ID: m.ID, X: 3, Y: 4}
	want := fmt.Sprintf("enter %d 3.00 4.00", surf)
	if entered := recvWire(t, r.xc, "first pointer enter", first.events); entered != want {
		t.Fatalf("first pointer enter: %s", entered)
	}
	second := &surfacePointer{events: make(chan string, 32)}
	second.SetID(r.xc.AllocateID())
	registerWireProxy(r.xc, second)
	requestProtocol(t, r.xc, seat, wayland.SeatRequestGetPointer, second.ID())
	if late := recvWire(t, r.xc, "late pointer enter", second.events); late != want {
		t.Fatalf("late pointer enter: %s", late)
	}
}

// grabRig is an X11 window with an Xwayland keyboard grab on its surface.
func grabRig(t *testing.T) (r *x11Rig, w domain.WindowID, surf uint32) {
	t.Helper()
	r = newX11Rig(t)
	r.x11Window(x11Win)
	surf = r.pair(x11Win, x11Serial)
	w = awaitEvent[ports.WindowMapped](t, r.events).ID
	seat := bindProtocol(t, r.xc, "wl_seat")
	registerProtocol(t, r.xc, seat)
	mgr := bindProtocol(t, r.xc, "zwp_xwayland_keyboard_grab_manager_v1")
	grab := r.xc.AllocateID()
	requestProtocol(t, r.xc, mgr, xwaylandkeyboardgrab.ZwpXwaylandKeyboardGrabManagerV1RequestGrabKeyboard, grab, surf, seat)
	registerProtocol(t, r.xc, grab)
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return r, w, grab
}

// noInhibit fails when a ShortcutsInhibit event arrives shortly.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func noInhibit(t *testing.T, r *x11Rig) {
	t.Helper()
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.After(150 * time.Millisecond); ; {
		select {
		case ev := <-r.events:
			if v, ok := ev.(ports.ShortcutsInhibit); ok {
				t.Fatalf("unexpected %+v", v)
			}
		case <-deadline:
			return
		}
	}
}

func TestXwaylandKeyboardGrabThreeStates(t *testing.T) {
	r, w, grab := grabRig(t)
	// Windowed and focused: accepted, no effect.
	r.commands <- ports.ConfigureWindow{ID: w, Width: 100, Height: 100, Visible: true}
	r.commands <- ports.FocusWindow{ID: w}
	noInhibit(t, r)
	// Focused and fullscreen: inhibits.
	r.commands <- ports.ConfigureWindow{ID: w, Width: 100, Height: 100, Visible: true, Fullscreen: true}
	if v := awaitEvent[ports.ShortcutsInhibit](t, r.events); v != (ports.ShortcutsInhibit{Window: w, Active: true}) {
		t.Fatal(v)
	}
	// Focus lost: released.
	r.commands <- ports.FocusWindow{}
	if v := awaitEvent[ports.ShortcutsInhibit](t, r.events); v != (ports.ShortcutsInhibit{Window: w}) {
		t.Fatal(v)
	}
	// Back, then the grab object goes away.
	r.commands <- ports.FocusWindow{ID: w}
	awaitEvent[ports.ShortcutsInhibit](t, r.events)
	requestProtocol(t, r.xc, grab, xwaylandkeyboardgrab.ZwpXwaylandKeyboardGrabV1RequestDestroy)
	if v := awaitEvent[ports.ShortcutsInhibit](t, r.events); v != (ports.ShortcutsInhibit{Window: w}) {
		t.Fatal(v)
	}
}

// x11Pointer gives the Xwayland client a wl_pointer and a relative pointer,
// both with event logs.
func (r *x11Rig) x11Pointer() (pointer uint32, ptrEvents, relEvents chan []float64) {
	r.t.Helper()
	seat := bindProtocol(r.t, r.xc, "wl_seat")
	registerProtocol(r.t, r.xc, seat)
	pointer = r.xc.AllocateID()
	ptrEvents = logProxy(r.t, r.xc, pointer)
	requestProtocol(r.t, r.xc, seat, wayland.SeatRequestGetPointer, pointer)
	mgr := bindProtocol(r.t, r.xc, "zwp_relative_pointer_manager_v1")
	rel := r.xc.AllocateID()
	relEvents = logProxy(r.t, r.xc, rel)
	requestProtocol(r.t, r.xc, mgr, relativepointer.ZwpRelativePointerManagerV1RequestGetRelativePointer, rel, pointer)
	return pointer, ptrEvents, relEvents
}

// A lock on an X11 surface activates with pointer and keyboard focus, sends
// relative motion to the Xwayland client and deactivates when a native
// window takes the focus; it comes back with the X11 focus.
func TestX11LockFollowsFocusAndRelativeMotion(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	r.x11Window(win)
	surf := r.pair(win, x11Serial)
	m := awaitEvent[ports.WindowMapped](t, r.events)
	pointer, _, relEvents := r.x11Pointer()
	constraints := bindProtocol(t, r.xc, "zwp_pointer_constraints_v1")
	lock := r.xc.AllocateID()
	lockEvents := logProxy(t, r.xc, lock)
	requestProtocol(t, r.xc, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestLockPointer, lock, surf, pointer, uint32(0), uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent))
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(lockEvents) > 0 {
		t.Fatal("locked without focus")
	}

	r.commands <- ports.PointerFocus{ID: m.ID}
	r.commands <- ports.FocusWindow{ID: m.ID}
	r.commands <- ports.X11Active{ID: m.ID}
	if ev := next(t, r.xc, lockEvents); ev[0] != float64(pointerconstraints.ZwpLockedPointerV1EventLocked) {
		t.Fatalf("lock event %v", ev)
	}
	if got := constrained(t, r.events); got.ID != m.ID || got.Mode != domain.ConstraintLock || got.Rect != (domain.Rect{}) {
		t.Fatalf("constrained %+v", got)
	}

	r.commands <- ports.PointerMotionTo{ID: m.ID, X: 5, Y: 5, DX: 2.5, DY: -1, UnaccelDX: 4, UnaccelDY: -2}
	if ev := next(t, r.xc, relEvents); len(ev) != 5 || ev[1] != 2.5 || ev[2] != -1 || ev[3] != 4 || ev[4] != -2 {
		t.Fatalf("relative motion %v", ev)
	}

	// A native window takes the focus: the X11 lock ends and the XWM drops
	// the X focus.
	nc := protocolClient(t, r.s, r.dir)
	nm, _, _ := surfaceMapper(t, nc, r.events)()
	skip := len(r.peer.requests())
	r.commands <- ports.FocusWindow{ID: nm.ID}
	r.commands <- ports.X11Active{}
	if ev := next(t, r.xc, lockEvents); ev[0] != float64(pointerconstraints.ZwpLockedPointerV1EventUnlocked) {
		t.Fatalf("unlock event %v", ev)
	}
	if got := constrained(t, r.events); got.ID != 0 {
		t.Fatalf("constrained %+v after native focus", got)
	}
	// The SetInputFocus for the earlier X11 focus may land after skip was
	// taken, so wait for the request that moves the X focus away.
	r.peer.waitReq(skip, func(q x11Req) bool { return q.major == x11OpSetInputFocus && q.u32(4) != win })

	r.commands <- ports.FocusWindow{ID: m.ID}
	if ev := next(t, r.xc, lockEvents); ev[0] != float64(pointerconstraints.ZwpLockedPointerV1EventLocked) {
		t.Fatalf("relock event %v", ev)
	}
}

// A confine on an X11 surface reaches core with its region as given: an X11
// window has no geometry offset.
func TestX11ConfineRegionHasNoOffset(t *testing.T) {
	r := newX11Rig(t)
	win := uint32(x11Win)
	r.x11Window(win)
	surf := r.pair(win, x11Serial)
	m := awaitEvent[ports.WindowMapped](t, r.events)
	pointer, _, _ := r.x11Pointer()
	region := r.xc.AllocateID()
	registerProtocol(t, r.xc, region)
	requestProtocol(t, r.xc, r.comp, wayland.CompositorRequestCreateRegion, region)
	requestProtocol(t, r.xc, region, wayland.RegionRequestAdd, int32(5), int32(6), int32(20), int32(30))
	constraints := bindProtocol(t, r.xc, "zwp_pointer_constraints_v1")
	confine := r.xc.AllocateID()
	confineEvents := logProxy(t, r.xc, confine)
	requestProtocol(t, r.xc, constraints, pointerconstraints.ZwpPointerConstraintsV1RequestConfinePointer, confine, surf, pointer, region, uint32(pointerconstraints.ZwpPointerConstraintsV1LifetimePersistent))
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	r.state(func() {
		if g := r.s.windows[m.ID].geometry(); g != (domain.Rect{}) {
			t.Errorf("X11 geometry %+v, want zero", g)
		}
	})

	r.commands <- ports.PointerFocus{ID: m.ID, X: 10, Y: 10}
	r.commands <- ports.FocusWindow{ID: m.ID}
	r.commands <- ports.PointerMotionTo{ID: m.ID, X: 10, Y: 10}
	if ev := next(t, r.xc, confineEvents); ev[0] != float64(pointerconstraints.ZwpConfinedPointerV1EventConfined) {
		t.Fatalf("confine event %v", ev)
	}
	want := ports.PointerConstrained{ID: m.ID, PointerConstraint: domain.PointerConstraint{Mode: domain.ConstraintConfine, Rect: domain.Rect{X: 5, Y: 6, W: 20, H: 30}}}
	if got := constrained(t, r.events); got != want {
		t.Fatalf("constrained %+v, want %+v", got, want)
	}
}

// waitOn waits until window id's surface is on output name: commands reach
// the display goroutine asynchronously.
func (r *x11Rig) waitOn(id domain.WindowID, name string) {
	r.t.Helper()
	flushCommands(r.t, r.s, r.commands)
	r.state(func() {
		w := r.s.windows[id]
		if w == nil || w.surf().on == nil || w.surf().on.name() != name {
			r.t.Fatalf("surface not on %s", name)
		}
	})

}

// An X11 window is on one output only, even straddling two: a single
// wl_surface.enter, and a leave only when its output changes.
func TestX11WindowEntersOneOutput(t *testing.T) {
	r := newX11Rig(t)
	r.commands <- ports.SetOutputs{Outputs: domain.Layout{
		{Info: domain.OutputInfo{Name: "A"}, X: 0, Y: 0, Width: 1920, Height: 1080, Scale: 1},
		{Info: domain.OutputInfo{Name: "B"}, X: 1920, Y: 0, Width: 1920, Height: 1080, Scale: 1},
	}, Focused: "A"}
	// Join the asynchronous command forwarder and display work before the
	// client roundtrip collects the globals from this layout change.
	flushCommands(t, r.s, r.commands)
	roundtrip(t, r.xc)
	outputs := 0
	for _, g := range r.xc.Registry().GetGlobals() {
		if g.Interface != "wl_output" {
			continue
		}
		id, err := bindWireID(r.xc, g.Name, g.Interface, 1)
		if err != nil {
			t.Fatal(err)
		}
		registerProtocol(t, r.xc, id)
		outputs++
	}
	if outputs != 2 {
		t.Fatalf("%d wl_output globals, want 2", outputs)
	}

	win := uint32(x11Win)
	r.x11Window(win)
	events := make(chan []float64, 64)
	r.surfaceProxy = func(id uint32) wlturbo.Proxy {
		p := &eventLog{events: events}
		p.SetID(id)
		return p
	}
	r.pair(win, x11Serial)
	m := awaitEvent[ports.WindowMapped](t, r.events)

	// Straddling both outputs, configured on A.
	r.commands <- ports.PlaceWindow{ID: m.ID, X: 1900, Y: 0}
	r.commands <- ports.ConfigureWindow{ID: m.ID, Width: 200, Height: 100, Visible: true, Output: "A"}
	r.waitOn(m.ID, "A")
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	count := func() (enters, leaves int) {
		for len(events) > 0 {
			switch ev := <-events; ev[0] {
			case float64(wayland.SurfaceEventEnter):
				enters++
			case float64(wayland.SurfaceEventLeave):
				leaves++
			}
		}
		return
	}
	if e, l := count(); e != 1 || l != 0 {
		t.Fatalf("straddling window: %d enters, %d leaves, want 1, 0", e, l)
	}
	r.state(func() {
		if on := r.s.windows[m.ID].surf().on; on == nil || on.name() != "A" {
			t.Errorf("surface on %v, want A", on)
		}
	})

	// Moving to B leaves A and enters B once.
	r.commands <- ports.ConfigureWindow{ID: m.ID, Width: 200, Height: 100, Visible: true, Output: "B"}
	r.waitOn(m.ID, "B")
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if e, l := count(); e != 1 || l != 1 {
		t.Fatalf("move to B: %d enters, %d leaves, want 1, 1", e, l)
	}
}
