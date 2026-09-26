package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"golang.org/x/sys/unix"
)

// Damage reaches the renderer in buffer pixels, per content, with the
// window's recent history; a new buffer without damage is a full change.
func TestDamageHistory(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindVersion(t, c, "wl_compositor", 4)
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	surf, xdg, top := c.AllocateID(), c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 8)
	xp := &configureProxy{serial: serials}
	xp.SetID(xdg)
	c.Context().Register(xp)
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	fd, err := unix.MemfdCreate("damage", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 2*16*16*4); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	registerProtocol(t, c, pool)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(2*16*16*4)); err != nil {
		t.Fatal(err)
	}
	bufs := [2]uint32{c.AllocateID(), c.AllocateID()}
	for i, b := range bufs {
		registerProtocol(t, c, b)
		requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, b, int32(i*16*16*4), int32(16), int32(16), int32(64), uint32(0))
	}
	next := func() ports.SurfaceContent {
		t.Helper()
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(2 * time.Second)
		for {
			select {
			case got := <-contents:
				if got.Width == 16 {
					return got
				}
			case <-deadline:
				t.Fatal("no content")
			}
		}
	}
	// A new size: full.
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, bufs[0], int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestDamageBuffer, int32(0), int32(0), int32(4), int32(4))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	mapped(t, events, 2*time.Second)
	first := next()
	if h := first.DamageHistory; len(h) == 0 || h[len(h)-1].Seq != first.Seq || !h[len(h)-1].Full {
		t.Fatalf("resize: %+v", h)
	}
	// Buffer and surface damage, clipped to the buffer.
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, bufs[1], int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestDamageBuffer, int32(2), int32(3), int32(4), int32(5))
	requestProtocol(t, c, surf, wayland.SurfaceRequestDamage, int32(14), int32(14), int32(1<<30), int32(1<<30))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	second := next()
	got, ok := second.DamageSince(first.Seq)
	want := []ports.Rect{{X: 2, Y: 3, W: 4, H: 5}, {X: 14, Y: 14, W: 2, H: 2}}
	if !ok || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("damage %v %v", got, ok)
	}
	// No damage with a new buffer: full.
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, bufs[0], int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	third := next()
	if _, ok := third.DamageSince(second.Seq); ok {
		t.Fatal("undamaged new buffer taken as unchanged")
	}
}
