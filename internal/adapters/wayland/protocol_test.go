package wayland

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

func protocolClient(t *testing.T, s *Server, dir string) *wlturbo.Display {
	t.Helper()
	c, err := wlturbo.Connect(filepath.Join(dir, s.SocketName()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return c
}

func bindProtocol(t *testing.T, c *wlturbo.Display, iface string) uint32 {
	t.Helper()
	return bindVersion(t, c, iface, 1)
}

func bindVersion(t *testing.T, c *wlturbo.Display, iface string, version uint32) uint32 {
	t.Helper()
	g, ok := c.Registry().FindGlobal(iface)
	if !ok {
		t.Fatalf("missing %s", iface)
	}
	id, err := c.Registry().BindID(g.Name, g.Interface, version)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func requestProtocol(t *testing.T, c *wlturbo.Display, id uint32, op uint32, args ...any) {
	t.Helper()
	if err := c.SendRequest(id, uint16(op), args...); err != nil {
		t.Fatal(err)
	}
}

func expectProtocolError(t *testing.T, c *wlturbo.Display, object, code uint32) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		for {
			if err := c.Dispatch(); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		var displayErr *wlturbo.DisplayError
		if !errors.As(err, &displayErr) {
			t.Fatalf("expected display error, got %v", err)
		}
		if displayErr.ObjectID != object || displayErr.Code != code {
			t.Fatalf("protocol error = %v, want object=%d code=%d", displayErr, object, code)
		}
	case <-time.After(2 * time.Second):
		_ = c.Close()
		t.Fatal("protocol error timeout")
	}
}

type protocolProxy struct{ wlturbo.BaseProxy }

func (*protocolProxy) Dispatch(*wlturbo.Event) {}

func registerProtocol(t *testing.T, c *wlturbo.Display, id uint32) {
	t.Helper()
	p := &protocolProxy{}
	p.SetID(id)
	c.Context().Register(p)
}

func TestXDGProtocolErrors(t *testing.T) {
	for _, scenario := range []string{"bogus ack", "buffer before ack", "subsurface role", "defunct toplevel"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			compositor := bindProtocol(t, c, "wl_compositor")
			wm := bindProtocol(t, c, "xdg_wm_base")
			surface := c.AllocateID()
			requestProtocol(t, c, compositor, wayland.CompositorRequestCreateSurface, surface)
			registerProtocol(t, c, surface)
			if scenario == "subsurface role" {
				parent := c.AllocateID()
				requestProtocol(t, c, compositor, wayland.CompositorRequestCreateSurface, parent)
				registerProtocol(t, c, parent)
				subcomp := bindProtocol(t, c, "wl_subcompositor")
				requestProtocol(t, c, subcomp, wayland.SubcompositorRequestGetSubsurface, c.AllocateID(), surface, parent)
				requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, c.AllocateID(), surface)
				expectProtocolError(t, c, wm, uint32(xdgshell.WmBaseErrorRole))
				return
			}
			xdg := c.AllocateID()
			requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surface)
			top := c.AllocateID()
			registerProtocol(t, c, xdg)
			registerProtocol(t, c, top)
			requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
			switch scenario {
			case "bogus ack":
				requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, uint32(0))
				expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorInvalidSerial))
			case "buffer before ack":
				shm := bindProtocol(t, c, "wl_shm")
				fd, err := unix.MemfdCreate("protocol-buffer", 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				if err := unix.Ftruncate(fd, 4); err != nil {
					t.Fatal(err)
				}
				pool := c.AllocateID()
				registerProtocol(t, c, shm)
				if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
					t.Fatal(err)
				}
				buffer := c.AllocateID()
				registerProtocol(t, c, pool)
				registerProtocol(t, c, buffer)
				requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
				requestProtocol(t, c, surface, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
				requestProtocol(t, c, surface, wayland.SurfaceRequestCommit)
				expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorUnconfiguredBuffer))
			case "defunct toplevel":
				requestProtocol(t, c, xdg, xdgshell.SurfaceRequestDestroy)
				expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorDefunctRoleObject))
			}
		})
	}
}

func TestPopupBlocksSecondRole(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	registerProtocol(t, c, xdg)
	registerProtocol(t, c, surf)
	positioner := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, positioner)
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetSize, int32(10), int32(10))
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetAnchorRect, int32(0), int32(0), int32(1), int32(1))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	popup := c.AllocateID()
	registerProtocol(t, c, popup)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetPopup, popup, uint32(0), positioner)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, c.AllocateID())
	expectProtocolError(t, c, xdg, uint32(xdgshell.SurfaceErrorAlreadyConstructed))
}

type configureProxy struct {
	wlturbo.BaseProxy
	serial chan uint32
}

func (p *configureProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(xdgshell.SurfaceEventConfigure) {
		p.serial <- e.Uint32()
	}
}

func TestRecreatedToplevelRequiresNewBuffer(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 4)
	proxy := &configureProxy{serial: serials}
	proxy.SetID(xdg)
	c.Context().Register(proxy)
	fd, err := unix.MemfdCreate("recreated-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, shm)
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	case <-time.After(2 * time.Second):
		t.Fatal("initial configure timeout")
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	mapped(t, events, 2*time.Second)
	requestProtocol(t, c, top, xdgshell.ToplevelRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if _, ok := ev.(ports.WindowUnmapped); !ok {
			t.Fatalf("expected unmap, got %T", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unmap timeout")
	}
	top = c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	case <-time.After(2 * time.Second):
		t.Fatal("recreated configure timeout")
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("mapped without new buffer: %T", ev)
	case <-time.After(300 * time.Millisecond):
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	mapped(t, events, 2*time.Second)
}

func TestSHMInvalidStride(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	shm := bindProtocol(t, c, "wl_shm")
	fd, err := unix.MemfdCreate("invalid-stride", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 16); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	registerProtocol(t, c, shm)
	registerProtocol(t, c, pool)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(16)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, c.AllocateID(), int32(0), int32(2), int32(1), int32(4), uint32(wayland.ShmFormatArgb8888))
	expectProtocolError(t, c, shm, uint32(wayland.ShmErrorInvalidStride))
}

func TestSHMUnpaddedLastRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int32
		valid bool
	}{{"exact", 28, true}, {"short", 27, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			shm := bindProtocol(t, c, "wl_shm")
			fd, err := unix.MemfdCreate("unpadded-last-row", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if err := unix.Ftruncate(fd, int64(tc.size)); err != nil {
				t.Fatal(err)
			}
			pool := c.AllocateID()
			registerProtocol(t, c, shm)
			registerProtocol(t, c, pool)
			if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, tc.size); err != nil {
				t.Fatal(err)
			}
			buffer := c.AllocateID()
			registerProtocol(t, c, buffer)
			requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(4), int32(2), int32(2), int32(16), uint32(wayland.ShmFormatXrgb8888))
			if !tc.valid {
				expectProtocolError(t, c, shm, uint32(wayland.ShmErrorInvalidStride))
				return
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A buffer whose pool file was shrunk below it has no content.
func TestBufferContentTruncatedPool(t *testing.T) {
	fd, err := unix.MemfdCreate("truncated-pool", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "truncated-pool")
	defer f.Close()
	if err := f.Truncate(28); err != nil {
		t.Fatal(err)
	}
	b := &buffer{pool: &pool{id: 3, file: f, size: 28}, offset: 4, width: 2, height: 2, stride: 16}
	c, ok := b.content(1)
	if !ok || c.SHM == nil || c.SHM.Pool != 3 || c.SHM.Offset != 4 || c.SHM.Stride != 16 {
		t.Fatalf("content ok=%v %+v", ok, c.SHM)
	}
	if err := f.Truncate(27); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.content(1); ok {
		t.Fatal("content from a truncated pool")
	}
}

type releaseProxy struct {
	wlturbo.BaseProxy
	released chan uint32
}

func (p *releaseProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.BufferEventRelease) {
		p.released <- p.ID()
	}
}

// wl_shm buffers are read in place: released when replaced or when their
// surface is destroyed, never on commit.
func TestSHMBufferReleasedOnReplaceAndDestroy(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	shm := bindProtocol(t, c, "wl_shm")
	fd, err := unix.MemfdCreate("release", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 64); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	registerProtocol(t, c, shm)
	registerProtocol(t, c, pool)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(64)); err != nil {
		t.Fatal(err)
	}
	released := make(chan uint32, 4)
	bufs := make([]uint32, 2)
	for i := range bufs {
		bufs[i] = c.AllocateID()
		p := &releaseProxy{released: released}
		p.SetID(bufs[i])
		c.Context().Register(p)
		requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, bufs[i], int32(i*32), int32(2), int32(2), int32(8), uint32(wayland.ShmFormatXrgb8888))
	}
	surf := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	expect := func(want []uint32) {
		t.Helper()
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		var got []uint32
		for len(released) > 0 {
			got = append(got, <-released)
		}
		if len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
			t.Fatalf("released %v, want %v", got, want)
		}
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, bufs[0], int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	expect(nil)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, bufs[1], int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	expect([]uint32{bufs[0]})
	requestProtocol(t, c, surf, wayland.SurfaceRequestDestroy)
	expect([]uint32{bufs[1]})
}

type pointerEvents struct {
	wlturbo.BaseProxy
	enters  chan [2]float64
	buttons chan uint32
	axes    chan string
}

func (p *pointerEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.PointerEventEnter):
		_ = e.Uint32()
		_ = e.Uint32()
		p.enters <- [2]float64{e.Fixed().Float64(), e.Fixed().Float64()}
	case uint16(wayland.PointerEventButton):
		_ = e.Uint32()
		_ = e.Uint32()
		p.buttons <- e.Uint32()
	case uint16(wayland.PointerEventAxisSource):
		p.axes <- fmt.Sprint("source ", e.Uint32())
	case uint16(wayland.PointerEventAxisValue120):
		p.axes <- fmt.Sprint("v120 ", e.Uint32(), " ", e.Int32())
	case uint16(wayland.PointerEventAxis):
		_ = e.Uint32()
		p.axes <- fmt.Sprint("axis ", e.Uint32(), " ", e.Fixed().Float64())
	case uint16(wayland.PointerEventAxisStop):
		_ = e.Uint32()
		p.axes <- fmt.Sprint("stop ", e.Uint32())
	}
}

func TestPointerProtocol(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	seat := bindVersion(t, c, "wl_seat", 8)
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	p := &pointerEvents{enters: make(chan [2]float64, 1), buttons: make(chan uint32, 8), axes: make(chan string, 16)}
	p.SetID(pointer)
	c.Context().Register(p)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 1)
	xp := &configureProxy{serial: serials}
	xp.SetID(xdg)
	c.Context().Register(xp)
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	fd, err := unix.MemfdCreate("pointer-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	w := mapped(t, events, 2*time.Second)
	commands <- ports.PointerFocus{ID: w.ID, X: 10, Y: 20}
	commands <- ports.PointerButtonTo{ID: w.ID, Button: 0x110, Pressed: true, TimeMsec: 1}
	// Middle, side and extra buttons pass through as evdev codes.
	for _, b := range []uint32{0x112, 0x113, 0x114} {
		commands <- ports.PointerButtonTo{ID: w.ID, Button: b, Pressed: true, TimeMsec: 1}
	}
	commands <- ports.PointerAxisTo{ID: w.ID, Axis: ports.PointerAxis{Source: ports.AxisWheel, Vertical: ports.ScrollAxis{Set: true, Value: 15, V120: 120}, TimeMsec: 2}}
	commands <- ports.PointerAxisTo{ID: w.ID, Axis: ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Stop: true}, TimeMsec: 3}}
	deadline := time.After(2 * time.Second)
	dispatched := make(chan error, 1)
	go func() {
		for {
			if err := c.Dispatch(); err != nil {
				dispatched <- err
				return
			}
		}
	}()
	for len(p.enters) == 0 || len(p.buttons) < 4 || len(p.axes) < 5 {
		select {
		case err := <-dispatched:
			t.Fatal(err)
		case <-deadline:
			t.Fatal("pointer events timeout")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case coords := <-p.enters:
		if coords != [2]float64{10, 20} {
			t.Fatalf("enter: %v", coords)
		}
	default:
		t.Fatal("missing pointer enter")
	}
	for _, want := range []uint32{0x110, 0x112, 0x113, 0x114} {
		if b := <-p.buttons; b != want {
			t.Fatalf("button: %#x, want %#x", b, want)
		}
	}
	for _, want := range []string{"source 0", "v120 0 120", "axis 0 15", "source 1", "stop 1"} {
		if got := <-p.axes; got != want {
			t.Fatalf("axis event %q, want %q", got, want)
		}
	}
}

type heldKeyProxy struct {
	wlturbo.BaseProxy
	enters chan []byte
}

func (p *heldKeyProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.KeyboardEventEnter) {
		_ = e.Uint32()
		_ = e.Uint32()
		p.enters <- e.Array()
	}
}

func TestHeldKeyOnKeyboardFocusTransfer(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	keyboard := c.AllocateID()
	kp := &heldKeyProxy{enters: make(chan []byte, 4)}
	kp.SetID(keyboard)
	c.Context().Register(kp)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, keyboard)
	mapWindow := toplevelMapper(t, c, events)
	a := mapWindow().ID
	b := mapWindow().ID
	// Dispatch without sending a new request after focus changes.
	dispatched := make(chan error, 1)
	go func() {
		for {
			if err := c.Dispatch(); err != nil {
				dispatched <- err
				return
			}
		}
	}()
	enter := func(want []byte) {
		t.Helper()
		select {
		case got := <-kp.enters:
			if !bytes.Equal(got, want) {
				t.Fatalf("keyboard enter keys = %v, want %v", got, want)
			}
		case err := <-dispatched:
			t.Fatal(err)
		case <-time.After(2 * time.Second):
			t.Fatal("keyboard enter timeout")
		}
	}
	commands <- ports.FocusWindow{ID: a}
	enter(nil)
	commands <- ports.ForwardKey{ID: a, Key: ports.KeyEvent{Keycode: 30, Pressed: true, TimeMsec: 1}}
	commands <- ports.FocusWindow{ID: b}
	enter([]byte{30, 0, 0, 0})
	commands <- ports.ForwardKey{ID: b, Key: ports.KeyEvent{Keycode: 30, TimeMsec: 2}}
	commands <- ports.FocusWindow{ID: a}
	enter(nil)
}

// toplevelMapper returns a function that maps a new 1x1 xdg toplevel on c and
// returns the WindowMapped event it produced.
func toplevelMapper(t *testing.T, c *wlturbo.Display, events <-chan ports.ClientEvent) func() ports.WindowMapped {
	t.Helper()
	m := surfaceMapper(t, c, events)
	return func() ports.WindowMapped { w, _, _ := m(); return w }
}

// surfaceMapper is toplevelMapper also returning the wl_surface and
// xdg_surface IDs.
func surfaceMapper(t *testing.T, c *wlturbo.Display, events <-chan ports.ClientEvent) func() (ports.WindowMapped, uint32, uint32) {
	t.Helper()
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("held-key-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))
	return func() (ports.WindowMapped, uint32, uint32) {
		surf, xdg, top := c.AllocateID(), c.AllocateID(), c.AllocateID()
		requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
		registerProtocol(t, c, surf)
		requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
		serials := make(chan uint32, 1)
		xp := &configureProxy{serial: serials}
		xp.SetID(xdg)
		c.Context().Register(xp)
		registerProtocol(t, c, top)
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case serial := <-serials:
			requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
		default:
			t.Fatal("missing configure")
		}
		requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		return mapped(t, events, 2*time.Second), surf, xdg
	}
}

type popupDoneProxy struct {
	wlturbo.BaseProxy
	done chan struct{}
}

func (p *popupDoneProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(xdgshell.PopupEventPopupDone) {
		p.done <- struct{}{}
	}
}

// xdgWindow maps a 1×1 xdg_surface; role sets its role before the first commit.
func xdgWindow(t *testing.T, c *wlturbo.Display, comp, wm, buffer uint32, role func(xdg uint32)) (surf, xdg uint32, serials chan uint32) {
	t.Helper()
	surf, xdg = c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	registerProtocol(t, c, surf)
	serials = make(chan uint32, 4)
	proxy := &configureProxy{serial: serials}
	proxy.SetID(xdg)
	c.Context().Register(proxy)
	role(xdg)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return surf, xdg, serials
}

func ackAndAttach(t *testing.T, c *wlturbo.Display, surf, xdg, buffer uint32, serials chan uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(serials) == 0 && time.Now().Before(deadline) {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	default:
		t.Fatal("configure timeout")
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

func TestPopupAndFloatingLifecycle(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("popup-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))

	// A fixed-size toplevel (min == max) floats, like a splash screen.
	surf, xdg, serials := xdgWindow(t, c, comp, wm, buffer, func(xdg uint32) {
		top := c.AllocateID()
		registerProtocol(t, c, top)
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
		requestProtocol(t, c, top, xdgshell.ToplevelRequestSetMinSize, int32(1), int32(1))
		requestProtocol(t, c, top, xdgshell.ToplevelRequestSetMaxSize, int32(1), int32(1))
	})
	ackAndAttach(t, c, surf, xdg, buffer, serials)
	top := mapped(t, events, 2*time.Second)
	if !top.Floating {
		t.Fatalf("splash not floating: %+v", top)
	}
	if ev, ok := waitEvent(t, events, 2*time.Second).(ports.WindowResized); !ok || ev.Width != 1 || ev.Height != 1 {
		t.Fatalf("splash size %+v", ev)
	}

	positioner := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, positioner)
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetSize, int32(1), int32(1))
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetAnchorRect, int32(0), int32(0), int32(1), int32(1))
	done := make(chan struct{}, 1)
	psurf, pxdg, pserials := xdgWindow(t, c, comp, wm, buffer, func(pxdg uint32) {
		popup := c.AllocateID()
		proxy := &popupDoneProxy{done: done}
		proxy.SetID(popup)
		c.Context().Register(proxy)
		requestProtocol(t, c, pxdg, xdgshell.SurfaceRequestGetPopup, popup, xdg, positioner)
	})
	req, ok := waitEvent(t, events, 2*time.Second).(ports.PopupRequest)
	if !ok || req.Parent != top.ID || req.Positioner.Width != 1 {
		t.Fatalf("popup request %+v", req)
	}
	commands <- ports.ConfigurePopup{ID: req.ID, Rect: ports.Rect{X: 2, Y: 3, W: 1, H: 1}}
	ackAndAttach(t, c, psurf, pxdg, buffer, pserials)
	if ev, ok := waitEvent(t, events, 2*time.Second).(ports.PopupMapped); !ok || ev.ID != req.ID {
		t.Fatalf("popup mapped %+v", ev)
	}
	commands <- ports.ClosePopup{ID: req.ID}
	for deadline := time.Now().Add(2 * time.Second); len(done) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("popup_done timeout")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPopupIncompletePositioner(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	surf, xdg := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	registerProtocol(t, c, xdg)
	positioner := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, positioner)
	requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetSize, int32(10), int32(10))
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetPopup, c.AllocateID(), uint32(0), positioner)
	expectProtocolError(t, c, wm, uint32(xdgshell.WmBaseErrorInvalidPositioner))
}
