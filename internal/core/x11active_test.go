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
	r := keepFocusStart(t, keep)
	r.client <- ports.WindowMapped{ID: nativeID, AppID: "foot"}
	r.settle(t)
	return r
}

// keepFocusStart is keepFocusRig before the native window maps: steamID
// is focused on tv, the focused output. F1 stashes the focused window, F2
// toggles the overview, F3 toggles fullscreen, F4 shows or hides the stash.
func keepFocusStart(t *testing.T, keep bool) *dragRig {
	t.Helper()
	r := startDragRig(t, func(c *domain.Config) {
		c.Binds["F1"] = "toggle-window-stash"
		c.Binds["F2"] = "toggle-overview"
		c.Binds["F3"] = "toggle-fullscreen"
		c.Binds["F4"] = "toggle-stash-visible"
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
	// This fixture promises tv owns the seat; placement no longer implies it.
	r.client <- ports.WindowActivate{ID: steamID}
	r.settle(t)
	r.drain()
	return r
}

// tap presses and releases an unmodified key.
func (r *dragRig) tap(t *testing.T, sym string) {
	t.Helper()
	r.in(ports.KeyEvent{Keysym: sym, Pressed: true})
	r.in(ports.KeyEvent{Keysym: sym})
	r.settle(t)
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

// focusNative moves the keyboard to nativeID (already mapped on DP-1),
// as a click or an activation would.
func (r *dragRig) focusNative(t *testing.T) []ports.ClientCommand {
	t.Helper()
	r.client <- ports.WindowActivate{ID: nativeID}
	r.settle(t)
	cmds := r.drain()
	if got := x11Changes(cmds); len(got) > 0 {
		x11Seen[r] = got[len(got)-1]
	}
	if got := focusChanges(cmds); len(got) != 1 || got[0] != nativeID {
		t.Fatalf("keyboard focus %v, want [%d]", got, nativeID)
	}
	return cmds
}

// A stashed tv window, hidden with the stash, is not held; shown again it is.
func TestKeepFocusStashedWindowNotHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusStart(t, true)
		x11Seen[r] = steamID
		r.tap(t, "F1") // into the stash, shown
		r.tap(t, "F4") // hide the stash
		if hidden := sceneHidden(r.last, "tv", steamID); !hidden {
			t.Fatal("stash not hidden")
		}
		r.client <- ports.WindowMapped{ID: nativeID, AppID: "foot"}
		r.settle(t)
		if got := lastX11(r); got != 0 {
			t.Fatalf("X11-active %d with the tv window hidden, want 0", got)
		}
		r.client <- ports.WindowActivate{ID: steamID}
		r.settle(t)
		if hidden := sceneHidden(r.last, "tv", steamID); hidden {
			t.Fatal("activating the stashed window did not show it")
		}
		lastX11(r)
		r.focusNative(t)
		if hidden := sceneHidden(r.last, "tv", steamID); hidden {
			t.Fatal("the tv window hid again when the native window took the keyboard")
		}
		if got := x11Seen[r]; got != steamID {
			t.Fatalf("X11-active %d once shown again, want %d", got, steamID)
		}
	})
}

// A fullscreen X11 window on tv stays held under native focus elsewhere.
func TestKeepFocusFullscreenWindowHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusStart(t, true)
		r.client <- ports.WindowMapped{ID: nativeID, AppID: "foot"}
		r.settle(t)
		r.client <- ports.WindowActivate{ID: steamID}
		r.settle(t)
		r.tap(t, "F3")
		r.drain()
		var full bool
		for _, w := range dragScene(r.last, "tv").Windows {
			full = full || w.ID == steamID && w.Fullscreen && !w.Hidden
		}
		if !full {
			t.Fatal("tv window not fullscreen")
		}
		cmds := r.focusNative(t)
		if got := x11Changes(cmds); len(got) != 0 {
			t.Fatalf("X11Active %v: the fullscreen tv window must stay held", got)
		}
		if a, ok := lastActivated(cmds, steamID); ok && !a {
			t.Fatal("held fullscreen window deactivated")
		}
	})
}

// The overview on another output leaves the hold. On tv (focused, so the
// X11 window has the keyboard) the window stays X11-active as the keyboard
// focus, its preview notwithstanding.
func TestKeepFocusOverview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		r.drain()
		x11Seen[r] = steamID
		r.tap(t, "F2") // overview on DP-1, the focused output
		if got := x11Changes(r.drain()); len(got) != 0 {
			t.Fatalf("X11Active %v with the overview open on DP-1", got)
		}
		r.tap(t, "F2")
		r.drain()
		r.client <- ports.WindowActivate{ID: steamID}
		r.settle(t)
		r.tap(t, "F2") // overview on tv
		var preview bool
		for _, w := range dragScene(r.last, "tv").Windows {
			preview = preview || w.ID == steamID && w.Preview > 0
		}
		if !preview {
			t.Fatal("tv window not a preview in its overview")
		}
		if got := lastX11(r); got != steamID {
			t.Fatalf("X11-active %d with tv in the overview, want %d", got, steamID)
		}
		r.tap(t, "F2")
		if got := lastX11(r); got != steamID {
			t.Fatalf("X11-active %d after the overview closed, want %d", got, steamID)
		}
	})
}

// Removing tv moves its windows to DP-1, which keeps nothing: the keyboard
// stays on the native window and no X11 window is X11-active.
func TestKeepFocusOutputRemoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		r.drain()
		r.output <- ports.OutputRemoved{Name: "tv"}
		r.settle(t)
		cmds := r.drain()
		if got := focusChanges(cmds); len(got) != 0 {
			t.Fatalf("keyboard focus moved to %v, want it kept on %d", got, nativeID)
		}
		if got := x11Changes(cmds); len(got) != 1 || got[0] != 0 {
			t.Fatalf("X11Active %v after tv was removed, want [0]", got)
		}
	})
}

// GO-001: a lock and unlock core never observed protected (the gate moved
// from generation 0 to 2 between two observations) still resets the
// display's X11 focus, so core sends X11Active again, unchanged or not.
func TestKeepFocusRestoredAfterUnseenLockCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := keepFocusRig(t, true)
		r.drain()
		r.epoch.Store(domain.SecurityState{Generation: 1, Protected: true})
		r.epoch.Store(domain.SecurityState{Generation: 2})
		r.client <- ports.SessionLockChanged{State: domain.SecurityState{Generation: 2}}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != steamID {
			t.Fatalf("X11Active %v after an unseen lock cycle, want [%d]", got, steamID)
		}
	})
}

// sceneHidden reports whether id is missing from output's scene or hidden.
func sceneHidden(set []domain.Scene, output string, id domain.WindowID) bool {
	for _, w := range dragScene(set, output).Windows {
		if w.ID == id {
			return w.Hidden
		}
	}
	return true
}

// x11Seen tracks the last X11Active per rig, across drains.
var x11Seen = map[*dragRig]domain.WindowID{}

// lastX11 is the X11-active window as last sent, draining the commands.
func lastX11(r *dragRig) domain.WindowID {
	if got := x11Changes(r.drain()); len(got) > 0 {
		x11Seen[r] = got[len(got)-1]
	}
	return x11Seen[r]
}

// Mapping Steam must not depend on a subsequent pointer or keyboard event.
func TestKeepFocusIdleFullscreenMap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) {
			c.Outputs = []domain.OutputConfig{{Name: "DP-1"}, {Name: "tv", Virtual: true, KeepFocus: true}}
			c.Rules = []domain.WindowRule{winRule("steam", "steam", func(r *domain.WindowRule) { r.Output = "tv" })}
		}, domain.OutputInfo{Name: "DP-1", Width: 800, Height: 600}, domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
		r.client <- ports.WindowMapped{ID: nativeID, AppID: "foot"}
		r.settle(t)
		r.drain()
		r.client <- ports.WindowMapped{ID: steamID, AppID: "steam", Xwayland: true, Fullscreen: true}
		r.settle(t)
		if got := x11Changes(r.drain()); len(got) != 1 || got[0] != steamID {
			t.Fatalf("idle map: X11Active %v, want [%d]", got, steamID)
		}
		x11Seen[r] = steamID
		r.client <- ports.WindowResized{ID: steamID, Width: 640, Height: 360}
		r.settle(t)
		if got := lastX11(r); got != steamID {
			t.Fatalf("idle resize: X11Active %d, want %d", got, steamID)
		}
	})
}
