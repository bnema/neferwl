package core_test

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

var (
	laptop = domain.OutputInfo{Name: "eDP-1", EDIDID: "aa01", Width: 200, Height: 100}
	extA   = domain.OutputInfo{Name: "DP-5", Make: "Dell", Model: "U27", EDIDID: "bb01", EDIDHash: "h1", Width: 400, Height: 200}
	extB   = func() domain.OutputInfo { o := extA; o.Name = "DP-7"; return o }()
)

// unpluggedExternal leaves window 1 on the laptop and, on a workspace of
// the external (extA), tile 2 with 3 in its hidden stash; the external is
// then unplugged while the laptop is focused.
func unpluggedExternal(t *testing.T, edit func(*domain.Config)) *multiRig {
	t.Helper()
	r := startMulti(t, edit, laptop, extA)
	r.mapWindow(t, 1)
	r.key(t, "Right", domain.ModAlt|domain.ModCtrl)
	r.mapWindow(t, 2)
	r.mapWindow(t, 3)
	r.key(t, "s", domain.ModAlt|domain.ModShift)
	if got := shown(r.key(t, "s", domain.ModAlt)); !slices.Equal(got["DP-5"], []domain.WindowID{2}) {
		t.Fatalf("setup: %v", got)
	}
	r.key(t, "Left", domain.ModAlt|domain.ModCtrl)
	r.output <- ports.OutputRemoved{Name: "DP-5"}
	if got := shown(receive(t, r.scenes)); !slices.Equal(got["eDP-1"], []domain.WindowID{1}) {
		t.Fatalf("after unplug: %v", got)
	}
	return r
}

func sameIDs(got []domain.WindowID, want ...domain.WindowID) bool {
	got = slices.Clone(got)
	slices.Sort(got)
	return slices.Equal(got, want)
}

// The external comes back on another connector without a text serial: its
// workspace, tiles and stash, returns there (AC1).
func TestReplugOnAnotherConnectorReturnsWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := unpluggedExternal(t, nil)
		got := shown(r.plug(t, extB))
		if !slices.Equal(got["DP-7"], []domain.WindowID{2}) || !slices.Equal(got["eDP-1"], []domain.WindowID{1}) {
			t.Fatalf("replug: %v", got)
		}
		r.key(t, "Right", domain.ModAlt|domain.ModCtrl)
		if got := shown(r.key(t, "s", domain.ModAlt)); !sameIDs(got["DP-7"], 2, 3) {
			t.Fatalf("stash on DP-7: %v", got)
		}
	})
}

// An unreadable EDID on the same connector still brings the workspace home
// (AC2).
func TestReplugWithoutIdentityOnSameConnectorReturnsWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := unpluggedExternal(t, nil)
		got := shown(r.plug(t, domain.OutputInfo{Name: "DP-5", Width: 400, Height: 200}))
		if !slices.Equal(got["DP-5"], []domain.WindowID{2}) {
			t.Fatalf("replug: %v", got)
		}
	})
}

// Another known monitor on the same connector takes nothing (AC2).
func TestReplugOfAnotherMonitorOnSameConnectorKeepsWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := unpluggedExternal(t, nil)
		got := shown(r.plug(t, domain.OutputInfo{Name: "DP-5", EDIDID: "cc01", Width: 400, Height: 200}))
		if len(got["DP-5"]) != 0 || !slices.Equal(got["eDP-1"], []domain.WindowID{1}) {
			t.Fatalf("replug: %v", got)
		}
		// The guest waits on the laptop, its stash hidden.
		set := r.key2(t)
		if got := shown(set); !slices.Equal(got["eDP-1"], []domain.WindowID{2}) {
			t.Fatalf("guest on the laptop: %v", got)
		}
	})
}

// key2 presses Cmd+2 (workspace 2) with its keycode.
func (r *multiRig) key2(t *testing.T) []domain.Scene {
	t.Helper()
	r.input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: domain.ModAlt, Pressed: true}
	set := receive(t, r.scenes)
	r.input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: domain.ModAlt}
	return set
}

// A named workspace without a configured monitor returns to the monitor it
// was unplugged from, on another connector too (AC6).
func TestNamedWorkspaceReturnsToItsMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, func(c *domain.Config) {
			c.Workspaces = []domain.WorkspaceConfig{{Name: "web"}}
			c.Binds["Alt+w"] = "workspace web"
		}, laptop, extA)
		r.mapWindow(t, 1)
		r.key(t, "Right", domain.ModAlt|domain.ModCtrl)
		r.key(t, "w", domain.ModAlt)
		if got := shown(r.mapWindow(t, 5)); !slices.Equal(got["DP-5"], []domain.WindowID{5}) {
			t.Fatalf("web on the external: %v", got)
		}
		r.key(t, "Left", domain.ModAlt|domain.ModCtrl)
		r.output <- ports.OutputRemoved{Name: "DP-5"}
		receive(t, r.scenes)
		r.plug(t, extB)
		st := stateAfter(t, r.state, func(st domain.State) bool { return len(st.Outputs) == 2 })
		for _, w := range st.Workspaces {
			if w.Name == "web" && w.Output != "DP-7" {
				t.Fatalf("web on %s, want DP-7", w.Output)
			}
		}
	})
}

// On arrival the home takes the monitor's current identity: the connector
// fallback then follows DP-7, not the original DP-5.
func TestArrivalRefreshesHomeConnector(t *testing.T) {
	for _, tc := range []struct {
		name, replug string
		back         bool
	}{
		{"new connector", "DP-7", true},
		{"old connector", "DP-5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := unpluggedExternal(t, nil)
				if got := shown(r.plug(t, extB)); !slices.Equal(got["DP-7"], []domain.WindowID{2}) {
					t.Fatalf("replug: %v", got)
				}
				r.output <- ports.OutputRemoved{Name: "DP-7"}
				receive(t, r.scenes)
				got := shown(r.plug(t, domain.OutputInfo{Name: tc.replug, Width: 400, Height: 200}))
				if back := slices.Equal(got[tc.replug], []domain.WindowID{2}); back != tc.back {
					t.Fatalf("replug %s without identity: %v, want back %t", tc.replug, got, tc.back)
				}
				if !tc.back && len(got[tc.replug]) != 0 {
					t.Fatalf("%s not empty: %v", tc.replug, got)
				}
			})
		})
	}
}

// lidClosed runs with the external only: tile 2 with 3 in its hidden stash,
// then unplugs it, so the placeholder keeps the workspace stranded.
func lidClosed(t *testing.T) *multiRig {
	t.Helper()
	r := startMulti(t, nil, extA)
	r.mapWindow(t, 2)
	r.mapWindow(t, 3)
	r.key(t, "s", domain.ModAlt|domain.ModShift)
	if got := shown(r.key(t, "s", domain.ModAlt)); !slices.Equal(got["DP-5"], []domain.WindowID{2}) {
		t.Fatalf("setup: %v", got)
	}
	r.output <- ports.OutputRemoved{Name: "DP-5"}
	receive(t, r.scenes)
	return r
}

func stateOutput(t *testing.T, r *multiRig) string {
	t.Helper()
	return stateAfter(t, r.state, func(domain.State) bool { return true }).Output
}

// Lid closed on the external, unplug it, open the lid, replug the external:
// the stranded workspace returns to it although shown on the laptop, and the
// focus follows (AC3).
func TestStrandedWorkspaceReturnsWithFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := lidClosed(t)
		if got := shown(r.plug(t, laptop)); !slices.Equal(got["eDP-1"], []domain.WindowID{2}) {
			t.Fatalf("lid open: %v", got)
		}
		got := shown(r.plug(t, extA))
		if !slices.Equal(got["DP-5"], []domain.WindowID{2}) || len(got["eDP-1"]) != 0 {
			t.Fatalf("replug: %v", got)
		}
		if out := stateOutput(t, r); out != "DP-5" {
			t.Fatalf("focused %q, want DP-5", out)
		}
		if got := shown(r.mapWindow(t, 4)); !slices.Contains(got["DP-5"], 4) {
			t.Fatalf("new window not on DP-5: %v", got)
		}
		if got := shown(r.key(t, "s", domain.ModAlt)); !slices.Contains(got["DP-5"], 3) || slices.Contains(got["eDP-1"], 3) {
			t.Fatalf("stash: %v", got)
		}
	})
}

// A workspace made on the laptop after the lid opened belongs there.
func TestStrandedReturnKeepsNativeLaptopWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := lidClosed(t)
		r.plug(t, laptop)
		r.key2(t)
		if got := shown(r.mapWindow(t, 4)); !slices.Equal(got["eDP-1"], []domain.WindowID{4}) {
			t.Fatalf("laptop: %v", got)
		}
		got := shown(r.plug(t, extA))
		if !slices.Equal(got["eDP-1"], []domain.WindowID{4}) || !slices.Equal(got["DP-5"], []domain.WindowID{2}) {
			t.Fatalf("replug: %v", got)
		}
		if out := stateOutput(t, r); out != "eDP-1" {
			t.Fatalf("focused %q, want eDP-1", out)
		}
	})
}

// The external comes back first: the workspace is home, no longer stranded.
// A later ordinary unplug leaves a shown guest on the laptop until the user
// leaves it (AC5).
func TestStrandedClearedWhenHomeReturnsFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := lidClosed(t)
		if got := shown(r.plug(t, extA)); !slices.Equal(got["DP-5"], []domain.WindowID{2}) {
			t.Fatalf("home first: %v", got)
		}
		r.plug(t, laptop) // placed right of the external, plugged first
		r.key(t, "Right", domain.ModAlt|domain.ModCtrl)
		if out := stateOutput(t, r); out != "eDP-1" {
			t.Fatalf("focused %q, want eDP-1", out)
		}
		r.output <- ports.OutputRemoved{Name: "DP-5"}
		receive(t, r.scenes)
		// The laptop's only workspace was empty: the guest is workspace 1.
		r.input <- ports.KeyEvent{Keysym: "1", Keycode: 2, Mods: domain.ModAlt, Pressed: true}
		got := shown(receive(t, r.scenes))
		r.input <- ports.KeyEvent{Keysym: "1", Keycode: 2, Mods: domain.ModAlt}
		if !slices.Equal(got["eDP-1"], []domain.WindowID{2}) {
			t.Fatalf("guest shown: %v", got)
		}
		got = shown(r.plug(t, extA))
		if !slices.Equal(got["eDP-1"], []domain.WindowID{2}) || len(got["DP-5"]) != 0 {
			t.Fatalf("shown guest moved: %v", got)
		}
		if got := shown(r.key2(t)); !slices.Equal(got["DP-5"], []domain.WindowID{2}) || slices.Contains(got["eDP-1"], 2) {
			t.Fatalf("left guest: %v", got)
		}
	})
}

// windowOutput is the output holding window id in the latest state.
func windowOutput(t *testing.T, r *multiRig, id domain.WindowID) string {
	t.Helper()
	st := stateAfter(t, r.state, func(domain.State) bool { return true })
	for _, w := range st.Windows {
		if w.ID == id {
			return w.Output
		}
	}
	t.Fatalf("window %d not in state", id)
	return ""
}

// keyNum presses Cmd+n with its keycode.
func (r *multiRig) keyNum(t *testing.T, n int) []domain.Scene {
	t.Helper()
	sym, code := string(rune('0'+n)), uint32(n+1)
	r.input <- ports.KeyEvent{Keysym: sym, Keycode: code, Mods: domain.ModAlt, Pressed: true}
	set := receive(t, r.scenes)
	r.input <- ports.KeyEvent{Keysym: sym, Keycode: code, Mods: domain.ModAlt}
	return set
}

// A workspace moved to one of two identical monitors remembers that twin:
// unplugging it never sends the workspace to the other twin, another
// connector does not take it, and its own connector does (AC4).
func TestMovedWorkspaceRemembersItsTwin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t1 := domain.OutputInfo{Name: "DP-1", EDIDID: "dd01", EDIDHash: "h", Width: 400, Height: 200}
		t2 := t1
		t2.Name = "DP-2"
		r := startMulti(t, nil, laptop, t1, t2)
		r.mapWindow(t, 1)
		r.key(t, "Right", domain.ModAlt|domain.ModCtrl)
		r.mapWindow(t, 2)
		if got := shown(r.key(t, "Right", domain.ModAlt|domain.ModCtrl|domain.ModShift)); !slices.Equal(got["DP-2"], []domain.WindowID{2}) || len(got["DP-1"]) != 0 {
			t.Fatalf("moved to t2: %v", got)
		}
		r.key(t, "Left", domain.ModAlt|domain.ModCtrl)
		r.key(t, "Left", domain.ModAlt|domain.ModCtrl)
		r.output <- ports.OutputRemoved{Name: "DP-2"}
		receive(t, r.scenes)
		// Show the guest, then leave it: the settle must not pick twin t1.
		if got := shown(r.keyNum(t, 2)); !slices.Equal(got["eDP-1"], []domain.WindowID{2}) {
			t.Fatalf("guest: %v", got)
		}
		if got := shown(r.keyNum(t, 1)); len(got["DP-1"]) != 0 {
			t.Fatalf("guest went to the other twin: %v", got)
		}
		if out := windowOutput(t, r, 2); out != "eDP-1" {
			t.Fatalf("window 2 on %s, want eDP-1", out)
		}
		// The same fingerprint on another connector is ambiguous: it stays.
		if got := shown(r.plug(t, func() domain.OutputInfo { o := t1; o.Name = "DP-3"; return o }())); len(got["DP-3"]) != 0 {
			t.Fatalf("DP-3 took the guest: %v", got)
		}
		if out := windowOutput(t, r, 2); out != "eDP-1" {
			t.Fatalf("window 2 on %s, want eDP-1", out)
		}
		r.output <- ports.OutputRemoved{Name: "DP-3"}
		receive(t, r.scenes)
		if got := shown(r.plug(t, t2)); !slices.Equal(got["DP-2"], []domain.WindowID{2}) {
			t.Fatalf("t2 back: %v", got)
		}
	})
}
