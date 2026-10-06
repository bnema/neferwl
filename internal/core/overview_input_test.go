package core_test

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// A launcher holding the keyboard over the overview gets its keys; the
// overview takes them back once it closes.
func TestOverviewLauncherKeepsKeys(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 2}
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "o", Mods: ports.ModSuper, Pressed: true}
	sceneMatch(t, scenes, func(s ports.Scene) bool { return len(s.Windows) == 2 && s.Windows[0].Preview > 0 })
	input <- ports.KeyEvent{Keysym: "o", Mods: ports.ModSuper}

	launcher := ports.LayerSurface{ID: 7, Layer: ports.LayerOverlay, Width: 100, Height: 40, Keyboard: 1}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{launcher}}
	scene(t, scenes)
	for len(commands) > 0 {
		<-commands
	}
	input <- ports.KeyEvent{Keysym: "h", Pressed: true}
	if v := next(t, commands, anyOf[ports.ForwardKey]); v.ID != 7 || v.Key.Keysym != "h" {
		t.Fatalf("forward %+v, want the launcher", v)
	}
	input <- ports.KeyEvent{Keysym: "h"}
	next(t, commands, anyOf[ports.ForwardKey])

	client <- ports.LayerChanged{}
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "h", Pressed: true}
	sceneMatch(t, scenes, func(s ports.Scene) bool {
		for _, w := range s.Windows {
			if w.ID == 1 && w.Focused && w.Preview > 0 {
				return true
			}
		}
		return false
	})
}

// Focus binds (cmd+arrows and their Vim twins) navigate the overview over
// a covering float: cmd+k/j round-trips through the columns card, then
// cmd+up brings it forward, cmd+left selects a column and cmd+down returns.
func TestOverviewFocusBindsNavigate(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200}}
	scene(t, scenes)
	for id := ports.WindowID(1); id <= 2; id++ {
		client <- ports.WindowMapped{ID: id}
		scene(t, scenes)
	}
	client <- ports.WindowMapped{ID: 9, Floating: true, Width: 300, Height: 200}
	scene(t, scenes)
	focused := func(id ports.WindowID) func(ports.Scene) bool {
		return func(s ports.Scene) bool {
			for _, w := range s.Windows {
				if w.ID == id && w.Focused && w.Preview > 0 && w.Dim == 0 {
					return true
				}
			}
			return false
		}
	}
	press := func(key string) {
		input <- ports.KeyEvent{Keysym: key, Mods: ports.ModSuper, Pressed: true}
		input <- ports.KeyEvent{Keysym: key, Mods: ports.ModSuper}
	}
	press("o")
	sceneMatch(t, scenes, focused(9))
	press("k")
	sceneMatch(t, scenes, focused(2))
	press("j")
	sceneMatch(t, scenes, focused(9))
	press("Up")
	sceneMatch(t, scenes, focused(2))
	press("Left")
	sceneMatch(t, scenes, focused(1))
	press("Down")
	sceneMatch(t, scenes, focused(9))
}

// Closing through a real bind follows the preview, not the covering float.
func TestOverviewCloseBindTargetsPreview(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200}}
	scene(t, scenes)
	for _, id := range []ports.WindowID{1, 2} {
		client <- ports.WindowMapped{ID: id}
		scene(t, scenes)
	}
	client <- ports.WindowMapped{ID: 9, Floating: true, Width: 300, Height: 200}
	scene(t, scenes)
	press := func(key string, mods ports.Mods) {
		input <- ports.KeyEvent{Keysym: key, Mods: mods, Pressed: true}
		input <- ports.KeyEvent{Keysym: key, Mods: mods}
	}
	press("o", ports.ModSuper)
	sceneMatch(t, scenes, func(s ports.Scene) bool {
		for _, w := range s.Windows {
			if w.ID == 9 && w.Focused && w.Preview > 0 {
				return true
			}
		}
		return false
	})
	press("Up", 0)
	sceneMatch(t, scenes, func(s ports.Scene) bool {
		for _, w := range s.Windows {
			if w.ID == 2 && w.Focused && w.Preview > 0 {
				return true
			}
		}
		return false
	})
	press("q", ports.ModSuper)
	if got := next(t, commands, anyOf[ports.CloseWindow]); got.ID != 2 {
		t.Fatalf("close target %d, want preview 2", got.ID)
	}
}

func TestFloatOnlyMonitorDirectionalExit(t *testing.T) {
	for _, dir := range []int{-1, 1} {
		r := startMulti(t, nil, left, right)
		if dir < 0 {
			r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
		}
		r.client <- ports.WindowMapped{ID: 9, Floating: true, Width: 20, Height: 20}
		receive(t, r.scenes)
		key := "Right"
		want := right.Name
		if dir < 0 {
			key, want = "Left", left.Name
		}
		r.key(t, key, ports.ModAlt)
		out := lastOutputs(t, r.commands)
		if out.Focused != want {
			t.Fatalf("direction %d: focused output %q, want %q", dir, out.Focused, want)
		}
	}
}

// Three-finger navigation changes selection during movement and resets on lift.
func TestOverviewThreeFingerNavigation(t *testing.T) {
	cfg := config.Defaults()
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200}}
	scene(t, scenes)
	for id := ports.WindowID(1); id <= 3; id++ {
		client <- ports.WindowMapped{ID: id}
		scene(t, scenes)
	}
	focused := func(id ports.WindowID) func(ports.Scene) bool {
		return func(s ports.Scene) bool {
			for _, w := range s.Windows {
				if w.ID == id && w.Focused && w.Preview > 0 {
					return true
				}
			}
			return false
		}
	}
	input <- ports.KeyEvent{Keysym: "o", Mods: ports.ModSuper, Pressed: true}
	sceneMatch(t, scenes, focused(3))
	input <- ports.KeyEvent{Keysym: "o", Mods: ports.ModSuper}
	finger := func(dx, dy float64) ports.SwipeUpdate {
		return ports.SwipeUpdate{DX: dx, DY: dy}
	}
	input <- ports.SwipeBegin{Fingers: 3}
	// 50 left, lifted, then 50, 50 and 100 left: one step (to 2), the
	// next ones are longer. Without the reset it would be two (to 1), and
	// the l below would land on 2.
	input <- finger(-50, 5)
	input <- ports.SwipeEnd{}
	input <- ports.SwipeBegin{Fingers: 3}
	input <- finger(-50, 5)
	input <- finger(-50, 0)
	input <- finger(-100, 0)
	sceneMatch(t, scenes, focused(2))
	input <- ports.SwipeEnd{}
	input <- ports.SwipeBegin{Fingers: 3}
	input <- ports.KeyEvent{Keysym: "l", Pressed: true}
	sceneMatch(t, scenes, focused(3))
	input <- ports.KeyEvent{Keysym: "l"}
	input <- finger(-60, 0)
	sceneMatch(t, scenes, focused(2))
	input <- ports.SwipeEnd{}
	input <- ports.SwipeBegin{Fingers: 3}
	// A wheel notch down has nowhere to go (no workspace below with
	// windows); a finger scroll left moves on.
	input <- ports.PointerAxis{Source: ports.AxisWheel, Vertical: ports.ScrollAxis{Set: true, Value: 15, V120: 120}}
	input <- finger(-70, 0)
	sceneMatch(t, scenes, focused(1))
}
