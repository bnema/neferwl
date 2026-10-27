package core_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// outboxRig runs a core whose command channel nobody reads until the test
// does. Run and the sender live in a synctest bubble, so synctest.Wait
// returns once both are idle.
type outboxRig struct {
	input    chan ports.InputEvent
	client   chan ports.ClientEvent
	output   chan ports.OutputEvent
	commands chan ports.ClientCommand
	scenes   chan []domain.Scene
}

func startOutboxRig(t *testing.T, commandCap int) *outboxRig {
	t.Helper()
	cfg := scrollDefaults()
	cfg.Border.Width = 0
	cfg.Outputs = []domain.OutputConfig{{Name: "DP-2", Scale: 2}}
	cfg.Binds["Cmd+minus"] = "scale-down"
	r := &outboxRig{
		input:    make(chan ports.InputEvent),
		client:   make(chan ports.ClientEvent),
		output:   make(chan ports.OutputEvent),
		commands: make(chan ports.ClientCommand, commandCap),
		scenes:   make(chan []domain.Scene, 1),
	}
	c, err := core.New(cfg, core.Channels{Input: r.input, Client: r.client, Output: r.output, Commands: r.commands, Scenes: r.scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		// Run waits for its sender, so a sender stuck on the full channel
		// would keep this from returning.
		if err := receive(t, done); err != nil {
			t.Error(err)
		}
	})
	r.output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "DP-2", Width: 5120, Height: 2160}}
	r.client <- ports.WindowMapped{ID: 1}
	synctest.Wait()
	// Settle: drop the scenes and the commands of the map.
	r.drain()
	return r
}

// drain empties the scene and command channels and lets the sender refill
// them from its queue until nothing moves.
func (r *outboxRig) drain() []ports.ClientCommand {
	var got []ports.ClientCommand
	for {
		synctest.Wait()
		select {
		case <-r.scenes:
		default:
		}
		select {
		case v := <-r.commands:
			got = append(got, v)
		default:
			synctest.Wait()
			if len(r.commands) == 0 {
				return got
			}
		}
	}
}

// A display that reads nothing must not stall core: motion keeps coming, and
// a bind after it still runs and publishes its scene.
func TestCommandsNeverBlockCore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startOutboxRig(t, 32)
		const n = 10000
		for i := range n {
			// Each send returns only once Run is back in its select.
			r.input <- ports.PointerMotion{X: 500 + float64(i%100), Y: 200, DX: 1, DY: 2, UnaccelDX: 3, UnaccelDY: 4}
		}
		r.input <- ports.KeyEvent{Keysym: "minus", Keycode: 12, Mods: domain.ModSuper, Pressed: true}
		synctest.Wait()
		select {
		case set := <-r.scenes:
			if set[0].Scale != 5.0/3 {
				t.Fatalf("scale %v, want 5/3", set[0].Scale)
			}
		default:
			t.Fatal("no scene: core is blocked on commands")
		}
		// Once the display reads again, it gets everything in order, with
		// the motion merged and no movement lost.
		var dx, dy, ux, uy float64
		var motions int
		var scaled bool
		for _, v := range r.drain() {
			switch v := v.(type) {
			case ports.PointerMotionTo:
				motions++
				dx, dy, ux, uy = dx+v.DX, dy+v.DY, ux+v.UnaccelDX, uy+v.UnaccelDY
			case ports.SetOutputs:
				scaled = v.Outputs[0].Scale == 5.0/3
			}
		}
		if dx != n || dy != 2*n || ux != 3*n || uy != 4*n {
			t.Fatalf("lost movement: %v %v %v %v", dx, dy, ux, uy)
		}
		if motions > 40 {
			t.Fatalf("%d motions reached the display, want them merged while it was away", motions)
		}
		if !scaled {
			t.Fatal("scale change not sent after the motions")
		}
	})
}

// Commands reach the display in the order core made them. Only adjacent
// motions of one window merge.
func TestCommandsKeepOrderAndMergeAdjacentMotion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startOutboxRig(t, 0)
		// No reader: the sender holds the first command, which is in flight
		// and no longer merges, and the rest queues behind it.
		motion := func(x, dx float64) {
			r.input <- ports.PointerMotion{X: x, Y: 200, DX: dx, UnaccelDX: dx}
			synctest.Wait()
		}
		// How the first commands split between the sender's batch and the
		// queue depends on when it wakes. The first key is a barrier: what
		// is pushed after the owner handled it never merges with what
		// came before, so the rest is deterministic.
		motion(500, 1)
		r.input <- ports.KeyEvent{Keysym: "y", Keycode: 21, Pressed: true}
		synctest.Wait()
		motion(501, 2)
		motion(502, 4)
		r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
		synctest.Wait()
		motion(503, 8)
		r.input <- ports.KeyEvent{Keysym: "x", Keycode: 45, Pressed: true}
		synctest.Wait()
		motion(504, 16)
		motion(505, 32)
		var got []ports.ClientCommand
		behind := false
		for _, v := range r.drainUnbuffered() {
			switch v.(type) {
			case ports.UserActivity, ports.SetOutputs:
				continue
			}
			if k, ok := v.(ports.ForwardKey); ok && k.Key.Keysym == "y" {
				behind = true
				continue
			}
			if behind {
				got = append(got, v)
			}
		}
		want := []ports.ClientCommand{
			ports.PointerMotionTo{ID: 1, X: 502, Y: 200, DX: 6, UnaccelDX: 6},
			ports.PointerButtonTo{ID: 1, Button: 0x110, Pressed: true, Grab: true},
			ports.PointerMotionTo{ID: 1, X: 503, Y: 200, DX: 8, UnaccelDX: 8},
			ports.ForwardKey{ID: 1, Key: ports.KeyEvent{Keysym: "x", Keycode: 45, Pressed: true}},
			ports.PointerMotionTo{ID: 1, X: 505, Y: 200, DX: 48, UnaccelDX: 48},
		}
		wantOrder(t, got, want)
	})
}

// drainUnbuffered receives what the sender hands over one by one.
func (r *outboxRig) drainUnbuffered() []ports.ClientCommand {
	var got []ports.ClientCommand
	for {
		synctest.Wait()
		select {
		case v := <-r.commands:
			got = append(got, v)
		default:
			return got
		}
	}
}

func wantOrder(t *testing.T, got, want []ports.ClientCommand) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d:\n%#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d: got %#v, want %#v", i, got[i], want[i])
		}
	}
}
