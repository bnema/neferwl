package core_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// virtualCore runs core with one 100x80 output holding window 1; it
// returns the channels and the window rectangle.
func virtualCore(t *testing.T) (chan ports.ClientEvent, chan ports.ClientCommand, chan domain.PointerConstraint, domain.Rect) {
	cfg := scrollDefaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []domain.Scene, 1)
	constraints := make(chan domain.PointerConstraint, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes, Constraints: constraints}, testOptions(t, core.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	r := receive(t, scenes)[0].Windows[0].Rect
	return client, commands, constraints, r
}

// nextCommand skips to the first command of type T.
func nextCommand[T ports.ClientCommand](t *testing.T, commands chan ports.ClientCommand) T {
	t.Helper()
	for {
		if v, ok := command(t, commands).(T); ok {
			return v
		}
	}
}

// A virtual pointer drives core as real input: relative and absolute motion
// move the cursor and enter the window, the cursor position goes to input
// as a warp, and click and scroll reach the hovered window.
func TestVirtualPointerRoutesAsInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, commands, constraints, w := virtualCore(t)
		const dev = domain.InputDeviceID(1 << 62)
		// Stamped protected: dropped, the cursor stays.
		client <- ports.VirtualPointerInput{State: domain.SecurityState{Generation: 1, Protected: true}, Absolute: true, FX: 0.1, FY: 0.1, Event: ports.PointerMotion{Device: dev}}
		synctest.Wait()
		select {
		case g := <-constraints:
			t.Fatalf("protected virtual motion moved the cursor: %+v", g)
		default:
		}
		client <- ports.VirtualPointerInput{Absolute: true, FX: 0.5, FY: 0.5, Event: ports.PointerMotion{Device: dev}}
		focus := nextCommand[ports.PointerFocus](t, commands)
		if focus.ID != 1 {
			t.Fatalf("focus %+v", focus)
		}
		m := nextCommand[ports.PointerMotionTo](t, commands)
		if m.ID != 1 || m.X != float64(50-w.X) || m.Y != float64(40-w.Y) {
			t.Fatalf("absolute motion %+v, window %+v", m, w)
		}
		if g := receive(t, constraints); !g.Warp || g.X != 50 || g.Y != 40 {
			t.Fatalf("cursor warp %+v", g)
		}
		client <- ports.VirtualPointerInput{Event: ports.PointerMotion{Device: dev, DX: 5, DY: -10}}
		m = nextCommand[ports.PointerMotionTo](t, commands)
		if m.X != float64(55-w.X) || m.Y != float64(30-w.Y) || m.DX != 5 || m.DY != -10 {
			t.Fatalf("relative motion %+v", m)
		}
		if g := receive(t, constraints); g.X != 55 || g.Y != 30 {
			t.Fatalf("cursor warp %+v", g)
		}
		// An unpaired release goes nowhere; the click does.
		client <- ports.VirtualPointerInput{Event: ports.PointerButton{Device: dev, Button: 0x111}}
		client <- ports.VirtualPointerInput{Event: ports.PointerButton{Device: dev, Button: 0x110, Pressed: true}}
		client <- ports.VirtualPointerInput{Event: ports.PointerButton{Device: dev, Button: 0x110}}
		press := nextCommand[ports.PointerButtonTo](t, commands)
		release := nextCommand[ports.PointerButtonTo](t, commands)
		if press.ID != 1 || press.Button != 0x110 || !press.Pressed || release.Pressed || release.Button != 0x110 {
			t.Fatalf("click %+v %+v", press, release)
		}
		client <- ports.VirtualPointerInput{Event: ports.PointerAxis{Device: dev, Vertical: domain.ScrollAxis{Set: true, Value: 15, V120: 120}}}
		axis := nextCommand[ports.PointerAxisTo](t, commands)
		if axis.ID != 1 || axis.Axis.Vertical.Value != 15 {
			t.Fatalf("scroll %+v", axis)
		}
		// Clamped to the layout.
		client <- ports.VirtualPointerInput{Event: ports.PointerMotion{Device: dev, DX: 1000, DY: 1000}}
		if g := receive(t, constraints); g.X != 99 || g.Y != 79 {
			t.Fatalf("clamped warp %+v", g)
		}
	})
}

// Absolute motion bound to an output maps into that output only.
func TestVirtualPointerWithOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := scrollDefaults()
		client := make(chan ports.ClientEvent, 8)
		output := make(chan ports.OutputEvent, 8)
		commands := make(chan ports.ClientCommand, 64)
		scenes := make(chan []domain.Scene, 1)
		constraints := make(chan domain.PointerConstraint, 1)
		c, err := core.New(cfg, core.Channels{Client: client, Input: make(chan ports.InputEvent), Output: output, Commands: commands, Scenes: scenes, Constraints: constraints}, testOptions(t, core.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go c.Run(ctx)
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 80}}
		scene(t, scenes)
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "B", Width: 200, Height: 100}}
		scene(t, scenes)
		client <- ports.VirtualPointerInput{Absolute: true, FX: 0.5, FY: 0.5, Output: "B", Event: ports.PointerMotion{}}
		if g := receive(t, constraints); g.X != 200 || g.Y != 50 {
			t.Fatalf("output warp %+v", g)
		}
		// The whole layout is 300x100.
		client <- ports.VirtualPointerInput{Absolute: true, FX: 0.25, FY: 0.5, Event: ports.PointerMotion{}}
		if g := receive(t, constraints); g.X != 75 || g.Y != 50 {
			t.Fatalf("layout warp %+v", g)
		}
	})
}
