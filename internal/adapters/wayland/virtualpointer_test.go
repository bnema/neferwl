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

const (
	vpMotion    = wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion
	vpAbsolute  = wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotionAbsolute
	vpButton    = wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton
	vpFrame     = wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame
	btnLeftCode = uint32(0x110)
)

// An unframed release is discarded on disconnect: the forwarded press is
// still released exactly once.
func TestVirtualPointerUnframedReleaseThenDisconnect(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	requestProtocol(t, c, p, vpButton, uint32(1), btnLeftCode, uint32(1))
	requestProtocol(t, c, p, vpFrame)
	virtualEvents(t, c, events, 1)
	requestProtocol(t, c, p, vpButton, uint32(2), btnLeftCode, uint32(0))
	virtualEvents(t, c, events, 0)
	_ = c.Close()
	b := nextEvent[ports.VirtualPointerInput](t, events).Event.(ports.PointerButton)
	if b.Button != btnLeftCode || b.Pressed {
		t.Fatalf("release %+v", b)
	}
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	select {
	case ev := <-events:
		if v, ok := ev.(ports.VirtualPointerInput); ok {
			t.Fatalf("extra event %+v", v)
		}
	default:
	}
}

// Every forwarded held button is released at destroy; a button pressed
// only in an unframed request is not.
func TestVirtualPointerReleasesAllHeld(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	for _, b := range []uint32{0x110, 0x111, 0x112} {
		requestProtocol(t, c, p, vpButton, uint32(1), b, uint32(1))
	}
	requestProtocol(t, c, p, vpFrame)
	virtualEvents(t, c, events, 3)
	requestProtocol(t, c, p, vpButton, uint32(2), uint32(0x113), uint32(1))
	requestProtocol(t, c, p, wlrvirtualpointer.ZwlrVirtualPointerV1RequestDestroy)
	got := map[uint32]bool{}
	for _, v := range virtualEvents(t, c, events, 3) {
		b := v.Event.(ports.PointerButton)
		if b.Pressed {
			t.Fatalf("press on destroy %+v", b)
		}
		got[b.Button] = true
	}
	if !got[0x110] || !got[0x111] || !got[0x112] {
		t.Fatalf("released %v", got)
	}
}

// Repeated presses in one frame forward once; a frame over the limit is a
// protocol error and forwards nothing.
func TestVirtualPointerFrameLimit(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	for i := 0; i < 10; i++ {
		requestProtocol(t, c, p, vpButton, uint32(i), btnLeftCode, uint32(1))
	}
	requestProtocol(t, c, p, vpFrame)
	if got := virtualEvents(t, c, events, 1); len(got) != 1 {
		t.Fatalf("repeated presses %v", got)
	}
	c2, p2 := virtualMover(t, s, dir, false)
	for i := 0; i <= maxVirtualFrame; i++ {
		requestProtocol(t, c2, p2, vpButton, uint32(i), btnLeftCode, uint32(i%2))
	}
	expectProtocolError(t, c2, p2, virtualFrameTooLarge)
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	select {
	case ev := <-events:
		if v, ok := ev.(ports.VirtualPointerInput); ok {
			t.Fatalf("oversized frame forwarded %+v", v)
		}
	default:
	}
}

// An oversized frame buffer is not kept after the frame.
func TestVirtualPointerResetDropsLargeBuffer(t *testing.T) {
	p := &virtualPointer{pending: make([]ports.VirtualPointerInput, 100)}
	p.reset()
	if p.pending != nil {
		t.Fatalf("kept cap %d", cap(p.pending))
	}
	p.pending = make([]ports.VirtualPointerInput, 3, virtualFrameKeep)
	p.reset()
	if len(p.pending) != 0 || cap(p.pending) != virtualFrameKeep {
		t.Fatalf("small buffer len %d cap %d", len(p.pending), cap(p.pending))
	}
}

// x == extent and an extent of 1 normalize into [0, 1].
func TestVirtualPointerAbsoluteEdges(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	for _, tc := range []struct {
		x, ext uint32
		want   float64
	}{{1, 1, 1}, {0, 1, 0}, {1920, 1920, 1}, {5000, 1920, 1}} {
		requestProtocol(t, c, p, vpAbsolute, uint32(1), tc.x, tc.x, tc.ext, tc.ext)
		requestProtocol(t, c, p, vpFrame)
		if got := virtualEvents(t, c, events, 1)[0]; got.FX != tc.want || got.FY != tc.want {
			t.Fatalf("x %d extent %d: %v,%v want %v", tc.x, tc.ext, got.FX, got.FY, tc.want)
		}
	}
}

// Mixed absolute and relative motion keeps its order and goes out only at
// frame, in both directions.
func TestVirtualPointerMixedMotionWaitsForFrame(t *testing.T) {
	for _, absFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "abs-rel", false: "rel-abs"}[absFirst], func(t *testing.T) {
			s, events, _, dir := keyboardServer(t)
			c, p := virtualMover(t, s, dir, false)
			abs := func() { requestProtocol(t, c, p, vpAbsolute, uint32(1), uint32(1), uint32(1), uint32(4), uint32(4)) }
			rel := func() { requestProtocol(t, c, p, vpMotion, uint32(1), fixed(3), fixed(4)) }
			if absFirst {
				abs()
				rel()
				rel()
			} else {
				rel()
				abs()
			}
			virtualEvents(t, c, events, 0)
			requestProtocol(t, c, p, vpFrame)
			got := virtualEvents(t, c, events, 2)
			if got[0].Absolute != absFirst || got[1].Absolute == absFirst {
				t.Fatalf("order %+v", got)
			}
			rv := got[1]
			if !absFirst {
				rv = got[0]
			}
			m := rv.Event.(ports.PointerMotion)
			want := 3.0
			if absFirst {
				want = 6
			}
			if m.DX != want {
				t.Fatalf("relative %+v want dx %v", m, want)
			}
		})
	}
}

// Held buttons are bounded across valid frames: presses past the limit and
// codes outside the evdev button range are dropped, the pointer stays usable.
func TestVirtualPointerHeldLimit(t *testing.T) {
	s, events, _, dir := keyboardServer(t)
	c, p := virtualMover(t, s, dir, false)
	for i := uint32(0); i < maxVirtualHeld+4; i++ {
		requestProtocol(t, c, p, vpButton, i, 0x100+i, uint32(1))
		requestProtocol(t, c, p, vpFrame)
	}
	if got := virtualEvents(t, c, events, maxVirtualHeld); len(got) != maxVirtualHeld {
		t.Fatalf("forwarded %d presses", len(got))
	}
	for _, b := range []uint32{0, 0xff, 0x152, 0xffffffff} {
		requestProtocol(t, c, p, vpButton, uint32(1), b, uint32(1))
	}
	requestProtocol(t, c, p, vpFrame)
	virtualEvents(t, c, events, 0)
	// A release frees a slot for a new press.
	requestProtocol(t, c, p, vpButton, uint32(2), uint32(0x100), uint32(0))
	requestProtocol(t, c, p, vpButton, uint32(2), uint32(0x151), uint32(1))
	requestProtocol(t, c, p, vpFrame)
	got := virtualEvents(t, c, events, 2)
	if b := got[1].Event.(ports.PointerButton); b.Button != 0x151 || !b.Pressed {
		t.Fatalf("press after release %+v", b)
	}
}
