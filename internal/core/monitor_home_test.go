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
