package motion

import (
	"math"
	"testing"
	"testing/synctest"
	"time"
)

// workspaceSwipeMovement is the test gesture distance for one workspace.
const workspaceSwipeMovement = 300.0

func TestSwipeTrackerVelocity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr swipeTracker
		for i := range 11 {
			tr.push(10, time.Duration(i)*10*time.Millisecond)
		}
		// 150 ms of history: 16 pushes would fit, 11 span 100 ms and 110 units.
		if got := tr.velocity(); math.Abs(got-1100) > 1e-9 {
			t.Fatalf("velocity %v, want 1100", got)
		}
		if tr.pos != 110 {
			t.Fatalf("pos %v", tr.pos)
		}
		// A flick lands further than where the fingers left it.
		if end := tr.projectedEnd(); end <= tr.pos {
			t.Fatalf("projected %v not past %v", end, tr.pos)
		}

	})
}

func TestSwipeTrackerForgetsOldMovement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr swipeTracker
		tr.push(500, 0)
		tr.push(1, 10*time.Millisecond)
		// The fingers rested before lifting: the old flick no longer counts.
		tr.push(0, 400*time.Millisecond)
		if v := tr.velocity(); v != 0 {
			t.Fatalf("velocity %v after a pause", v)
		}
		if tr.projectedEnd() != tr.pos {
			t.Fatal("a still swipe lands where it is")
		}

	})
}

func TestSwipeTrackerIgnoresEarlierEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr swipeTracker
		tr.push(5, 20*time.Millisecond)
		tr.push(5, 10*time.Millisecond)
		if tr.pos != 5 || len(tr.history) != 1 {
			t.Fatalf("pos %v history %d", tr.pos, len(tr.history))
		}

	})
}

func TestSpringSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := ViewSpring(-300, 0)
		if v := s.valueAt(0); math.Abs(v+300) > 1e-9 {
			t.Fatalf("start %v", v)
		}
		d := s.duration()
		if d <= 0 || d > 2*time.Second {
			t.Fatalf("duration %v", d)
		}
		if v := s.valueAt(d); math.Abs(v) > 0.5 {
			t.Fatalf("end %v", v)
		}
		// Critically damped: it never passes its target.
		for at := time.Duration(0); at < d; at += time.Millisecond {
			if v := s.valueAt(at); v > 1e-9 {
				t.Fatalf("overshoot %v at %v", v, at)
			}
		}

	})
}

func TestSpringKeepsVelocity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A spring launched toward its target moves faster at first.
		still, flung := ViewSpring(-300, 0), ViewSpring(-300, 3000)
		at := 10 * time.Millisecond
		if still.valueAt(at) >= flung.valueAt(at) {
			t.Fatal("initial velocity ignored")
		}

	})
}

func TestSpringOverdamped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := Spring{From: 0, To: 1, DampingRatio: 6, Stiffness: 1200, Epsilon: 0.0001}
		d := s.duration()
		if v := s.valueAt(d); math.IsNaN(v) || math.Abs(v-1) > 0.01 {
			t.Fatalf("value %v at %v", v, d)
		}
		if (Spring{From: 0, To: 0, DampingRatio: 1.15, Stiffness: 850, Epsilon: 0.0001}).duration() != 0 {
			t.Fatal("a spring at rest takes time")
		}

	})
}

func TestMotionEndsOnTarget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(10, 0)
		m := NewMotion(WorkspaceSpring(0.4, 0), start, 1)
		if v, done := m.At(start); done || v != 0.4 {
			t.Fatalf("start %v %t", v, done)
		}
		if v, done := m.At(start.Add(10 * time.Second)); !done || v != 0 {
			t.Fatalf("end %v %t", v, done)
		}

	})
}

func TestRubberBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := WorkspaceBand
		if r.clamp(0, 2, 1.5) != 1.5 {
			t.Fatal("inside moves freely")
		}
		past := r.clamp(0, 2, 3)
		if past <= 2 || past >= 2+r.limit {
			t.Fatalf("past the end %v", past)
		}
		if before := r.clamp(0, 2, -10); before >= 0 || before <= -r.limit {
			t.Fatalf("before the start %v", before)
		}
		if r.clampDerivative(0, 2, 1) != 1 || r.clampDerivative(0, 2, 3) >= 1 {
			t.Fatal("derivative")
		}

	})
}

// swipe pushes n updates of delta, 8 ms apart, from time 0.
func swipe(s *SnapSwipe, n int, delta float64) time.Duration {
	at := time.Duration(0)
	for range n {
		at += 8 * time.Millisecond
		s.Push(delta, at)
	}
	return at
}

// slowSwipe pushes dist over two seconds and rests before the lift.
func slowSwipe(s *SnapSwipe, dist float64) time.Duration {
	at := time.Duration(0)
	for range 40 {
		at += 50 * time.Millisecond
		s.Push(dist/40, at)
	}
	return at + 200*time.Millisecond
}

func workspaceSnap(start float64) SnapSwipe {
	return NewSnapSwipe(start, math.Round(start), 1/workspaceSwipeMovement, IndexPoints(5), WorkspaceBand)
}

// One swipe reaches the next workspace at most, however fast or far: a
// slow one settles on the closest, back unless past halfway; a cancelled
// one returns.
func TestSnapSwipeOneStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			push      func(*SnapSwipe) time.Duration
			cancelled bool
			want      float64
		}{
			{"quick flick down", func(s *SnapSwipe) time.Duration { return swipe(s, 6, 30) }, false, 3},
			{"quick flick up", func(s *SnapSwipe) time.Duration { return swipe(s, 6, -30) }, false, 1},
			{"hard long flick", func(s *SnapSwipe) time.Duration { return swipe(s, 30, 60) }, false, 3},
			{"short quick flick", func(s *SnapSwipe) time.Duration { return swipe(s, 3, 20) }, false, 3},
			{"slow short", func(s *SnapSwipe) time.Duration { return slowSwipe(s, 100) }, false, 2},
			{"slow past half", func(s *SnapSwipe) time.Duration { return slowSwipe(s, 200) }, false, 3},
			{"slow far up", func(s *SnapSwipe) time.Duration { return slowSwipe(s, -900) }, false, 1},
			{"cancelled", func(s *SnapSwipe) time.Duration { return swipe(s, 10, 30) }, true, 2},
			// Most of the way down, then a hard flick back: one step up.
			{"forward then hard back", func(s *SnapSwipe) time.Duration {
				at := slowSwipe(s, 250)
				for range 4 {
					at += 8 * time.Millisecond
					s.Push(-30, at)
				}
				return at
			}, false, 1},
		} {
			s := workspaceSnap(2)
			at := tc.push(&s)
			if p := s.Pos(); p < 1-WorkspaceBand.limit || p > 3+WorkspaceBand.limit {
				t.Errorf("%s: view %v went past the neighbors", tc.name, p)
			}
			if got, _ := s.End(tc.cancelled, at); got != tc.want {
				t.Errorf("%s: landed on %v, want %v", tc.name, got, tc.want)
			}
		}

	})
}

// A swipe caught during a landing steps from the landing: one more.
func TestSnapSwipeDuringLanding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewSnapSwipe(2.6, 3, 1/workspaceSwipeMovement, IndexPoints(5), WorkspaceBand)
		at := swipe(&s, 6, 30)
		if p := s.Pos(); p > 4+WorkspaceBand.limit {
			t.Fatalf("view %v went past 4", p)
		}
		if got, _ := s.End(false, at); got != 4 {
			t.Fatalf("landed on %v, want 4", got)
		}

	})
}

// From a view between points (a merged column edge) a slow swipe never
// settles behind where it started; a quick one reaches the points around.
func TestSnapSwipeRestBetweenPoints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name string
			push func(*SnapSwipe) time.Duration
			want float64
		}{
			{"slow", func(s *SnapSwipe) time.Duration { return slowSwipe(s, 5) }, 640},
			{"quick back", func(s *SnapSwipe) time.Duration { return swipe(s, 4, -40) }, 600},
			{"quick on", func(s *SnapSwipe) time.Duration { return swipe(s, 4, 40) }, 1000},
		} {
			s := NewSnapSwipe(640, 640, 1, []float64{0, 600, 1000}, WorkspaceBand.Scaled(1000))
			at := tc.push(&s)
			if got, _ := s.End(false, at); got != tc.want {
				t.Errorf("%s: landed on %v, want %v", tc.name, got, tc.want)
			}
		}

	})
}

// A swipe caught while sliding to a far workspace lands on that one or
// its neighbors, never back where the slide shows.
func TestSnapSwipeCaughtFarFromRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, delta := range []float64{-60, 60} {
			s := NewSnapSwipe(0.5, 4, 1/workspaceSwipeMovement, IndexPoints(6), WorkspaceBand)
			at := swipe(&s, 20, delta)
			if got, _ := s.End(false, at); got < 3 || got > 5 {
				t.Errorf("delta %v: landed on %v, want 3 to 5", delta, got)
			}
		}

	})
}

// The view follows the fingers in their direction, without jumps, from a
// start on a point, between points, or caught past the edge (a spring back
// from the rubber band); near a point it sticks, a full step shows whole.
func TestSnapSwipeDetents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, start := range []float64{2, 2.4, -0.03, 4.03} {
			s := workspaceSnap(start)
			if p := s.Pos(); math.Abs(p-start) > 1e-9 {
				t.Fatalf("start %v shows %v", start, p)
			}
			for _, dir := range []float64{1, -1} {
				s := workspaceSnap(start)
				prev := s.Pos()
				for i := range 400 {
					s.Push(dir, time.Duration(i)*time.Millisecond)
					p := s.Pos()
					if (p-prev)*dir < 0 || math.Abs(p-prev) > 0.01 {
						t.Fatalf("start %v dir %v: view %v then %v at %d", start, dir, prev, p, i)
					}
					prev = p
				}
			}
		}
		s := workspaceSnap(2)
		s.Push(300, 0)
		if p := s.Pos(); math.Abs(p-3) > 1e-9 {
			t.Fatalf("a full step shows %v", p)
		}
		s = workspaceSnap(2)
		s.Push(30, 0)
		if p := s.Pos(); p >= 2.1 || p <= 2 {
			t.Fatalf("near a point the view moved to %v for 0.1", p)
		}

	})
}

// One workspace: nowhere to go, the view resists and stays.
func TestSnapSwipeSinglePoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewSnapSwipe(0, 0, 1/workspaceSwipeMovement, IndexPoints(1), WorkspaceBand)
		at := swipe(&s, 10, 40)
		if p := s.Pos(); p <= 0 || p > WorkspaceBand.limit {
			t.Fatalf("view %v", p)
		}
		target, velocity := s.End(false, at)
		if target != 0 || math.IsNaN(velocity) {
			t.Fatalf("landed on %v at %v", target, velocity)
		}

	})
}

func TestStepSwipe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			push      func(*SnapSwipe) time.Duration
			cancelled bool
			want      int
		}{
			{"quick back", func(s *SnapSwipe) time.Duration { return swipe(s, 3, -40) }, false, -1},
			{"quick on", func(s *SnapSwipe) time.Duration { return swipe(s, 3, 40) }, false, 1},
			{"hard", func(s *SnapSwipe) time.Duration { return swipe(s, 30, 80) }, false, 1},
			{"slow short", func(s *SnapSwipe) time.Duration { return slowSwipe(s, -100) }, false, 0},
			{"slow past half", func(s *SnapSwipe) time.Duration { return slowSwipe(s, -200) }, false, -1},
			{"cancelled", func(s *SnapSwipe) time.Duration { return swipe(s, 3, 40) }, true, 0},
		} {
			s := NewStepSwipe()
			at := tc.push(&s)
			target, v := s.End(tc.cancelled, at)
			if got := int(math.Round(target)); got != tc.want {
				t.Errorf("%s: step %d, want %d", tc.name, got, tc.want)
			}
			if math.IsNaN(v) {
				t.Errorf("%s: velocity NaN", tc.name)
			}
		}

	})
}

func TestSpringVelocityAt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		springs := map[string]Spring{
			"critical": {From: -300, DampingRatio: 1, Stiffness: 800, Epsilon: 0.0001},
			"under":    {From: -300, DampingRatio: 0.5, Stiffness: 800, Epsilon: 0.0001},
			"over":     {From: -300, DampingRatio: 6, Stiffness: 1200, Epsilon: 0.0001},
		}
		const h = time.Microsecond
		for name, s := range springs {
			for _, v0 := range []float64{0, 3000} {
				s := s
				s.Velocity = v0
				if got := s.velocityAt(0); math.Abs(got-v0) > 1e-9*max(1, math.Abs(v0)) {
					t.Errorf("%s v0=%v: velocityAt(0) = %v", name, v0, got)
				}
				for _, at := range []time.Duration{5, 50, 200} {
					at *= time.Millisecond
					want := (s.valueAt(at+h) - s.valueAt(at-h)) / (2 * h.Seconds())
					got := s.velocityAt(at)
					if math.Abs(got-want) > 1e-3*max(1, math.Abs(want)) {
						t.Errorf("%s v0=%v at %v: velocityAt = %v, derivative %v", name, v0, at, got, want)
					}
				}
			}
		}

	})
}

func TestMotionSlowdownScalesTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(10, 0)
		s := ViewSpring(-300, 0)
		one, two := NewMotion(s, start, 1), NewMotion(s, start, 2)
		if two.end != 2*one.end {
			t.Fatalf("end %v, want %v", two.end, 2*one.end)
		}
		for _, d := range []time.Duration{5 * time.Millisecond, 40 * time.Millisecond, 150 * time.Millisecond} {
			a, _ := one.At(start.Add(d))
			b, _ := two.At(start.Add(2 * d))
			if math.Abs(a-b) > 1e-9 {
				t.Errorf("at %v: %v vs %v", d, a, b)
			}
		}
		if m := NewMotion(s, start, 0); m.Slow != 1 || m.end != one.end {
			t.Fatalf("slowdown 0 is not 1: %+v", m)
		}

	})
}

func TestMotionVelocity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(10, 0)
		for _, slow := range []float64{1, 2.5} {
			m := NewMotion(ViewSpring(-300, 3000), start, slow)
			if v := m.Velocity(); v != 3000 {
				t.Fatalf("slow %v: initial velocity %v", slow, v)
			}
			d := 30 * time.Millisecond
			m.At(start.Add(d))
			tau := time.Duration(float64(d) / slow)
			if want := m.Spring.velocityAt(tau) / slow; m.Velocity() != want {
				t.Fatalf("slow %v: velocity %v, want %v", slow, m.Velocity(), want)
			}
			if _, done := m.At(start.Add(time.Hour)); !done || m.Velocity() != 0 {
				t.Fatalf("slow %v: settled velocity %v", slow, m.Velocity())
			}
		}

	})
}

// A pixel spring settles once its rounded position can no longer differ
// from the target: well before its epsilon tail, and from then on the
// spring itself (run to its epsilon) never leaves half a pixel of the
// target. A level spring (fade, veil) runs to its epsilon.
func TestMotionPixelSnapCutsTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(10, 0)
		for _, tc := range []struct {
			name     string
			from, v0 float64
		}{{"from rest", 100, 0}, {"flung past", 100, -3000}, {"flung back", 100, 3000}, {"short", 3, 0}, {"stopped near", 0.4, 60}} {
			m := NewMotion(ViewSpring(tc.from, tc.v0), start, 1)
			var settledAt time.Duration
			for d := time.Duration(0); d <= m.end; d += time.Millisecond {
				if _, done := m.At(start.Add(d)); done {
					settledAt = d
					break
				}
			}
			if settledAt == 0 || settledAt >= m.end {
				t.Fatalf("%s: settled at %v of %v", tc.name, settledAt, m.end)
			}
			if v, done := m.At(start.Add(settledAt)); !done || v != 0 {
				t.Fatalf("%s: settled value %v %v", tc.name, v, done)
			}
			for d := settledAt; d <= m.end+time.Second; d += 100 * time.Microsecond {
				if x := m.Spring.valueAt(d); math.Abs(x) >= PixelSnap {
					t.Fatalf("%s: %v past the snap at %v (settled at %v)", tc.name, x, d, settledAt)
				}
			}
			t.Logf("%s: settled at %v, epsilon end %v", tc.name, settledAt, m.end)
		}
		level := NewMotion(LevelSpring(1, 0), start, 1)
		if v, done := level.At(start.Add(level.end - time.Millisecond)); done || v == 0 {
			t.Fatalf("a level spring snapped: %v %v", v, done)
		}
		if _, done := level.At(start.Add(level.end)); !done {
			t.Fatal("a level spring never ends")
		}

	})
}
