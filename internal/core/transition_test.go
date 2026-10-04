package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// xOf is the X of window id in s, shown or not.
func xOf(t *testing.T, s ports.Scene, id ports.WindowID) int {
	t.Helper()
	for _, w := range s.Windows {
		if w.ID == id {
			return w.Rect.X
		}
	}
	t.Fatalf("window %d not in the scene", id)
	return 0
}

// fourColumns maps four windows of half the width and goes back to the
// first column, settled: the view is at 0.
func fourColumns(t *testing.T, r *swipeRig) {
	t.Helper()
	for id := ports.WindowID(1); id <= 4; id++ {
		r.mapWindow(t, id)
	}
	for range 3 {
		r.key(t, "Left", ports.ModAlt)
	}
	if r.cfg.Animations.On {
		r.settle(t)
	}
}

// A key that scrolls the view shows the old view right after it, an
// intermediate one on the first frame and the final one once settled.
func TestKeyScrollTransition(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	r.key(t, "Left", ports.ModAlt)
	r.key(t, "Left", ports.ModAlt)
	r.settle(t)
	r.key(t, "Right", ports.ModAlt) // 2 is on screen: nothing scrolls
	s := r.key(t, "Right", ports.ModAlt)[0]
	if got := xOf(t, s, 1); got != 0 {
		t.Fatalf("column 1 at %d right after the key, want the old 0", got)
	}
	got := xOf(t, r.frame(t, 16*time.Millisecond), 1)
	if got >= 0 || got <= -400 {
		t.Fatalf("column 1 at %d on the first frame, want between 0 and -400", got)
	}
	if got := xOf(t, r.settle(t), 1); got != -400 {
		t.Fatalf("column 1 at %d settled, want -400", got)
	}
}

// Presses during a transition never send the view backward.
func TestChainedKeysNeverJumpBack(t *testing.T) {
	r := startSwipe(t, nil)
	fourColumns(t, r)
	r.key(t, "Right", ports.ModAlt)
	r.key(t, "Right", ports.ModAlt)
	prev := xOf(t, r.frame(t, 16*time.Millisecond), 1)
	r.key(t, "Right", ports.ModAlt)
	for i := range 40 {
		got := xOf(t, r.frame(t, 16*time.Millisecond), 1)
		if got > prev {
			t.Fatalf("frame %d: column 1 at %d after %d, the view went back", i, got, prev)
		}
		prev = got
		if got == -800 {
			return
		}
	}
	t.Fatalf("column 1 settled at %d, want -800", prev)
}

func TestWorkspaceKeyTransition(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	s := r.key(t, "Next", ports.ModAlt)[0]
	if got, ok := rectOf(s, 1); !ok || got.Y != 0 {
		t.Fatalf("workspace 1 at %+v (shown %t) right after the key, want the old position", got, ok)
	}
	s = r.frame(t, 16*time.Millisecond)
	got, ok := rectOf(s, 2)
	if !ok || got.Y <= 0 || got.Y >= 600 {
		t.Fatalf("workspace 2 at %+v (shown %t) on the first frame, want Y between 0 and 600", got, ok)
	}
	if got, _ := rectOf(r.settle(t), 2); got.Y != 0 {
		t.Fatalf("workspace 2 at %d settled, want 0", got.Y)
	}
}

// The workspace slide of a key keeps running when the next key goes on.
func TestChainedWorkspaceKeysKeepSliding(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	r.keySettled(t, "Next")
	r.keySettled(t, "Next")
	r.mapWindow(t, 3)
	r.keySettled(t, "Prior")
	r.keySettled(t, "Prior")
	r.key(t, "Next", ports.ModAlt)
	r.frame(t, 16*time.Millisecond)
	r.key(t, "Next", ports.ModAlt)
	if got, ok := rectOf(r.settle(t), 3); !ok || got.Y != 0 {
		t.Fatalf("workspace 3 at %+v (shown %t) settled", got, ok)
	}
}

func TestAnimationsOffKeysLandAtOnce(t *testing.T) {
	r := startSwipe(t, animationsOff)
	fourColumns(t, r)
	r.key(t, "Right", ports.ModAlt)
	if got := xOf(t, r.key(t, "Right", ports.ModAlt)[0], 1); got != -400 {
		t.Fatalf("column 1 at %d right after the key, want -400", got)
	}
	r.noFrameScene(t)

	r = startSwipe(t, animationsOff)
	r.workspaces(t)
	s := r.key(t, "Next", ports.ModAlt)[0]
	if got, ok := rectOf(s, 2); !ok || got.Y != 0 {
		t.Fatalf("workspace 2 at %+v (shown %t) right after the key, want the settled rect", got, ok)
	}
	r.noFrameScene(t)
}

// With the overview open before or after, nothing slides: opening it on a
// scrolled view starts no motion, and neither does a bind that moves the view
// while it is open.
func TestNoTransitionWithOverview(t *testing.T) {
	for _, tc := range []struct {
		name string
		// open is what the overview shows before the keys of the bind.
		sym  string
		mods ports.Mods
	}{
		{"open and close", "", 0},
		{"focus bind while open", "Left", ports.ModAlt},
		{"workspace bind while open", "Next", ports.ModAlt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startSwipe(t, nil)
			fourColumns(t, r)
			r.key(t, "Right", ports.ModAlt) // 2 is on screen: no scroll
			r.keySettled(t, "Right")        // the view scrolls away from 0
			r.keySettled(t, "Right")
			r.key(t, "o", ports.ModAlt)
			r.noFrameScene(t)
			if tc.sym != "" {
				r.key(t, tc.sym, tc.mods)
				r.noFrameScene(t)
			}
			r.key(t, "o", ports.ModAlt)
			r.noFrameScene(t)
		})
	}
}

// A workspace that goes to another monitor gets no rect or switch motion on
// either side.
func TestMovedWorkspaceHasNoMotion(t *testing.T) {
	r := startSwipe(t, nil)
	twoColumns(t, r)
	other := ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000}
	r.plug(t, other)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	r.noFrameScene(t, wide.Name, other.Name)
}

// rectNow is the rect of window id in s; it must be in the scene.
func rectNow(t *testing.T, s ports.Scene, id ports.WindowID) ports.Rect {
	t.Helper()
	r, ok := rectOf(s, id)
	if !ok {
		t.Fatalf("window %d not shown", id)
	}
	return r
}

// twoColumns maps two windows: 1 on the left half, 2 on the right, focused.
func twoColumns(t *testing.T, r *swipeRig) ports.Scene {
	t.Helper()
	r.mapWindow(t, 1)
	return r.mapWindow(t, 2)[0]
}

// Moving a column swaps two windows: the scene right after the key still
// shows the old rects, a frame an intermediate one, and the end the final.
func TestMoveColumnRectTransition(t *testing.T) {
	r := startSwipe(t, nil)
	twoColumns(t, r)
	s := r.key(t, "Left", ports.ModAlt|ports.ModShift)[0]
	if got1, got2 := rectNow(t, s, 1), rectNow(t, s, 2); got1.X != 0 || got2.X != 400 {
		t.Fatalf("right after the key 1 at %+v and 2 at %+v, want the old 0 and 400", got1, got2)
	}
	s = r.frame(t, 16*time.Millisecond)
	if got := rectNow(t, s, 1).X; got <= 0 || got >= 400 {
		t.Fatalf("window 1 at %d on the first frame, want between 0 and 400", got)
	}
	if got := rectNow(t, s, 2).X; got <= 0 || got >= 400 {
		t.Fatalf("window 2 at %d on the first frame, want between 0 and 400", got)
	}
	s = r.settle(t)
	if got1, got2 := rectNow(t, s, 1), rectNow(t, s, 2); got1.X != 400 || got2.X != 0 {
		t.Fatalf("settled 1 at %+v and 2 at %+v, want 400 and 0", got1, got2)
	}
}

// A move that also scrolls the view moves a window by the layout delta only:
// window 1 keeps its place on screen while the view and the others slide.
func TestMoveColumnThatScrollsKeepsCameraFrame(t *testing.T) {
	r := startSwipe(t, nil)
	fourColumns(t, r)
	r.key(t, "Right", ports.ModAlt|ports.ModShift)
	r.settle(t) // order 2 1 3 4, view 0
	s := r.key(t, "Right", ports.ModAlt|ports.ModShift)[0]
	// Order 2 3 1 4; the view scrolls by 400 to show column 1.
	for id, want := range map[ports.WindowID]int{2: 0, 1: 400, 3: 800} {
		if got := rectNow(t, s, id).X; got != want {
			t.Fatalf("window %d at %d right after the key, want the old %d", id, got, want)
		}
	}
	prev := 800
	for i := range 40 {
		s = r.frame(t, 16*time.Millisecond)
		if i == 39 {
			t.Fatal("still moving after 40 frames")
		}
		if got := rectNow(t, s, 1).X; got < 399 || got > 401 {
			t.Fatalf("frame %d: window 1 at %d, want it to keep 400 (the scroll is not a layout move)", i, got)
		}
		got := rectNow(t, s, 3).X
		if got > prev {
			t.Fatalf("frame %d: window 3 at %d after %d", i, got, prev)
		}
		if i == 0 && (got <= 0 || got >= 800) {
			t.Fatalf("window 3 at %d on the first frame, want between 0 and 800", got)
		}
		prev = got
		if got == 0 {
			break
		}
	}
	for id, want := range map[ports.WindowID]int{2: -400, 3: 0, 1: 400} {
		if got := rectNow(t, s, id).X; got != want {
			t.Fatalf("window %d at %d settled, want %d", id, got, want)
		}
	}
}

// A width change animates the frame while the client gets one configure to
// the final size.
func TestSetColumnWidthRectTransition(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Binds["Cmd+Ctrl+Right"] = "set-column-width +10%" })
	twoColumns(t, r)
	r.key(t, "Left", ports.ModAlt) // focus window 1: both columns fit, no scroll
	for len(r.commands) > 0 {
		<-r.commands
	}
	mods := ports.ModAlt | ports.ModCtrl
	s := r.key(t, "Right", mods)[0]
	if got := rectNow(t, s, 1); got.W != 400 {
		t.Fatalf("window 1 is %d wide right after the key, want the old 400", got.W)
	}
	if got := rectNow(t, s, 2); got.X != 400 {
		t.Fatalf("window 2 at %d right after the key, want the old 400", got.X)
	}
	s = r.frame(t, 16*time.Millisecond)
	if got := rectNow(t, s, 1).W; got <= 400 || got >= 480 {
		t.Fatalf("window 1 is %d wide on the first frame, want between 400 and 480", got)
	}
	if got := rectNow(t, s, 2).X; got <= 400 || got >= 480 {
		t.Fatalf("window 2 at %d on the first frame, want between 400 and 480", got)
	}
	s = r.settle(t)
	if got := rectNow(t, s, 1).W; got != 480 {
		t.Fatalf("window 1 is %d wide settled, want 480", got)
	}
	if got := rectNow(t, s, 2).X; got != 480 {
		t.Fatalf("window 2 at %d settled, want 480", got)
	}
	var widths []int
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.ConfigureWindow); ok && v.ID == 1 {
			widths = append(widths, v.Width)
		}
	}
	if len(widths) != 1 || widths[0] != 480 {
		t.Fatalf("configures of window 1 carried widths %v, want exactly [480]", widths)
	}
}

// Hit-testing follows what is drawn: a point only the shown rect covers
// reaches that window mid-transition.
func TestPointerHitsShownRectMidTransition(t *testing.T) {
	r := startSwipe(t, nil)
	twoColumns(t, r)
	// Window 2 (focused) moves to the left half, 1 to the right; drawn, 1
	// still holds the left half for now.
	s := r.key(t, "Left", ports.ModAlt|ports.ModShift)[0]
	if got := rectNow(t, s, 1).X; got != 0 {
		t.Fatalf("window 1 at %d right after the key, want the old 0", got)
	}
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
	s = scene(t, r.scenes)
	for _, w := range s.Windows {
		if w.Focused != (w.ID == 1) {
			t.Fatalf("window %d focused %t after a click on the shown window 1", w.ID, w.Focused)
		}
	}
	r.input <- ports.PointerButton{Button: 0x110}
}

// toggle-fullscreen grows the window to the output over the frames.
func TestFullscreenRectTransition(t *testing.T) {
	r := startSwipe(t, nil)
	twoColumns(t, r)
	s := r.key(t, "f", ports.ModAlt|ports.ModShift)[0]
	if got := rectNow(t, s, 2); got.W != 400 {
		t.Fatalf("window 2 is %d wide right after the key, want the old 400", got.W)
	}
	got := rectNow(t, r.frame(t, 16*time.Millisecond), 2)
	if got.W <= 400 || got.W >= 800 {
		t.Fatalf("window 2 is %d wide on the first frame, want between 400 and 800", got.W)
	}
	if got := rectNow(t, r.settle(t), 2); got != (ports.Rect{W: 800, H: 600}) {
		t.Fatalf("window 2 at %+v settled, want the output", got)
	}
}

// With animations off a layout change lands at once.
func TestAnimationsOffRectsLandAtOnce(t *testing.T) {
	r := startSwipe(t, animationsOff)
	twoColumns(t, r)
	s := r.key(t, "Left", ports.ModAlt|ports.ModShift)[0]
	if got1, got2 := rectNow(t, s, 1), rectNow(t, s, 2); got1.X != 400 || got2.X != 0 {
		t.Fatalf("1 at %+v and 2 at %+v right after the key, want the final 400 and 0", got1, got2)
	}
	r.noFrameScene(t)
}

// A second action during a rect transition starts from what is drawn: the
// scene right after it shows the rects of the last frame.
func TestChainedMovesStartFromShownRects(t *testing.T) {
	r := startSwipe(t, nil)
	twoColumns(t, r)
	r.key(t, "Left", ports.ModAlt|ports.ModShift)
	mid := r.frame(t, 16*time.Millisecond)
	s := r.key(t, "Right", ports.ModAlt|ports.ModShift)[0]
	for _, id := range []ports.WindowID{1, 2} {
		if got, want := rectNow(t, s, id), rectNow(t, mid, id); got != want {
			t.Fatalf("window %d at %+v right after the second key, want the shown %+v", id, got, want)
		}
	}
	end := r.settle(t)
	if got1, got2 := rectNow(t, end, 1), rectNow(t, end, 2); got1.X != 0 || got2.X != 400 {
		t.Fatalf("settled 1 at %+v and 2 at %+v, want 0 and 400", got1, got2)
	}
}

// A click on a column that is partly off screen focuses it: the scene right
// after the click still shows the old view, a frame an intermediate one and
// the settled scene the new one.
func TestClickFocusTransition(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Binds["Cmd+Ctrl+Right"] = "set-column-width +10%" })
	threeColumns(t, r)
	// Column 3 grows to 480: column 2 sticks out 80 px on the left.
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	if got := xOf(t, r.settle(t), 2); got != -80 {
		t.Fatalf("column 2 at %d, want -80", got)
	}
	// The release of the modifiers: a plain click, not a window drag.
	r.input <- ports.KeyEvent{Keysym: "Alt_L"}
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	r.drain()
	r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
	s := scene(t, r.scenes)
	if got := xOf(t, s, 2); got != -80 {
		t.Fatalf("column 2 at %d right after the click, want the old -80", got)
	}
	got := xOf(t, r.frame(t, 16*time.Millisecond), 2)
	if got <= -80 || got >= 0 {
		t.Fatalf("column 2 at %d on the first frame, want between -80 and 0", got)
	}
	if got := xOf(t, r.settle(t), 2); got != 0 {
		t.Fatalf("column 2 at %d settled, want 0", got)
	}
}

// A discrete swipe step (fixed overflow: nothing follows the fingers) moves
// through a rect motion: the scene right after the lift shows the old rects.
func TestDiscreteSwipeTransition(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" })
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.key(t, "f", ports.ModAlt) // maximize window 2
	if got := rectNow(t, r.settle(t), 2); got.W != 800 {
		t.Fatalf("window 2 is %d wide, want maximized 800", got.W)
	}
	r.begin()
	for range 10 {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: -40, Time: r.at}
	}
	s := r.end(t, false)
	if got := rectNow(t, s, 2); got.W != 800 {
		t.Fatalf("window 2 is %d wide right after the lift, want the old 800", got.W)
	}
	got := rectNow(t, r.frame(t, 16*time.Millisecond), 2)
	if got.W <= 400 || got.W >= 800 {
		t.Fatalf("window 2 is %d wide on the first frame, want between 400 and 800", got.W)
	}
	s = r.settle(t)
	if got1, got2 := rectNow(t, s, 1), rectNow(t, s, 2); got1.W != 400 || got2.X != 400 || got2.W != 400 {
		t.Fatalf("settled 1 at %+v and 2 at %+v, want 1 beside 2 on halves", got1, got2)
	}
}
