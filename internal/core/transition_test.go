package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/core"
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

// The overview has no camera: opening it on a scrolled view, a bind that
// moves the selection and closing it only move the cards (rect motions), which
// settle: no spring runs afterwards and nothing slides the view.
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
			// settleAll: a bind that moves nothing starts no spring.
			r.key(t, "o", ports.ModAlt)
			r.settleAll(t, wide.Name)
			r.noFrameScene(t)
			if tc.sym != "" {
				r.key(t, tc.sym, tc.mods)
				r.settleAll(t, wide.Name)
				r.noFrameScene(t)
			}
			r.key(t, "o", ports.ModAlt)
			r.settleAll(t, wide.Name)
			r.noFrameScene(t)
		})
	}
}

// movedRig plugs a second 800x600 output right of the first.
func movedRig(t *testing.T, edit func(*ports.Config)) (*swipeRig, ports.OutputInfo) {
	t.Helper()
	other := ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000}
	return startSwipeOn(t, edit, wide, other), other
}

func outScene(t *testing.T, set []ports.Scene, output string) ports.Scene {
	t.Helper()
	for _, s := range set {
		if s.Output == output {
			return s
		}
	}
	t.Fatalf("no scene for %s", output)
	return ports.Scene{}
}

// A workspace that goes to another monitor slides in on it from where the
// source drew it, in the destination's coordinates, and settles.
func TestMovedWorkspaceSlidesInOnDestination(t *testing.T) {
	r, other := movedRig(t, nil)
	twoColumns(t, r)
	set := r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	dst := outScene(t, set, other.Name)
	// DP-2 starts at x=800: the source's 0 and 400 are -800 and -400 there.
	if got1, got2 := rectNow(t, dst, 1), rectNow(t, dst, 2); got1.X != -800 || got2.X != -400 {
		t.Fatalf("right after the key 1 at %+v and 2 at %+v, want the source's rects (-800, -400)", got1, got2)
	}
	set, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, other.Name)
	if !ok {
		t.Fatal("no frame while the windows slide")
	}
	dst = outScene(t, set, other.Name)
	for id, end := range map[ports.WindowID]int{1: 0, 2: 400} {
		if got := rectNow(t, dst, id).X; got <= end-800 || got >= end {
			t.Fatalf("window %d at %d on the first frame, want between %d and %d", id, got, end-800, end)
		}
	}
	set, _ = r.settleAll(t, wide.Name, other.Name)
	dst = outScene(t, set, other.Name)
	if got1, got2 := rectNow(t, dst, 1), rectNow(t, dst, 2); got1.X != 0 || got2.X != 400 {
		t.Fatalf("settled 1 at %+v and 2 at %+v, want 0 and 400", got1, got2)
	}
	r.noFrameScene(t, wide.Name, other.Name)
}

// The source screen slides to the workspace it falls back on when that one
// was in the list it showed, else it changes at once.
func TestMovedWorkspaceSourceSwitchRule(t *testing.T) {
	r, other := movedRig(t, nil)
	r.workspaces(t) // window 1 on the first workspace, 2 on the second
	set := r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	src := outScene(t, set, wide.Name)
	// The second workspace was below the moved one: it slides up from there.
	if got := rectNow(t, src, 2).Y; got != 600 {
		t.Fatalf("source shows window 2 at y=%d right after the key, want 600 (sliding in)", got)
	}
	set, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, other.Name)
	if !ok {
		t.Fatal("no frame while the source slides")
	}
	if got := rectNow(t, outScene(t, set, wide.Name), 2).Y; got <= 0 || got >= 600 {
		t.Fatalf("window 2 at y=%d on the first frame, want between 0 and 600", got)
	}
	set, _ = r.settleAll(t, wide.Name, other.Name)
	if got := rectNow(t, outScene(t, set, wide.Name), 2); got.Y != 0 {
		t.Fatalf("settled window 2 at %+v, want y=0", got)
	}
}

// A source whose new workspace was not in the list it showed (the moved one
// was alone: a fresh empty one replaces it) changes at once: nothing slides
// in, only the destination's windows move.
func TestMovedWorkspaceSourceWithoutNeighbourIsInstant(t *testing.T) {
	r, other := movedRig(t, nil)
	twoColumns(t, r)
	set := r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	if n := len(outScene(t, set, wide.Name).Windows); n != 0 {
		t.Fatalf("source draws %d windows right after the key, want none", n)
	}
	set, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, other.Name)
	if !ok {
		t.Fatal("no frame while the windows slide")
	}
	if n := len(outScene(t, set, wide.Name).Windows); n != 0 {
		t.Fatalf("source draws %d windows on the first frame, want none", n)
	}
}

// With animations off the moved workspace lands at once on both screens.
func TestMovedWorkspaceOffIsInstant(t *testing.T) {
	r, other := movedRig(t, animationsOff)
	twoColumns(t, r)
	set := r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	dst := outScene(t, set, other.Name)
	if got1, got2 := rectNow(t, dst, 1), rectNow(t, dst, 2); got1.X != 0 || got2.X != 400 {
		t.Fatalf("1 at %+v and 2 at %+v right after the key, want the settled rects", got1, got2)
	}
	r.noFrameScene(t, wide.Name, other.Name)
}

// A workspace that comes back to the screen it left within the spring of
// its first move slides its windows only: the screen's running workspace
// slide (its list still holds the workspace) must not start a vertical one.
func TestMovedBackWorkspaceHasNoSwitchSlide(t *testing.T) {
	r, other := movedRig(t, nil)
	twoColumns(t, r)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	// Within the first spring: the source is still sliding.
	if _, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, other.Name); !ok {
		t.Fatal("no frame while the windows slide")
	}
	set := r.key(t, "Left", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	dst := outScene(t, set, wide.Name)
	for id := ports.WindowID(1); id <= 2; id++ {
		if got := rectNow(t, dst, id); got.Y != 0 {
			t.Fatalf("window %d at %+v right after moving back, want y=0 (no vertical slide)", id, got)
		}
	}
	set, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, other.Name)
	if !ok {
		t.Fatal("no frame while the windows slide back")
	}
	dst = outScene(t, set, wide.Name)
	for id := ports.WindowID(1); id <= 2; id++ {
		if got := rectNow(t, dst, id); got.Y != 0 {
			t.Fatalf("window %d at %+v on the first frame, want y=0 (rects slide only)", id, got)
		}
	}
	set, _ = r.settleAll(t, wide.Name, other.Name)
	dst = outScene(t, set, wide.Name)
	if got1, got2 := rectNow(t, dst, 1), rectNow(t, dst, 2); got1 != (ports.Rect{W: 400, H: 600}) || got2 != (ports.Rect{X: 400, W: 400, H: 600}) {
		t.Fatalf("settled 1 at %+v and 2 at %+v", got1, got2)
	}
	r.noFrameScene(t, wide.Name, other.Name)
}

// The start rects are the source's translated by the difference of the
// outputs' positions on both axes, with the source's size; they settle on
// the destination's layout.
func TestMovedWorkspaceStartsFromSourceGeometry(t *testing.T) {
	big := ports.OutputInfo{Name: "DP-2", Width: 1024, Height: 768, RefreshMilli: 60000}
	for _, tc := range []struct {
		name         string
		dst          ports.OutputInfo
		anchor       ports.OutputAnchor
		key          string
		start, final [2]ports.Rect
	}{
		{
			name: "right same size", dst: ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000},
			anchor: ports.OutputAnchor{Relation: ports.RelationRightOf, To: "DP-1"}, key: "Right",
			start: [2]ports.Rect{{X: -800, W: 400, H: 600}, {X: -400, W: 400, H: 600}},
			final: [2]ports.Rect{{W: 400, H: 600}, {X: 400, W: 400, H: 600}},
		},
		{
			name: "below larger", dst: big,
			anchor: ports.OutputAnchor{Relation: ports.RelationBelow, To: "DP-1"}, key: "Down",
			start: [2]ports.Rect{{Y: -600, W: 400, H: 600}, {X: 400, Y: -600, W: 400, H: 600}},
			final: [2]ports.Rect{{W: 512, H: 768}, {X: 512, W: 512, H: 768}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startSwipeOn(t, func(c *ports.Config) {
				c.Outputs = []ports.OutputConfig{{Name: "DP-1"}, {Name: "DP-2", Anchor: tc.anchor}}
				c.Binds["Cmd+Ctrl+Shift+Down"] = string(core.ActionMoveWorkspaceToMonitorDown)
			}, wide, tc.dst)
			twoColumns(t, r)
			dst := outScene(t, r.key(t, tc.key, ports.ModAlt|ports.ModCtrl|ports.ModShift), tc.dst.Name)
			for i, id := range []ports.WindowID{1, 2} {
				if got := rectNow(t, dst, id); got != tc.start[i] {
					t.Fatalf("window %d starts at %+v, want %+v", id, got, tc.start[i])
				}
			}
			set, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, tc.dst.Name)
			if !ok {
				t.Fatal("no frame while the windows slide")
			}
			if got := rectNow(t, outScene(t, set, tc.dst.Name), 1); got == tc.start[0] || got == tc.final[0] {
				t.Fatalf("window 1 at %+v on the first frame, want it between start and final", got)
			}
			set, _ = r.settleAll(t, wide.Name, tc.dst.Name)
			dst = outScene(t, set, tc.dst.Name)
			for i, id := range []ports.WindowID{1, 2} {
				if got := rectNow(t, dst, id); got != tc.final[i] {
					t.Fatalf("window %d settles at %+v, want %+v", id, got, tc.final[i])
				}
			}
		})
	}
}

// A named workspace pulled to the focused screen from the one showing it
// moves like a monitor move: its windows slide in from the source.
func TestPulledNamedWorkspaceSlidesIn(t *testing.T) {
	r, other := movedRig(t, func(c *ports.Config) {
		c.Workspaces = []ports.WorkspaceConfig{{Name: "web"}}
		c.Binds["Alt+w"] = "workspace web"
	})
	// web shows on DP-2 with a window, then DP-1 pulls it.
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.key(t, "w", ports.ModAlt)
	r.settleAll(t, wide.Name, other.Name)
	r.mapWindow(t, 1)
	r.settleAll(t, wide.Name, other.Name)
	r.key(t, "Left", ports.ModAlt|ports.ModCtrl)
	set := r.key(t, "w", ports.ModAlt)
	if got := rectNow(t, outScene(t, set, wide.Name), 1); got.X != 800 {
		t.Fatalf("window 1 at %+v right after the pull, want x=800 (where DP-2 drew it)", got)
	}
	set, ok := r.clk.flip(t, r.frames, r.scenes, 16*time.Millisecond, wide.Name, other.Name)
	if !ok {
		t.Fatal("no frame while the window slides")
	}
	if got := rectNow(t, outScene(t, set, wide.Name), 1).X; got <= 0 || got >= 800 {
		t.Fatalf("window 1 at x=%d on the first frame, want between 0 and 800", got)
	}
	set, _ = r.settleAll(t, wide.Name, other.Name)
	if got := rectNow(t, outScene(t, set, wide.Name), 1); got != (ports.Rect{W: 800, H: 600}) {
		t.Fatalf("settled at %+v", got)
	}
}

// Focusing another monitor moves no window: it is not animated.
func TestFocusMonitorHasNoMotion(t *testing.T) {
	r, other := movedRig(t, nil)
	twoColumns(t, r)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
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

// A window unmapped while its rect motion runs stops moving: with
// animations on it stays in the scene only as a leaving entry (drawn where
// it was, fading out and shrinking, hidden to input), the others keep their
// motions, and once everything settles it is gone and no frame is wanted:
// a motion kept for a gone window would ask for frames with nothing to
// move. With animations off it leaves the scene at once.
func TestUnmapMidRectMotion(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*ports.Config)
	}{{"on", nil}, {"off", animationsOff}} {
		t.Run(tt.name, func(t *testing.T) {
			r := startSwipe(t, tt.edit)
			threeColumns(t, r)
			// 3, focused, swaps with 2: both move.
			mid := r.key(t, "Left", ports.ModAlt|ports.ModShift)[0]
			if tt.edit == nil {
				// Off: the swap is instant, a flip draws no new scene.
				mid = r.frame(t, 16*time.Millisecond)
				if x := rectNow(t, mid, 3).X; x <= 0 || x >= 400 {
					t.Fatalf("window 3 at %d on the first frame, want it moving", x)
				}
			}
			moving := rectNow(t, mid, 2).X
			drawn3 := rectNow(t, mid, 3)
			r.client <- ports.WindowUnmapped{ID: 3}
			s := scene(t, r.scenes)
			w3, shown3 := listedIn(s, 3)
			switch {
			case tt.edit != nil && shown3:
				t.Fatalf("the unmapped window is still shown with animations off: %+v", w3)
			case tt.edit == nil && (!shown3 || w3.Fade != 0 || w3.Rect != drawn3):
				// It leaves from where it was drawn, not from the settled
				// slot it was moving to.
				t.Fatalf("the unmapped window right after the unmap: %+v (shown %t), want it leaving from %+v", w3, shown3, drawn3)
			}
			// The neighbour's own motion carries on from where it was drawn.
			if got := rectNow(t, s, 2).X; got != moving {
				t.Fatalf("window 2 jumped from %d to %d on the unmap", moving, got)
			}
			if tt.edit == nil {
				next := r.frame(t, 16*time.Millisecond)
				if got := rectNow(t, next, 2).X; got == moving {
					t.Fatalf("window 2 stopped at %d: its motion died with the unmapped window", got)
				}
				if w, ok := listedIn(next, 3); !ok || !(w.Fade > 0 && w.Fade < 1) || w.Rect.W >= 400 {
					t.Fatalf("the leaving window on the next frame: %+v (shown %t), want it fading and shrinking", w, ok)
				}
			} else if moving != 400 {
				t.Fatalf("window 2 at %d with animations off, want 400", moving)
			}
			if tt.edit == nil {
				s = r.settle(t)
			}
			if _, ok := listedIn(s, 3); ok {
				t.Fatal("the unmapped window came back")
			}
			if got := rectNow(t, s, 2).X; got != 400 {
				t.Fatalf("window 2 settled at %d, want 400", got)
			}
			r.noFrameScene(t)
		})
	}
}

// listedIn is the scene window id, shown or not.
func listedIn(s ports.Scene, id ports.WindowID) (ports.SceneWindow, bool) {
	for _, w := range s.Windows {
		if w.ID == id {
			return w, true
		}
	}
	return ports.SceneWindow{}, false
}
