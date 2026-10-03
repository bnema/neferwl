package core_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

func powerCore(t *testing.T) (chan ports.InputEvent, chan ports.ClientEvent, chan ports.OutputEvent, chan ports.ClientCommand, chan []ports.Scene) {
	t.Helper()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 4)
	output := make(chan ports.OutputEvent, 2)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(config.Defaults(), core.Channels{Input: input, Client: client, Output: output, Commands: commands, Scenes: scenes, Spawn: make(chan ports.SpawnRequest, 4)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); receive(t, done) })
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-2", Width: 100, Height: 80}}
	return input, client, output, commands, scenes
}

// sceneOf waits for a scene set in which pred holds for the named output.
func sceneOf(t *testing.T, scenes <-chan []ports.Scene, name string, pred func(ports.Scene) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case set := <-scenes:
			for _, s := range set {
				if s.Output == name && pred(s) {
					return
				}
			}
		case <-deadline:
			t.Fatalf("no matching scene for %s", name)
		}
	}
}

// A client turning an output off marks its scene and tells wayland; back
// on clears both. Other outputs stay on.
func TestOutputPower(t *testing.T) {
	_, client, _, commands, scenes := powerCore(t)
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return !s.Off })
	client <- ports.OutputPower{Output: "OUT-2", On: false}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return s.Off })
	for {
		if v := commandOf[ports.SetOutputs](t, commands); slices.Equal(v.Off, []string{"OUT-2"}) {
			break
		}
	}
	client <- ports.OutputPower{Output: "OUT-2", On: true}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return !s.Off })
	for {
		if v := commandOf[ports.SetOutputs](t, commands); len(v.Off) == 0 {
			break
		}
	}
	// Unknown outputs are ignored.
	client <- ports.OutputPower{Output: "NOPE", On: false}
	sceneOf(t, scenes, "OUT-1", func(s ports.Scene) bool { return !s.Off })
}

// An output unplugged while off (a display in deep sleep) reconnects off;
// after input it reconnects on.
func TestOutputReconnectedWhileOffStaysOff(t *testing.T) {
	input, client, output, commands, scenes := powerCore(t)
	stop := make(chan struct{})
	defer close(stop)
	active := make(chan struct{}, 1)
	go func() { // core blocks on a full command channel
		for {
			select {
			case c := <-commands:
				if _, ok := c.(ports.UserActivity); ok {
					active <- struct{}{}
				}
			case <-stop:
				return
			}
		}
	}()
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return !s.Off })
	client <- ports.OutputPower{Output: "OUT-2", On: false}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return s.Off })
	output <- ports.OutputRemoved{Name: "OUT-2"}
	gone(t, scenes, "OUT-2") // no older scene can answer below
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-2", Width: 100, Height: 80}}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return s.Off })

	client <- ports.OutputPower{Output: "OUT-2", On: true}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return !s.Off })
	client <- ports.OutputPower{Output: "OUT-2", On: false}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return s.Off })
	output <- ports.OutputRemoved{Name: "OUT-2"}
	input <- ports.PointerMotion{X: 1, Y: 1}
	select { // input and outputs are separate channels: order them
	case <-active:
	case <-time.After(2 * time.Second):
		t.Fatal("input not handled")
	}
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-2", Width: 100, Height: 80}}
	sceneOf(t, scenes, "OUT-2", func(s ports.Scene) bool { return !s.Off })
}

// gone waits for a scene set without the named output.
func gone(t *testing.T, scenes <-chan []ports.Scene, name string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case set := <-scenes:
			if !slices.ContainsFunc(set, func(s ports.Scene) bool { return s.Output == name }) {
				return
			}
		case <-deadline:
			t.Fatalf("%s still in the scenes", name)
		}
	}
}

// Input reports user activity to wayland, at most once per interval.
func TestUserActivity(t *testing.T) {
	input, _, _, commands, _ := powerCore(t)
	for range 3 {
		input <- ports.PointerMotion{X: 1, Y: 1}
	}
	commandOf[ports.UserActivity](t, commands)
	deadline := time.After(ports.ActivityInterval / 2)
	for {
		select {
		case c := <-commands:
			if _, ok := c.(ports.UserActivity); ok {
				t.Fatal("second UserActivity within the interval")
			}
			continue
		case <-deadline:
		}
		break
	}
	time.Sleep(ports.ActivityInterval)
	input <- ports.PointerMotion{X: 2, Y: 2}
	commandOf[ports.UserActivity](t, commands)
}
