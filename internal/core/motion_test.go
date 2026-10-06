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
	m := newMotion(workspaceSpring(0.4, 0), start, 1)
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

// With no column fully shown (a column wider than the space between the
// gaps), the focus goes to the column at the left edge.
func TestSnapFocusWideColumn(t *testing.T) {
	w := &Workspace{Output: Rect{W: 800, H: 600}, Usable: Rect{W: 800, H: 600}, Gaps: 10}
	for i := range 3 {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
	}
	// Full width with gaps: 780 wide, between 10 and 790. The view
	// columnX(1)+1 shifts it 1 px left, so it is not fully shown.
	w.Columns[1].FullWidth = true
	if !w.slidable() {
		t.Fatal("the workspace does not scroll")
	}
	view := w.columnX(1) - w.Usable.X - w.gap() + 1
	for _, forward := range []bool{true, false} {
		if focus := w.snapFocus(view, forward); focus != 1 {
			t.Fatalf("forward=%t: focus %d, want the wide column 1", forward, focus)
		}
	}
}

func TestSpringVelocityAt(t *testing.T) {
	springs := map[string]spring{
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
}

func TestMotionSlowdownScalesTime(t *testing.T) {
	start := time.Unix(10, 0)
	s := viewSpring(-300, 0)
	one, two := newMotion(s, start, 1), newMotion(s, start, 2)
	if two.end != 2*one.end {
		t.Fatalf("end %v, want %v", two.end, 2*one.end)
	}
	for _, d := range []time.Duration{5 * time.Millisecond, 40 * time.Millisecond, 150 * time.Millisecond} {
		a, _ := one.at(start.Add(d))
		b, _ := two.at(start.Add(2 * d))
		if math.Abs(a-b) > 1e-9 {
			t.Errorf("at %v: %v vs %v", d, a, b)
		}
	}
	if m := newMotion(s, start, 0); m.slow != 1 || m.end != one.end {
		t.Fatalf("slowdown 0 is not 1: %+v", m)
	}
}

func TestMotionVelocity(t *testing.T) {
	start := time.Unix(10, 0)
	for _, slow := range []float64{1, 2.5} {
		m := newMotion(viewSpring(-300, 3000), start, slow)
		if v := m.velocity(); v != 3000 {
			t.Fatalf("slow %v: initial velocity %v", slow, v)
		}
		d := 30 * time.Millisecond
		m.at(start.Add(d))
		tau := time.Duration(float64(d) / slow)
		if want := m.spring.velocityAt(tau) / slow; m.velocity() != want {
			t.Fatalf("slow %v: velocity %v, want %v", slow, m.velocity(), want)
		}
		if _, done := m.at(start.Add(time.Hour)); !done || m.velocity() != 0 {
			t.Fatalf("slow %v: settled velocity %v", slow, m.velocity())
		}
	}
}

func TestRetargetKeepsVelocityAndSlowdown(t *testing.T) {
	start := time.Unix(10, 0)
	w := &Workspace{View: 100, view: slide{off: -300}}
	w.view.motion = newMotion(viewSpring(w.view.off, 2000), start, 2)
	w.view.motion.at(start.Add(20 * time.Millisecond))
	v := w.view.motion.velocity()
	if v <= 0 {
		t.Fatalf("velocity %v", v)
	}
	w.View = 40
	w.retarget(100)
	if w.view.off != -240 || !w.view.motion.on || w.view.motion.slow != 2 {
		t.Fatalf("shift %v motion %+v", w.view.off, w.view.motion)
	}
	if got := w.view.motion.velocity(); got != v {
		t.Fatalf("velocity %v after retarget, want %v", got, v)
	}
	if got := w.view.motion.spring.Velocity; math.Abs(got-2*v) > 1e-9 {
		t.Fatalf("spring velocity %v, want %v in spring time", got, 2*v)
	}
	var idle Workspace
	idle.View = 5
	idle.retarget(0)
	if idle.view.motion.on || idle.view.off != 0 {
		t.Fatal("retarget without a slide must not start one")
	}
}

// A pixel spring settles once its rounded position can no longer differ
// from the target: well before its epsilon tail, and from then on the
// spring itself (run to its epsilon) never leaves half a pixel of the
// target. A level spring (fade, veil) runs to its epsilon.
func TestMotionPixelSnapCutsTail(t *testing.T) {
	start := time.Unix(10, 0)
	for _, tc := range []struct {
		name     string
		from, v0 float64
	}{{"from rest", 100, 0}, {"flung past", 100, -3000}, {"flung back", 100, 3000}, {"short", 3, 0}, {"stopped near", 0.4, 60}} {
		m := newMotion(viewSpring(tc.from, tc.v0), start, 1)
		var settledAt time.Duration
		for d := time.Duration(0); d <= m.end; d += time.Millisecond {
			if _, done := m.at(start.Add(d)); done {
				settledAt = d
				break
			}
		}
		if settledAt == 0 || settledAt >= m.end {
			t.Fatalf("%s: settled at %v of %v", tc.name, settledAt, m.end)
		}
		if v, done := m.at(start.Add(settledAt)); !done || v != 0 {
			t.Fatalf("%s: settled value %v %v", tc.name, v, done)
		}
		for d := settledAt; d <= m.end+time.Second; d += 100 * time.Microsecond {
			if x := m.spring.valueAt(d); math.Abs(x) >= pixelSnap {
				t.Fatalf("%s: %v past the snap at %v (settled at %v)", tc.name, x, d, settledAt)
			}
		}
		t.Logf("%s: settled at %v, epsilon end %v", tc.name, settledAt, m.end)
	}
	level := newMotion(levelSpring(1, 0), start, 1)
	if v, done := level.at(start.Add(level.end - time.Millisecond)); done || v == 0 {
		t.Fatalf("a level spring snapped: %v %v", v, done)
	}
	if _, done := level.at(start.Add(level.end)); !done {
		t.Fatal("a level spring never ends")
	}
}

// A workspace slide snaps in output pixels: at a 600 px output half a pixel
// is 1/1200 of a workspace.
func TestSwitchSpringSnapsAtOutputPixels(t *testing.T) {
	m := newMonitorWithIDs("", "", new(uint64))
	m.template.Output = Rect{W: 800, H: 600}
	s := m.switchSpring(0.5, 0)
	if s.Snap != pixelSnap/600 {
		t.Fatalf("snap %v", s.Snap)
	}
	if s.settled(0.5/600, 0) || !s.settled(0.2/600, 0) {
		t.Fatal("snap threshold")
	}
}
