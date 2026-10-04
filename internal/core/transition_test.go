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

// With the overview open before or after, nothing slides.
func TestNoTransitionWithOverview(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	r.key(t, "o", ports.ModAlt)
	r.key(t, "o", ports.ModAlt)
	r.noFrameScene(t)
}

// A workspace that goes to another monitor gets no motion on either side.
func TestMovedWorkspaceHasNoMotion(t *testing.T) {
	r := startSwipe(t, nil)
	r.mapWindow(t, 1)
	r.plug(t, ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000})
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	r.noFrameScene(t)
}
