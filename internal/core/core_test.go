package core_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

// scene receives the next scene set and returns the first output's scene.
func scene(t *testing.T, ch <-chan []ports.Scene) ports.Scene {
	t.Helper()
	return receive(t, ch)[0]
}

// command receives the next client command, skipping output scale updates
// and user activity reports.
func command(t *testing.T, ch <-chan ports.ClientCommand) ports.ClientCommand {
	t.Helper()
	for {
		switch v := receive(t, ch); v.(type) {
		case nil, ports.SetOutputs, ports.UserActivity:
		default:
			return v
		}
	}
}

// modeLanding gives a hand-rolled core test its animation mode: off, or on with
// a clock the test moves and the page flips it sends. land returns the scene
// set once every spring has landed (the set itself when none runs, and
// always with animations off). An on run that never saw a spring would be
// the off run again, so it fails.
type modeLanding struct {
	t       *testing.T
	clk     *stepClock
	frames  chan ports.OutputFrame
	scenes  chan []ports.Scene
	outputs []string
	lands   int
}

// newLanding sets cfg's animations and, when on, the clock of opts and flips of ch.
func newLanding(t *testing.T, animated bool, cfg *ports.Config, ch *core.Channels, opts *core.Options, outputs ...string) *modeLanding {
	t.Helper()
	cfg.Animations.On = animated
	l := &modeLanding{t: t, scenes: ch.Scenes, outputs: outputs}
	if animated {
		l.clk, l.frames = newStepClock(t), make(chan ports.OutputFrame)
		opts.Clock, ch.Frames = l.clk.clock, l.frames
		t.Cleanup(func() {
			if l.lands == 0 {
				t.Error("no spring ever ran: the animations-on variant checked nothing")
			}
		})
	}
	return l
}

func (l *modeLanding) land(set []ports.Scene) []ports.Scene {
	l.t.Helper()
	if l.clk == nil {
		return set
	}
	landed, ok := l.clk.settle(l.t, l.frames, l.scenes, l.outputs...)
	if !ok {
		return set
	}
	l.lands++
	return landed
}

// one is land for the first output's scene.
func (l *modeLanding) one(s ports.Scene) ports.Scene { return l.land([]ports.Scene{s})[0] }

func TestOwner(t *testing.T) { both(t, owner) }

func owner(t *testing.T, animated bool) {
	cfg := altCmdDefaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 8
	client := make(chan ports.ClientEvent, 128)
	input := make(chan ports.InputEvent, 32)
	output := make(chan ports.OutputEvent, 8)
	reload := make(chan ports.ConfigChanged, 8)
	commands := make(chan ports.ClientCommand, 256)
	spawn := make(chan ports.SpawnRequest, 8)
	scenes := make(chan []ports.Scene, 1)
	errs := make(chan error, 8)
	ch := core.Channels{Client: client, Input: input, Output: output, Config: reload, Commands: commands, Spawn: spawn, Scenes: scenes, ConfigErrors: errs}
	opts := core.Options{}
	l := newLanding(t, animated, &cfg, &ch, &opts, "OUT-1")
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	s := scene(t, scenes)
	if s.OutputWidth != 100 {
		t.Fatal(s)
	}
	client <- ports.WindowMapped{ID: 1}
	s = l.one(scene(t, scenes))
	// A single column fills the usable width.
	if len(s.Windows) != 1 || s.Windows[0].Rect.W != 84 || s.Windows[0].Inset != 0 {
		t.Fatal(s)
	}
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 84, Height: 64, Activated: true, Output: "OUT-1", Visible: true}) {
		t.Fatal(v)
	}
	command(t, commands)
	client <- ports.WindowMapped{ID: 2}
	s = scene(t, scenes)
	if len(s.Windows) != 2 {
		t.Fatal(s)
	}
	// Commands for focus changes and second mapping.
	for len(commands) > 0 {
		<-commands
	}
	client <- ports.WindowMapped{ID: 2}
	scene(t, scenes)
	if len(commands) != 0 {
		t.Fatal("duplicate configure")
	}
	input <- ports.KeyEvent{Keysym: "Return", Mods: ports.ModAlt, Pressed: true}
	scene(t, scenes)
	if v := receive(t, spawn); len(v.Argv) != 1 || v.Argv[0] != "foot" {
		t.Fatal(v)
	}
	input <- ports.KeyEvent{Keysym: "Return", Mods: ports.ModAlt}
	input <- ports.KeyEvent{Keysym: "x", Pressed: true}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 2, Key: ports.KeyEvent{Keysym: "x", Pressed: true}}) {
		t.Fatal(v)
	}
	input <- ports.KeyEvent{Keysym: "Q", Mods: ports.ModAlt, Pressed: true}
	scene(t, scenes)
	if v := command(t, commands); v != (ports.CloseWindow{ID: 2}) {
		t.Fatal(v)
	}
	client <- ports.WindowUnmapped{ID: 2}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	cfg.Layout.Gaps = 4
	reload <- ports.ConfigChanged{Config: cfg}
	s = l.one(scene(t, scenes))
	if s.Windows[0].Rect.W != 92 {
		t.Fatal(s)
	}
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 92, Height: 72, Activated: true, Output: "OUT-1", Visible: true}) {
		t.Fatal(v)
	}
	cfg.Layout.MaxColumns = 0
	reload <- ports.ConfigChanged{Config: cfg}
	receive(t, errs)
	for i := 0; i < 100; i++ {
		client <- ports.WindowMapped{ID: ports.WindowID(i + 3)}
	}
	input <- ports.KeyEvent{Keysym: "BackSpace", Mods: ports.ModCtrl | ports.ModAlt, Pressed: true}
	if err := receive(t, done); !errors.Is(err, core.ErrQuit) {
		t.Fatal(err)
	}
}
func TestCancelAndSceneCapacity(t *testing.T) {
	cfg := config.Defaults()
	if _, err := core.New(cfg, core.Channels{Scenes: make(chan []ports.Scene, 2)}, core.Options{}); err == nil {
		t.Fatal("capacity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c, err := core.New(cfg, core.Channels{Scenes: make(chan []ports.Scene, 1)}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	cancel()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestKeyPressState(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	commands := make(chan ports.ClientCommand, 16)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	command(t, commands)
	command(t, commands)
	press := ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true}
	input <- press
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper}
	input <- ports.KeyEvent{Keysym: "Left", Pressed: true}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "Left", Pressed: true}}) {
		t.Fatal(v)
	}
	input <- ports.KeyEvent{Keysym: "Left"}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "Left"}}) {
		t.Fatal(v)
	}
	input <- press
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "Left", Pressed: true}
	command(t, commands)
	input <- ports.KeyEvent{Keysym: "Left"}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "Left"}}) {
		t.Fatal(v)
	}
}

func TestBoundReleaseSwallowed(t *testing.T) {
	cfg := config.Defaults()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 2)
	commands := make(chan ports.ClientCommand, 8)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	command(t, commands)
	command(t, commands)
	input <- ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true}
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "Left"}
	input <- ports.KeyEvent{Keysym: "x", Pressed: true}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "x", Pressed: true}}) {
		t.Fatal(v)
	}
}

func TestPointerFocusAndGrab(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 8
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	first := scene(t, scenes).Windows[0].Rect
	client <- ports.WindowMapped{ID: 2}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.PointerMotion{X: float64(first.X + 2), Y: float64(first.Y + 3), Time: 1 * time.Millisecond}
	if v := command(t, commands); v != (ports.PointerFocus{ID: 1, X: 2, Y: 3}) {
		t.Fatal(v)
	}
	if v := command(t, commands); v != (ports.PointerMotionTo{ID: 1, X: 2, Y: 3, Time: 1 * time.Millisecond}) {
		t.Fatal(v)
	}
	input <- ports.PointerAxis{Vertical: ports.ScrollAxis{Set: true, Value: 15, V120: 120}}
	if v := command(t, commands); v != (ports.PointerAxisTo{ID: 1, Axis: ports.PointerAxis{Vertical: ports.ScrollAxis{Set: true, Value: 15, V120: 120}}}) {
		t.Fatal(v)
	}
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	if v := command(t, commands); v != (ports.PointerButtonTo{ID: 1, Button: 0x110, Pressed: true}) {
		t.Fatal(v)
	}
	scene(t, scenes)
	found := false
	for len(commands) > 0 {
		if v, ok := (<-commands).(ports.FocusWindow); ok && v.ID == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("missing keyboard focus")
	}
	input <- ports.PointerMotion{X: 0, Y: 0}
	if v := command(t, commands); v != (ports.PointerFocus{}) {
		t.Fatal(v)
	}
	// Mid-drag off the window, scroll goes to what is under the pointer:
	// nothing here, not the grab window that got a leave.
	input <- ports.PointerAxis{Vertical: ports.ScrollAxis{Set: true, Value: 1}}
	input <- ports.PointerButton{Button: 0x110}
	if v := command(t, commands); v != (ports.PointerButtonTo{ID: 1, Button: 0x110}) {
		t.Fatal(v)
	}
}

func TestBorderInset(t *testing.T) { both(t, borderInset) }

func borderInset(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Border.Width = 2
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	ch := core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}
	opts := core.Options{}
	l := newLanding(t, animated, &cfg, &ch, &opts, "OUT-1")
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	// Alone, the window is borderless and gets the full rect.
	client <- ports.WindowMapped{ID: 1}
	r := l.one(scene(t, scenes)).Windows[0].Rect
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: r.W, Height: r.H, Activated: true, Output: "OUT-1", Visible: true}) {
		t.Fatalf("%v for outer %v", v, r)
	}
	// With a second column, the left window owns the shared separator: its
	// client is 2px narrower on the right; the right window keeps its rect.
	client <- ports.WindowMapped{ID: 2}
	s := l.one(scene(t, scenes))
	r = s.Windows[1].Rect
	if s.Windows[0].Inset != ports.SideRight || s.Windows[1].Inset != 0 || len(s.Separators) == 0 {
		t.Fatal(s)
	}
	want := ports.ConfigureWindow{ID: 1, Width: s.Windows[0].Rect.W - 2, Height: r.H, Activated: false, Output: "OUT-1", Visible: true}
	found := false
	for len(commands) > 0 {
		if v := <-commands; v == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %v for outer %v", want, r)
	}
	input <- ports.PointerMotion{X: float64(r.X + 2), Y: float64(r.Y + 5), Time: 1 * time.Millisecond}
	if v := command(t, commands); v != (ports.PointerFocus{ID: 2, X: 2, Y: 5}) {
		t.Fatal(v)
	}
}

func TestLayerKeyboardFocus(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	focusOf := func() ports.WindowID {
		t.Helper()
		for {
			if v, ok := command(t, commands).(ports.FocusWindow); ok {
				return v.ID
			}
		}
	}
	if id := focusOf(); id != 1 {
		t.Fatalf("window focus %d", id)
	}
	// An on-demand bar does not steal focus; an exclusive launcher does.
	launcher := ports.LayerSurface{ID: 7, Layer: ports.LayerOverlay, Width: 40, Height: 20, Keyboard: 1}
	bar := ports.LayerSurface{ID: 6, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 5, Keyboard: 2}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar, launcher}}
	scene(t, scenes)
	if id := focusOf(); id != 7 {
		t.Fatalf("layer focus %d", id)
	}
	input <- ports.KeyEvent{Keysym: "a", Keycode: 30, Pressed: true}
	for {
		if v, ok := command(t, commands).(ports.ForwardKey); ok {
			if v.ID != 7 {
				t.Fatalf("key went to %d", v.ID)
			}
			break
		}
	}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	scene(t, scenes)
	if id := focusOf(); id != 1 {
		t.Fatalf("focus not restored: %d", id)
	}
}

func TestWorkspaceSwitch(t *testing.T) { workspaceSwitch(t, false) }

// With animations on the scene right after the key shows the slide's start:
// the test lands it before it asserts.
func TestWorkspaceSwitchAnimated(t *testing.T) { workspaceSwitch(t, true) }

// settledScene lands the springs an action started on the single output
// OUT-1 of a rig with a stepClock and returns its scene; animations off:
// the scene itself.
func settledScene(t *testing.T, sc *stepClock, frames chan ports.OutputFrame, scenes chan []ports.Scene, s ports.Scene, mustAnimate bool) ports.Scene {
	t.Helper()
	if sc == nil {
		return s
	}
	set, ok := sc.settle(t, frames, scenes, "OUT-1")
	if !ok {
		if mustAnimate {
			t.Fatal("the action started no animation: the case checks nothing")
		}
		return s
	}
	return set[0]
}

func workspaceSwitch(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Animations.On = animated
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	ch := core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}
	opts := core.Options{}
	var sc *stepClock
	frames := make(chan ports.OutputFrame)
	if animated {
		sc = newStepClock(t)
		opts.Clock, ch.Frames = sc.clock, frames
	}
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	// AZERTY: the 2 key prints eacute; the default bind is on its physical key (code 3).
	input <- ports.KeyEvent{Keysym: "eacute", Base: "eacute", Keycode: 3, Mods: ports.ModSuper, Pressed: true}
	s := settledScene(t, sc, frames, scenes, scene(t, scenes), animated)
	if len(s.Windows) != 1 || !s.Windows[0].Hidden {
		t.Fatal(s)
	}
	// Window 1 is deactivated and loses keyboard focus.
	var sawDeactivate, sawFocus bool
	for len(commands) > 0 {
		switch v := (<-commands).(type) {
		case ports.ConfigureWindow:
			sawDeactivate = sawDeactivate || (v.ID == 1 && !v.Activated)
		case ports.FocusWindow:
			sawFocus = sawFocus || v.ID == 0
		}
	}
	if !sawDeactivate || !sawFocus {
		t.Fatal(sawDeactivate, sawFocus)
	}
	// A new window opens on workspace 2, alone and borderless.
	client <- ports.WindowMapped{ID: 2}
	s = settledScene(t, sc, frames, scenes, scene(t, scenes), false)
	if len(s.Windows) != 2 || s.Windows[1].ID != 2 || s.Windows[1].Hidden || s.Windows[1].Inset != 0 {
		t.Fatal(s)
	}
}

func TestClickAfterWorkspaceSwitch(t *testing.T) { clickAfterWorkspaceSwitch(t, false) }

func TestClickAfterWorkspaceSwitchAnimated(t *testing.T) { clickAfterWorkspaceSwitch(t, true) }

func clickAfterWorkspaceSwitch(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Animations.On = animated
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	ch := core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}
	opts := core.Options{}
	var sc *stepClock
	frames := make(chan ports.OutputFrame)
	if animated {
		sc = newStepClock(t)
		opts.Clock, ch.Frames = sc.clock, frames
	}
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	input <- ports.PointerMotion{X: 50, Y: 40}
	for len(commands) < 3 {
		time.Sleep(time.Millisecond)
	}
	for len(commands) > 0 {
		<-commands
	}
	// Cmd+2 hides window 1; the pointer must leave it.
	input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModSuper, Pressed: true}
	settledScene(t, sc, frames, scenes, scene(t, scenes), animated)
	var cleared bool
	for len(commands) > 0 {
		if v, ok := (<-commands).(ports.PointerFocus); ok && v.ID == 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("pointer focus kept on hidden window")
	}
	// A click without moving does not reach window 1 nor switch back.
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	input <- ports.PointerButton{Button: 0x110}
	client <- ports.WindowMapped{ID: 2}
	s := settledScene(t, sc, frames, scenes, scene(t, scenes), false)
	if s.Windows[0].ID != 1 || !s.Windows[0].Hidden || s.Windows[1].Hidden {
		t.Fatal(s)
	}
	for len(commands) > 0 {
		if v, ok := (<-commands).(ports.PointerButtonTo); ok {
			t.Fatal(v)
		}
	}
}

func TestShiftReleasedFirst(t *testing.T) {
	cfg := config.Defaults()
	input := make(chan ports.InputEvent, 8)
	client := make(chan ports.ClientEvent, 2)
	commands := make(chan ports.ClientCommand, 16)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	// US Cmd+Shift+1 prints exclam; the physical key (code 2) matches move-column-to-workspace 1.
	input <- ports.KeyEvent{Keysym: "exclam", Base: "1", Keycode: 2, Mods: ports.ModSuper | ports.ModShift, Pressed: true}
	scene(t, scenes)
	// Shift goes up first, so the release reports "1": still swallowed.
	input <- ports.KeyEvent{Keysym: "1", Keycode: 2, Mods: ports.ModSuper}
	input <- ports.KeyEvent{Keysym: "x", Keycode: 45, Pressed: true}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "x", Keycode: 45, Pressed: true}}) {
		t.Fatal(v)
	}
}

func TestOutputScale(t *testing.T) { both(t, outputScale) }

func outputScale(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	cfg.Outputs = []ports.OutputConfig{{Name: "DP-2", Scale: 2}}
	cfg.Binds["Cmd+equal"] = "scale-up"
	cfg.Binds["Cmd+minus"] = "scale-down"
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	reload := make(chan ports.ConfigChanged, 1)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	ch := core.Channels{Client: client, Input: input, Output: output, Config: reload, Commands: commands, Scenes: scenes}
	opts := core.Options{}
	l := newLanding(t, animated, &cfg, &ch, &opts, "DP-2")
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	// 5120x2160 at scale 2 is 2560x1080 logical.
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "DP-2", Width: 5120, Height: 2160}}
	s := scene(t, scenes)
	if s.Scale != 2 || s.OutputWidth != 2560 || s.OutputHeight != 1080 {
		t.Fatal(s.Scale, s.OutputWidth, s.OutputHeight)
	}
	if v := receive(t, commands).(ports.SetOutputs); len(v.Outputs) != 1 || v.Outputs[0].Scale != 2 || v.Outputs[0].Width != 2560 || v.Outputs[0].Height != 1080 || v.Focused != "DP-2" {
		t.Fatal(v)
	}
	client <- ports.WindowMapped{ID: 1}
	l.one(scene(t, scenes))
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 2560, Height: 1080, Activated: true, Output: "DP-2", Visible: true}) {
		t.Fatal(v)
	}
	for len(commands) > 0 {
		<-commands
	}
	// The pointer arrives in global logical pixels.
	input <- ports.PointerMotion{X: 500, Y: 200}
	if animated {
		// The page flips that landed the map's springs re-hit the pointer
		// (still at the origin, over the window): focus was already sent.
		if v, ok := command(t, commands).(ports.PointerMotionTo); !ok || v.ID != 1 || v.X != 500 || v.Y != 200 {
			t.Fatal(v)
		}
	} else if v := command(t, commands); v != (ports.PointerFocus{ID: 1, X: 500, Y: 200}) {
		t.Fatal(v)
	}
	// Cmd+- steps down through the clean scales of 5120x2160: 2 → 5/3.
	input <- ports.KeyEvent{Keysym: "minus", Keycode: 12, Mods: ports.ModSuper, Pressed: true}
	s = scene(t, scenes)
	if s.Scale != 5.0/3 || s.OutputWidth != 3072 || s.OutputHeight != 1296 {
		t.Fatal(s.Scale, s.OutputWidth, s.OutputHeight)
	}
	input <- ports.KeyEvent{Keysym: "minus", Keycode: 12, Mods: ports.ModSuper}
	input <- ports.KeyEvent{Keysym: "equal", Keycode: 13, Mods: ports.ModSuper, Pressed: true}
	if s = scene(t, scenes); s.Scale != 2 {
		t.Fatal(s.Scale)
	}
	// A reload that keeps the config scale keeps the live one too.
	input <- ports.KeyEvent{Keysym: "equal", Keycode: 13, Mods: ports.ModSuper}
	input <- ports.KeyEvent{Keysym: "equal", Keycode: 13, Mods: ports.ModSuper, Pressed: true}
	if s = scene(t, scenes); s.Scale != 2.5 {
		t.Fatal(s.Scale)
	}
	cfg.Layout.Gaps = 1
	reload <- ports.ConfigChanged{Config: cfg}
	if s = scene(t, scenes); s.Scale != 2.5 {
		t.Fatal(s.Scale)
	}
	// Changing the config scale wins.
	cfg.Outputs = []ports.OutputConfig{{Name: "DP-2", Scale: 1.25}}
	reload <- ports.ConfigChanged{Config: cfg}
	if s = scene(t, scenes); s.Scale != 1.25 || s.OutputWidth != 4096 {
		t.Fatal(s.Scale, s.OutputWidth)
	}
}

// A pointer warp moves the cursor on the window under it: input takes the
// position and the window gets the motion. Other windows cannot warp.
func TestPointerWarp(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	constraints := make(chan ports.PointerConstraint, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes, Constraints: constraints}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "A", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	r := receive(t, scenes)[0].Windows[0].Rect
	input <- ports.PointerMotion{X: 50, Y: 40}
	for {
		if _, ok := command(t, commands).(ports.PointerMotionTo); ok {
			break
		}
	}
	// Another window cannot move the pointer.
	client <- ports.PointerWarp{ID: 2, X: 5, Y: 5}
	client <- ports.PointerWarp{ID: 1, X: 10, Y: 20}
	got := receive(t, constraints)
	if !got.Warp || got.X != float64(r.X+10) || got.Y != float64(r.Y+20) {
		t.Fatalf("warp %+v, window %+v", got, r)
	}
	for {
		if v, ok := command(t, commands).(ports.PointerMotionTo); ok {
			if v.ID != 1 || v.X != 10 || v.Y != 20 || v.DX != 0 || v.DY != 0 {
				t.Fatalf("motion %+v", v)
			}
			break
		}
	}
	// A point outside the window is refused.
	client <- ports.PointerWarp{ID: 1, X: float64(r.W), Y: 0}
	client <- ports.PointerWarp{ID: 1, X: 1, Y: 1}
	if got := receive(t, constraints); got.X != float64(r.X+1) || got.Y != float64(r.Y+1) {
		t.Fatalf("warp %+v", got)
	}
}

func TestPointerConstraint(t *testing.T) { both(t, pointerConstraint) }

func pointerConstraint(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	constraints := make(chan ports.PointerConstraint, 1)
	ch := core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes, Constraints: constraints}
	opts := core.Options{}
	l := newLanding(t, animated, &cfg, &ch, &opts, "A", "B")
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "A", Width: 100, Height: 80}}
	scene(t, scenes)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "B", Width: 100, Height: 80}}
	scene(t, scenes)
	input <- ports.PointerMotion{X: 150, Y: 40}
	scene(t, scenes) // the pointer moves the focus to B
	client <- ports.WindowMapped{ID: 1}
	r := l.land(receive(t, scenes))[1].Windows[0].Rect
	// The confine region is window-local and clipped to the window.
	client <- ports.PointerConstrained{ID: 1, PointerConstraint: ports.PointerConstraint{Mode: ports.ConstraintConfine, Rect: ports.Rect{X: 10, Y: 10, W: 1000, H: 20}}}
	scene(t, scenes)
	want := ports.PointerConstraint{Mode: ports.ConstraintConfine, Rect: ports.Rect{X: 100 + r.X + 10, Y: r.Y + 10, W: r.W - 10, H: 20}}
	// Input gets core's cursor, clamped into the region.
	if got := receive(t, constraints); got.Mode != want.Mode || got.Rect != want.Rect || got.X != 150 || got.Y != 29 {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for len(commands) > 0 {
		<-commands
	}
	// Motion is clamped into the region; deltas pass through.
	input <- ports.PointerMotion{X: 199, Y: 79, DX: 5, DY: 6}
	for {
		if v, ok := command(t, commands).(ports.PointerMotionTo); ok {
			if v.X != float64(want.Rect.X+want.Rect.W-1-100-r.X) || v.Y != float64(want.Rect.Y+want.Rect.H-1-r.Y) || v.DX != 5 || v.DY != 6 {
				t.Fatalf("motion %+v", v)
			}
			break
		}
	}
	// A lock keeps the pointer still.
	client <- ports.PointerConstrained{ID: 1, PointerConstraint: ports.PointerConstraint{Mode: ports.ConstraintLock}}
	scene(t, scenes)
	if got := receive(t, constraints); got.Mode != ports.ConstraintLock {
		t.Fatalf("lock %+v", got)
	}
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.PointerMotion{X: 101, Y: 1, DX: -50}
	for {
		if v, ok := command(t, commands).(ports.PointerMotionTo); ok {
			if v.X != float64(want.Rect.X+want.Rect.W-1-100-r.X) || v.DX != -50 {
				t.Fatalf("locked motion %+v", v)
			}
			break
		}
	}
	client <- ports.PointerConstrained{}
	scene(t, scenes)
	if got := receive(t, constraints); got.Mode != ports.ConstraintNone {
		t.Fatalf("release %+v", got)
	}
}

// A click outside a grabbing menu chain closes it, the topmost popup first.
func TestPopupChainDismissOrder(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	pos := ports.Positioner{Width: 10, Height: 10, AnchorRect: ports.Rect{W: 1, H: 1}, Anchor: ports.EdgeBottomRight, Gravity: ports.EdgeBottomRight}
	for _, v := range []struct{ id, parent ports.WindowID }{{2, 1}, {3, 2}} {
		client <- ports.PopupRequest{ID: v.id, Parent: v.parent, Positioner: pos, Grab: true}
		for {
			if cp, ok := command(t, commands).(ports.ConfigurePopup); ok && cp.ID == v.id {
				break
			}
		}
		client <- ports.PopupMapped{ID: v.id}
		scene(t, scenes)
	}
	for len(commands) > 0 {
		<-commands
	}
	// The window's far corner is outside both popups.
	input <- ports.PointerMotion{X: 90, Y: 70}
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	var closed []ports.WindowID
	for len(closed) < 2 {
		if v, ok := command(t, commands).(ports.ClosePopup); ok {
			closed = append(closed, v.ID)
		}
	}
	if closed[0] != 3 || closed[1] != 2 {
		t.Fatalf("close order %v, want [3 2]", closed)
	}
}

// A dialog opened under a fullscreen window stays hidden and out of reach
// of the pointer until the window leaves fullscreen.
func TestFloatOverFullscreenHit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{Clock: steppingClock(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 2, Floating: true, Width: 20, Height: 10}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.PointerMotion{X: 50, Y: 40}
	if v, ok := command(t, commands).(ports.PointerFocus); !ok || v.ID != 1 {
		t.Fatalf("pointer went to %v, want the fullscreen window", v)
	}
	client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: false}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.PointerMotion{X: 50, Y: 41}
	if v, ok := command(t, commands).(ports.PointerFocus); !ok || v.ID != 2 {
		t.Fatalf("pointer went to %v, want the dialog", v)
	}
}

// A dialog of the fullscreen window (a portal dialog parented through
// xdg-foreign) shows over it and takes the pointer at once.
func TestDialogOverFullscreenHit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{Clock: steppingClock(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 2, Floating: true, Width: 20, Height: 10, Parent: 1}
	s := scene(t, scenes)
	shown := false
	for _, w := range s.Windows {
		if w.ID == 2 {
			shown = !w.Hidden && w.Rect.W > 0
		}
	}
	if !shown {
		t.Fatalf("dialog not shown: %+v", s.Windows)
	}
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.PointerMotion{X: 50, Y: 40}
	if v, ok := command(t, commands).(ports.PointerFocus); !ok || v.ID != 2 {
		t.Fatalf("pointer went to %v, want the dialog", v)
	}
}

// A window that asks for fullscreen as it maps (Wine at a remembered
// monitor size) stays in its column: the windows it opens next are seen.
// A later request is honoured.
func TestFullscreenAtMapIgnored(t *testing.T) { both(t, fullscreenAtMapIgnored) }

func fullscreenAtMapIgnored(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Layout.Overflow = "fixed"
	cfg.Focus.Animation = ports.FocusAnimationOff
	client := make(chan ports.ClientEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	// Core reads the time on its goroutine: the clock is the test's.
	ch := core.Channels{Client: client, Output: output, Commands: commands, Scenes: scenes}
	opts := core.Options{}
	l := newLanding(t, animated, &cfg, &ch, &opts, "OUT-1")
	clk := l.clk
	if clk == nil {
		clk = newStepClock(t)
		opts.Clock = clk.clock
	}
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 2}
	scene(t, scenes)
	client <- ports.WindowFullscreenRequest{ID: 2, Fullscreen: true}
	client <- ports.WindowMapped{ID: 3, Floating: true, Width: 20, Height: 10}
	s := scene(t, scenes)
	for !slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 3 }) {
		s = scene(t, scenes)
	}
	for _, w := range s.Windows {
		if w.Fullscreen || w.Hidden {
			t.Fatalf("startup fullscreen applied: %+v", s.Windows)
		}
	}
	// With animations on this lands the maps' springs: the clock moves past
	// the grace, as the next line does anyway.
	l.one(s)
	clk.advance(time.Second)
	// Only the focused window may cover the screen (ADR 011).
	client <- ports.WindowUnmapped{ID: 3}
	scene(t, scenes)
	client <- ports.WindowFullscreenRequest{ID: 2, Fullscreen: true}
	s = scene(t, scenes)
	// Honoured, in place.
	if !slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 && w.Fullscreen && !w.Hidden }) {
		t.Fatalf("later fullscreen ignored: %+v", s.Windows)
	}
}

// Under fixed overflow, a window opening over a fullscreen game waits
// hidden: the game keeps the screen and the keyboard. An activation (a
// user action in the new client) leaves fullscreen and shows both tiles;
// a focus move back to the game and the bind make it fullscreen again.
func TestFixedFullscreenArrivalWaits(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left)
	r.mapWindow(t, 1)
	r.key(t, "f", ports.ModAlt|ports.ModShift)
	set := r.mapWindow(t, 2)
	for len(r.commands) > 0 {
		<-r.commands
	}
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1}) || focused != 1 {
		t.Fatal("game disturbed:", got, focused)
	}
	r.client <- ports.WindowActivate{ID: 2}
	set = receive(t, r.scenes)
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1, 2}) || focused != 2 {
		t.Fatal("after activate:", got, focused)
	}
	for {
		if f, ok := command(t, r.commands).(ports.FocusWindow); ok && f.ID == 2 {
			break
		}
	}
	r.key(t, "Left", ports.ModAlt)
	set = r.key(t, "f", ports.ModAlt|ports.ModShift)
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1}) || focused != 1 {
		t.Fatal("game again:", got, focused)
	}
}

// A taskbar's fullscreen request is the user's: it applies at once.
func TestExternalFullscreenAtMapApplies(t *testing.T) { both(t, externalFullscreenAtMapApplies) }

func externalFullscreenAtMapApplies(t *testing.T, animated bool) {
	cfg := config.Defaults()
	cfg.Focus.Animation = ports.FocusAnimationOff
	client := make(chan ports.ClientEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	ch := core.Channels{Client: client, Output: output, Commands: commands, Scenes: scenes}
	opts := core.Options{}
	l := newLanding(t, animated, &cfg, &ch, &opts, "OUT-1")
	if l.clk == nil {
		opts.Clock = newStepClock(t).clock
	}
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	l.one(scene(t, scenes))
	client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	s := scene(t, scenes)
	if len(s.Windows) != 1 || !s.Windows[0].Fullscreen {
		t.Fatalf("taskbar fullscreen ignored: %+v", s.Windows)
	}
}

// Under fixed overflow, a taskbar's fullscreen on an unfocused window is
// the user's choice: the window takes the focus and covers the screen.
func TestExternalFullscreenFocusesFixed(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	set := receive(t, r.scenes)
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1}) || focused != 1 {
		t.Fatal("taskbar fullscreen:", got, focused)
	}
	// Over another covering window too: it leaves fullscreen.
	r.client <- ports.WindowFullscreenRequest{ID: 2, Fullscreen: true, External: true}
	set = receive(t, r.scenes)
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{2}) || focused != 2 {
		t.Fatal("taskbar fullscreen over another:", got, focused)
	}
	// Leaving from the taskbar keeps the focus.
	r.client <- ports.WindowFullscreenRequest{ID: 2, External: true}
	set = receive(t, r.scenes)
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1, 2}) || focused != 2 {
		t.Fatal("taskbar leave:", got, focused)
	}
}

// A taskbar fullscreen on a window of a workspace off screen, on another
// output, brings it on screen with the focus, as an activation.
func TestExternalFullscreenOffScreen(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Next", ports.ModAlt|ports.ModShift) // 1 to workspace 2, not followed
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl) // focus DP-2
	r.mapWindow(t, 2)
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	set := receive(t, r.scenes)
	found := false
	for _, s := range set {
		for _, w := range s.Windows {
			if w.ID != 1 {
				continue
			}
			found = true
			if s.Output != "DP-1" || w.Hidden || !w.Fullscreen || !w.Focused {
				t.Fatalf("window 1 on %s: %+v", s.Output, w)
			}
		}
	}
	if !found {
		t.Fatal("window 1 in no scene")
	}
}

// In the overview, a taskbar fullscreen on a window of another workspace
// selects its row: closing the overview lands on it, fullscreen.
func TestExternalFullscreenInOverview(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left)
	r.mapWindow(t, 1)
	r.key(t, "Next", ports.ModAlt|ports.ModShift) // 1 to workspace 2
	r.mapWindow(t, 2)
	r.key(t, "o", ports.ModAlt)
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	receive(t, r.scenes)
	set := r.key(t, "o", ports.ModAlt)
	if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{1}) || focused != 1 {
		t.Fatal("after the overview:", got, focused)
	}
}

// focus-window-down from a fullscreen tile at the bottom of its column
// goes to the next workspace and fullscreen stays.
func TestFocusWindowDownKeepsFullscreen(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startLanding(t, animated, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left)
		r.mapLanded(t, 1)
		r.mapLanded(t, 2)
		r.keyLanded(t, "f", ports.ModAlt|ports.ModShift)
		set := r.keyLanded(t, "Down", ports.ModAlt)
		if got, _ := windowsOf(set, "DP-1"); len(got) != 0 {
			t.Fatal("still on the first workspace:", got)
		}
		set = r.keyLanded(t, "Up", ports.ModAlt)
		if got, focused := windowsOf(set, "DP-1"); !reflect.DeepEqual(got, []ports.WindowID{2}) || focused != 2 {
			t.Fatal("fullscreen lost:", got, focused)
		}
	})
}

// A refused fullscreen request still gets a configure, unchanged: clients
// wait for it (xdg-shell).
func TestRefusedFullscreenConfigured(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" }, left)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	for len(r.commands) > 0 {
		<-r.commands
	}
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true}
	receive(t, r.scenes)
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.ConfigureWindow); ok && v.ID == 1 {
			if v.Fullscreen {
				t.Fatalf("refused request applied: %+v", v)
			}
			return
		}
	}
	t.Fatal("no configure answered the request")
}

// An activated window on another workspace comes on screen with focus.
func TestWindowActivateShowsAndFocuses(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	// Window 2 opens on workspace 2, then the user goes back to 1.
	input <- ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModSuper, Pressed: true}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 2}
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "1", Keycode: 2, Mods: ports.ModSuper, Pressed: true}
	visible := func(sc ports.Scene) (shown []ports.SceneWindow) {
		for _, w := range sc.Windows {
			if !w.Hidden {
				shown = append(shown, w)
			}
		}
		return shown
	}
	if v := visible(scene(t, scenes)); len(v) != 1 || v[0].ID != 1 {
		t.Fatalf("workspace 1: %+v", v)
	}
	client <- ports.WindowActivate{ID: 2}
	v := visible(scene(t, scenes))
	for len(v) != 1 || v[0].ID != 2 {
		v = visible(scene(t, scenes))
	}
	if !v[0].Focused {
		t.Fatalf("after activate: %+v", v)
	}
	for {
		if f, ok := command(t, commands).(ports.FocusWindow); ok && f.ID == 2 {
			break
		}
	}

}
