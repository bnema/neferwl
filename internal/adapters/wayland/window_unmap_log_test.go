package wayland

import (
	"strings"
	"testing"
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// windowLogs returns the "window unmapped" and "window closed" entries
// logged so far.
func windowLogs(out *lockedBuffer) []string {
	var logs []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.Contains(l, `"window unmapped"`) || strings.Contains(l, `"window closed"`) {
			logs = append(logs, l)
		}
	}
	return logs
}

func requireWindowLogs(t *testing.T, out *lockedBuffer, want ...[]string) {
	t.Helper()
	logs := windowLogs(out)
	if len(logs) != len(want) {
		t.Fatalf("%d window entries, want %d: %q", len(logs), len(want), logs)
	}
	for i, parts := range want {
		for _, p := range append([]string{`"component":"wayland"`, `"level":"info"`, `"app_id":""`}, parts...) {
			if !strings.Contains(logs[i], p) {
				t.Errorf("entry %d lacks %s: %s", i, p, logs[i])
			}
		}
	}
}

// A mapped toplevel logs "window unmapped" when hidden and "window
// closed" once when destroyed, telling whether the app closed it or its
// client went away; a popup logs nothing.
func TestWindowCloseLogs(t *testing.T) {
	unmapped := []string{`"message":"window unmapped"`}
	closed := []string{`"message":"window closed"`, `"reason":"closed"`}
	gone := []string{`"message":"window closed"`, `"reason":"client_disconnected"`}
	for _, tc := range []struct {
		name       string
		nullBuffer bool
		disconnect bool
		want       [][]string
	}{
		{"closed", false, false, [][]string{closed}},
		{"client_disconnected", false, true, [][]string{gone}},
		{"null_buffer_then_destroy", true, false, [][]string{unmapped, closed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out lockedBuffer
			log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")
			s, events, commands, _, dir := contentServerWith(t, Options{}, log)
			c := protocolClient(t, s, dir)
			win, surf, xdg := surfaceMapperAt(t, c, events, 4)()
			top := xdg + 1 // allocated right after its xdg_surface

			// A popup of the window maps and unmaps without a log.
			comp := bindProtocol(t, c, "wl_compositor")
			wm := bindProtocol(t, c, "xdg_wm_base")
			buffer := shmBuffer(t, c)
			positioner := c.AllocateID()
			requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, positioner)
			requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetSize, int32(1), int32(1))
			requestProtocol(t, c, positioner, xdgshell.PositionerRequestSetAnchorRect, int32(0), int32(0), int32(1), int32(1))
			psurf, pxdg, pserials := xdgWindow(t, c, comp, wm, buffer, func(pxdg uint32) {
				popup := c.AllocateID()
				registerProtocol(t, c, popup)
				requestProtocol(t, c, pxdg, xdgshell.SurfaceRequestGetPopup, popup, xdg, positioner)
			})
			req := clientEvent[ports.PopupRequest](t, events)
			commands <- ports.ConfigurePopup{ID: req.ID, Rect: domain.Rect{W: 1, H: 1}}
			ackAndAttach(t, c, psurf, pxdg, buffer, pserials)
			clientEvent[ports.PopupMapped](t, events)
			requestProtocol(t, c, psurf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
			requestProtocol(t, c, psurf, wayland.SurfaceRequestCommit)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			requireWindowLogs(t, &out)

			if tc.nullBuffer {
				// What foot does before destroying its toplevel.
				requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
				requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
			}
			if tc.disconnect {
				_ = c.Close()
			} else {
				requestProtocol(t, c, top, xdgshell.ToplevelRequestDestroy)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
			}
			// The logs precede the event on the display goroutine.
			for {
				ev, ok := waitEvent(t, events, 2*time.Second).(ports.WindowDestroyed)
				if ok && ev.ID == win.ID {
					break
				}
			}
			requireWindowLogs(t, &out, tc.want...)
		})
	}
}
