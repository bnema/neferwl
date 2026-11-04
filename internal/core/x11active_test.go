package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// x11Changes lists the X11Active commands among cmds.
func x11Changes(cmds []ports.ClientCommand) (ids []domain.WindowID) {
	for _, v := range cmds {
		if a, ok := v.(ports.X11Active); ok {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// lastActivated is the Activated flag of the last configure of id in cmds.
func lastActivated(cmds []ports.ClientCommand, id domain.WindowID) (activated, ok bool) {
	for _, v := range cmds {
		if c, is := v.(ports.ConfigureWindow); is && c.ID == id {
			activated, ok = c.Activated, true
		}
	}
	return activated, ok
}

const (
	steamID  domain.WindowID = 1 // X11, ruled to tv
	nativeID domain.WindowID = 2 // native, ruled to DP-1
	xdeskID  domain.WindowID = 3 // X11, ruled to DP-1
)

// keepFocusRig runs DP-1 and a virtual tv with keep-focus as given, the
// X11 window steamID mapped on tv and the native window nativeID on DP-1,
// which holds the keyboard.
func keepFocusRig(t *testing.T, keep bool) *dragRig {
	t.Helper()
	r := startDragRig(t, func(c *domain.Config) {
		c.Outputs = []domain.OutputConfig{{Name: "DP-1"}, {Name: "tv", Virtual: true, KeepFocus: keep}}
		c.Rules = []domain.WindowRule{
			winRule("steam", "steam", func(r *domain.WindowRule) { r.Output = "tv" }),
			winRule("desk", "foot|xterm", func(r *domain.WindowRule) { r.Output = "DP-1" }),
		}
	}, domain.OutputInfo{Name: "DP-1", Width: 800, Height: 600}, domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
	r.client <- ports.WindowMapped{ID: steamID, AppID: "steam", Xwayland: true}
	r.settle(t)
	if got := x11Changes(r.drain()); len(got) != 1 || got[0] != steamID {
		t.Fatalf("X11 window focused on tv: X11Active %v, want [%d]", got, steamID)
	}
	r.client <- ports.WindowMapped{ID: nativeID, AppID: "foot"}
	r.settle(t)
	return r
}

func TestKeepFocusHoldsTVWindowUnderNativeFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		cmds := r.drain()
		if got := focusChanges(cmds); len(got) != 1 || got[0] != nativeID {
			t.Fatalf("keyboard focus %v, want [%d]", got, nativeID)
		}
		if got := x11Changes(cmds); len(got) != 0 {
			t.Fatalf("X11Active %v: tv window must stay active", got)
		}
		if a, ok := lastActivated(cmds, steamID); ok && !a {
			t.Fatal("held tv window deactivated")
		}
		for _, w := range dragScene(r.last, "tv").Windows {
			if w.ID == steamID && w.Focused {
				t.Fatal("held tv window drawn focused")
			}
		}
	})
}

func TestKeepFocusOffDropsX11Focus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, false)
		cmds := r.drain()
		if got := x11Changes(cmds); len(got) != 1 || got[0] != 0 {
			t.Fatalf("X11Active %v, want [0]", got)
		}
		if a, ok := lastActivated(cmds, steamID); !ok || a {
			t.Fatalf("tv window activated %v (configured %v), want deactivated", a, ok)
		}
	})
}

func TestKeepFocusX11DesktopFocusWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		r.drain()
		r.client <- ports.WindowMapped{ID: xdeskID, AppID: "xterm", Xwayland: true}
		r.settle(t)
		cmds := r.drain()
		if got := x11Changes(cmds); len(got) != 1 || got[0] != xdeskID {
			t.Fatalf("X11Active %v, want [%d]", got, xdeskID)
		}
		if a, ok := lastActivated(cmds, steamID); !ok || a {
			t.Fatal("tv window must be deactivated while another X11 window has the keyboard")
		}
		r.client <- ports.WindowUnmapped{ID: xdeskID}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != steamID {
			t.Fatalf("X11Active %v after the desktop X11 window closed, want [%d]", got, steamID)
		}
	})
}

func TestKeepFocusLockClears(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		r.drain()
		locked := domain.SecurityState{Generation: 1, Protected: true}
		r.epoch.Store(locked)
		r.client <- ports.SessionLockChanged{State: locked}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != 0 {
			t.Fatalf("X11Active %v under lock, want [0]", got)
		}
		unlocked := domain.SecurityState{Generation: 2}
		r.epoch.Store(unlocked)
		r.client <- ports.SessionLockChanged{State: unlocked}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != steamID {
			t.Fatalf("X11Active %v after unlock, want [%d]", got, steamID)
		}
	})
}

func TestKeepFocusTVWindowCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		r.drain()
		r.client <- ports.WindowUnmapped{ID: steamID}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != 0 {
			t.Fatalf("X11Active %v after the tv window closed, want [0]", got)
		}
	})
}

func TestKeepFocusReloadToggles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, false)
		r.drain()
		cfg := r.cfg
		cfg.Outputs = []domain.OutputConfig{{Name: "DP-1"}, {Name: "tv", Virtual: true, KeepFocus: true}}
		r.reload <- domain.ConfigChanged{Config: cfg}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != steamID {
			t.Fatalf("X11Active %v after keep-focus on, want [%d]", got, steamID)
		}
		cfg.Outputs = []domain.OutputConfig{{Name: "DP-1"}, {Name: "tv", Virtual: true}}
		r.reload <- domain.ConfigChanged{Config: cfg}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != 0 {
			t.Fatalf("X11Active %v after keep-focus off, want [0]", got)
		}
	})
}
