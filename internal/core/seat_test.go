package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

var (
	superDown = ports.KeyEvent{Keysym: "Super_L", Keycode: 125, Mods: domain.ModSuper, Pressed: true}
	superUp   = ports.KeyEvent{Keysym: "Super_L", Keycode: 125}
	leftDown  = func(m domain.Mods) ports.KeyEvent {
		return ports.KeyEvent{Keysym: "Left", Keycode: 105, Mods: m, Pressed: true}
	}
)

// seatCycle is a VT switch away and back.
func (r *dragRig) seatCycle() {
	r.in(ports.SeatChanged{Active: false})
	r.in(ports.SeatChanged{Active: true})
}

func focusChanges(cmds []ports.ClientCommand) (ids []domain.WindowID) {
	for _, v := range cmds {
		if f, ok := v.(ports.FocusWindow); ok {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

func hasCommand[T ports.ClientCommand](cmds []ports.ClientCommand) bool {
	for _, v := range cmds {
		if _, ok := v.(T); ok {
			return true
		}
	}
	return false
}

func forwardedKeys(cmds []ports.ClientCommand) (codes []uint32) {
	for _, v := range cmds {
		if f, ok := v.(ports.ForwardKey); ok {
			codes = append(codes, f.Key.Keycode)
		}
	}
	return codes
}

func mapRow(r *dragRig, n int) {
	for id := domain.WindowID(1); id <= domain.WindowID(n); id++ {
		r.client <- ports.WindowMapped{ID: id}
	}
}

// Super held across a VT switch is gone after it: the next Left is a plain
// key, not cmd+Left, and the late Super release goes nowhere.
func TestSeatResetDropsHeldSuper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		mapRow(r, 3)
		r.settle(t)
		r.in(superDown)
		r.settle(t)
		r.drain()
		r.seatCycle()
		r.settle(t)
		cmds := r.drain()
		if !hasCommand[ports.KeyboardReset](cmds) {
			t.Fatal("no KeyboardReset on seat deactivation")
		}
		r.in(leftDown(0))
		r.in(superUp)
		r.settle(t)
		cmds = r.drain()
		if ids := focusChanges(cmds); len(ids) != 0 {
			t.Fatalf("Left without Super moved focus: %v", ids)
		}
		for _, code := range forwardedKeys(cmds) {
			if code == 125 {
				t.Fatal("late Super release reached a client")
			}
		}
	})
}

// A repeating cmd+Left stops when the seat goes inactive.
func TestSeatInactiveStopsRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		mapRow(r, 5)
		r.settle(t)
		r.drain()
		r.in(superDown)
		r.in(leftDown(domain.ModSuper))
		r.settle(t)
		if ids := focusChanges(r.drain()); len(ids) != 1 {
			t.Fatalf("press: focus changes %v, want 1", ids)
		}
		r.in(ports.SeatChanged{Active: false})
		r.settle(t)
		r.drain()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if ids := focusChanges(r.drain()); len(ids) != 0 {
			t.Fatalf("repeat went on while inactive: %v", ids)
		}
	})
}

// A drag in progress is cancelled: the float returns, the button release
// that follows reaches nobody, and the pointer focus is cleared.
func TestSeatInactiveCancelsDragAndClearsButtons(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		r.client <- ports.WindowMapped{ID: 1}
		r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
		set := r.settle(t)
		f := dragRect(t, set, "A", 2)
		r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(float64(f.X+60), float64(f.Y+40), 2)
		r.settle(t)
		r.drain()
		r.in(ports.SeatChanged{Active: false})
		set = r.settle(t)
		if got := dragRect(t, set, "A", 2); got != f {
			t.Fatalf("float at %+v, want %+v back", got, f)
		}
		cmds := r.drain()
		if !hasCommand[ports.KeyboardReset](cmds) {
			t.Fatal("no KeyboardReset")
		}
		cleared := false
		for _, v := range cmds {
			if pf, ok := v.(ports.PointerFocus); ok && pf.ID == 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Fatal("pointer focus not cleared")
		}
		r.in(ports.SeatChanged{Active: true})
		r.in(ports.PointerButton{Button: 0x110})
		r.in(superUp)
		r.settle(t)
		for _, v := range pointerSent(r.drain()) {
			if _, ok := v.(ports.PointerButtonTo); ok {
				t.Fatalf("stale release reached a client: %#v", v)
			}
		}
	})
}

// The focused output survives the seat cycle, kept by name.
func TestSeatCycleKeepsFocusedOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, nil, left, right)
		w := watchState(r)
		r.mapWindow(t, 1)
		r.input <- ports.PointerMotion{X: 250, Y: 50}
		synctest.Wait()
		if got := w.get().Output; got != "DP-2" {
			t.Fatalf("pointer on DP-2: focused %q", got)
		}
		r.input <- ports.SeatChanged{Active: false}
		r.input <- ports.SeatChanged{Active: true}
		synctest.Wait()
		if got := w.get().Output; got != "DP-2" {
			t.Fatalf("focused output %q after the seat cycle, want DP-2", got)
		}
	})
}

// stateWatch keeps the latest snapshot: core publishes one only on a change.
type stateWatch struct {
	ch   chan domain.State
	last domain.State
}

func watchState(r *multiRig) *stateWatch { return &stateWatch{ch: r.state} }

func (w *stateWatch) get() domain.State {
	synctest.Wait()
	for {
		select {
		case w.last = <-w.ch:
		default:
			return w.last
		}
	}
}

// A four-finger swipe in progress when the seat goes away does nothing: its
// late end is ignored.
func TestSeatInactiveDropsFourFingerSwipe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startSwipe(t, nil)
		w := watchState(r.multiRig)
		r.mapWindow(t, 1)
		before := w.get().Outputs[0].Active
		r.at += time.Second
		r.input <- ports.SwipeBegin{Fingers: 4, Time: r.at}
		for range 10 {
			r.at += 8 * time.Millisecond
			r.input <- ports.SwipeUpdate{DX: 0, DY: -40, Time: r.at}
		}
		r.input <- ports.SeatChanged{Active: false}
		r.input <- ports.SwipeEnd{Time: r.at}
		r.settleAll(t, r.outs...)
		for _, s := range drainScenes(r.scenes) {
			for _, w := range s.Windows {
				if w.Preview > 0 {
					t.Fatal("the late swipe end opened the overview")
				}
			}
		}
		if st := w.get(); st.Outputs[0].Active != before {
			t.Fatalf("workspace changed from %d: %+v", before, st.Outputs)
		}
	})
}

func drainScenes(ch chan []domain.Scene) (out []domain.Scene) {
	synctest.Wait()
	for {
		select {
		case set := <-ch:
			out = append(out, set...)
		default:
			return out
		}
	}
}

// A click on an output with no window under the pointer focuses it, while a
// keyboard move to another output stays until a click.
func TestClickOnEmptyOutputFocusesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, nil, left, right)
		w := watchState(r)
		r.mapWindow(t, 1) // on DP-1
		r.input <- ports.PointerMotion{X: 250, Y: 50}
		synctest.Wait()
		// The keyboard moves back to DP-1; the pointer stays on DP-2.
		r.key(t, "Left", domain.ModAlt|domain.ModCtrl)
		synctest.Wait()
		if got := w.get().Output; got != "DP-1" {
			t.Fatalf("after focus-monitor-left: %q", got)
		}
		r.input <- ports.PointerMotion{X: 251, Y: 50}
		synctest.Wait()
		if got := w.get().Output; got != "DP-1" {
			t.Fatalf("motion within DP-2 moved the focus to %q", got)
		}
		r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
		r.input <- ports.PointerButton{Button: 0x110}
		synctest.Wait()
		if got := w.get().Output; got != "DP-2" {
			t.Fatalf("click on the empty DP-2 left the focus on %q", got)
		}
	})
}

// Pins the current edge behavior on the rightmost output of a cascade (the
// state the VT report ended in): Right at the last column steps to the
// trailing empty workspace of the output; Left from there leaves for the
// output on the left and keeps the empty workspace shown on the right one.
func TestCascadeRightmostOutputEdgeRightThenLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, func(c *domain.Config) {
			c.Layout.Overflow = "cascade"
			c.Layout.MaxColumns = 2
		}, left, right)
		w := watchState(r)
		r.input <- ports.PointerMotion{X: 250, Y: 50}
		synctest.Wait()
		for id := domain.WindowID(1); id <= 3; id++ {
			r.mapWindow(t, id)
		}
		st := w.get()
		if st.Output != "DP-2" || st.Window == nil || st.Window.ID != 3 {
			t.Fatalf("setup: output %q window %+v", st.Output, st.Window)
		}
		active := func(s domain.State) int {
			for _, o := range s.Outputs {
				if o.Name == "DP-2" {
					return o.Active
				}
			}
			t.Fatal("no DP-2")
			return 0
		}
		before := active(st)
		r.key(t, "Right", domain.ModAlt)
		st = w.get()
		if st.Output != "DP-2" || active(st) != before+1 || st.Window != nil {
			t.Fatalf("Right: output %q workspace %d window %v, want DP-2's empty workspace %d", st.Output, active(st), st.Window, before+1)
		}
		r.key(t, "Left", domain.ModAlt)
		st = w.get()
		if st.Output != "DP-1" || active(st) != before+1 {
			t.Fatalf("Left: output %q, DP-2 on workspace %d, want DP-1 focused and DP-2 still on %d", st.Output, active(st), before+1)
		}
	})
}

// lockedSeatRig runs a secured core with window 1 on output A, unlocked.
func lockedSeatRig(t *testing.T, binds chan domain.InputTrace) (r *multiRig, lock func() domain.SecurityState) {
	t.Helper()
	epoch := &atomic.Value{}
	epoch.Store(domain.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) })
	r = &multiRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 32),
		output: make(chan ports.OutputEvent, 4), reload: make(chan domain.ConfigChanged, 4),
		commands: make(chan ports.ClientCommand, 256), scenes: make(chan []domain.Scene, 1),
	}
	c, err := core.New(scrollDefaults(), core.Channels{Client: r.client, Input: r.input, Output: r.output, Config: r.reload, Commands: r.commands, Scenes: r.scenes, Binds: binds, Constraints: make(chan domain.PointerConstraint, 1)}, testOptions(t, core.Options{Security: gate}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	r.output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 300, Height: 200}}
	scene(t, r.scenes)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	return r, func() domain.SecurityState {
		locked := domain.SecurityState{Generation: 1, Protected: true}
		epoch.Store(locked)
		r.client <- ports.SessionLockChanged{State: locked, Surfaces: []domain.LockSurfacePlacement{{ID: 10, Output: "A", Width: 300, Height: 200}}}
		sceneMatch(t, r.scenes, func(s domain.Scene) bool { return s.Security == locked && len(s.Windows) == 1 })
		return locked
	}
}

// A button held on the lock surface across a VT switch is released for good:
// the lock client gets a pointer leave (PointerFocus{} in the locked epoch)
// before the keyboard reset, which carries the kept modifier state, and no
// desktop scene is published.
func TestSeatInactiveWhileLockedClearsPointerAndButtons(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, lock := lockedSeatRig(t, nil)
		locked := lock()
		r.input <- ports.SecurityInput{State: locked, Event: ports.PointerMotion{X: 50, Y: 50}}
		r.input <- ports.SecurityInput{State: locked, Event: ports.PointerButton{Button: 0x110, Pressed: true}}
		synctest.Wait()
		for len(r.commands) > 0 {
			<-r.commands
		}
		kept := domain.ModState{Locked: 2, Group: 1}
		r.input <- ports.SecurityInput{State: locked, Event: ports.SeatChanged{State: kept}}
		synctest.Wait()
		var got []ports.ClientCommand
		for len(r.commands) > 0 {
			w, ok := (<-r.commands).(ports.SecurityCommand)
			if !ok || w.State != locked {
				t.Fatalf("command %#v not stamped with the locked epoch", w)
			}
			got = append(got, w.Command)
		}
		want := []ports.ClientCommand{ports.PointerFocus{}, ports.KeyboardReset{State: kept}}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("commands %#v, want %#v", got, want)
		}
		if len(r.scenes) != 0 {
			t.Fatal("a scene was published under the lock")
		}
		// The release that follows reaches nobody, and a new press is a fresh grab.
		r.input <- ports.SecurityInput{State: locked, Event: ports.PointerButton{Button: 0x110}}
		synctest.Wait()
		for len(r.commands) > 0 {
			if w := <-r.commands; w != nil {
				if s, ok := w.(ports.SecurityCommand); ok {
					if _, isBtn := s.Command.(ports.PointerButtonTo); isBtn {
						t.Fatalf("stale release forwarded: %#v", s.Command)
					}
				}
			}
		}
	})
}

// The reset trace counts each key once, though pressed and inputKeys both
// track it in secure mode.
func TestSeatResetTraceCountsKeysOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binds := make(chan domain.InputTrace, 8)
		r, _ := lockedSeatRig(t, binds)
		un := domain.SecurityState{}
		r.input <- ports.SecurityInput{State: un, Event: ports.KeyEvent{Keysym: "Super_L", Keycode: 125, Mods: domain.ModSuper, Pressed: true}}
		r.input <- ports.SecurityInput{State: un, Event: ports.KeyEvent{Keysym: "a", Keycode: 30, Mods: domain.ModSuper, Pressed: true}}
		r.input <- ports.SecurityInput{State: un, Event: ports.SeatChanged{}}
		synctest.Wait()
		for len(binds) > 0 {
			if v, ok := (<-binds).(domain.SeatInputReset); ok {
				if v.Keys != 2 || v.Mods != 1 {
					t.Fatalf("trace %+v, want 2 keys and 1 mod", v)
				}
				return
			}
		}
		t.Fatal("no SeatInputReset")
	})
}
