package wayland

import (
	"testing"

	"bnema.dev/wlturbo"
	"github.com/bnema/go-wayland-bindings/server/wlrvirtualpointer"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// virtualMover binds a virtual pointer on its own connection, as wlrctl
// does; withOutput uses create_virtual_pointer_with_output on wl_output.
func virtualMover(t *testing.T, s *Server, dir string, withOutput bool) (*wlturbo.Display, uint32) {
	t.Helper()
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	manager := bindVersion(t, c, virtualPointerInterface, 2)
	registerProtocol(t, c, manager)
	if withOutput {
		out := bindVersion(t, c, "wl_output", 4)
		registerProtocol(t, c, out)
		p := c.AllocateID()
		registerProtocol(t, c, p)
		requestProtocol(t, c, manager, wlrvirtualpointer.ZwlrVirtualPointerManagerV1RequestCreateVirtualPointerWithOutput, seat, out, p)
		return c, p
	}
	p := c.AllocateID()
	registerProtocol(t, c, p)
	requestProtocol(t, c, manager, wlrvirtualpointer.ZwlrVirtualPointerManagerV1RequestCreateVirtualPointer, seat, p)
	return c, p
}

func fixed(v float64) wlturbo.Fixed { return wlturbo.Fixed(int32(v * 256)) }

// virtualEvents waits for n VirtualPointerInput events after a roundtrip
// and fails on any further one already forwarded.
func virtualEvents(t *testing.T, c *wlturbo.Display, events chan ports.ClientEvent, n int) []ports.VirtualPointerInput {
	t.Helper()
	roundtrip(t, c)
	var out []ports.VirtualPointerInput
	for len(out) < n {
		out = append(out, nextEvent[ports.VirtualPointerInput](t, events))
	}
	for {
		select {
		case ev := <-events:
			if v, ok := ev.(ports.VirtualPointerInput); ok {
				t.Fatalf("unexpected virtual event %+v", v)
			}
		default:
			return out
		}
	}
}

// Requests are held until frame and reach core in one batch: motion, then
// buttons, then scroll, under the virtual device identity.
func TestVirtualPointerFrameBatch(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion, uint32(5), fixed(10), fixed(-4))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion, uint32(6), fixed(2.5), fixed(1))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, uint32(7), uint32(0x110), uint32(1))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestAxisSource, uint32(0))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestAxisDiscrete, uint32(8), uint32(0), fixed(15), int32(1))
	roundtrip(t, c)
	select {
	case ev := <-events:
		if _, ok := ev.(ports.VirtualPointerInput); ok {
			t.Fatal("virtual event before frame")
		}
	default:
	}
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
	got := virtualEvents(t, c, events, 3)
	m, ok := got[0].Event.(ports.PointerMotion)
	if !ok || got[0].Absolute || m.DX != 12.5 || m.DY != -3 || m.Device < virtualPointerDevices {
		t.Fatalf("motion %+v", got[0])
	}
	b, ok := got[1].Event.(ports.PointerButton)
	if !ok || b.Button != 0x110 || !b.Pressed || b.Device != m.Device {
		t.Fatalf("button %+v", got[1])
	}
	a, ok := got[2].Event.(ports.PointerAxis)
	if !ok || a.Source != domain.AxisWheel || !a.Vertical.Set || a.Vertical.Value != 15 || a.Vertical.V120 != 120 || a.Horizontal.Set {
		t.Fatalf("axis %+v", got[2])
	}
}

// Absolute motion becomes a fraction of the layout, or of the bound output.
func TestVirtualPointerAbsolute(t *testing.T) {
	for _, withOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "layout", true: "with-output"}[withOutput], func(t *testing.T) {
			s, events, _, dir := keyboardServer(t)
			c, p := virtualMover(t, s, dir, withOutput)
			requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotionAbsolute, uint32(1), uint32(480), uint32(270), uint32(1920), uint32(1080))
			requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
			got := virtualEvents(t, c, events, 1)[0]
			want := ""
			if withOutput {
				want = "HEADLESS-1"
			}
			if !got.Absolute || got.FX != 0.25 || got.FY != 0.25 || got.Output != want {
				t.Fatalf("absolute %+v", got)
			}
		})
	}
}

// Losing the pointer mid-click releases its buttons; an unpaired release
// never goes out.
func TestVirtualPointerDisconnectReleases(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, uint32(1), uint32(0x111), uint32(0))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, uint32(1), uint32(0x110), uint32(1))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
	if b := virtualEvents(t, c, events, 1)[0].Event.(ports.PointerButton); b.Button != 0x110 || !b.Pressed {
		t.Fatalf("press %+v", b)
	}
	_ = c.Close()
	b, ok := nextEvent[ports.VirtualPointerInput](t, events).Event.(ports.PointerButton)
	if !ok || b.Button != 0x110 || b.Pressed {
		t.Fatalf("release on disconnect %+v", b)
	}
}

// Bad axis values are protocol errors.
func TestVirtualPointerInvalidAxis(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestAxis, uint32(1), uint32(7), fixed(1))
	expectProtocolError(t, c, p, uint32(wlrvirtualpointer.ZwlrVirtualPointerV1ErrorInvalidAxis))
}

// While protected nothing is injected, the frame in progress is dropped and
// a press from before the lock is not released into the locked session.
func TestProtectedVirtualPointer(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	state := installSecurity(t, s)
	c, p := virtualMover(t, s, dir, false)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, uint32(1), uint32(0x110), uint32(1))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
	virtualEvents(t, c, events, 1)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion, uint32(2), fixed(5), fixed(5))
	roundtrip(t, c)
	protect(t, s, state, 1, true)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion, uint32(3), fixed(5), fixed(5))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, uint32(3), uint32(0x110), uint32(0))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
	virtualEvents(t, c, events, 0)
	protect(t, s, state, 2, false)
	// The old press died with the epoch: its release is not paired.
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, uint32(4), uint32(0x110), uint32(0))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
	virtualEvents(t, c, events, 0)
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion, uint32(5), fixed(1), fixed(0))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame)
	if got := virtualEvents(t, c, events, 1)[0]; got.State.Generation != 2 || got.State.Protected {
		t.Fatalf("post-unlock state %+v", got.State)
	}
}

// Sandboxed clients do not see the manager global.
func TestVirtualPointerHiddenFromSandbox(t *testing.T) {
	h := newAdmissionHarness(t, 4)
	plain := protocolClient(t, h.s, h.dir)
	if _, ok := plain.Registry().FindGlobal(virtualPointerInterface); !ok {
		t.Fatal("plain client lacks the virtual pointer global")
	}
	l := newSandboxListener(t)
	cc := newContextClient(t, h)
	cc.commit(t, l, "org.flatpak", "org.example.App")
	sandboxed := l.connect(t)
	if _, ok := sandboxed.Registry().FindGlobal(virtualPointerInterface); ok {
		t.Fatal("sandboxed client sees the virtual pointer global")
	}
}
