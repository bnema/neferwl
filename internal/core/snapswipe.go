package core

import (
	"math"
	"slices"
	"time"
)

// A snap swipe moves a view through sorted snap points (libadwaita's swipe
// tracker): one gesture reaches the neighbors of the point it starts from
// at most, so a quick flick never skips a point. Past them the view resists
// (rubber band); between points it sticks near each point and speeds up in
// between (detents). When the fingers lift, a quick swipe goes on to the
// next point in its direction; a slow one settles on the closest point.

const (
	// snapVelocity is the touchpad speed, in units per second, above which
	// a swipe goes on to the next point (libadwaita's touchpad threshold).
	snapVelocity = 600.0
	// detent is how much the view sticks near a snap point: it follows the
	// fingers at 1-detent there and 1+detent halfway between points.
	detent = 0.3
	// snapEpsilon is how close to a point counts as on it.
	snapEpsilon = 1e-6
)

type snapSwipe struct {
	tracker swipeTracker
	// scale turns touchpad units into view units.
	scale float64
	// start is the view shown when the swipe began; points are sorted.
	start  float64
	points []float64
	// home indexes the point the view rests on or lands on when the swipe
	// began: the swipe steps from it, so a swipe during a landing goes one
	// step past the landing, not from wherever the slide shows.
	home int
	// landLo and landHi are home's neighbors: where the swipe may land.
	landLo, landHi float64
	// lo and hi bound the fingers' view before resistance: the neighbors,
	// widened to start when a slide was caught past them.
	lo, hi float64
	// stops are the detent ends: the points within [lo, hi], and start.
	stops []float64
	band  rubberBand
}

// newSnapSwipe follows a swipe from the view start toward points, the view
// resting on or heading to rest. A rest between points becomes a point:
// a slow swipe never settles behind the view it started from.
func newSnapSwipe(start, rest, scale float64, points []float64, band rubberBand) snapSwipe {
	s := snapSwipe{scale: scale, start: start, points: points, band: band}
	s.points = withPoint(s.points, rest)
	s.home = s.closest(rest)
	s.landLo = s.points[max(s.home-1, 0)]
	s.landHi = s.points[min(s.home+1, len(s.points)-1)]
	s.lo, s.hi = min(s.landLo, start), max(s.landHi, start)
	for _, v := range s.points {
		if v >= s.lo && v <= s.hi {
			s.stops = append(s.stops, v)
		}
	}
	s.stops = withPoint(s.stops, start)
	return s
}

// withPoint returns sorted points with v added unless one is within
// snapEpsilon of it.
func withPoint(points []float64, v float64) []float64 {
	i, _ := slices.BinarySearch(points, v)
	if i < len(points) && points[i]-v <= snapEpsilon || i > 0 && v-points[i-1] <= snapEpsilon {
		return points
	}
	return slices.Insert(slices.Clone(points), i, v)
}

// indexPoints are the points 0 to n-1: one per workspace.
func indexPoints(n int) []float64 {
	points := make([]float64, n)
	for i := range points {
		points[i] = float64(i)
	}
	return points
}

func (s *snapSwipe) closest(pos float64) int {
	best := 0
	for i, v := range s.points {
		if math.Abs(v-pos) < math.Abs(s.points[best]-pos) {
			best = i
		}
	}
	return best
}

func (s *snapSwipe) push(delta float64, at time.Duration) { s.tracker.push(delta, at) }

// raw is where the fingers put the view, before resistance and detents.
func (s *snapSwipe) raw() float64 { return s.start + s.tracker.pos*s.scale }

// pos is the view shown: rubber-banded past the bounds, with detents
// between them.
func (s *snapSwipe) pos() float64 {
	x := s.raw()
	if x < s.lo || x > s.hi {
		return s.band.clamp(s.lo, s.hi, x)
	}
	a, b, ok := s.around(x)
	if !ok {
		return x
	}
	t := (x - a) / (b - a)
	return a + (b-a)*(t-detent*math.Sin(2*math.Pi*t)/(2*math.Pi))
}

// slope is the derivative of pos at the fingers' view.
func (s *snapSwipe) slope() float64 {
	x := s.raw()
	if x < s.lo || x > s.hi {
		return s.band.clampDerivative(s.lo, s.hi, x)
	}
	a, b, ok := s.around(x)
	if !ok {
		return 1
	}
	return 1 - detent*math.Cos(2*math.Pi*(x-a)/(b-a))
}

// around returns the stops around x, which lies within [lo, hi].
func (s *snapSwipe) around(x float64) (a, b float64, ok bool) {
	for i := 1; i < len(s.stops); i++ {
		if x <= s.stops[i] {
			a, b = s.stops[i-1], s.stops[i]
			return a, b, b-a > snapEpsilon
		}
	}
	return 0, 0, false
}

// end is the point the swipe settles on when the fingers lift (home when
// cancelled), and the view's speed for the spring there, in view units
// per second.
func (s *snapSwipe) end(cancelled bool, at time.Duration) (target, velocity float64) {
	// Idle time before the lift slows the swipe down.
	s.tracker.push(0, at)
	v := s.tracker.velocity()
	velocity = v * s.scale * s.slope()
	switch {
	case cancelled:
		return s.points[s.home], velocity
	case math.Abs(v) < snapVelocity:
		return min(max(s.points[s.closest(s.raw())], s.landLo), s.landHi), velocity
	}
	// A quick swipe lands on the first landing point at or past where its
	// speed would carry it, in its direction.
	proj := min(max(s.start+s.tracker.projectedEnd()*s.scale, s.landLo), s.landHi)
	land := [3]float64{s.landLo, s.points[s.home], s.landHi}
	if v > 0 {
		for _, p := range land {
			if p >= proj-snapEpsilon {
				return p, velocity
			}
		}
	}
	// proj lies within [landLo, landHi]: one of them always matches.
	for _, p := range slices.Backward(land[:]) {
		if p <= proj+snapEpsilon {
			return p, velocity
		}
	}
	return s.landLo, velocity
}

// discreteSwipeMovement is the touchpad distance of one step of a swipe
// that runs an action instead of sliding the view.
const discreteSwipeMovement = 300.0

// newStepSwipe is a swipe that settles on a step of -1, 0 or 1.
func newStepSwipe() snapSwipe {
	return newSnapSwipe(0, 0, 1/discreteSwipeMovement, []float64{-1, 0, 1}, workspaceBand)
}

// step is where a step swipe settles when the fingers lift.
func (s *snapSwipe) step(cancelled bool, at time.Duration) int {
	target, _ := s.end(cancelled, at)
	return int(math.Round(target))
}
