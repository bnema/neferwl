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

// Column edges closer than snapSpacing merge: a step always shows. The
// last column's end stays, dropping a point close before it.
func TestSnapPointsMergeClose(t *testing.T) {
	for _, tc := range []struct {
		widths []int
		want   []float64
	}{
		// Views aligning a column: 0, 600, 640 (merged into 600), 1000.
		{[]int{600, 400, 640, 360}, []float64{0, 600, 1000}},
		// 0, 360, 600, 960 and the end 1000: 960 goes, 1000 stays.
		{[]int{600, 360, 400, 640}, []float64{0, 360, 600, 1000}},
		// Columns narrower than the view: one point.
		{[]int{300, 300}, []float64{0}},
	} {
		w := &Workspace{Output: Rect{W: 1000, H: 600}, Usable: Rect{W: 1000, H: 600}}
		for _, px := range tc.widths {
			w.Columns = append(w.Columns, Column{Width: Width{Pixels: px}, Windows: []WindowID{WindowID(len(w.Columns) + 1)}})
		}
		if got := w.snapPoints(); !slices.Equal(got, tc.want) {
			t.Errorf("widths %v: snap points %v, want %v", tc.widths, got, tc.want)
		}
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

// slowSwipe pushes dist over two seconds and rests before the lift.
func slowSwipe(s *snapSwipe, dist float64) time.Duration {
	at := time.Duration(0)
	for range 40 {
		at += 50 * time.Millisecond
		s.push(dist/40, at)
	}
	return at + 200*time.Millisecond
}

func workspaceSnap(start float64) snapSwipe {
	return newSnapSwipe(start, math.Round(start), 1/workspaceSwipeMovement, indexPoints(5), workspaceBand)
}

// One swipe reaches the next workspace at most, however fast or far: a
// slow one settles on the closest, back unless past halfway; a cancelled
// one returns.
func TestSnapSwipeOneStep(t *testing.T) {
	for _, tc := range []struct {
		name      string
		push      func(*snapSwipe) time.Duration
		cancelled bool
		want      float64
	}{
		{"quick flick down", func(s *snapSwipe) time.Duration { return swipe(s, 6, 30) }, false, 3},
		{"quick flick up", func(s *snapSwipe) time.Duration { return swipe(s, 6, -30) }, false, 1},
		{"hard long flick", func(s *snapSwipe) time.Duration { return swipe(s, 30, 60) }, false, 3},
		{"short quick flick", func(s *snapSwipe) time.Duration { return swipe(s, 3, 20) }, false, 3},
		{"slow short", func(s *snapSwipe) time.Duration { return slowSwipe(s, 100) }, false, 2},
		{"slow past half", func(s *snapSwipe) time.Duration { return slowSwipe(s, 200) }, false, 3},
		{"slow far up", func(s *snapSwipe) time.Duration { return slowSwipe(s, -900) }, false, 1},
		{"cancelled", func(s *snapSwipe) time.Duration { return swipe(s, 10, 30) }, true, 2},
		// Most of the way down, then a hard flick back: one step up.
		{"forward then hard back", func(s *snapSwipe) time.Duration {
			at := slowSwipe(s, 250)
			for range 4 {
				at += 8 * time.Millisecond
				s.push(-30, at)
			}
			return at
		}, false, 1},
	} {
		s := workspaceSnap(2)
		at := tc.push(&s)
		if p := s.pos(); p < 1-workspaceBand.limit || p > 3+workspaceBand.limit {
			t.Errorf("%s: view %v went past the neighbors", tc.name, p)
		}
		if got, _ := s.end(tc.cancelled, at); got != tc.want {
			t.Errorf("%s: landed on %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A swipe caught during a landing steps from the landing: one more.
func TestSnapSwipeDuringLanding(t *testing.T) {
	s := newSnapSwipe(2.6, 3, 1/workspaceSwipeMovement, indexPoints(5), workspaceBand)
	at := swipe(&s, 6, 30)
	if p := s.pos(); p > 4+workspaceBand.limit {
		t.Fatalf("view %v went past 4", p)
	}
	if got, _ := s.end(false, at); got != 4 {
		t.Fatalf("landed on %v, want 4", got)
	}
}

// From a view between points (a merged column edge) a slow swipe never
// settles behind where it started; a quick one reaches the points around.
func TestSnapSwipeRestBetweenPoints(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(*snapSwipe) time.Duration
		want float64
	}{
		{"slow", func(s *snapSwipe) time.Duration { return slowSwipe(s, 5) }, 640},
		{"quick back", func(s *snapSwipe) time.Duration { return swipe(s, 4, -40) }, 600},
		{"quick on", func(s *snapSwipe) time.Duration { return swipe(s, 4, 40) }, 1000},
	} {
		s := newSnapSwipe(640, 640, 1, []float64{0, 600, 1000}, workspaceBand.scaled(1000))
		at := tc.push(&s)
		if got, _ := s.end(false, at); got != tc.want {
			t.Errorf("%s: landed on %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A swipe caught while sliding to a far workspace lands on that one or
// its neighbors, never back where the slide shows.
func TestSnapSwipeCaughtFarFromRest(t *testing.T) {
	for _, delta := range []float64{-60, 60} {
		s := newSnapSwipe(0.5, 4, 1/workspaceSwipeMovement, indexPoints(6), workspaceBand)
		at := swipe(&s, 20, delta)
		if got, _ := s.end(false, at); got < 3 || got > 5 {
			t.Errorf("delta %v: landed on %v, want 3 to 5", delta, got)
		}
	}
}

// The view follows the fingers in their direction, without jumps, from a
// start on a point, between points, or caught past the edge (a spring back
// from the rubber band); near a point it sticks, a full step shows whole.
func TestSnapSwipeDetents(t *testing.T) {
	for _, start := range []float64{2, 2.4, -0.03, 4.03} {
		s := workspaceSnap(start)
		if p := s.pos(); math.Abs(p-start) > 1e-9 {
			t.Fatalf("start %v shows %v", start, p)
		}
		for _, dir := range []float64{1, -1} {
			s := workspaceSnap(start)
			prev := s.pos()
			for i := range 400 {
				s.push(dir, time.Duration(i)*time.Millisecond)
				p := s.pos()
				if (p-prev)*dir < 0 || math.Abs(p-prev) > 0.01 {
					t.Fatalf("start %v dir %v: view %v then %v at %d", start, dir, prev, p, i)
				}
				prev = p
			}
		}
	}
	s := workspaceSnap(2)
	s.push(300, 0)
	if p := s.pos(); math.Abs(p-3) > 1e-9 {
		t.Fatalf("a full step shows %v", p)
	}
	s = workspaceSnap(2)
	s.push(30, 0)
	if p := s.pos(); p >= 2.1 || p <= 2 {
		t.Fatalf("near a point the view moved to %v for 0.1", p)
	}
}

// One workspace: nowhere to go, the view resists and stays.
func TestSnapSwipeSinglePoint(t *testing.T) {
	s := newSnapSwipe(0, 0, 1/workspaceSwipeMovement, indexPoints(1), workspaceBand)
	at := swipe(&s, 10, 40)
	if p := s.pos(); p <= 0 || p > workspaceBand.limit {
		t.Fatalf("view %v", p)
	}
	target, velocity := s.end(false, at)
	if target != 0 || math.IsNaN(velocity) {
		t.Fatalf("landed on %v at %v", target, velocity)
	}
}

func TestStepSwipe(t *testing.T) {
	for _, tc := range []struct {
		name      string
		push      func(*snapSwipe) time.Duration
		cancelled bool
		want      int
	}{
		{"quick back", func(s *snapSwipe) time.Duration { return swipe(s, 3, -40) }, false, -1},
		{"quick on", func(s *snapSwipe) time.Duration { return swipe(s, 3, 40) }, false, 1},
		{"hard", func(s *snapSwipe) time.Duration { return swipe(s, 30, 80) }, false, 1},
		{"slow short", func(s *snapSwipe) time.Duration { return slowSwipe(s, -100) }, false, 0},
		{"slow past half", func(s *snapSwipe) time.Duration { return slowSwipe(s, -200) }, false, -1},
		{"cancelled", func(s *snapSwipe) time.Duration { return swipe(s, 3, 40) }, true, 0},
	} {
		s := newStepSwipe()
		at := tc.push(&s)
		target, v := s.end(tc.cancelled, at)
		if got := int(math.Round(target)); got != tc.want {
			t.Errorf("%s: step %d, want %d", tc.name, got, tc.want)
		}
		if math.IsNaN(v) {
			t.Errorf("%s: velocity NaN", tc.name)
		}
	}
}

// With no column fully shown (a fullscreen column wider than the usable
// area), the focus goes to the column at the left edge.
func TestSnapFocusWideColumn(t *testing.T) {
	w := &Workspace{Output: Rect{W: 800, H: 600}, Usable: Rect{X: 50, W: 700, H: 600}}
	for i := range 3 {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	w.fullscreen = 2
	// Column 1 (index 1) is 800 wide from x 350+50: view 350 puts its
	// left edge on the usable area's.
	view := w.columnX(1) - w.Usable.X
	for _, forward := range []bool{true, false} {
		if focus := w.snapFocus(view, forward); focus != 1 {
			t.Fatalf("forward=%t: focus %d, want the wide column 1", forward, focus)
		}
	}
}
