package core_test

import (
	"regexp"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func winRule(name, appID string, edit func(*ports.WindowRule)) ports.WindowRule {
	r := ports.WindowRule{Name: name, AppID: regexp.MustCompile("^(?:" + appID + ")$")}
	if edit != nil {
		edit(&r)
	}
	return r
}

func ruleOn() *bool  { v := true; return &v }
func ruleOff() *bool { v := false; return &v }

func declareDev(c *ports.Config) {
	c.Workspaces = []ports.WorkspaceConfig{{Name: "dev"}}
	c.Binds["Alt+d"] = "workspace dev"
}

// mapApp maps a window with an app ID and returns the state once it is in.
func (r *multiRig) mapApp(t *testing.T, id ports.WindowID, appID string) ports.State {
	t.Helper()
	r.client <- ports.WindowMapped{ID: id, AppID: appID}
	return r.stateWith(t, id)
}

func (r *multiRig) stateWith(t *testing.T, id ports.WindowID) ports.State {
	t.Helper()
	return stateAfter(t, r.state, func(st ports.State) bool { return stWindow(st, id) != nil })
}

func stWindow(st ports.State, id ports.WindowID) *ports.WindowState {
	for i := range st.Windows {
		if st.Windows[i].ID == id {
			return &st.Windows[i]
		}
	}
	return nil
}

func stFocus(st ports.State) ports.WindowID {
	if st.Window == nil {
		return 0
	}
	return st.Window.ID
}

func stOutput(st ports.State, name string) ports.OutputState {
	for _, o := range st.Outputs {
		if o.Name == name {
			return o
		}
	}
	return ports.OutputState{}
}

func TestRuleFloatingOnAndOff(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Rules = []ports.WindowRule{
			winRule("float", "calc", func(r *ports.WindowRule) { r.Floating = ruleOn() }),
			winRule("tile", "splash", func(r *ports.WindowRule) { r.Floating = ruleOff() }),
		}
	}, left)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "calc")
	if w := stWindow(st, 2); !w.Floating || stFocus(st) != 2 {
		t.Fatalf("calc not floating on the current workspace: %+v focus %d", w, stFocus(st))
	}
	if w := stWindow(st, 1); w.Floating {
		t.Fatal("unrelated window floats")
	}
	// A fixed-size window (Floating from the client) tiles under floating = off.
	r.client <- ports.WindowMapped{ID: 3, AppID: "splash", Floating: true, Width: 50, Height: 40}
	st = r.stateWith(t, 3)
	if w := stWindow(st, 3); w.Floating || w.Column == 0 {
		t.Fatalf("splash not tiled: %+v", w)
	}
	// Without a rule the same window floats as before.
	r.client <- ports.WindowMapped{ID: 4, AppID: "other", Floating: true, Width: 50, Height: 40}
	if w := stWindow(r.stateWith(t, 4), 4); !w.Floating {
		t.Fatalf("unmatched fixed-size window tiled: %+v", w)
	}
}

func TestRuleNamedWorkspaceKeepsFocusAndView(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("dev", "code", func(r *ports.WindowRule) { r.Workspace = "dev" })}
	}, left)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "code")
	w := stWindow(st, 2)
	if w.WorkspaceName != "dev" || w.Workspace != 0 || w.Visible {
		t.Fatalf("window not quietly on dev: %+v", w)
	}
	if stFocus(st) != 1 || stOutput(st, "DP-1").Workspace != "" || stOutput(st, "DP-1").Active != 1 {
		t.Fatalf("rule moved focus or view: %+v", st)
	}
	// The workspace shows its window when the user goes there.
	r.key(t, "d", ports.ModAlt)
	st = stateAfter(t, r.state, func(st ports.State) bool { return stOutput(st, "DP-1").Workspace == "dev" })
	if stFocus(st) != 2 {
		t.Fatalf("dev window not focused on arrival: %d", stFocus(st))
	}
}

func TestRuleNamedWorkspaceFloat(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("dev", "code", func(r *ports.WindowRule) { r.Workspace = "dev"; r.Floating = ruleOn() })}
	}, left)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "code")
	if w := stWindow(st, 2); w.WorkspaceName != "dev" || !w.Floating || stFocus(st) != 1 {
		t.Fatalf("%+v focus %d", w, stFocus(st))
	}
	r.key(t, "d", ports.ModAlt)
	st = stateAfter(t, r.state, func(st ports.State) bool { return stOutput(st, "DP-1").Workspace == "dev" })
	if stFocus(st) != 2 {
		t.Fatalf("quiet float not focused on arrival: %d", stFocus(st))
	}
}

func TestRuleUndeclaredWorkspaceFallsBack(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Rules = []ports.WindowRule{winRule("x", "code", func(r *ports.WindowRule) { r.Workspace = "nope" })}
	}, left)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "code")
	if w := stWindow(st, 2); w.Workspace != 1 || stFocus(st) != 2 {
		t.Fatalf("%+v", w)
	}
}

func TestRuleNumberedWorkspace(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Rules = []ports.WindowRule{
			winRule("two", "two", func(r *ports.WindowRule) { r.Workspace = "2" }),
			winRule("far", "far", func(r *ports.WindowRule) { r.Workspace = "9" }),
		}
	}, left)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "two")
	if w := stWindow(st, 2); w.Workspace != 2 || stFocus(st) != 1 || stOutput(st, "DP-1").Active != 1 {
		t.Fatalf("%+v active %d focus %d", w, stOutput(st, "DP-1").Active, stFocus(st))
	}
	// 9 is clamped to the last workspace, the spare after workspace 2: no
	// pile of empty workspaces in between.
	st = r.mapApp(t, 3, "far")
	if w := stWindow(st, 3); w.Workspace != 3 {
		t.Fatalf("clamped to %d", w.Workspace)
	}
	if o := stOutput(st, "DP-1"); o.Count != 3 || o.Active != 1 || stFocus(st) != 1 {
		t.Fatalf("%+v focus %d", o, stFocus(st))
	}
}

func TestRuleNumberOnCurrentWorkspaceActsAsNormalMap(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Rules = []ports.WindowRule{winRule("one", "term", func(r *ports.WindowRule) { r.Workspace = "1" })}
	}, left)
	r.mapApp(t, 1, "other")
	st := r.mapApp(t, 2, "term")
	if w := stWindow(st, 2); w.Workspace != 1 || stFocus(st) != 2 {
		t.Fatalf("%+v focus %d", w, stFocus(st))
	}
}

func TestRuleMonitor(t *testing.T) {
	for _, mon := range []string{"DP-2", "Acme B 2"} {
		t.Run(mon, func(t *testing.T) {
			r := startMulti(t, func(c *ports.Config) {
				c.Rules = []ports.WindowRule{winRule("m", "chat", func(r *ports.WindowRule) { r.Monitor = mon })}
			}, left, right)
			r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
			before := r.mapApp(t, 1, "term")
			if before.Output != "DP-1" {
				t.Fatalf("focused %s", before.Output)
			}
			st := r.mapApp(t, 2, "chat")
			w := stWindow(st, 2)
			if w.Output != "DP-2" || st.Output != "DP-1" || stFocus(st) != 1 {
				t.Fatalf("%+v focused output %s window %d", w, st.Output, stFocus(st))
			}
			if !w.Visible {
				t.Fatal("the window is not shown on the current workspace of DP-2")
			}
		})
	}
}

func TestRuleMonitorWithNumberedWorkspace(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Rules = []ports.WindowRule{winRule("m", "chat", func(r *ports.WindowRule) { r.Monitor = "DP-2"; r.Workspace = "2" })}
	}, left, right)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapApp(t, 3, "term") // DP-2 gets a window, so a second workspace
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "chat")
	if w := stWindow(st, 2); w.Output != "DP-2" || w.Workspace != 2 || st.Output != "DP-1" || stFocus(st) != 1 || stOutput(st, "DP-2").Active != 1 {
		t.Fatalf("%+v", w)
	}
}

func TestRuleDisconnectedMonitorFallsBack(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Rules = []ports.WindowRule{winRule("m", "chat", func(r *ports.WindowRule) { r.Monitor = "HDMI-9" })}
	}, left, right)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "chat")
	if w := stWindow(st, 2); w.Output != "DP-1" || stFocus(st) != 2 {
		t.Fatalf("%+v focus %d", w, stFocus(st))
	}
}

func TestRulesMerge(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Layout.MaxColumns = 4
		c.Rules = []ports.WindowRule{
			winRule("all", ".*", func(r *ports.WindowRule) { r.Monitor = "DP-2"; r.Width = "1/4" }),
			winRule("chat", "chat", func(r *ports.WindowRule) { r.Width = "1/2" }),
		}
	}, left, right)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	r.client <- ports.WindowMapped{ID: 1, AppID: "chat"}
	r.client <- ports.WindowMapped{ID: 2, AppID: "term"}
	set := sceneMatch2(t, r, func(s ports.Scene) bool { return s.Output == "DP-2" && len(s.Windows) == 2 })
	got := map[ports.WindowID]int{}
	for _, w := range set.Windows {
		got[w.ID] = w.Rect.W
	}
	// DP-2 is 400 wide: the later rule wins the width, the generic monitor still applies.
	if got[1] != 200 || got[2] != 100 {
		t.Fatalf("widths %v", got)
	}
}

// sceneMatch2 waits for a scene of the multi rig that ok accepts.
func sceneMatch2(t *testing.T, r *multiRig, ok func(ports.Scene) bool) ports.Scene {
	t.Helper()
	for range 50 {
		for _, s := range receive(t, r.scenes) {
			if ok(s) {
				return s
			}
		}
	}
	t.Fatal("no matching scene")
	return ports.Scene{}
}

func TestRuleWidthOnCurrentWorkspace(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Layout.MaxColumns = 4
		c.Rules = []ports.WindowRule{winRule("w", "wide", func(r *ports.WindowRule) { r.Width = "50%" })}
	}, left)
	r.client <- ports.WindowMapped{ID: 1, AppID: "wide"}
	s := sceneMatch2(t, r, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	if s.Windows[0].Rect.W != 100 {
		t.Fatalf("width %d", s.Windows[0].Rect.W)
	}
}

func TestSlotWinsOverRule(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Workspaces = []ports.WorkspaceConfig{{Name: "dev", Slots: []ports.SlotConfig{{Index: 1, Width: "50%", Argv: []string{"code"}}}}}
		c.Binds["Alt+d"] = "workspace dev"
		c.Rules = []ports.WindowRule{winRule("x", "code", func(r *ports.WindowRule) { r.Workspace = "1"; r.Floating = ruleOn() })}
	}, left)
	req := receive(t, r.spawn)
	var slot string
	for _, e := range req.Env {
		if len(e) > len(ports.SlotEnv)+1 && e[:len(ports.SlotEnv)+1] == ports.SlotEnv+"=" {
			slot = e[len(ports.SlotEnv)+1:]
		}
	}
	r.client <- ports.WindowMapped{ID: 1, AppID: "code", Slot: slot}
	st := r.stateWith(t, 1)
	if w := stWindow(st, 1); w.WorkspaceName != "dev" || w.Floating {
		t.Fatalf("rule overrode the slot: %+v", w)
	}
}

func TestDialogStaysOverParentDespiteRules(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("x", ".*", func(r *ports.WindowRule) { r.Workspace = "dev"; r.Floating = ruleOff() })}
	}, left)
	r.client <- ports.WindowMapped{ID: 1, AppID: "term"}
	st := r.stateWith(t, 1)
	if w := stWindow(st, 1); w.WorkspaceName != "dev" {
		t.Fatalf("parent: %+v", w)
	}
	r.key(t, "d", ports.ModAlt)
	stateAfter(t, r.state, func(st ports.State) bool { return stOutput(st, "DP-1").Workspace == "dev" })
	r.client <- ports.WindowMapped{ID: 2, AppID: "term", Floating: true, Width: 30, Height: 30, Parent: 1}
	st = r.stateWith(t, 2)
	if w := stWindow(st, 2); w.WorkspaceName != "dev" || !w.Floating {
		t.Fatalf("dialog moved or tiled: %+v", w)
	}
	// Same app ID without a parent: the rule applies (tiled on dev).
	r.client <- ports.WindowMapped{ID: 3, AppID: "term", Floating: true, Width: 30, Height: 30}
	if w := stWindow(r.stateWith(t, 3), 3); w.Floating {
		t.Fatalf("parentless fixed-size window ignores floating = off: %+v", w)
	}
}

func TestAppIDChangeAfterMapMovesNothing(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("dev", "code", func(r *ports.WindowRule) { r.Workspace = "dev" })}
	}, left)
	r.mapApp(t, 1, "")
	r.client <- ports.WindowAppID{ID: 1, AppID: "code"}
	st := r.mapApp(t, 2, "term") // after the change, to know it was handled
	if w := stWindow(st, 1); w.Workspace != 1 || w.AppID != "code" {
		t.Fatalf("window re-placed: %+v", w)
	}
}

func TestRemapKeepsPlace(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("dev", "code", func(r *ports.WindowRule) { r.Workspace = "dev" })}
	}, left)
	r.mapApp(t, 1, "term")
	r.client <- ports.WindowMapped{ID: 1, AppID: "code"}
	if w := stWindow(r.mapApp(t, 2, "term"), 1); w.Workspace != 1 {
		t.Fatalf("re-map moved the window: %+v", w)
	}
}

// A toplevel that maps again after an unmap (a tray app restored) opens as
// any other window: on the workspace the user looks at, not its rule's.
func TestRuleSkipsRemapAfterUnmap(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("dev", "discord", func(r *ports.WindowRule) { r.Workspace = "dev" })}
	}, left)
	if w := stWindow(r.mapApp(t, 1, "discord"), 1); w.WorkspaceName != "dev" {
		t.Fatalf("first map not placed by the rule: %+v", w)
	}
	r.client <- ports.WindowUnmapped{ID: 1}
	stateAfter(t, r.state, func(st ports.State) bool { return stWindow(st, 1) == nil })
	r.client <- ports.WindowMapped{ID: 1, AppID: "discord", Remap: true}
	st := r.stateWith(t, 1)
	if w := stWindow(st, 1); w.WorkspaceName != "" || w.Workspace != 1 || stFocus(st) != 1 {
		t.Fatalf("remap placed by the rule: %+v focus %d", w, stFocus(st))
	}
}

func TestRuleReloadAffectsFutureMapsOnly(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Rules = []ports.WindowRule{winRule("dev", "code", func(r *ports.WindowRule) { r.Workspace = "dev" })}
	}, left)
	r.mapApp(t, 1, "code")
	cfg := r.cfg
	cfg.Rules = []ports.WindowRule{winRule("fl", "code", func(r *ports.WindowRule) { r.Floating = ruleOn() })}
	r.reload <- ports.ConfigChanged{Config: cfg}
	// Handled in order with the map below.
	st := r.mapApp(t, 2, "code")
	if w := stWindow(st, 1); w.WorkspaceName != "dev" || w.Floating {
		t.Fatalf("reload moved a mapped window: %+v", w)
	}
	if w := stWindow(st, 2); w.WorkspaceName != "" || !w.Floating {
		t.Fatalf("new rules not applied: %+v", w)
	}
}

func TestRuleQuietPlacementWithAnimations(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		declareDev(c)
		c.Animations.On = true
		c.Rules = []ports.WindowRule{
			winRule("dev", "code", func(r *ports.WindowRule) { r.Workspace = "dev" }),
			winRule("fl", "calc", func(r *ports.WindowRule) { r.Floating = ruleOn() }),
		}
	}, left)
	r.mapApp(t, 1, "term")
	st := r.mapApp(t, 2, "code")
	if w := stWindow(st, 2); w.WorkspaceName != "dev" || w.Visible || stFocus(st) != 1 {
		t.Fatalf("%+v focus %d", w, stFocus(st))
	}
	st = r.mapApp(t, 3, "calc")
	if w := stWindow(st, 3); !w.Floating || stFocus(st) != 3 {
		t.Fatalf("%+v focus %d", w, stFocus(st))
	}
}

func TestRuleWidthIgnoredWithFixedOverflow(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Layout.MaxColumns = 2
		c.Layout.Overflow = "fixed"
		c.Rules = []ports.WindowRule{winRule("w", "wide", func(r *ports.WindowRule) { r.Width = "10%" })}
	}, left)
	r.client <- ports.WindowMapped{ID: 1, AppID: "term"}
	r.client <- ports.WindowMapped{ID: 2, AppID: "wide"}
	s := sceneMatch2(t, r, func(s ports.Scene) bool { return len(s.Windows) == 2 })
	if s.Windows[0].Rect.W != 100 || s.Windows[1].Rect.W != 100 {
		t.Fatalf("%+v", s.Windows)
	}
}
