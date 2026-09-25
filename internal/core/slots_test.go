package core_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/ports"
)

type slotRig struct {
	client chan ports.ClientEvent
	input  chan ports.InputEvent
	reload chan ports.ConfigChanged
	spawn  chan ports.SpawnRequest
	scenes chan []ports.Scene
	errs   chan error
	cfg    ports.Config
}

// startSlots runs core with a hidden "dev" workspace of two slots, toggled
// by Alt+d, on a 100x80 output.
func startSlots(t *testing.T, numbered ...bool) *slotRig {
	t.Helper()
	cfg := config.Defaults()
	cfg.Keyboard.CmdKey = "alt"
	cfg.Border.Width = 0
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "dev", Hidden: len(numbered) == 0, Slots: []ports.SlotConfig{
		{Index: 1, Width: "70%", Argv: []string{"code"}},
		{Index: 2, Width: "30%", Argv: []string{"foot"}},
	}}}
	cfg.Binds["Alt+d"] = "workspace dev"
	r := &slotRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 16),
		reload: make(chan ports.ConfigChanged, 4), spawn: make(chan ports.SpawnRequest, 16),
		scenes: make(chan []ports.Scene, 1), errs: make(chan error, 4), cfg: cfg,
	}
	output := make(chan ports.OutputEvent, 1)
	commands := make(chan ports.ClientCommand, 1024)
	c, err := core.New(cfg, core.Channels{Client: r.client, Input: r.input, Output: output, Config: r.reload, Commands: commands, Spawn: r.spawn, Scenes: r.scenes, ConfigErrors: r.errs})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	go func() { // commands are not under test
		for range commands {
		}
	}()
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, r.scenes)
	return r
}

// token returns the SlotEnv value of a spawn request.
func token(t *testing.T, req ports.SpawnRequest) string {
	t.Helper()
	for _, e := range req.Env {
		if v, ok := strings.CutPrefix(e, ports.SlotEnv+"="); ok {
			return v
		}
	}
	t.Fatalf("no %s in %v", ports.SlotEnv, req.Env)
	return ""
}

// noSpawn checks that nothing was spawned. Core sends spawns before it
// publishes the scene of the same event, so callers wait for that scene first.
func noSpawn(t *testing.T, ch <-chan ports.SpawnRequest) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected spawn %v", v.Argv)
	default:
	}
}

// press sends Alt+key and waits for the scene it publishes.
func (r *slotRig) press(t *testing.T, key string) ports.Scene {
	t.Helper()
	r.input <- ports.KeyEvent{Keysym: key, Mods: ports.ModAlt, Pressed: true}
	r.input <- ports.KeyEvent{Keysym: key, Mods: ports.ModAlt}
	return scene(t, r.scenes)
}

// sync waits for core to handle everything sent so far.
func (r *slotRig) sync(t *testing.T) ports.Scene {
	t.Helper()
	return r.press(t, "Left") // focus-column-left publishes a scene
}

// fill maps the slot windows 2 (code) and 3 (foot) and waits until core
// has placed them: client and input events are separate channels.
func (r *slotRig) fill(t *testing.T) (code, foot ports.SpawnRequest) {
	t.Helper()
	code, foot = receive(t, r.spawn), receive(t, r.spawn)
	r.client <- ports.WindowMapped{ID: 2, Slot: token(t, code)}
	r.client <- ports.WindowMapped{ID: 3, Slot: token(t, foot)}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 2 })
	return code, foot
}

// sceneMatch waits for a first-output scene matching ok.
func sceneMatch(t *testing.T, ch <-chan []ports.Scene, ok func(ports.Scene) bool) ports.Scene {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case s := <-ch:
			if ok(s[0]) {
				return s[0]
			}
		case <-deadline:
			t.Fatal("no matching scene")
		}
	}
}

func visible(s ports.Scene) map[ports.WindowID]ports.Rect {
	m := map[ports.WindowID]ports.Rect{}
	for _, w := range s.Windows {
		if !w.Hidden {
			m[w.ID] = w.Rect
		}
	}
	return m
}

func TestSlotsSpawnAtStartAndFillInOrder(t *testing.T) {
	r := startSlots(t)
	code, foot := receive(t, r.spawn), receive(t, r.spawn)
	if !slices.Equal(code.Argv, []string{"code"}) || !slices.Equal(foot.Argv, []string{"foot"}) || token(t, code) == token(t, foot) {
		t.Fatal(code, foot)
	}
	// A normal window on workspace 1 keeps focus while slot windows map.
	r.client <- ports.WindowMapped{ID: 1}
	// foot maps first; code's window still goes left of it.
	r.client <- ports.WindowMapped{ID: 3, Slot: token(t, foot)}
	r.client <- ports.WindowMapped{ID: 2, Slot: token(t, code)}
	s := sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 3 })
	for _, w := range s.Windows {
		if w.ID != 1 && !w.Hidden || w.ID == 1 && !w.Focused {
			t.Fatalf("slot window on screen or focus moved: %+v", s.Windows)
		}
	}
	s = r.press(t, "d")
	got := visible(s)
	if got[2].X != 0 || got[2].W != 70 || got[3].X != 70 || got[3].W != 30 {
		t.Fatal(got)
	}
	noSpawn(t, r.spawn) // full slots are not respawned
}

func TestSlotRefilledOnlyWhenShown(t *testing.T) {
	r := startSlots(t)
	// Wait for both windows: a one-window scene from before the unmap
	// would let the key below race it.
	_, foot := r.fill(t)
	r.client <- ports.WindowUnmapped{ID: 3} // foot exits
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	noSpawn(t, r.spawn) // nothing relaunches on its own
	r.input <- ports.KeyEvent{Keysym: "d", Mods: ports.ModAlt, Pressed: true}
	r.input <- ports.KeyEvent{Keysym: "d", Mods: ports.ModAlt}
	again := receive(t, r.spawn)
	if !slices.Equal(again.Argv, []string{"foot"}) || token(t, again) == token(t, foot) {
		t.Fatal(again)
	}
	r.client <- ports.WindowMapped{ID: 4, Slot: token(t, again)}
	s := sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(visible(s)) == 2 })
	if got := visible(s); got[4].X != 70 {
		t.Fatal(got)
	}
	// Leaving and showing dev again with full slots spawns nothing.
	r.press(t, "d")
	r.press(t, "d")
	noSpawn(t, r.spawn)
}

func TestSlotExtraWindowsAreNormal(t *testing.T) {
	r := startSlots(t)
	code := receive(t, r.spawn)
	receive(t, r.spawn)
	r.client <- ports.WindowMapped{ID: 2, Slot: token(t, code)}
	// A second window of the same process: the slot is taken, normal rules.
	r.client <- ports.WindowMapped{ID: 5, Slot: token(t, code)}
	r.client <- ports.WindowMapped{ID: 6, Slot: "unknown"}
	s := sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 3 })
	if got := visible(s); len(got) != 2 || got[5] == (ports.Rect{}) || got[6] == (ports.Rect{}) {
		t.Fatalf("extra windows not on the workspace on screen: %v", got)
	}
}

func TestSlotReload(t *testing.T) {
	r := startSlots(t)
	code, foot := receive(t, r.spawn), receive(t, r.spawn)
	r.client <- ports.WindowMapped{ID: 2, Slot: token(t, code)}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	cfg := r.cfg
	ws := cfg.Workspaces[0]
	ws.Slots = []ports.SlotConfig{
		{Index: 1, Width: "50%", Argv: []string{"code"}},  // width change only
		{Index: 2, Width: "30%", Argv: []string{"kitty"}}, // empty slot, new command
		{Index: 3, Width: "20%", Argv: []string{"btop"}},  // new slot
	}
	cfg.Workspaces = []ports.WorkspaceConfig{ws}
	r.reload <- ports.ConfigChanged{Config: cfg}
	var argv []string
	for range 2 {
		argv = append(argv, receive(t, r.spawn).Argv[0])
	}
	slices.Sort(argv)
	if !slices.Equal(argv, []string{"btop", "kitty"}) {
		t.Fatal(argv)
	}
	scene(t, r.scenes)
	noSpawn(t, r.spawn)
	// The old foot spawn no longer fills slot 2 (its command changed): it
	// opens as a normal window on workspace 1, on screen.
	r.client <- ports.WindowMapped{ID: 3, Slot: token(t, foot)}
	s := sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 2 })
	if got := visible(s); len(got) != 1 || got[3] == (ports.Rect{}) {
		t.Fatal(got)
	}
	r.input <- ports.KeyEvent{Keysym: "d", Mods: ports.ModAlt, Pressed: true}
	r.input <- ports.KeyEvent{Keysym: "d", Mods: ports.ModAlt}
	s = sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(visible(s)) == 1 })
	if got := visible(s); got[2].W != 50 {
		t.Fatal(got)
	}
	noSpawn(t, r.spawn) // kitty and btop are still starting
	// Dropping the workspace's slots spawns nothing and keeps the window.
	ws.Slots = nil
	cfg.Workspaces = []ports.WorkspaceConfig{ws}
	r.reload <- ports.ConfigChanged{Config: cfg}
	scene(t, r.scenes)
	noSpawn(t, r.spawn)
}

func TestInvalidSlotWidthRejected(t *testing.T) {
	r := startSlots(t)
	receive(t, r.spawn)
	receive(t, r.spawn)
	cfg := r.cfg
	ws := cfg.Workspaces[0]
	ws.Slots = []ports.SlotConfig{{Index: 1, Width: "wide", Argv: []string{"x"}}}
	cfg.Workspaces = []ports.WorkspaceConfig{ws}
	r.reload <- ports.ConfigChanged{Config: cfg}
	if err := receive(t, r.errs); err == nil {
		t.Fatal("no error")
	}
	r.sync(t)
	noSpawn(t, r.spawn)
}

// Reloading while the slot workspace is on screen spawns a new slot once.
func TestSlotReloadWhileShownSpawnsOnce(t *testing.T) {
	r := startSlots(t)
	r.fill(t)
	r.press(t, "d")
	cfg := r.cfg
	ws := cfg.Workspaces[0]
	ws.Slots = append(ws.Slots, ports.SlotConfig{Index: 3, Width: "20%", Argv: []string{"btop"}})
	cfg.Workspaces = []ports.WorkspaceConfig{ws}
	r.reload <- ports.ConfigChanged{Config: cfg}
	if v := receive(t, r.spawn); v.Argv[0] != "btop" {
		t.Fatal(v)
	}
	scene(t, r.scenes)
	noSpawn(t, r.spawn)
}

// Binds on the workspace on screen do not refill its empty slots; only
// showing it again does.
func TestSlotNotRefilledByOtherBinds(t *testing.T) {
	r := startSlots(t)
	r.fill(t)
	r.press(t, "d")
	r.client <- ports.WindowUnmapped{ID: 3}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	r.sync(t)
	noSpawn(t, r.spawn)
	r.press(t, "d")
	r.press(t, "d")
	if v := receive(t, r.spawn); v.Argv[0] != "foot" {
		t.Fatal(v)
	}
}

// After a slot window closes, another window with the old token (a child
// of the slot app) opens as a normal window, not in the empty slot.
func TestSlotOldTokenIsNormal(t *testing.T) {
	r := startSlots(t)
	_, foot := r.fill(t)
	r.client <- ports.WindowUnmapped{ID: 3}
	r.client <- ports.WindowMapped{ID: 9, Slot: token(t, foot)}
	s := sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(visible(s)) == 1 })
	if got := visible(s); got[9] == (ports.Rect{}) {
		t.Fatalf("old-token window hidden in the slot: %v", got)
	}
}

// A slot whose command never maps a window gets one show to do it; on the
// next show it is respawned and its old token no longer fills it.
func TestPendingSlotRespawnedOnSecondShow(t *testing.T) {
	r := startSlots(t)
	code, foot := receive(t, r.spawn), receive(t, r.spawn)
	r.client <- ports.WindowMapped{ID: 2, Slot: token(t, code)}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	r.press(t, "d") // foot still starting: not respawned
	noSpawn(t, r.spawn)
	r.press(t, "d")
	r.press(t, "d")
	again := receive(t, r.spawn)
	if again.Argv[0] != "foot" || token(t, again) == token(t, foot) {
		t.Fatal(again)
	}
	r.client <- ports.WindowMapped{ID: 7, Slot: token(t, foot)}
	s := sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 2 })
	if got := visible(s); got[7] == (ports.Rect{}) {
		t.Fatalf("stale-token window not placed as a normal one: %v", got)
	}
	r.client <- ports.WindowMapped{ID: 8, Slot: token(t, again)}
	s = sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 3 })
	if got := visible(s); got[8].W != 30 {
		t.Fatalf("respawned window not in its slot: %v", got)
	}
}

// Moving a slot window to another workspace releases the slot; showing the
// workspace again refills it.
func TestSlotReleasedWhenWindowMoved(t *testing.T) {
	r := startSlots(t, true) // dev is numbered: [1] [dev] [empty]
	r.fill(t)
	r.press(t, "d") // on dev, focus on code (slot 1)
	r.input <- ports.KeyEvent{Keysym: "Prior", Mods: ports.ModAlt | ports.ModShift, Pressed: true}
	scene(t, r.scenes) // move-to-workspace-up: code goes to workspace 1
	noSpawn(t, r.spawn)
	r.press(t, "d") // back to workspace 1
	r.press(t, "d") // dev again: slot 1 is empty
	if v := receive(t, r.spawn); v.Argv[0] != "code" {
		t.Fatal(v)
	}
}
