package core

import (
	"math"
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
	for _, tc := range []struct {
		target  float64
		forward bool
		view    int
		focus   int
	}{
		{target: 130, forward: true, view: 0, focus: 0},
		{target: 330, forward: true, view: 400, focus: 2},
		{target: 330, forward: false, view: 400, focus: 1},
		{target: 5000, forward: true, view: 800, focus: 3},
		{target: -900, forward: false, view: 0, focus: 0},
	} {
		view, focus := w.snap(tc.target, tc.forward)
		if view != tc.view || focus != tc.focus {
			t.Errorf("snap(%v, %t) = %d, %d; want %d, %d", tc.target, tc.forward, view, focus, tc.view, tc.focus)
		}
	}
}
