package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func animationsOff(c *ports.Config) { c.Animations.On = false }

// stillMoves sends n swipe updates of (dx, dy) and expects no scene: with
// animations off the view does not follow the fingers.
func (r *swipeRig) stillMoves(t *testing.T, n int, dx, dy float64) {
	t.Helper()
	for range n {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: dx, DY: dy, Time: r.at}
	}
	if s, ok := r.published(); ok {
		t.Fatalf("a swipe update published a scene (%d windows): the view follows the fingers", len(s))
	}
}

// published reports the scene set core published for the events sent so
// far, if any. A flip that names no output is a barrier: core handles its
// events in order and publishes before taking the next one, so once the
// send returns every earlier event is handled.
func (r *swipeRig) published() ([]ports.Scene, bool) {
	send(r.frames, ports.OutputFrame{Output: "sync"})
	select {
	case s := <-r.scenes:
		return s, true
	default:
		return nil, false
	}
}

func focusedID(s ports.Scene) ports.WindowID {
	for _, w := range s.Windows {
		if w.Focused {
			return w.ID
		}
	}
	return 0
}

func TestAnimationsOffColumnSwipeFocusesAtOnce(t *testing.T) {
	r := startSwipe(t, animationsOff)
	before := threeColumns(t, r)
	r.begin()
	r.stillMoves(t, 10, -40, 0)
	s := r.end(t, false)
	if got := focusedID(s); got != 2 {
		t.Fatalf("focused %d right after the lift, want 2", got)
	}
	if got, _ := rectOf(before, 3); got.X == 0 {
		t.Fatalf("bad setup: column 3 at %+v", got)
	}
	r.noFrameScene(t)
}

func TestAnimationsOffWorkspaceSwipeLandsAtOnce(t *testing.T) {
	r := startSwipe(t, animationsOff)
	r.workspaces(t)
	r.begin()
	r.stillMoves(t, 9, 0, 30)
	s := r.end(t, false)
	if _, ok := rectOf(s, 1); ok {
		t.Fatal("old workspace still shown right after the lift")
	}
	if got, ok := rectOf(s, 2); !ok || got.Y != 0 {
		t.Fatalf("workspace 2 at %+v (shown %t), want the settled rect", got, ok)
	}
}

func TestAnimationsOffSlowSwipeStays(t *testing.T) {
	r := startSwipe(t, animationsOff)
	before := threeColumns(t, r)
	r.begin()
	r.stillMoves(t, 2, -10, 0)
	r.input <- ports.SwipeEnd{Time: r.at + time.Second}
	if s := scene(t, r.scenes); !s.SameAs(before) {
		t.Fatalf("a short slow swipe changed the scene: %+v, was %+v", s.Windows, before.Windows)
	}
	r.noFrameScene(t)
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
func (r *swipeRig) noFrameScene(t *testing.T, outputs ...string) {
	t.Helper()
	if len(outputs) == 0 {
		outputs = []string{wide.Name}
	}
	for range 2 {
		r.advance(16 * time.Millisecond)
		for _, o := range outputs {
			r.frames <- ports.OutputFrame{Output: o}
		}
	}
	if s, ok := r.published(); ok {
		t.Fatalf("a flip published a scene (%d windows): a spring still runs", len(s))
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
