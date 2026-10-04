package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func animationsOff(c *ports.Config) { c.Animations.On = false }

func TestAnimationsOffFlickLandsAtOnce(t *testing.T) {
	r := startSwipe(t, animationsOff)
	threeColumns(t, r)
	s := r.flick(t, 10, -40, 0)
	if got, ok := rectOf(s, 1); !ok || got.X != 0 {
		t.Fatalf("column 1 at %+v (shown %t) right after the lift, want the settled rect", got, ok)
	}
	r.noFrameScene(t)
}

func TestAnimationsOffWorkspaceSwipeLandsAtOnce(t *testing.T) {
	r := startSwipe(t, animationsOff)
	r.workspaces(t)
	r.begin()
	for range 9 {
		r.move(t, 0, 30)
	}
	s := r.end(t, false)
	if _, ok := rectOf(s, 1); ok {
		t.Fatal("old workspace still shown right after the lift")
	}
	if got, ok := rectOf(s, 2); !ok || got.Y != 0 {
		t.Fatalf("workspace 2 at %+v (shown %t), want the settled rect", got, ok)
	}
}

func TestAnimationsOffFingersStillFollowed(t *testing.T) {
	r := startSwipe(t, animationsOff)
	before, _ := rectOf(threeColumns(t, r), 2)
	r.begin()
	r.input <- ports.SwipeUpdate{DX: -10, Time: r.at}
	r.move(t, -10, 0)
	s := r.move(t, -290, 0)
	if got, _ := rectOf(s, 2); got.X-before.X != 200 {
		t.Fatalf("column moved %d px with the fingers, want 200", got.X-before.X)
	}
}

func TestReloadToOffSettlesALanding(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	s := r.flick(t, 10, -40, 0)
	if got, _ := rectOf(s, 1); got.X == 0 {
		t.Fatal("no landing slide to interrupt")
	}
	cfg := r.cfg
	cfg.Animations.On = false
	r.reload <- ports.ConfigChanged{Config: cfg}
	s = scene(t, r.scenes)
	if got, _ := rectOf(s, 1); got.X != 0 {
		t.Fatalf("column 1 at %d after the reload, want the settled rect", got.X)
	}
	r.noFrameScene(t)
}

// noFrameScene sends page flips and expects no scene: no spring runs, so
// the flips have nothing to move and publish nothing.
func (r *swipeRig) noFrameScene(t *testing.T) {
	t.Helper()
	for range 2 {
		r.advance(16 * time.Millisecond)
		r.frames <- ports.OutputFrame{Output: wide.Name}
	}
	select {
	case s := <-r.scenes:
		t.Fatalf("a flip published a scene (%d windows): a spring still runs", len(s))
	case <-time.After(50 * time.Millisecond):
	}
}

// landing flicks left and returns the rect of column 1 after each given
// time since the lift.
func landing(t *testing.T, slowdown float64, after ...time.Duration) []int {
	t.Helper()
	r := startSwipe(t, func(c *ports.Config) { c.Animations.Slowdown = slowdown })
	threeColumns(t, r)
	r.flick(t, 10, -40, 0)
	var xs []int
	var at time.Duration
	for _, d := range after {
		got, _ := rectOf(r.frame(t, d-at), 1)
		xs = append(xs, got.X)
		at = d
	}
	return xs
}

func TestSlowdownStretchesALanding(t *testing.T) {
	// Critically damped at stiffness 800: settled after about 330 ms, 660 ms
	// with slowdown 2 (the exact doubling is checked on the motion).
	one := landing(t, 1, 100*time.Millisecond, 400*time.Millisecond)
	two := landing(t, 2, 100*time.Millisecond, 400*time.Millisecond, 700*time.Millisecond)
	if one[0] == 0 || two[0] == 0 {
		t.Fatalf("landing does not move after 100 ms: %v %v", one, two)
	}
	if one[1] != 0 || two[1] == 0 {
		t.Fatalf("at 400 ms: slowdown 1 at %d, want settled; slowdown 2 at %d, want moving", one[1], two[1])
	}
	if two[2] != 0 {
		t.Fatalf("slowdown 2 does not settle by 700 ms: %v", two)
	}
}
