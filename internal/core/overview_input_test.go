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
