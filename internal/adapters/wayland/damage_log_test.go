package wayland

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/zerowrap"
)

// --debug=wayland-damage logs one "client damage" entry per fresh commit
// with the damage in buffer pixels; without it nothing is logged.
//
//testpolicy:realtime real Wayland unix socket between the display loop and a wlturbo client; kernel readiness is not a durably blocked bubble operation
func TestClientDamageLog(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("LogDamage=%v", on), func(t *testing.T) {
			var out lockedBuffer
			log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &out}).WithField("component", "wayland")
			s, events, _, contents, dir := contentServerWith(t, Options{LogDamage: on}, log)
			c := protocolClient(t, s, dir)
			// A mapped window (its 1×1 first buffer is not under test), then
			// the 16×16 buffer the commits below attach.
			_, surf, _ := surfaceMapperAt(t, c, events, 4)()
			drainContents(t, s, contents)
			buf := shmBufferSized(t, c, 16, 16)
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
				case <-waylandDeadline(2 * time.Second):
					t.Fatal("no content")
				}
			}
			// First buffer: a new size, full. Then a small rect, then one
			// covering the whole buffer.
			commit([4]int32{0, 0, 4, 4})
			commit([4]int32{2, 3, 4, 5})
			commit([4]int32{0, 0, 16, 16})
			var damage []string
			for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				if strings.Contains(l, `"client damage"`) {
					damage = append(damage, l)
				}
			}
			if !on {
				if len(damage) != 0 {
					t.Fatalf("logged with LogDamage off: %q", damage)
				}
				return
			}
			// The mapping commit logged one entry of its own.
			if len(damage) != 4 {
				t.Fatalf("%d client damage entries, want 4 (mapping commit and 3 commits): %q", len(damage), damage)
			}
			damage = damage[1:]
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
		})
	}
}
