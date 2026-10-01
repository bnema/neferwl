package core

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestSwipeTrackerVelocity(t *testing.T) {
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
}

func TestSwipeTrackerForgetsOldMovement(t *testing.T) {
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
}

func TestSwipeTrackerIgnoresEarlierEvents(t *testing.T) {
	var tr swipeTracker
	tr.push(5, 20*time.Millisecond)
	tr.push(5, 10*time.Millisecond)
	if tr.pos != 5 || len(tr.history) != 1 {
		t.Fatalf("pos %v history %d", tr.pos, len(tr.history))
	}
}

func TestSpringSettles(t *testing.T) {
	s := viewSpring(-300, 0)
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
}

func TestSpringKeepsVelocity(t *testing.T) {
	// A spring launched toward its target moves faster at first.
	still, flung := viewSpring(-300, 0), viewSpring(-300, 3000)
	at := 10 * time.Millisecond
	if still.valueAt(at) >= flung.valueAt(at) {
		t.Fatal("initial velocity ignored")
	}
}

func TestSpringOverdamped(t *testing.T) {
	s := spring{From: 0, To: 1, DampingRatio: 6, Stiffness: 1200, Epsilon: 0.0001}
	d := s.duration()
	if v := s.valueAt(d); math.IsNaN(v) || math.Abs(v-1) > 0.01 {
		t.Fatalf("value %v at %v", v, d)
	}
	if (spring{From: 0, To: 0, DampingRatio: 1.15, Stiffness: 850, Epsilon: 0.0001}).duration() != 0 {
		t.Fatal("a spring at rest takes time")
	}
}

func TestMotionEndsOnTarget(t *testing.T) {
	start := time.Unix(10, 0)
	m := newMotion(workspaceSpring(0.4, 0), start)
	if v, done := m.at(start); done || v != 0.4 {
		t.Fatalf("start %v %t", v, done)
	}
	if v, done := m.at(start.Add(10 * time.Second)); !done || v != 0 {
		t.Fatalf("end %v %t", v, done)
	}
}

func TestRubberBand(t *testing.T) {
	r := workspaceBand
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
}

func TestSnap(t *testing.T) {
	// Four columns of 400 on an 800 wide view, no gaps: views align on
	// column edges between 0 and 800.
	w := &Workspace{Output: Rect{W: 800, H: 600}, Usable: Rect{W: 800, H: 600}, MaxColumns: 2}
	for i := range 4 {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	if got := w.snapPoints(); !slices.Equal(got, []float64{0, 400, 800}) {
		t.Fatalf("snap points %v", got)
	}
	for _, tc := range []struct {
		view    int
		forward bool
		focus   int
	}{
		{view: 0, forward: true, focus: 0},
		{view: 400, forward: true, focus: 2},
		{view: 400, forward: false, focus: 1},
		{view: 800, forward: true, focus: 3},
	} {
		if focus := w.snapFocus(tc.view, tc.forward); focus != tc.focus {
			t.Errorf("snapFocus(%d, %t) = %d; want %d", tc.view, tc.forward, focus, tc.focus)
		}
	}
}

// Column edges closer than snapSpacing merge: a step always shows.
func TestSnapPointsMergeClose(t *testing.T) {
	w := &Workspace{Output: Rect{W: 1000, H: 600}, Usable: Rect{W: 1000, H: 600}}
	// Views aligning a column: 0, 600, 640 (merged into 600) and 1000.
	for _, px := range []int{600, 400, 640, 360} {
		w.Columns = append(w.Columns, Column{Width: Width{Pixels: px}, Windows: []WindowID{WindowID(len(w.Columns) + 1)}})
	}
	if got := w.snapPoints(); !slices.Equal(got, []float64{0, 600, 1000}) {
		t.Fatalf("snap points %v", got)
	}
}

// swipe pushes n updates of delta, 8 ms apart, from time 0.
func swipe(s *snapSwipe, n int, delta float64) time.Duration {
	at := time.Duration(0)
	for range n {
		at += 8 * time.Millisecond
		s.push(delta, at)
	}
	return at
}

func workspaceSnap(start float64) snapSwipe {
	return newSnapSwipe(start, math.Round(start), 1/workspaceSwipeMovement, []float64{0, 1, 2, 3, 4}, workspaceBand)
}

// One swipe reaches the next workspace at most, however fast or far.
func TestSnapSwipeOneStep(t *testing.T) {
	for _, tc := range []struct {
		name  string
		n     int
		delta float64
		want  float64
	}{
		{"quick flick down", 6, 30, 3},
		{"quick flick up", 6, -30, 1},
		{"hard long flick", 30, 60, 3},
		{"short quick flick", 3, 20, 3},
	} {
		s := workspaceSnap(2)
		at := swipe(&s, tc.n, tc.delta)
		if got, _ := s.end(false, at); got != tc.want {
			t.Errorf("%s: landed on %v, want %v", tc.name, got, tc.want)
		}
		if p := s.pos(); p < 1-workspaceBand.limit || p > 3+workspaceBand.limit {
			t.Errorf("%s: view %v went past the neighbors", tc.name, p)
		}
	}
}

// A slow swipe settles on the closest point: back unless past halfway.
func TestSnapSwipeSlowSettlesClosest(t *testing.T) {
	for _, tc := range []struct {
		dist, want float64
	}{{100, 2}, {200, 3}, {-200, 1}} {
		s := workspaceSnap(2)
		at := time.Duration(0)
		for range 40 {
			at += 50 * time.Millisecond
			s.push(tc.dist/40, at)
		}
		if got, _ := s.end(false, at+200*time.Millisecond); got != tc.want {
			t.Errorf("slow %v: landed on %v, want %v", tc.dist, got, tc.want)
		}
	}
}

func TestSnapSwipeCancelReturns(t *testing.T) {
	s := workspaceSnap(2)
	at := swipe(&s, 10, 30)
	if got, _ := s.end(true, at); got != 2 {
		t.Fatalf("cancelled swipe landed on %v", got)
	}
}

// A swipe caught during a landing steps from the landing: one more.
func TestSnapSwipeFromBetweenPoints(t *testing.T) {
	s := newSnapSwipe(2.6, 3, 1/workspaceSwipeMovement, []float64{0, 1, 2, 3, 4}, workspaceBand)
	if s.lo != 2 || s.hi != 4 {
		t.Fatalf("bounds %v %v", s.lo, s.hi)
	}
	at := swipe(&s, 6, 30)
	if got, _ := s.end(false, at); got != 4 {
		t.Fatalf("landed on %v, want 4", got)
	}
}

// Detents: the view sticks near points and catches up between them,
// moving with the fingers in the same direction, ending where they do.
func TestSnapSwipeDetents(t *testing.T) {
	s := workspaceSnap(2)
	prev := s.pos()
	for i := range 300 {
		s.push(1, time.Duration(i)*time.Millisecond)
		p := s.pos()
		if p < prev {
			t.Fatalf("view went back at %d: %v then %v", i, prev, p)
		}
		prev = p
	}
	if math.Abs(prev-3) > 1e-9 {
		t.Fatalf("a full step shows %v", prev)
	}
	s = workspaceSnap(2)
	s.push(30, 0)
	if p := s.pos(); p >= 2.1 || p <= 2 {
		t.Fatalf("near a point the view moved to %v for 0.1", p)
	}
}

func TestStepSwipe(t *testing.T) {
	s := newStepSwipe()
	at := swipe(&s, 3, -40)
	if got := s.step(false, at); got != -1 {
		t.Fatalf("quick swipe step %d", got)
	}
	s = newStepSwipe()
	s.push(-20, time.Second)
	if got := s.step(false, 2*time.Second); got != 0 {
		t.Fatalf("slow short swipe step %d", got)
	}
}
