package core_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

type slotRig struct {
	client     chan ports.ClientEvent
	input      chan ports.InputEvent
	reload     chan ports.ConfigChanged
	spawn      chan ports.SpawnRequest
	scenes     chan []ports.Scene
	workspaces chan ports.Workspaces
	errs       chan error
	cfg        ports.Config
	// With animations on and a clock the test moves (startSlotsMode),
	// land flips the output past every spring.
	clk    *stepClock
	frames chan ports.OutputFrame
	lands  int
}

// startSlots runs core with a hidden "dev" workspace of two slots, toggled
// by Alt+d, on a 100x80 output. One flag makes dev numbered, two also set
// fixed overflow.
func startSlots(t *testing.T, numbered ...bool) *slotRig {
	t.Helper()
	return startSlotsMode(t, nil, numbered...)
}

// startSlotsMode is startSlots with animations forced off (*false) or on
// with a clock the test moves (*true), whose springs land through land;
// nil keeps the default config on the system clock.
func startSlotsMode(t *testing.T, animated *bool, numbered ...bool) *slotRig {
	t.Helper()
	cfg := altCmdDefaults()
	if animated != nil {
		cfg.Animations.On = *animated
	}
	cfg.Border.Width = 0
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "dev", Slots: []ports.SlotConfig{
		{Index: 1, Width: "70%", Argv: []string{"code"}},
		{Index: 2, Width: "30%", Argv: []string{"foot"}},
	}}}
	cfg.Binds["Alt+d"] = "workspace dev"
	if len(numbered) > 1 {
		cfg.Layout.Overflow = "fixed"
	}
	r := &slotRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 16),
		reload: make(chan ports.ConfigChanged, 4), spawn: make(chan ports.SpawnRequest, 16),
		scenes: make(chan []ports.Scene, 1), workspaces: make(chan ports.Workspaces, 1), errs: make(chan error, 4), cfg: cfg,
	}
	output := make(chan ports.OutputEvent, 1)
	commands := make(chan ports.ClientCommand, 1024)
	ch := core.Channels{Client: r.client, Input: r.input, Output: output, Config: r.reload, Commands: commands, Spawn: r.spawn, Scenes: r.scenes, Workspaces: r.workspaces, ConfigErrors: r.errs}
	opts := core.Options{}
	if animated != nil && *animated {
		r.clk, r.frames = newStepClock(t), make(chan ports.OutputFrame)
		opts.Clock, ch.Frames = r.clk.clock, r.frames
		t.Cleanup(func() {
			if r.lands == 0 {
				t.Error("no spring ever ran: the animations-on variant checked nothing")
			}
		})
	}
	c, err := core.New(cfg, ch, opts)
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

// land is s once every spring has landed: the scene the flips publish, or s
// itself when nothing animates (always with animations off or on the
// system clock).
func (r *slotRig) land(t *testing.T, s ports.Scene) ports.Scene {
	t.Helper()
	if r.clk == nil {
		return s
	}
	set, ok := r.clk.settle(t, r.frames, r.scenes, "OUT-1")
	if !ok {
		return s
	}
	r.lands++
	return set[0]
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

// Activating a workspace through the protocol must count as showing it for
// pending-slot retry, just like switching to it with a key.
func TestWorkspaceActivateRetriesPendingSlot(t *testing.T) {
	r := startSlots(t)
	first, second := receive(t, r.spawn), receive(t, r.spawn)
	// The channel keeps the latest snapshot: one from before the output
	// may still be there.
	initial := receive(t, r.workspaces)
	for len(initial.Outputs) == 0 {
		initial = receive(t, r.workspaces)
	}
	var dev uint64
	for _, w := range initial.Outputs[0].Workspaces {
		if w.Configured == "dev" {
			dev = w.ID
		}
	}
	if dev == 0 {
		t.Fatal(initial)
	}
	r.client <- ports.WorkspaceActivate{IDs: []uint64{dev}}
	scene(t, r.scenes)
	noSpawn(t, r.spawn) // first show marks pending slots stale
	r.press(t, "d")     // leave dev
	r.client <- ports.WorkspaceActivate{IDs: []uint64{dev}}
	scene(t, r.scenes)
	again := receive(t, r.spawn)
	if token(t, again) == token(t, first) || token(t, again) == token(t, second) {
		t.Fatal("activation did not refresh the pending slot token")
	}
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
	both(t, func(t *testing.T, animated bool) { slotRefilledOnlyWhenShown(t, startSlotsMode(t, &animated)) })
}

func slotRefilledOnlyWhenShown(t *testing.T, r *slotRig) {
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
	if got := visible(r.land(t, s)); got[4].X != 70 {
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
	both(t, func(t *testing.T, animated bool) { pendingSlotRespawnedOnSecondShow(t, startSlotsMode(t, &animated)) })
}

func pendingSlotRespawnedOnSecondShow(t *testing.T, r *slotRig) {
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
	if got := visible(r.land(t, s)); got[8].W != 30 {
		t.Fatalf("respawned window not in its slot: %v", got)
	}
}

// Moving a slot window to another workspace releases the slot; showing the
// workspace again refills it.
// Startup commands are spawned once, before the slots.
func TestStartupCommands(t *testing.T) {
	cfg := config.Defaults()
	cfg.Startup = [][]string{{"wl-paste", "--watch", "cliphist", "store"}, {"waybar"}}
	spawn := make(chan ports.SpawnRequest, 4)
	c, err := core.New(cfg, core.Channels{Client: make(chan ports.ClientEvent), Input: make(chan ports.InputEvent), Output: make(chan ports.OutputEvent), Config: make(chan ports.ConfigChanged), Commands: make(chan ports.ClientCommand, 16), Spawn: spawn, Scenes: make(chan []ports.Scene, 1), ConfigErrors: make(chan error, 1)}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for _, want := range cfg.Startup {
		if got := receive(t, spawn); !slices.Equal(got.Argv, want) {
			t.Fatalf("spawned %q, want %q", got.Argv, want)
		}
	}
}

// A spawn bind with several commands sends each one, in order.
func TestSpawnBindSeveralCommands(t *testing.T) {
	cfg := altCmdDefaults()
	cfg.Terminal.AutoOpen = "off"
	cfg.Binds["Alt+p"] = "spawn grim -g x; notify-send done"
	input := make(chan ports.InputEvent, 4)
	output := make(chan ports.OutputEvent, 1)
	scenes := make(chan []ports.Scene, 1)
	spawn := make(chan ports.SpawnRequest, 4)
	c, err := core.New(cfg, core.Channels{Client: make(chan ports.ClientEvent), Input: input, Output: output, Config: make(chan ports.ConfigChanged), Commands: make(chan ports.ClientCommand, 16), Spawn: spawn, Scenes: scenes, ConfigErrors: make(chan error, 1)}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "p", Mods: ports.ModAlt, Pressed: true}
	for _, want := range [][]string{{"grim", "-g", "x"}, {"notify-send", "done"}} {
		if got := receive(t, spawn); !slices.Equal(got.Argv, want) {
			t.Fatalf("spawned %q, want %q", got.Argv, want)
		}
	}
}

// A fullscreen slot window (fixed overflow) keeps its slot: coming back to
// dev spawns no duplicate.
func TestSlotKeptWhileFullscreen(t *testing.T) {
	r := startSlots(t, true, true)
	r.fill(t)
	r.press(t, "d") // on dev, focus on code (slot 1)
	r.input <- ports.KeyEvent{Keysym: "f", Mods: ports.ModAlt | ports.ModShift, Pressed: true}
	r.input <- ports.KeyEvent{Keysym: "f", Mods: ports.ModAlt | ports.ModShift}
	scene(t, r.scenes)
	r.press(t, "d") // leave and show dev while code is away
	r.press(t, "d")
	noSpawn(t, r.spawn)
	// code opens a second window while fullscreen: the slot stays.
	r.client <- ports.WindowMapped{ID: 9}
	scene(t, r.scenes)
	r.press(t, "d")
	r.press(t, "d")
	noSpawn(t, r.spawn)
}

// Expelling a slot window from a stacked column keeps one column per slot:
// the slot and its width follow the window, and nothing is respawned.
func TestSlotFollowsExpelledWindow(t *testing.T) {
	r := startSlots(t, true) // dev is numbered: [1] [dev] [empty]
	r.fill(t)
	r.press(t, "d") // dev: [code] [foot], focus on code
	r.client <- ports.WindowMapped{ID: 9}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 3 })
	r.press(t, "bracketleft")  // 9 joins code: [code 9] [foot]
	r.press(t, "Up")           // focus code, the slot window
	r.press(t, "bracketright") // code leaves: [9] [code] [foot]
	r.press(t, "d")
	s := r.press(t, "d") // shown again: no slot is empty
	noSpawn(t, r.spawn)
	if got := visible(s); got[2].W != 70 {
		t.Fatalf("slots: %v", got)
	}
}

// A floating child keeps the pending claim for the first ordinary window.
func TestFloatingSlotTokenDoesNotClaim(t *testing.T) {
	r := startSlots(t)
	code, _ := receive(t, r.spawn), receive(t, r.spawn)
	r.client <- ports.WindowMapped{ID: 10, Slot: token(t, code), Floating: true, Width: 20, Height: 20}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	r.client <- ports.WindowMapped{ID: 11, Slot: token(t, code)}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 2 })
	s := r.press(t, "d")
	if got := visible(s); got[11].W != 70 {
		t.Fatalf("floating child consumed slot claim: %v", got)
	}
	r.client <- ports.WindowMapped{ID: 12, Slot: token(t, code)}
	s = sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 3 })
	if got := visible(s); got[12].W == 70 {
		t.Fatalf("duplicate token claimed slot: %v", got)
	}
}
