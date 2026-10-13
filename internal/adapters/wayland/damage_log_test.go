package wayland

import (
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// --debug=wayland-damage logs one "client damage" entry per fresh commit
// with the damage in buffer pixels; without it nothing is logged.
func TestClientDamageLog(t *testing.T) {
	for _, on := range []bool{true, false} {
		var out lockedBuffer
		log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &out}).WithField("component", "wayland")
		s, events, _, contents, dir := contentServerWith(t, Options{LogDamage: on}, log)
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
		registerWireProxy(c, xp)
		registerProtocol(t, c, top)
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
		fd, err := unix.MemfdCreate("damagelog", 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if err := unix.Ftruncate(fd, 16*16*4); err != nil {
			t.Fatal(err)
		}
		pool, buf := c.AllocateID(), c.AllocateID()
		registerProtocol(t, c, pool)
		if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(16*16*4)); err != nil {
			t.Fatal(err)
		}
		registerProtocol(t, c, buf)
		requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(16), int32(16), int32(64), uint32(0))
		commit := func(rects ...[4]int32) {
			t.Helper()
			requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
			for _, r := range rects {
				requestProtocol(t, c, surf, wayland.SurfaceRequestDamageBuffer, r[0], r[1], r[2], r[3])
			}
			requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-contents:
			case <-time.After(2 * time.Second):
				t.Fatal("no content")
			}
		}
		// First buffer: a new size, full. Then a small rect, then one
		// covering the whole buffer.
		commit([4]int32{0, 0, 4, 4})
		mapped(t, events, 2*time.Second)
		commit([4]int32{2, 3, 4, 5})
		commit([4]int32{0, 0, 16, 16})
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		var damage []string
		for _, l := range lines {
			if strings.Contains(l, `"client damage"`) {
				damage = append(damage, l)
			}
		}
		if !on {
			if len(damage) != 0 {
				t.Fatalf("logged with LogDamage off: %q", damage)
			}
			continue
		}
		if len(damage) != 3 {
			t.Fatalf("%d client damage entries, want 3: %q", len(damage), damage)
		}
		for i, want := range []string{`"full_buffer":true`, `"full_buffer":false`, `"full_buffer":true`} {
			if !strings.Contains(damage[i], want) {
				t.Errorf("entry %d lacks %s: %s", i, want, damage[i])
			}
		}
		for _, want := range []string{`"component":"wayland"`, `"bw":16`, `"bh":16`, `"rects":1`, `"window":`, `"first_rects":[{"x":2,"y":3,"w":4,"h":5}]`} {
			if !strings.Contains(damage[1], want) {
				t.Errorf("entry 1 lacks %s: %s", want, damage[1])
			}
		}
	}
}
