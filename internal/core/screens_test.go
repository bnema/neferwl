package core_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/ports"
)

type multiRig struct {
	client   chan ports.ClientEvent
	input    chan ports.InputEvent
	output   chan ports.OutputEvent
	reload   chan ports.ConfigChanged
	commands chan ports.ClientCommand
	scenes   chan []ports.Scene
	spawn    chan ports.SpawnRequest
	state    chan ports.State
	cfg      ports.Config
}

var (
	left  = ports.OutputInfo{Name: "DP-1", Make: "Acme", Model: "A", Serial: "1", Width: 200, Height: 100}
	right = ports.OutputInfo{Name: "DP-2", Make: "Acme", Model: "B", Serial: "2", Width: 400, Height: 200}
)

// startMulti runs core with Alt as Cmd, no border and no gaps, and plugs
// the given outputs in order.
func startMulti(t *testing.T, edit func(*ports.Config), outs ...ports.OutputInfo) *multiRig {
	t.Helper()
	return startRig(t, false, edit, outs...)
}

// startRig is startMulti; with terminal set, core keeps a terminal on every
// empty workspace on screen.
func startRig(t *testing.T, terminal bool, edit func(*ports.Config), outs ...ports.OutputInfo) *multiRig {
	t.Helper()
	cfg := config.Defaults()
	cfg.Keyboard.CmdKey = "alt"
	cfg.Border.Width = 0
	if edit != nil {
		edit(&cfg)
	}
	r := &multiRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 16),
		output: make(chan ports.OutputEvent, 4), reload: make(chan ports.ConfigChanged, 4),
		commands: make(chan ports.ClientCommand, 1024), scenes: make(chan []ports.Scene, 1), spawn: make(chan ports.SpawnRequest, 16), state: make(chan ports.State, 1), cfg: cfg,
	}
	c, err := core.New(cfg, core.Channels{Client: r.client, Input: r.input, Output: r.output, Config: r.reload, Commands: r.commands, Scenes: r.scenes, Spawn: r.spawn, State: r.state, Terminal: terminal})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, o := range outs {
		r.plug(t, o)
	}
	return r
}

func (r *multiRig) plug(t *testing.T, o ports.OutputInfo) []ports.Scene {
	t.Helper()
	r.output <- ports.OutputAdded{Info: o}
	return receive(t, r.scenes)
}

func (r *multiRig) key(t *testing.T, sym string, mods ports.Mods) []ports.Scene {
	t.Helper()
	r.input <- ports.KeyEvent{Keysym: sym, Mods: mods, Pressed: true}
	s := receive(t, r.scenes)
	r.input <- ports.KeyEvent{Keysym: sym, Mods: mods}
	return s
}

func (r *multiRig) mapWindow(t *testing.T, id ports.WindowID) []ports.Scene {
	t.Helper()
	r.client <- ports.WindowMapped{ID: id}
	return receive(t, r.scenes)
}

// shown returns the visible window IDs of each output, by connector.
func shown(set []ports.Scene) map[string][]ports.WindowID {
	out := map[string][]ports.WindowID{}
	for _, s := range set {
		out[s.Output] = []ports.WindowID{}
		for _, w := range s.Windows {
			if !w.Hidden {
				out[s.Output] = append(out[s.Output], w.ID)
			}
		}
	}
	return out
}

func lastOutputs(t *testing.T, ch chan ports.ClientCommand) ports.SetOutputs {
	t.Helper()
	var last ports.SetOutputs
	found := false
	for len(ch) > 0 {
		if v, ok := (<-ch).(ports.SetOutputs); ok {
			last, found = v, true
		}
	}
	if !found {
		t.Fatal("no SetOutputs")
	}
	return last
}

func TestOutputsLeftToRight(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Outputs = []ports.OutputConfig{{Name: "DP-2", Scale: 2}, {Name: "DP-1"}}
	}, left, right)
	out := lastOutputs(t, r.commands)
	// Config order wins: DP-2 (200x100 logical at scale 2), then DP-1.
	if len(out.Outputs) != 2 || out.Outputs[0].Info.Name != "DP-2" || out.Outputs[0].X != 0 || out.Outputs[0].Width != 200 ||
		out.Outputs[1].Info.Name != "DP-1" || out.Outputs[1].X != 200 || out.Focused != "DP-1" {
		t.Fatalf("%+v", out)
	}
}

func TestWindowsOpenOnFocusedOutput(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	set := r.mapWindow(t, 2)
	if got := shown(set); len(got["DP-1"]) != 1 || got["DP-1"][0] != 1 || len(got["DP-2"]) != 1 || got["DP-2"][0] != 2 {
		t.Fatal(got)
	}
	// Only the focused output draws a focused window.
	for _, s := range set {
		for _, w := range s.Windows {
			if w.Focused != (s.Output == "DP-2") {
				t.Fatalf("%s: %+v", s.Output, w)
			}
		}
	}
}

func TestFocusColumnCrossesOutputs(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	// The only column is the edge: focus moves to the right output.
	r.key(t, "Right", ports.ModAlt)
	r.mapWindow(t, 2)
	for len(r.commands) > 0 {
		<-r.commands
	}
	r.key(t, "Left", ports.ModAlt)
	var focus ports.WindowID
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.FocusWindow); ok {
			focus = v.ID
		}
	}
	if focus != 1 {
		t.Fatal(focus)
	}
}

func TestPointerCrossesOutputs(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 2)
	for len(r.commands) > 0 {
		<-r.commands
	}
	// DP-2 starts at x 200: global (250, 50) is (50, 50) on window 2.
	r.input <- ports.PointerMotion{X: 250, Y: 50}
	if v := command(t, r.commands); v != (ports.PointerFocus{ID: 2, X: 50, Y: 50}) {
		t.Fatal(v)
	}
	// Back on DP-1, then below it (100 high): off every output, y stays on DP-1.
	r.input <- ports.PointerMotion{X: 10, Y: 50}
	r.input <- ports.PointerMotion{X: 10, Y: 150}
	for {
		if v, ok := command(t, r.commands).(ports.PointerMotionTo); ok && v.Y == 99 {
			if v.ID != 1 || v.X != 10 {
				t.Fatal(v)
			}
			break
		}
	}
	// A click focuses the output under the pointer.
	r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
	set := receive(t, r.scenes)
	for _, s := range set {
		for _, w := range s.Windows {
			if w.Focused != (w.ID == 1) {
				t.Fatalf("%s: %+v", s.Output, w)
			}
		}
	}
}

func TestMoveWorkspaceToMonitor(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	set := r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	if got := shown(set); len(got["DP-1"]) != 0 || len(got["DP-2"]) != 1 {
		t.Fatal(got)
	}
	// It is DP-2's now: unplugging DP-1 does not move it.
	r.output <- ports.OutputRemoved{Name: "DP-1"}
	set = receive(t, r.scenes)
	if len(set) != 1 || set[0].Output != "DP-2" || len(shown(set)["DP-2"]) != 1 {
		t.Fatal(shown(set))
	}
}

func TestUnplugMovesWorkspacesAndReplugReturnsThem(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 2)
	// Unplug DP-2 while it is focused: its workspace joins DP-1 below its own.
	r.output <- ports.OutputRemoved{Name: "DP-2"}
	set := receive(t, r.scenes)
	if len(set) != 1 || len(shown(set)["DP-1"]) != 1 || shown(set)["DP-1"][0] != 1 {
		t.Fatal(shown(set))
	}
	// Cmd+2 shows the guest.
	r.input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModAlt, Pressed: true}
	set = receive(t, r.scenes)
	r.input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModAlt}
	if got := shown(set)["DP-1"]; len(got) != 1 || got[0] != 2 {
		t.Fatal(got)
	}
	// Replugged while on screen: the guest stays until the user leaves it.
	set = r.plug(t, right)
	if got := shown(set); len(got["DP-1"]) != 1 || got["DP-1"][0] != 2 || len(got["DP-2"]) != 0 {
		t.Fatal(got)
	}
	r.input <- ports.KeyEvent{Keysym: "1", Keycode: 2, Mods: ports.ModAlt, Pressed: true}
	set = receive(t, r.scenes)
	if got := shown(set); len(got["DP-1"]) != 1 || got["DP-1"][0] != 1 || len(got["DP-2"]) != 1 || got["DP-2"][0] != 2 {
		t.Fatal(got)
	}
}

func TestReplugReturnsHiddenGuestAtOnce(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 2)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	r.output <- ports.OutputRemoved{Name: "DP-2"}
	receive(t, r.scenes)
	// Not on screen: it goes home as soon as DP-2 is back.
	set := r.plug(t, right)
	if got := shown(set); len(got["DP-2"]) != 1 || got["DP-2"][0] != 2 {
		t.Fatal(got)
	}
}

func TestNamedWorkspaceOnItsMonitor(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Workspaces = []ports.WorkspaceConfig{{Name: "web", Monitor: "DP-2", Hidden: true}}
		c.Binds["Alt+w"] = "workspace web"
	}, left)
	// DP-2 is not plugged: web lives on DP-1 as a guest.
	r.key(t, "w", ports.ModAlt)
	r.mapWindow(t, 1)
	r.key(t, "w", ports.ModAlt)
	// DP-2 arrives: web goes home (not on screen).
	r.plug(t, right)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	set := r.key(t, "w", ports.ModAlt)
	if got := shown(set); len(got["DP-2"]) != 1 || got["DP-2"][0] != 1 {
		t.Fatal(got)
	}
}

func TestLayersStayOnTheirOutput(t *testing.T) {
	r := startMulti(t, nil, left, right)
	bar := ports.LayerSurface{ID: 9, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 10, ExclusiveZone: 10, Output: "DP-2"}
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	set := receive(t, r.scenes)
	for _, s := range set {
		if (len(s.Layers) == 1) != (s.Output == "DP-2") {
			t.Fatalf("%s: %+v", s.Output, s.Layers)
		}
	}
	if set[1].Layers[0].Rect != (ports.Rect{W: 400, H: 10}) {
		t.Fatal(set[1].Layers)
	}
}

func TestScaleBindTargetsFocusedOutput(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Binds["Alt+equal"] = "scale-up" }, left, right)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	set := r.key(t, "equal", ports.ModAlt)
	if set[0].Scale != 1 || set[1].Scale == 1 {
		t.Fatal(set[0].Scale, set[1].Scale)
	}
}

func TestLastOutputUnpluggedKeepsWindows(t *testing.T) {
	r := startMulti(t, nil, left)
	r.mapWindow(t, 1)
	r.output <- ports.OutputRemoved{Name: "DP-1"}
	if set := receive(t, r.scenes); len(set) != 1 || set[0].Output != "" {
		t.Fatal(shown(set))
	}
	if out := lastOutputs(t, r.commands); len(out.Outputs) != 0 {
		t.Fatalf("%+v", out)
	}
	// Another monitor takes the windows over.
	set := r.plug(t, right)
	if got := shown(set); len(got) != 1 || len(got["DP-2"]) != 1 || got["DP-2"][0] != 1 {
		t.Fatal(got)
	}
}

func TestModeChangeKeepsScreen(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 2)
	// A new mode on DP-2 is only a new size: nothing moves, focus stays.
	bigger := right
	bigger.Width, bigger.Height = 800, 400
	set := r.plug(t, bigger)
	if got := shown(set); len(got["DP-2"]) != 1 || got["DP-2"][0] != 2 || len(got["DP-1"]) != 1 {
		t.Fatal(got)
	}
	if out := lastOutputs(t, r.commands); out.Focused != "DP-2" || out.Outputs[1].Width != 800 {
		t.Fatalf("%+v", out)
	}
}

func TestPrimaryOutputGetsFocusAtStartup(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Outputs = []ports.OutputConfig{{Name: "DP-1"}, {Name: "DP-2", Primary: true}}
	}, left, right)
	out := lastOutputs(t, r.commands)
	// Placed right of DP-1, but focused.
	if out.Focused != "DP-2" || out.Outputs[1].Info.Name != "DP-2" || !out.Outputs[1].Primary || out.Outputs[0].Primary {
		t.Fatalf("%+v", out)
	}
	set := r.mapWindow(t, 1)
	if got := shown(set); len(got["DP-2"]) != 1 {
		t.Fatal(got)
	}
}

func TestMoveColumnCrossesOutputs(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	// Window 2 is the right column of DP-1: moving right first hits the
	// edge, then the column goes to DP-2 and focus follows it.
	set := r.key(t, "Right", ports.ModAlt|ports.ModShift)
	if got := shown(set); len(got["DP-1"]) != 1 || got["DP-1"][0] != 1 || len(got["DP-2"]) != 1 || got["DP-2"][0] != 2 {
		t.Fatal(got)
	}
	if out := lastOutputs(t, r.commands); out.Focused != "DP-2" {
		t.Fatal(out.Focused)
	}
	// Back left (no terminal kept here, so a last column may move): it
	// lands on the right side of DP-1.
	set = r.key(t, "Left", ports.ModAlt|ports.ModShift)
	if got := shown(set)["DP-1"]; len(got) != 2 {
		t.Fatal(got)
	}
	for _, s := range set {
		for _, w := range s.Windows {
			if w.ID == 2 && (s.Output != "DP-1" || !w.Focused || w.Rect.X == 0) {
				t.Fatalf("%s %+v", s.Output, w)
			}
		}
	}
}

func TestPointerFocusesOutput(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.input <- ports.PointerMotion{X: 250, Y: 50}
	receive(t, r.scenes)
	if out := lastOutputs(t, r.commands); out.Focused != "DP-2" {
		t.Fatal(out.Focused)
	}
	// New windows open where the pointer is.
	if got := shown(r.mapWindow(t, 1)); len(got["DP-2"]) != 1 {
		t.Fatal(got)
	}
}

func TestEmptyWorkspaceGetsTerminal(t *testing.T) {
	r := startRig(t, true, nil, left, right)
	// One terminal per output, each tagged for its workspace.
	var reqs []ports.SpawnRequest
	for len(reqs) < 2 {
		reqs = append(reqs, receive(t, r.spawn))
	}
	if len(r.spawn) != 0 || reqs[0].Argv[0] != "foot" || reqs[0].Env[0] == reqs[1].Env[0] {
		t.Fatal(reqs, len(r.spawn))
	}
	token := func(req ports.SpawnRequest) string { return req.Env[0][len(ports.SlotEnv)+1:] }
	// The focused screen is DP-2 now; DP-1's terminal still lands on DP-1.
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.client <- ports.WindowMapped{ID: 1, Slot: token(reqs[0])}
	r.client <- ports.WindowMapped{ID: 2, Slot: token(reqs[1])}
	receive(t, r.scenes)
	set := receive(t, r.scenes)
	if got := shown(set); len(got["DP-1"]) != 1 || len(got["DP-2"]) != 1 || got["DP-1"][0] == got["DP-2"][0] {
		t.Fatal(got)
	}
	// A terminal that exits right after mapping is not respawned at once
	// (no loop for a broken terminal command).
	r.client <- ports.WindowUnmapped{ID: 2}
	receive(t, r.scenes)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	if len(r.spawn) != 0 {
		t.Fatal("respawned at once")
	}
}

func TestLastColumnStaysOnItsOutput(t *testing.T) {
	r := startRig(t, true, nil, left, right)
	r.mapWindow(t, 1)
	set := r.key(t, "Right", ports.ModAlt|ports.ModShift)
	if got := shown(set); len(got["DP-1"]) != 1 || len(got["DP-2"]) != 0 {
		t.Fatal(got)
	}
}

func TestKeyboardScreenFocusSticksUntilPointerLeaves(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.input <- ports.PointerMotion{X: 10, Y: 10}
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	// Jitter on DP-1 keeps DP-2 focused: the next window opens there.
	r.input <- ports.PointerMotion{X: 11, Y: 10}
	if got := shown(r.mapWindow(t, 2)); len(got["DP-2"]) != 1 {
		t.Fatal(got)
	}
	// Entering DP-1 again focuses it.
	r.input <- ports.PointerMotion{X: 250, Y: 10}
	r.input <- ports.PointerMotion{X: 10, Y: 10}
	receive(t, r.scenes)
	if got := shown(r.mapWindow(t, 3)); len(got["DP-1"]) != 2 {
		t.Fatal(got)
	}
}

func TestStateSnapshot(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.client <- ports.WindowMapped{ID: 1, AppID: "foot", PID: 100}
	receive(t, r.scenes)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	<-r.state // the channel holds the latest snapshot only
	r.client <- ports.WindowMapped{ID: 2, AppID: "firefox", PID: 200}
	receive(t, r.scenes)
	// Snapshots from before window 2 may still be queued: wait for it.
	st := receive(t, r.state)
	for len(st.Windows) < 2 {
		st = receive(t, r.state)
	}
	want := ports.State{
		Output:  "DP-2",
		Outputs: []ports.OutputState{{Name: "DP-1", Active: 1, Count: 1}, {Name: "DP-2", Active: 1, Count: 1}},
		Windows: []ports.WindowState{
			{ID: 1, AppID: "foot", PID: 100, Output: "DP-1", Workspace: 1, Visible: true},
			{ID: 2, AppID: "firefox", PID: 200, Output: "DP-2", Workspace: 1, Visible: true},
		},
	}
	want.Window = &want.Windows[1]
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("got  %+v\nwant %+v", st, want)
	}
	// Moving to workspace 2 of DP-2: two workspaces there, window 2 off screen.
	r.input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModAlt, Pressed: true}
	receive(t, r.scenes)
	st = receive(t, r.state)
	if st.Outputs[1] != (ports.OutputState{Name: "DP-2", Active: 2, Count: 2}) || st.Windows[1].Visible || st.Window != nil {
		t.Fatalf("%+v", st)
	}
}

func TestStateFollowsAppIDAndHiddenWorkspace(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Workspaces = []ports.WorkspaceConfig{{Name: "notes", Hidden: true}}
		c.Binds["Alt+n"] = "workspace notes"
	}, left)
	r.client <- ports.WindowMapped{ID: 1, PID: 100}
	receive(t, r.scenes)
	<-r.state
	// An app ID set after map reaches the state.
	r.client <- ports.WindowAppID{ID: 1, AppID: "foot"}
	receive(t, r.scenes)
	// Snapshots from before the app ID may still be queued.
	for st := receive(t, r.state); st.Windows[0].AppID != "foot"; st = receive(t, r.state) {
	}
	// A hidden workspace on screen: no active number, its name, window 1 off screen.
	r.key(t, "n", ports.ModAlt)
	st := receive(t, r.state)
	if st.Outputs[0].Active != 0 || st.Outputs[0].Workspace != "notes" || st.Windows[0].Visible {
		t.Fatalf("%+v", st)
	}
}

func TestMoveColumnOntoFullscreenStaysVisible(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left, right)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	// Window 2 goes to DP-2, alone, and goes fullscreen in place.
	r.key(t, "Right", ports.ModAlt|ports.ModShift)
	r.key(t, "f", ports.ModAlt|ports.ModShift)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	// Window 1 joins from DP-1: the fullscreen ends so it is visible and focused.
	set := r.key(t, "Right", ports.ModAlt|ports.ModShift)
	for _, s := range set {
		for _, w := range s.Windows {
			if w.ID == 1 && (s.Output != "DP-2" || w.Hidden || !w.Focused) {
				t.Fatalf("%s %+v", s.Output, w)
			}
		}
	}
}

// windowsOf lists the tiled windows of an output in scene order, with the
// focused one.
func windowsOf(set []ports.Scene, out string) (ids []ports.WindowID, focused ports.WindowID) {
	for _, s := range set {
		if s.Output != out {
			continue
		}
		for _, w := range s.Windows {
			if !w.Hidden {
				ids = append(ids, w.ID)
			}
			if w.Focused {
				focused = w.ID
			}
		}
	}
	return ids, focused
}

func TestExpelCrossesToFullNeighbor(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left, right)
	// DP-1 (max 2): [1] [2] [3] in the spiral; consume 3 left gives
	// [1] [2 3], focus on 3 at the bottom of the edge column.
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.mapWindow(t, 3)
	r.key(t, "bracketleft", ports.ModAlt)
	// DP-2: [4] [5], full too.
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 4)
	r.mapWindow(t, 5)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	// Expel right at the edge: 3 stacks under 4, the first column of DP-2.
	set := r.key(t, "bracketright", ports.ModAlt)
	if got, _ := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1, 2}) {
		t.Fatal(got)
	}
	got, focused := windowsOf(set, "DP-2")
	if !reflect.DeepEqual(got, []ports.WindowID{4, 3, 5}) || focused != 3 {
		t.Fatal(got, focused)
	}
	if out := lastOutputs(t, r.commands); out.Focused != "DP-2" {
		t.Fatal(out.Focused)
	}
	// 3 is not in the edge column of DP-2: it stacks into [5].
	set = r.key(t, "bracketright", ports.ModAlt)
	if got, focused := windowsOf(set, "DP-2"); !reflect.DeepEqual(got, []ports.WindowID{4, 5, 3}) || focused != 3 {
		t.Fatal(got, focused)
	}
	// Now at the edge with no right monitor: nothing moves.
	set = r.key(t, "bracketright", ports.ModAlt)
	if got, focused := windowsOf(set, "DP-2"); !reflect.DeepEqual(got, []ports.WindowID{4, 5, 3}) || focused != 3 {
		t.Fatal(got, focused)
	}
}
