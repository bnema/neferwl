package core_test

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

const dndIcon ports.WindowID = 900

// dndRig is a drag rig with tiles 1 and 2 side by side and the pointer
// on 1 with its button held, as a client that is about to start_drag.
type dndRig struct {
	*dragRig
	one, two ports.Rect
}

func startDnd(t *testing.T, outs ...ports.OutputInfo) *dndRig {
	t.Helper()
	if len(outs) == 0 {
		outs = []ports.OutputInfo{dragOut}
	}
	r := startDragRig(t, nil, outs...)
	r.client <- ports.WindowMapped{ID: 1}
	r.client <- ports.WindowMapped{ID: 2}
	set := r.settle(t)
	d := &dndRig{dragRig: r, one: dragRect(t, set, outs[0].Name, 1), two: dragRect(t, set, outs[0].Name, 2)}
	r.moveTo(float64(d.one.X+20), float64(d.one.Y+30), 1)
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.settle(t)
	r.drain()
	return d
}

// dataDragCommands keeps what the data drag sends and the pointer events
// no client may see during it.
func dataDragCommands(cmds []ports.ClientCommand) []ports.ClientCommand {
	var out []ports.ClientCommand
	for _, v := range cmds {
		switch v.(type) {
		case ports.DataDragFocus, ports.DataDragMotion, ports.DataDragDrop, ports.DataDragCancel,
			ports.PointerFocus, ports.PointerMotionTo, ports.PointerButtonTo, ports.PointerAxisTo,
			ports.GestureBeginTo, ports.GestureUpdateTo, ports.GestureEndTo:
			out = append(out, v)
		}
	}
	return out
}

func (d *dndRig) start(t *testing.T, icon ports.WindowID) []ports.ClientCommand {
	t.Helper()
	d.client <- ports.DataDragStarted{Button: 0x110, Icon: icon}
	d.settle(t)
	return dataDragCommands(d.drain())
}

func wantCommands(t *testing.T, got []ports.ClientCommand, want ...ports.ClientCommand) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("commands %#v, want %#v", got, want)
	}
}

func TestDataDragRoutesToSurfaceUnderPointer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		// The origin loses the pointer and is the first target.
		wantCommands(t, d.start(t, 0),
			ports.PointerFocus{},
			ports.DataDragFocus{ID: 1, X: 20, Y: 30, Time: time.Millisecond})
		// Motion over the same surface.
		d.moveTo(float64(d.one.X+25), float64(d.one.Y+35), 2)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.DataDragMotion{ID: 1, X: 25, Y: 35, Time: 2 * time.Millisecond})
		// Over the other window: it is the new target.
		d.moveTo(float64(d.two.X+5), float64(d.two.Y+6), 3)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.DataDragFocus{ID: 2, X: 5, Y: 6, Time: 3 * time.Millisecond})
		d.moveTo(float64(d.two.X+7), float64(d.two.Y+8), 4)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.DataDragMotion{ID: 2, X: 7, Y: 8, Time: 4 * time.Millisecond})
		// Over nothing: no target.
		d.moveTo(float64(d.two.X+d.two.W+5), float64(d.two.Y+8), 5)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.DataDragFocus{Time: 5 * time.Millisecond})
		d.moveTo(float64(d.two.X+d.two.W+6), float64(d.two.Y+8), 6)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()))

	})
}

func TestDataDragDropsOnRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.moveTo(float64(d.two.X+5), float64(d.two.Y+6), 3)
		d.in(ports.PointerButton{Button: 0x110, Time: 4 * time.Millisecond})
		d.settle(t)
		// The release is the drop; the pointer then points at the window under
		// the cursor, not at the origin that grabbed it.
		wantCommands(t, dataDragCommands(d.drain()),
			ports.DataDragFocus{ID: 2, X: 5, Y: 6, Time: 3 * time.Millisecond},
			ports.DataDragDrop{},
			ports.PointerFocus{ID: 2, X: 5, Y: 6})
		// The drag is over: motion is plain pointer motion again.
		d.moveTo(float64(d.two.X+9), float64(d.two.Y+9), 5)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.PointerMotionTo{ID: 2, X: 9, Y: 9, Time: 5 * time.Millisecond})
		// The next press is an ordinary click.
		d.in(ports.PointerButton{Button: 0x110, Pressed: true})
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.PointerButtonTo{ID: 2, Button: 0x110, Pressed: true, Grab: true})

	})
}

func TestDataDragSendsNothingToClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.in(ports.PointerAxis{Vertical: ports.ScrollAxis{Set: true, Value: 1}})
		d.in(ports.PinchBegin{Fingers: 2, Time: time.Second})
		d.in(ports.PinchUpdate{Scale: 2, Time: time.Second})
		d.in(ports.PinchEnd{Time: time.Second})
		d.in(ports.HoldBegin{Fingers: 2, Time: time.Second})
		d.in(ports.HoldEnd{Time: time.Second})
		d.in(ports.SwipeBegin{Fingers: 2, Time: time.Second})
		d.in(ports.SwipeEnd{Time: time.Second})
		// Other buttons neither press nor release anywhere.
		d.in(ports.PointerButton{Button: 0x111, Pressed: true})
		d.in(ports.PointerButton{Button: 0x111})
		d.settle(t)
		for _, v := range dataDragCommands(d.drain()) {
			switch v.(type) {
			case ports.PointerButtonTo, ports.PointerAxisTo, ports.GestureBeginTo, ports.GestureUpdateTo, ports.GestureEndTo, ports.PointerMotionTo, ports.DataDragDrop, ports.DataDragCancel:
				t.Fatalf("sent %#v", v)
			}
		}
		// Still dragging: the drag button's release drops.
		d.in(ports.PointerButton{Button: 0x110})
		d.settle(t)
		if got := dataDragCommands(d.drain()); len(got) == 0 || got[0] != (ports.DataDragDrop{}) {
			t.Fatalf("commands %#v", got)
		}

	})
}

func TestDataDragKeyboardKeepsForwarding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.in(ports.KeyEvent{Keysym: "Shift_L", Keycode: 42, Pressed: true, Mods: ports.ModShift})
		d.in(ports.KeyEvent{Keysym: "Shift_L", Keycode: 42})
		d.settle(t)
		n := 0
		for _, v := range d.drain() {
			if _, ok := v.(ports.ForwardKey); ok {
				n++
			}
		}
		if n != 2 {
			t.Fatalf("%d keys forwarded, want 2", n)
		}

	})
}

func TestDataDragEscapeCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.moveTo(float64(d.two.X+5), float64(d.two.Y+6), 3)
		d.settle(t)
		d.drain()
		d.in(ports.KeyEvent{Keysym: "Escape", Keycode: 1, Pressed: true})
		d.in(ports.KeyEvent{Keysym: "Escape", Keycode: 1})
		d.in(ports.PointerButton{Button: 0x110})
		d.settle(t)
		cmds := d.drain()
		wantCommands(t, dataDragCommands(cmds),
			ports.DataDragCancel{},
			ports.PointerFocus{ID: 2, X: 5, Y: 6})
		for _, v := range cmds {
			if k, ok := v.(ports.ForwardKey); ok {
				t.Fatalf("client saw %#v", k)
			}
		}
		// The press that started the drag is over.
		d.moveTo(float64(d.two.X+9), float64(d.two.Y+9), 5)
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.PointerMotionTo{ID: 2, X: 9, Y: 9, Time: 5 * time.Millisecond})

	})
}

func TestDataDragEndedByWayland(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, dndIcon)
		d.client <- ports.DataDragIcon{ID: dndIcon, X: 1, Y: 2, W: 16, H: 16}
		d.moveTo(float64(d.two.X+5), float64(d.two.Y+6), 3)
		d.settle(t)
		d.drain()
		// The source died: no command, the icon goes and the pointer is
		// pointed at the window under the cursor.
		d.client <- ports.DataDragEnded{}
		set := d.settle(t)
		if _, ok := windowIn(set, "A", dndIcon); ok {
			t.Fatal("icon still drawn")
		}
		wantCommands(t, dataDragCommands(d.drain()),
			ports.PointerFocus{ID: 2, X: 5, Y: 6})
		// The held button's release reaches nobody.
		d.in(ports.PointerButton{Button: 0x110})
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()))
		// An end with no drag is ignored.
		d.client <- ports.DataDragEnded{}
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()))

	})
}

func TestDataDragRefused(t *testing.T) {
	t.Run("no press", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := startDnd(t)
			d.in(ports.PointerButton{Button: 0x110})
			d.settle(t)
			d.drain()
			wantCommands(t, d.start(t, 0), ports.DataDragCancel{})
			// No drag started: motion is plain.
			d.moveTo(float64(d.one.X+21), float64(d.one.Y+30), 2)
			d.settle(t)
			wantCommands(t, dataDragCommands(d.drain()), ports.PointerMotionTo{ID: 1, X: 21, Y: 30, Time: 2 * time.Millisecond})

		})
	})
	t.Run("window drag", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := startDragRig(t, nil, dragOut)
			r.client <- ports.WindowMapped{ID: 1}
			r.client <- ports.WindowMapped{ID: 2}
			set := r.settle(t)
			one := dragRect(t, set, "A", 1)
			r.moveTo(float64(one.X+20), float64(one.Y+30), 1)
			r.settle(t)
			r.press(t, 0x110, ports.ModSuper)
			r.settle(t)
			r.drain()
			r.client <- ports.DataDragStarted{Button: 0x110}
			r.settle(t)
			wantCommands(t, r.drain(), ports.DataDragCancel{})
			// The window drag goes on.
			r.moveTo(float64(one.X+200), float64(one.Y+30), 2)
			r.in(ports.PointerButton{Button: 0x110})
			r.settle(t)
			wantCommands(t, dataDragCommands(r.drain()), ports.PointerFocus{ID: 1, X: 200, Y: 30})

		})
	})
	t.Run("twice", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := startDnd(t)
			d.start(t, 0)
			wantCommands(t, d.start(t, 0), ports.DataDragCancel{})

		})
	})
	t.Run("locked", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := startDnd(t)
			locked := ports.SecurityState{Generation: 1, Protected: true}
			d.epoch.Store(locked)
			d.client <- ports.SessionLockChanged{State: locked}
			d.settle(t)
			d.drain()
			d.client <- ports.DataDragStarted{Button: 0x110}
			d.settle(t)
			var got []ports.ClientCommand
			for _, v := range d.drain() {
				switch v.(type) {
				case ports.DataDragFocus, ports.DataDragMotion, ports.DataDragDrop, ports.DataDragCancel:
					got = append(got, v)
				}
			}
			wantCommands(t, got, ports.DataDragCancel{})

		})
	})
}

func TestDataDragCommandsAreSecurityWrapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.client <- ports.DataDragStarted{Button: 0x110}
		d.settle(t)
		n := 0
		for {
			select {
			case v := <-d.commands:
				s, ok := v.(ports.SecurityCommand)
				if !ok {
					t.Fatalf("unwrapped %#v", v)
				}
				if _, ok := s.Command.(ports.DataDragFocus); ok {
					n++
				}
				continue
			default:
			}
			break
		}
		if n != 1 {
			t.Fatalf("%d DataDragFocus", n)
		}

	})
}

func TestDataDragLockCancelsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, dndIcon)
		d.client <- ports.DataDragIcon{ID: dndIcon, W: 16, H: 16}
		d.settle(t)
		locked := ports.SecurityState{Generation: 1, Protected: true}
		d.epoch.Store(locked)
		d.client <- ports.SessionLockChanged{State: locked, Surfaces: []ports.LockSurfacePlacement{{ID: 10, Output: "A", Width: 1000, Height: 600}}}
		set := d.settle(t)
		if _, ok := windowIn(set, "A", dndIcon); ok {
			t.Fatal("icon drawn while locked")
		}
		d.drain()
		d.in(ports.PointerMotion{X: 100, Y: 100, Time: time.Second})
		d.settle(t)
		for _, v := range d.drain() {
			switch v.(type) {
			case ports.DataDragFocus, ports.DataDragMotion, ports.DataDragDrop:
				t.Fatalf("data drag alive while locked: %#v", v)
			}
		}
		// After the unlock, the old drag is gone: plain pointer events.
		unlocked := ports.SecurityState{Generation: 2}
		d.epoch.Store(unlocked)
		d.client <- ports.SessionLockChanged{State: unlocked}
		d.settle(t)
		d.drain()
		d.moveTo(float64(d.one.X+21), float64(d.one.Y+30), 9)
		d.settle(t)
		got := dataDragCommands(d.drain())
		wantCommands(t, got, ports.PointerFocus{ID: 1, X: 21, Y: 30}, ports.PointerMotionTo{ID: 1, X: 21, Y: 30, Time: 9 * time.Millisecond})

	})
}

func TestDataDragTargetUnmapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.moveTo(float64(d.two.X+5), float64(d.two.Y+6), 3)
		d.settle(t)
		d.drain()
		d.client <- ports.WindowUnmapped{ID: 2}
		d.settle(t)
		cmds := dataDragCommands(d.drain())
		if len(cmds) == 0 || cmds[0] != (ports.DataDragFocus{Time: 3 * time.Millisecond}) {
			t.Fatalf("commands %#v", cmds)
		}

	})
}

func TestDataDragIconIsLastWindowAtCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, dndIcon)
		// Not drawn until wayland measured it.
		if _, ok := windowIn(d.settle(t), "A", dndIcon); ok {
			t.Fatal("icon drawn before its size")
		}
		cx, cy := d.one.X+20, d.one.Y+30
		d.client <- ports.DataDragIcon{ID: dndIcon, X: -4, Y: -6, W: 32, H: 24}
		set := d.settle(t)
		s := dragScene(set, "A")
		last := s.Windows[len(s.Windows)-1]
		want := ports.SceneWindow{ID: dndIcon, Rect: ports.Rect{X: cx - 4, Y: cy - 6, W: 32, H: 24}, Popup: true, OverLayers: true}
		if last != want {
			t.Fatalf("last window %+v, want %+v", last, want)
		}
		// It follows the cursor; the scene changes.
		d.moveTo(float64(cx+100), float64(cy+50), 2)
		moved := dragScene(d.settle(t), "A")
		if moved.SameAs(s) || moved.Seq == s.Seq {
			t.Fatal("scene did not change with the icon")
		}
		if got := moved.Windows[len(moved.Windows)-1]; got.Rect.X != cx+96 || got.Rect.Y != cy+44 || got.ID != dndIcon {
			t.Fatalf("moved icon %+v", got)
		}
		// Under the icon, the surface below is still the target.
		d.drain()
		d.moveTo(float64(cx+101), float64(cy+50), 3)
		d.settle(t)
		for _, v := range d.drain() {
			if f, ok := v.(ports.DataDragFocus); ok && f.ID == dndIcon {
				t.Fatalf("icon was targeted: %#v", f)
			}
		}
		// A commit that changes the offset moves it; a zero size hides it.
		d.client <- ports.DataDragIcon{ID: dndIcon, X: 0, Y: 0, W: 32, H: 24}
		if got := dragScene(d.settle(t), "A"); got.Windows[len(got.Windows)-1].Rect.X != cx+101 {
			t.Fatalf("offset ignored: %+v", got.Windows[len(got.Windows)-1])
		}
		d.client <- ports.DataDragIcon{ID: dndIcon}
		if _, ok := windowIn(d.settle(t), "A", dndIcon); ok {
			t.Fatal("hidden icon drawn")
		}
		// Geometry of another surface is not the icon's.
		d.client <- ports.DataDragIcon{ID: dndIcon + 1, W: 10, H: 10}
		if _, ok := windowIn(d.settle(t), "A", dndIcon+1); ok {
			t.Fatal("foreign icon drawn")
		}
		// The drop removes it.
		d.client <- ports.DataDragIcon{ID: dndIcon, W: 32, H: 24}
		d.settle(t)
		d.in(ports.PointerButton{Button: 0x110})
		set = d.settle(t)
		if _, ok := windowIn(set, "A", dndIcon); ok {
			t.Fatal("icon drawn after the drop")
		}

	})
}

func TestDataDragIconOnScreenUnderCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		other := ports.OutputInfo{Name: "B", Width: 800, Height: 600}
		d := startDnd(t, dragOut, other)
		d.start(t, dndIcon)
		d.client <- ports.DataDragIcon{ID: dndIcon, X: 2, Y: 3, W: 10, H: 10}
		d.moveTo(1000+100, 50, 2)
		set := d.settle(t)
		if _, ok := windowIn(set, "A", dndIcon); ok {
			t.Fatal("icon on the other screen")
		}
		w, ok := windowIn(set, "B", dndIcon)
		if !ok {
			t.Fatal("no icon on B")
		}
		if w.Rect != (ports.Rect{X: 102, Y: 53, W: 10, H: 10}) {
			t.Fatalf("icon %+v", w.Rect)
		}

	})
}

func TestDataDragNoIconIgnoresIconEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.client <- ports.DataDragIcon{ID: dndIcon, W: 10, H: 10}
		if _, ok := windowIn(d.settle(t), "A", dndIcon); ok {
			t.Fatal("icon drawn for a drag without one")
		}
		// Nor with no drag at all.
		d.in(ports.PointerButton{Button: 0x110})
		d.settle(t)
		d.client <- ports.DataDragIcon{ID: dndIcon, W: 10, H: 10}
		if _, ok := windowIn(d.settle(t), "A", dndIcon); ok {
			t.Fatal("icon drawn without a drag")
		}

	})
}

func TestDataDragFocusScreenFollowsPointer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut, ports.OutputInfo{Name: "B", Width: 1000, Height: 600})
		r.client <- ports.WindowMapped{ID: 1}
		r.settle(t)
		r.moveTo(1500, 300, 1)
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 3}
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+20), float64(one.Y+30), 2)
		set = r.settle(t)
		if w, _ := windowIn(set, "A", 1); !w.Focused {
			t.Fatal("1 not focused")
		}
		r.in(ports.PointerButton{Button: 0x110, Pressed: true})
		r.settle(t)
		r.client <- ports.DataDragStarted{Button: 0x110}
		r.moveTo(1500, 300, 3)
		set = r.settle(t)
		if w, _ := windowIn(set, "B", 3); !w.Focused {
			t.Fatal("the screen under the drag did not take the focus")
		}
		three := dragRect(t, set, "B", 3)
		var focus []ports.DataDragFocus
		for _, v := range r.drain() {
			if f, ok := v.(ports.DataDragFocus); ok {
				focus = append(focus, f)
			}
		}
		if n := len(focus); n != 2 || focus[1].ID != 3 || focus[1].X != float64(1500-1000-three.X) {
			t.Fatalf("focus %+v", focus)
		}

	})
}

func TestDataDragFollowsWorkspaceSwitchUnderStillCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.moveTo(float64(d.two.X+5), float64(d.two.Y+6), 3)
		d.settle(t)
		d.drain()
		// cmd+2 shows an empty workspace under the still cursor: the target
		// leaves, and the release drops on nothing.
		d.in(ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModSuper, Pressed: true})
		d.in(ports.KeyEvent{Keysym: "2", Keycode: 3, Mods: ports.ModSuper})
		d.settle(t)
		got := dataDragCommands(d.drain())
		if !slices.Contains(got, ports.ClientCommand(ports.DataDragFocus{Time: 3 * time.Millisecond})) {
			t.Fatalf("no leave after the switch: %#v", got)
		}
		d.in(ports.PointerButton{Button: 0x110, Time: 4 * time.Millisecond})
		d.settle(t)
		got = dataDragCommands(d.drain())
		if len(got) == 0 || got[0] != (ports.DataDragDrop{}) {
			t.Fatalf("commands %#v", got)
		}
		for _, v := range got {
			if f, ok := v.(ports.DataDragFocus); ok && f.ID != 0 {
				t.Fatalf("hidden window targeted: %#v", got)
			}
		}

	})
}

func TestDataDragTargetMovedUnderStillCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		// Over 2, focused by a click-free keyboard move, then cmd+f maximizes
		// its column: still the target under the cursor, at another point.
		d.moveTo(float64(d.two.X+d.two.W-20), float64(d.two.Y+30), 2)
		d.in(ports.KeyEvent{Keysym: "l", Keycode: 38, Mods: ports.ModSuper, Pressed: true})
		d.in(ports.KeyEvent{Keysym: "l", Keycode: 38, Mods: ports.ModSuper})
		d.settle(t)
		d.drain()
		d.in(ports.KeyEvent{Keysym: "f", Keycode: 33, Mods: ports.ModSuper, Pressed: true})
		d.in(ports.KeyEvent{Keysym: "f", Keycode: 33, Mods: ports.ModSuper})
		set := d.settle(t)
		wide := dragRect(t, set, dragOut.Name, 2)
		if wide.X == d.two.X {
			t.Fatalf("the column did not move: %v", wide)
		}
		x, y := float64(d.two.X+d.two.W-20-wide.X), float64(d.two.Y+30-wide.Y)
		got := dataDragCommands(d.drain())
		if !slices.Contains(got, ports.ClientCommand(ports.DataDragMotion{ID: 2, X: x, Y: y, Time: 2 * time.Millisecond})) {
			t.Fatalf("no motion to %v,%v: %#v", x, y, got)
		}

	})
}

func TestDataDragDropsOnItsOwnButton(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		// Right pressed after left: the drag is left's, the grab's.
		d.in(ports.PointerButton{Button: 0x111, Pressed: true})
		d.settle(t)
		d.drain()
		d.start(t, 0)
		d.in(ports.PointerButton{Button: 0x111})
		d.settle(t)
		if got := dataDragCommands(d.drain()); slices.Contains(got, ports.ClientCommand(ports.DataDragDrop{})) {
			t.Fatalf("right's release dropped: %#v", got)
		}
		d.in(ports.PointerButton{Button: 0x110})
		d.settle(t)
		if got := dataDragCommands(d.drain()); len(got) == 0 || got[0] != (ports.DataDragDrop{}) {
			t.Fatalf("left's release did not drop: %#v", got)
		}

	})
}

func TestDataDragEscapeCancelsOverSwitcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		d.in(ports.KeyEvent{Keysym: "Super_L", Keycode: 125, Mods: ports.ModSuper, Pressed: true})
		d.in(ports.KeyEvent{Keysym: "Tab", Keycode: 15, Mods: ports.ModSuper, Pressed: true})
		d.in(ports.KeyEvent{Keysym: "Tab", Keycode: 15, Mods: ports.ModSuper})
		d.settle(t)
		d.drain()
		d.in(ports.KeyEvent{Keysym: "Escape", Keycode: 1, Mods: ports.ModSuper, Pressed: true})
		d.settle(t)
		got := dataDragCommands(d.drain())
		if len(got) == 0 || got[0] != (ports.DataDragCancel{}) {
			t.Fatalf("Escape did not cancel the drag: %#v", got)
		}
		d.in(ports.PointerButton{Button: 0x110})
		d.settle(t)
		if slices.Contains(dataDragCommands(d.drain()), ports.ClientCommand(ports.DataDragDrop{})) {
			t.Fatal("dropped after Escape")
		}

	})
}

func TestDataDragIgnoresCompositorSwipes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		before := d.settle(t)
		// Flicks that page columns and workspaces, as navigation would.
		for _, fingers := range []int{3, 4} {
			at := time.Second
			d.in(ports.SwipeBegin{Fingers: fingers, Time: at})
			for range 10 {
				at += 8 * time.Millisecond
				d.in(ports.SwipeUpdate{DX: 60, AccelDX: 60, DY: 40, AccelDY: 40, Time: at})
			}
			d.in(ports.SwipeEnd{Time: at})
		}
		after := d.settle(t)
		if !slices.Equal(before[0].Windows, after[0].Windows) {
			t.Fatalf("a swipe moved windows during the drag:\n%#v\n%#v", before[0].Windows, after[0].Windows)
		}

	})
}

func TestDataDragRehitsAfterInputRegionChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := startDnd(t)
		d.start(t, 0)
		// The scene is settled and the cursor never moves after drag start.
		d.client <- ports.InputRegionChanged{ID: 1}
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.DataDragFocus{Time: time.Millisecond})
		d.client <- ports.InputRegionChanged{ID: 1, All: true}
		d.settle(t)
		wantCommands(t, dataDragCommands(d.drain()), ports.DataDragFocus{ID: 1, X: 20, Y: 30, Time: time.Millisecond})

	})
}
