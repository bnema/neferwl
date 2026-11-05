package app

import (
	"encoding/binary"
	"testing"
	"time"

	"bnema.dev/wlturbo"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	clientxdg "github.com/bnema/go-wayland-bindings/client/xdgshell"
	"golang.org/x/sys/unix"
)

// pointerTarget is a mapped toplevel showing an opaque red square, with what
// its pointer and keyboard received.
type pointerTarget struct {
	entered, focused bool
	pressed, release bool
	keys             []uint32
	buttons          []uint32 // button codes pressed, in order
	motions          int      // motions while the left button is down
	x, y             float64  // latest surface-local position
	scrollV, scrollH int32    // wheel detents (axis_discrete), summed
}

// mapPointerTarget maps a red toplevel (64x64 buffer; tiled, it fills its tile) on c with a pointer and keyboard.
func mapPointerTarget(t *testing.T, c *wlturbo.Display) *pointerTarget {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(c.Roundtrip())
	comp := clientcore.NewCompositor(c.Context())
	shm := clientcore.NewShm(c.Context())
	seat := clientcore.NewSeat(c.Context())
	wm := clientxdg.NewXdgWmBase(c.Context())
	wm.OnPing(func(serial uint32) { check(wm.Pong(serial)) })
	bindTestClient(t, c, clientcore.CompositorInterface, 1, comp)
	bindTestClient(t, c, clientcore.ShmInterface, 1, shm)
	bindTestClient(t, c, clientcore.SeatInterface, 5, seat)
	bindTestClient(t, c, clientxdg.XdgWmBaseInterface, 1, wm)
	v := &pointerTarget{}
	ptr, err := seat.GetPointer()
	check(err)
	ptr.OnEnter(func(_, _ uint32, x, y wlturbo.Fixed) { v.entered, v.x, v.y = true, x.Float64(), y.Float64() })
	ptr.OnMotion(func(_ uint32, x, y wlturbo.Fixed) {
		v.x, v.y = x.Float64(), y.Float64()
		if v.pressed && !v.release {
			v.motions++
		}
	})
	ptr.OnAxisDiscrete(func(axis uint32, n int32) {
		if axis == 0 {
			v.scrollV += n
		} else {
			v.scrollH += n
		}
	})
	ptr.OnButton(func(_, _, button, state uint32) {
		if state == 1 {
			v.buttons = append(v.buttons, button)
		}
		if button == 0x110 && state == 1 {
			v.pressed = true
		}
		if button == 0x110 && state == 0 && v.pressed {
			v.release = true
		}
	})
	kb, err := seat.GetKeyboard()
	check(err)
	kb.OnKeymap(func(_ uint32, fd *wlturbo.OwnedFD, _ uint32) { _ = fd.Close() })
	kb.OnEnter(func(uint32, uint32, []byte) { v.focused = true })
	kb.OnKey(func(_, _, key, state uint32) {
		if state == 1 {
			v.keys = append(v.keys, key)
		}
	})
	const side = 64
	fd, err := unix.MemfdCreate("pointer-target", 0)
	check(err)
	px := make([]byte, side*side*4)
	for i := 0; i < len(px); i += 4 {
		binary.LittleEndian.PutUint32(px[i:], 0xffff0000) // ARGB red
	}
	if _, err := unix.Write(fd, px); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	pool, err := shm.CreatePool(fd, int32(len(px)))
	if err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	buffer, err := pool.CreateBuffer(0, side, side, side*4, clientcore.FORMAT_XRGB8888)
	check(err)
	surf, err := comp.CreateSurface()
	check(err)
	xdg, err := wm.GetXdgSurface(surf)
	check(err)
	var serial uint32
	xdg.OnConfigure(func(value uint32) { serial = value })
	_, err = xdg.GetToplevel()
	check(err)
	check(surf.Commit())
	dispatchUntil(t, c, 5*time.Second, func() string { return "no configure" }, func() bool { return serial != 0 })
	check(xdg.AckConfigure(serial))
	check(surf.Attach(buffer, 0, 0))
	check(surf.Damage(0, 0, side, side))
	check(surf.Commit())
	return v
}
