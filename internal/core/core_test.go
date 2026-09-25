package core_test

import (
	"context"
	"errors"
	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/ports"
	"testing"
	"time"
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

// command receives the next client command, skipping output scale updates.
func command(t *testing.T, ch <-chan ports.ClientCommand) ports.ClientCommand {
	t.Helper()
	for {
		if v := receive(t, ch); v != nil {
			if _, ok := v.(ports.SetOutputs); !ok {
				return v
			}
		}
	}
}

func TestOwner(t *testing.T) {
	cfg := config.Defaults()
	cfg.Keyboard.CmdKey = "alt"
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
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Config: reload, Commands: commands, Spawn: spawn, Scenes: scenes, ConfigErrors: errs})
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
	s = scene(t, scenes)
	// A single column fills the usable width.
	if len(s.Windows) != 1 || s.Windows[0].Rect.W != 84 || !s.Windows[0].Borderless {
		t.Fatal(s)
	}
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 84, Height: 64, Activated: true, Output: "OUT-1"}) {
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
	s = scene(t, scenes)
	if s.Windows[0].Rect.W != 92 {
		t.Fatal(s)
	}
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 92, Height: 72, Activated: true, Output: "OUT-1"}) {
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
	if _, err := core.New(cfg, core.Channels{Scenes: make(chan []ports.Scene, 2)}); err == nil {
		t.Fatal("capacity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c, err := core.New(cfg, core.Channels{Scenes: make(chan []ports.Scene, 1)})
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
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Commands: commands, Scenes: scenes})
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
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Commands: commands, Scenes: scenes})
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
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
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
	input <- ports.PointerMotion{X: float64(first.X + 2), Y: float64(first.Y + 3), TimeMsec: 1}
	if v := command(t, commands); v != (ports.PointerFocus{ID: 1, X: 2, Y: 3}) {
		t.Fatal(v)
	}
	if v := command(t, commands); v != (ports.PointerMotionTo{ID: 1, X: 2, Y: 3, TimeMsec: 1}) {
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
	input <- ports.PointerButton{Button: 0x110}
	if v := command(t, commands); v != (ports.PointerButtonTo{ID: 1, Button: 0x110}) {
		t.Fatal(v)
	}
}

func TestBorderInset(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 2
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
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
	r := scene(t, scenes).Windows[0].Rect
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: r.W, Height: r.H, Activated: true, Output: "OUT-1"}) {
		t.Fatalf("%v for outer %v", v, r)
	}
	// With a second column, the scene keeps the outer rect and the client is
	// configured 2px smaller on each side.
	client <- ports.WindowMapped{ID: 2}
	s := scene(t, scenes)
	r = s.Windows[1].Rect
	if s.Windows[1].ID != 2 || s.Windows[1].Borderless {
		t.Fatal(s)
	}
	want := ports.ConfigureWindow{ID: 2, Width: r.W - 4, Height: r.H - 4, Activated: true, Output: "OUT-1"}
	found := false
	for len(commands) > 0 {
		if v := <-commands; v == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %v for outer %v", want, r)
	}
	input <- ports.PointerMotion{X: float64(r.X + 2), Y: float64(r.Y + 5), TimeMsec: 1}
	if v := command(t, commands); v != (ports.PointerFocus{ID: 2, X: 0, Y: 3}) {
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
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
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

func TestWorkspaceSwitch(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
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
	s := scene(t, scenes)
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
	s = scene(t, scenes)
	if len(s.Windows) != 2 || s.Windows[1].ID != 2 || s.Windows[1].Hidden || !s.Windows[1].Borderless {
		t.Fatal(s)
	}
}

func TestClickAfterWorkspaceSwitch(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
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
	scene(t, scenes)
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
	s := scene(t, scenes)
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
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Commands: commands, Scenes: scenes})
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
	// US Cmd+Shift+1 prints exclam; the physical key (code 2) matches move-to-workspace 1.
	input <- ports.KeyEvent{Keysym: "exclam", Base: "1", Keycode: 2, Mods: ports.ModSuper | ports.ModShift, Pressed: true}
	scene(t, scenes)
	// Shift goes up first, so the release reports "1": still swallowed.
	input <- ports.KeyEvent{Keysym: "1", Keycode: 2, Mods: ports.ModSuper}
	input <- ports.KeyEvent{Keysym: "x", Keycode: 45, Pressed: true}
	if v := command(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "x", Keycode: 45, Pressed: true}}) {
		t.Fatal(v)
	}
}

func TestOutputScale(t *testing.T) {
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
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Config: reload, Commands: commands, Scenes: scenes})
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
	scene(t, scenes)
	if v := command(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 2560, Height: 1080, Activated: true, Output: "DP-2"}) {
		t.Fatal(v)
	}
	for len(commands) > 0 {
		<-commands
	}
	// The pointer arrives in global logical pixels.
	input <- ports.PointerMotion{X: 500, Y: 200}
	if v := command(t, commands); v != (ports.PointerFocus{ID: 1, X: 500, Y: 200}) {
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

func TestPointerConstraint(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	constraints := make(chan ports.PointerConstraint, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes, Constraints: constraints})
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
	r := receive(t, scenes)[1].Windows[0].Rect
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
