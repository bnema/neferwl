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
	scenes := make(chan ports.Scene, 1)
	errs := make(chan error, 8)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Config: reload, Commands: commands, Spawn: spawn, Scenes: scenes, ConfigErrors: errs})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	output <- ports.OutputMode{Width: 100, Height: 80}
	s := receive(t, scenes)
	if s.OutputWidth != 100 {
		t.Fatal(s)
	}
	client <- ports.WindowMapped{ID: 1}
	s = receive(t, scenes)
	// A single column fills the usable width.
	if len(s.Windows) != 1 || s.Windows[0].Rect.W != 84 || !s.Windows[0].Borderless {
		t.Fatal(s)
	}
	if v := receive(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 84, Height: 64, Activated: true}) {
		t.Fatal(v)
	}
	receive(t, commands)
	client <- ports.WindowMapped{ID: 2}
	s = receive(t, scenes)
	if len(s.Windows) != 2 {
		t.Fatal(s)
	}
	// Commands for focus changes and second mapping.
	for len(commands) > 0 {
		<-commands
	}
	client <- ports.WindowMapped{ID: 2}
	receive(t, scenes)
	if len(commands) != 0 {
		t.Fatal("duplicate configure")
	}
	input <- ports.KeyEvent{Keysym: "Return", Mods: ports.ModAlt, Pressed: true}
	receive(t, scenes)
	if v := receive(t, spawn); len(v.Argv) != 1 || v.Argv[0] != "foot" {
		t.Fatal(v)
	}
	input <- ports.KeyEvent{Keysym: "Return", Mods: ports.ModAlt}
	input <- ports.KeyEvent{Keysym: "x", Pressed: true}
	if v := receive(t, commands); v != (ports.ForwardKey{ID: 2, Key: ports.KeyEvent{Keysym: "x", Pressed: true}}) {
		t.Fatal(v)
	}
	input <- ports.KeyEvent{Keysym: "Q", Mods: ports.ModAlt, Pressed: true}
	receive(t, scenes)
	if v := receive(t, commands); v != (ports.CloseWindow{ID: 2}) {
		t.Fatal(v)
	}
	client <- ports.WindowUnmapped{ID: 2}
	receive(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	cfg.Layout.Gaps = 4
	reload <- ports.ConfigChanged{Config: cfg}
	s = receive(t, scenes)
	if s.Windows[0].Rect.W != 92 {
		t.Fatal(s)
	}
	if v := receive(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: 92, Height: 72, Activated: true}) {
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
	if _, err := core.New(cfg, core.Channels{Scenes: make(chan ports.Scene, 2)}); err == nil {
		t.Fatal("capacity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c, err := core.New(cfg, core.Channels{Scenes: make(chan ports.Scene, 1)})
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
	scenes := make(chan ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	client <- ports.WindowMapped{ID: 1}
	receive(t, scenes)
	receive(t, commands)
	receive(t, commands)
	press := ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true}
	input <- press
	receive(t, scenes)
	input <- ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper}
	input <- ports.KeyEvent{Keysym: "Left", Pressed: true}
	if v := receive(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "Left", Pressed: true}}) {
		t.Fatal(v)
	}
	input <- ports.KeyEvent{Keysym: "Left"}
	if v := receive(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "Left"}}) {
		t.Fatal(v)
	}
	input <- press
	receive(t, scenes)
	input <- ports.KeyEvent{Keysym: "Left", Pressed: true}
	receive(t, commands)
	input <- ports.KeyEvent{Keysym: "Left"}
	if v := receive(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "Left"}}) {
		t.Fatal(v)
	}
}

func TestBoundReleaseSwallowed(t *testing.T) {
	cfg := config.Defaults()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 2)
	commands := make(chan ports.ClientCommand, 8)
	scenes := make(chan ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	client <- ports.WindowMapped{ID: 1}
	receive(t, scenes)
	receive(t, commands)
	receive(t, commands)
	input <- ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true}
	receive(t, scenes)
	input <- ports.KeyEvent{Keysym: "Left"}
	input <- ports.KeyEvent{Keysym: "x", Pressed: true}
	if v := receive(t, commands); v != (ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "x", Pressed: true}}) {
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
	scenes := make(chan ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputMode{Width: 100, Height: 80}
	receive(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	first := receive(t, scenes).Windows[0].Rect
	client <- ports.WindowMapped{ID: 2}
	receive(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.PointerMotion{X: float64(first.X + 2), Y: float64(first.Y + 3), TimeMsec: 1}
	if v := receive(t, commands); v != (ports.PointerFocus{ID: 1, X: 2, Y: 3}) {
		t.Fatal(v)
	}
	if v := receive(t, commands); v != (ports.PointerMotionTo{ID: 1, X: 2, Y: 3, TimeMsec: 1}) {
		t.Fatal(v)
	}
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	if v := receive(t, commands); v != (ports.PointerButtonTo{ID: 1, Button: 0x110, Pressed: true}) {
		t.Fatal(v)
	}
	receive(t, scenes)
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
	if v := receive(t, commands); v != (ports.PointerFocus{}) {
		t.Fatal(v)
	}
	input <- ports.PointerButton{Button: 0x110}
	if v := receive(t, commands); v != (ports.PointerButtonTo{ID: 1, Button: 0x110}) {
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
	scenes := make(chan ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputMode{Width: 100, Height: 80}
	receive(t, scenes)
	// Alone, the window is borderless and gets the full rect.
	client <- ports.WindowMapped{ID: 1}
	r := receive(t, scenes).Windows[0].Rect
	if v := receive(t, commands); v != (ports.ConfigureWindow{ID: 1, Width: r.W, Height: r.H, Activated: true}) {
		t.Fatalf("%v for outer %v", v, r)
	}
	// With a second column, the scene keeps the outer rect and the client is
	// configured 2px smaller on each side.
	client <- ports.WindowMapped{ID: 2}
	s := receive(t, scenes)
	r = s.Windows[1].Rect
	if s.Windows[1].ID != 2 || s.Windows[1].Borderless {
		t.Fatal(s)
	}
	want := ports.ConfigureWindow{ID: 2, Width: r.W - 4, Height: r.H - 4, Activated: true}
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
	if v := receive(t, commands); v != (ports.PointerFocus{ID: 2, X: 0, Y: 3}) {
		t.Fatal(v)
	}
}

func TestLayerKeyboardFocus(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputMode{Width: 100, Height: 80}
	receive(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	receive(t, scenes)
	focusOf := func() ports.WindowID {
		t.Helper()
		for {
			if v, ok := receive(t, commands).(ports.FocusWindow); ok {
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
	receive(t, scenes)
	if id := focusOf(); id != 7 {
		t.Fatalf("layer focus %d", id)
	}
	input <- ports.KeyEvent{Keysym: "a", Keycode: 30, Pressed: true}
	for {
		if v, ok := receive(t, commands).(ports.ForwardKey); ok {
			if v.ID != 7 {
				t.Fatalf("key went to %d", v.ID)
			}
			break
		}
	}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	receive(t, scenes)
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
	scenes := make(chan ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputMode{Width: 100, Height: 80}
	receive(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	receive(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	// AZERTY: the 2 key is eacute unshifted; Cmd+2 still matches through its shifted level.
	input <- ports.KeyEvent{Keysym: "eacute", Base: "eacute", Shifted: "2", Mods: ports.ModSuper, Pressed: true}
	s := receive(t, scenes)
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
	s = receive(t, scenes)
	if len(s.Windows) != 2 || s.Windows[1].ID != 2 || s.Windows[1].Hidden || !s.Windows[1].Borderless {
		t.Fatal(s)
	}
}
