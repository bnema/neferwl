package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// gestureRig is a rig with window 1 mapped on the left output and the
// pointer on it. The right output is empty: the pointer there has no focus.
func gestureRig(t *testing.T) *multiRig {
	t.Helper()
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.input <- ports.PointerMotion{X: 50, Y: 50}
	if v := pointerCommand(t, r.commands); v != (ports.PointerFocus{ID: 1, X: 50, Y: 50}) {
		t.Fatal(v)
	}
	if v := pointerCommand(t, r.commands); v != (ports.PointerMotionTo{ID: 1, X: 50, Y: 50}) {
		t.Fatal(v)
	}
	return r
}

// isPointerCommand reports whether a command is one the gesture tests follow:
// the pointer's and the gestures'.
func isPointerCommand(cmd ports.ClientCommand) bool {
	switch cmd.(type) {
	case ports.PointerFocus, ports.PointerMotionTo, ports.PointerAxisTo, ports.GestureBeginTo, ports.GestureUpdateTo, ports.GestureEndTo:
		return true
	}
	return false
}

// pointerCommand receives the next pointer or gesture command, skipping
// the configures and focus changes between them.
func pointerCommand(t *testing.T, ch <-chan ports.ClientCommand) ports.ClientCommand {
	t.Helper()
	for {
		if v := command(t, ch); isPointerCommand(v) {
			return v
		}
	}
}

// idle fails the test when core sent a command meanwhile. A marker input
// goes first, so the check waits for core to have handled what came before.
func (r *multiRig) idle(t *testing.T) {
	t.Helper()
	r.input <- ports.PointerAxis{Vertical: ports.ScrollAxis{Set: true, Value: 1}}
	if v := pointerCommand(t, r.commands); v != (ports.PointerAxisTo{ID: 1, Axis: ports.PointerAxis{Vertical: ports.ScrollAxis{Set: true, Value: 1}}}) {
		t.Fatalf("unexpected command %#v", v)
	}
}

func TestPinchGoesToPointerFocus(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.PinchBegin{Fingers: 2, Time: time.Second}
	r.input <- ports.PinchUpdate{DX: 1, DY: -2, Scale: 1.25, Rotation: 5, Time: 2 * time.Second}
	r.input <- ports.PinchEnd{Time: 3 * time.Second}
	for _, want := range []ports.ClientCommand{
		ports.GestureBeginTo{ID: 1, Kind: ports.GesturePinch, Fingers: 2, Time: time.Second},
		ports.GestureUpdateTo{ID: 1, Kind: ports.GesturePinch, DX: 1, DY: -2, Scale: 1.25, Rotation: 5, Time: 2 * time.Second},
		ports.GestureEndTo{ID: 1, Kind: ports.GesturePinch, Time: 3 * time.Second},
	} {
		if v := pointerCommand(t, r.commands); v != want {
			t.Fatalf("got %#v, want %#v", v, want)
		}
	}
}

// A removed touchpad ends its gesture with no time: the client gets the
// last one, so its clock never runs backwards.
func TestGestureEndWithoutTime(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.PinchBegin{Fingers: 2, Time: time.Second}
	r.input <- ports.PinchUpdate{Scale: 1, Time: 2 * time.Second}
	r.input <- ports.PinchEnd{Cancelled: true}
	pointerCommand(t, r.commands)
	pointerCommand(t, r.commands)
	if v := pointerCommand(t, r.commands); v != (ports.GestureEndTo{ID: 1, Kind: ports.GesturePinch, Cancelled: true, Time: 2 * time.Second}) {
		t.Fatal(v)
	}
}

func TestHoldGoesToPointerFocus(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.HoldBegin{Fingers: 3, Time: time.Second}
	r.input <- ports.HoldEnd{Cancelled: true, Time: 2 * time.Second}
	if v := pointerCommand(t, r.commands); v != (ports.GestureBeginTo{ID: 1, Kind: ports.GestureHold, Fingers: 3, Time: time.Second}) {
		t.Fatal(v)
	}
	if v := pointerCommand(t, r.commands); v != (ports.GestureEndTo{ID: 1, Kind: ports.GestureHold, Cancelled: true, Time: 2 * time.Second}) {
		t.Fatal(v)
	}
}

// Swipes of other finger counts than three and four go to the client; the
// compositor keeps those.
func TestSwipeFingerCounts(t *testing.T) {
	for _, fingers := range []int{2, 5} {
		r := gestureRig(t)
		r.input <- ports.SwipeBegin{Fingers: fingers, Time: time.Second}
		// The client gets the accelerated deltas.
		r.input <- ports.SwipeUpdate{DX: 3, DY: 4, AccelDX: 6, AccelDY: 8, Time: 2 * time.Second}
		r.input <- ports.SwipeEnd{Time: 3 * time.Second}
		for _, want := range []ports.ClientCommand{
			ports.GestureBeginTo{ID: 1, Kind: ports.GestureSwipe, Fingers: fingers, Time: time.Second},
			ports.GestureUpdateTo{ID: 1, Kind: ports.GestureSwipe, DX: 6, DY: 8, Time: 2 * time.Second},
			ports.GestureEndTo{ID: 1, Kind: ports.GestureSwipe, Time: 3 * time.Second},
		} {
			if v := pointerCommand(t, r.commands); v != want {
				t.Fatalf("%d fingers: got %#v, want %#v", fingers, v, want)
			}
		}
	}
	for _, fingers := range []int{3, 4} {
		r := gestureRig(t)
		r.input <- ports.SwipeBegin{Fingers: fingers, Time: time.Second}
		r.input <- ports.SwipeUpdate{DX: 40, DY: 0, Time: 2 * time.Second}
		r.input <- ports.SwipeEnd{Time: 3 * time.Second}
		r.idle(t)
	}
}

// A gesture that began with no window under the pointer reaches nobody,
// even when the pointer enters a window meanwhile.
func TestGestureWithoutPointerFocus(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.PointerMotion{X: 300, Y: 50}
	if v := pointerCommand(t, r.commands); v != (ports.PointerFocus{}) {
		t.Fatal(v)
	}
	r.input <- ports.PinchBegin{Fingers: 2, Time: time.Second}
	r.input <- ports.PointerMotion{X: 50, Y: 50}
	if v := pointerCommand(t, r.commands); v != (ports.PointerFocus{ID: 1, X: 50, Y: 50}) {
		t.Fatal(v)
	}
	if v := pointerCommand(t, r.commands); v != (ports.PointerMotionTo{ID: 1, X: 50, Y: 50}) {
		t.Fatal(v)
	}
	r.input <- ports.PinchUpdate{Scale: 2, Time: 2 * time.Second}
	r.input <- ports.PinchEnd{Time: 3 * time.Second}
	r.idle(t)
}

// The window losing the pointer focus gets its gesture cancelled before the
// leave, and nothing of the rest.
func TestGestureCancelledOnFocusChange(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.PinchBegin{Fingers: 2, Time: time.Second}
	if v := pointerCommand(t, r.commands); v != (ports.GestureBeginTo{ID: 1, Kind: ports.GesturePinch, Fingers: 2, Time: time.Second}) {
		t.Fatal(v)
	}
	r.input <- ports.PinchUpdate{Scale: 1.1, Time: 2 * time.Second}
	if v := pointerCommand(t, r.commands); v != (ports.GestureUpdateTo{ID: 1, Kind: ports.GesturePinch, Scale: 1.1, Time: 2 * time.Second}) {
		t.Fatal(v)
	}
	r.input <- ports.PointerMotion{X: 300, Y: 50}
	if v := pointerCommand(t, r.commands); v != (ports.GestureEndTo{ID: 1, Kind: ports.GesturePinch, Cancelled: true, Time: 2 * time.Second}) {
		t.Fatal(v)
	}
	if v := pointerCommand(t, r.commands); v != (ports.PointerFocus{}) {
		t.Fatal(v)
	}
	r.input <- ports.PinchUpdate{Scale: 1.2, Time: 3 * time.Second}
	r.input <- ports.PinchEnd{Time: 4 * time.Second}
	r.input <- ports.PointerMotion{X: 50, Y: 50}
	if v := pointerCommand(t, r.commands); v != (ports.PointerFocus{ID: 1, X: 50, Y: 50}) {
		t.Fatal(v)
	}
}

// A window that goes away mid-gesture has its gesture cancelled with the
// pointer focus.
func TestGestureCancelledOnUnmap(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.HoldBegin{Fingers: 3, Time: time.Second}
	if v := pointerCommand(t, r.commands); v != (ports.GestureBeginTo{ID: 1, Kind: ports.GestureHold, Fingers: 3, Time: time.Second}) {
		t.Fatal(v)
	}
	r.client <- ports.WindowUnmapped{ID: 1}
	if v := pointerCommand(t, r.commands); v != (ports.GestureEndTo{ID: 1, Kind: ports.GestureHold, Cancelled: true, Time: time.Second}) {
		t.Fatal(v)
	}
	if v := pointerCommand(t, r.commands); v != (ports.PointerFocus{}) {
		t.Fatal(v)
	}
}

// A gesture whose end was lost ends cancelled when the next one begins.
func TestGestureBeginEndsPrevious(t *testing.T) {
	r := gestureRig(t)
	r.input <- ports.PinchBegin{Fingers: 2, Time: time.Second}
	r.input <- ports.HoldBegin{Fingers: 3, Time: 2 * time.Second}
	for _, want := range []ports.ClientCommand{
		ports.GestureBeginTo{ID: 1, Kind: ports.GesturePinch, Fingers: 2, Time: time.Second},
		ports.GestureEndTo{ID: 1, Kind: ports.GesturePinch, Cancelled: true, Time: 2 * time.Second},
		ports.GestureBeginTo{ID: 1, Kind: ports.GestureHold, Fingers: 3, Time: 2 * time.Second},
	} {
		if v := pointerCommand(t, r.commands); v != want {
			t.Fatalf("got %#v, want %#v", v, want)
		}
	}
}

// A protected session forwards no gesture to a window; one running when
// the session locks ends with the pointer focus.
func TestGesturesRefusedWhileLocked(t *testing.T) {
	var epoch atomic.Value
	epoch.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) })
	r := &multiRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 32),
		output: make(chan ports.OutputEvent, 4), reload: make(chan ports.ConfigChanged, 4),
		commands: make(chan ports.ClientCommand, 256), scenes: make(chan []ports.Scene, 1),
	}
	cfg := scrollDefaults()
	c, err := core.New(cfg, core.Channels{Client: r.client, Input: r.input, Output: r.output, Config: r.reload, Commands: r.commands, Scenes: r.scenes, Constraints: make(chan ports.PointerConstraint, 1)}, core.Options{Security: gate})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	r.output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "A", Width: 300, Height: 200}}
	scene(t, r.scenes)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	unlocked := ports.SecurityState{}
	r.input <- ports.SecurityInput{State: unlocked, Event: ports.PointerMotion{X: 50, Y: 50}}
	r.input <- ports.SecurityInput{State: unlocked, Event: ports.PinchBegin{Fingers: 2, Time: time.Second}}
	for _, want := range []ports.ClientCommand{
		ports.PointerFocus{ID: 1, X: 50, Y: 50},
		ports.PointerMotionTo{ID: 1, X: 50, Y: 50},
		ports.GestureBeginTo{ID: 1, Kind: ports.GesturePinch, Fingers: 2, Time: time.Second},
	} {
		if v := unwrap(t, r.commands, unlocked); v != want {
			t.Fatalf("got %#v, want %#v", v, want)
		}
	}

	// The lock clears the pointer focus, which cancels the gesture in wayland.
	locked := ports.SecurityState{Generation: 1, Protected: true}
	epoch.Store(locked)
	r.client <- ports.SessionLockChanged{State: locked, Surfaces: []ports.LockSurfacePlacement{{ID: 10, Output: "A", Width: 300, Height: 200}}}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return s.Security == locked && len(s.Windows) == 1 })
	r.input <- ports.SecurityInput{State: locked, Event: ports.PointerMotion{X: 50, Y: 50}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.PinchBegin{Fingers: 2, Time: 2 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.PinchUpdate{Scale: 2, Time: 3 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.PinchEnd{Time: 4 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.HoldBegin{Fingers: 3, Time: 5 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.HoldEnd{Time: 6 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.SwipeBegin{Fingers: 2, Time: 7 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.SwipeEnd{Time: 8 * time.Second}}
	r.input <- ports.SecurityInput{State: locked, Event: ports.KeyEvent{Keysym: "marker", Keycode: 30, Pressed: true}}
	for {
		wrapped, ok := receive(t, r.commands).(ports.SecurityCommand)
		if !ok {
			t.Fatal("unwrapped command")
		}
		switch v := wrapped.Command.(type) {
		case ports.GestureBeginTo, ports.GestureUpdateTo, ports.GestureEndTo:
			if wrapped.State == locked {
				t.Fatalf("gesture forwarded while locked: %#v", v)
			}
		case ports.ForwardKey:
			if wrapped.State == locked {
				return
			}
		}
	}
}

// unwrap returns the next pointer command of an envelope of the given epoch.
func unwrap(t *testing.T, ch <-chan ports.ClientCommand, state ports.SecurityState) ports.ClientCommand {
	t.Helper()
	for {
		wrapped, ok := receive(t, ch).(ports.SecurityCommand)
		if !ok {
			t.Fatal("unwrapped command")
		}
		if wrapped.State != state {
			t.Fatalf("epoch %+v, want %+v", wrapped.State, state)
		}
		if isPointerCommand(wrapped.Command) {
			return wrapped.Command
		}
	}
}
