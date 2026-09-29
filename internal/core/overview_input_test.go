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

// The focus binds (cmd+arrows) move the overview selection like the bare
// keys, over a covering float too: cmd+up brings the columns card in
// front, cmd+left then selects a column, cmd+down returns to the float.
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
	press("Up")
	sceneMatch(t, scenes, focused(2))
	press("Left")
	sceneMatch(t, scenes, focused(1))
	press("Down")
	sceneMatch(t, scenes, focused(9))
}

// Two-finger scrolling moves the overview selection one column per step,
// along the axis the fingers move most; a small scroll does nothing and
// lifting the fingers starts over. Outside the overview it goes to the
// window.
func TestOverviewTwoFingerScroll(t *testing.T) {
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
	finger := func(dx, dy float64) ports.PointerAxis {
		return ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Value: dx}, Vertical: ports.ScrollAxis{Set: true, Value: dy}}
	}
	// 50 left, lifted, then 50 and 50 left: one step (to 2). Without the
	// reset it would be two (to 1), and the l below would land on 2.
	input <- finger(-50, 5)
	input <- ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Stop: true}}
	input <- finger(-50, 5)
	input <- finger(-50, 0)
	sceneMatch(t, scenes, focused(2))
	input <- ports.KeyEvent{Keysym: "l", Pressed: true}
	sceneMatch(t, scenes, focused(3))
	input <- ports.KeyEvent{Keysym: "l"}
	input <- finger(-60, 0)
	sceneMatch(t, scenes, focused(2))
	// A wheel notch down has nowhere to go (no workspace below with
	// windows); a finger scroll left moves on.
	input <- ports.PointerAxis{Source: ports.AxisWheel, Vertical: ports.ScrollAxis{Set: true, Value: 15, V120: 120}}
	input <- finger(-70, 0)
	sceneMatch(t, scenes, focused(1))
}
