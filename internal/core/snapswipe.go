package core

import (
	"math"
	"slices"
	"time"
)

// A snap swipe moves a view through sorted snap points (libadwaita's swipe
// tracker): one gesture reaches the start's neighbors at most, so a quick
// flick never skips a point. Past them the view resists (rubber band);
// between points it sticks near each point and speeds up in between
// (detents). When the fingers lift, a quick swipe goes to the next point
// in its direction; a slow one settles on the closest point.

const (
	// snapVelocity is the touchpad speed, in units per second, above which
	// a swipe goes on to the next point (libadwaita's touchpad threshold).
	snapVelocity = 600.0
	// detent is how much the view sticks near a snap point: it follows the
	// fingers at 1-detent there and 1+detent halfway between points.
	detent = 0.3
)

type snapSwipe struct {
	tracker swipeTracker
	// scale turns touchpad units into view units.
	scale float64
	// start is the view shown when the swipe began; points are sorted.
	start  float64
	points []float64
	// origin is the point the view rests on or lands on when the swipe
	// began: the swipe steps from it, so a swipe during a landing goes
	// one step past the landing, not from wherever the slide shows.
	origin int
	// lo and hi bound the swipe: the origin's neighbors.
	lo, hi float64
	band   rubberBand
}

// newSnapSwipe follows a swipe from the view start toward points, the
// view resting on or heading to the point closest to origin.
func newSnapSwipe(start, origin, scale float64, points []float64, band rubberBand) snapSwipe {
	s := snapSwipe{scale: scale, start: start, points: points, band: band}
	s.origin = s.closest(origin)
	n := len(points)
	s.lo, s.hi = points[max(s.origin-1, 0)], points[min(s.origin+1, n-1)]
	// A slide caught past them can still come back.
	s.lo, s.hi = min(s.lo, start), max(s.hi, start)
	return s
}

// snapEpsilon is how close to a point counts as on it.
const snapEpsilon = 1e-6

func (s *snapSwipe) closest(pos float64) int {
	best := 0
	for i, v := range s.points {
		if math.Abs(v-pos) < math.Abs(s.points[best]-pos) {
			best = i
		}
	}
	return best
}

// previous is the last point at or before pos, -1 for none.
func (s *snapSwipe) previous(pos float64) int {
	for i := len(s.points) - 1; i >= 0; i-- {
		if s.points[i] <= pos+snapEpsilon {
			return i
		}
	}
	return -1
}

// next is the first point at or after pos, len(points) for none.
func (s *snapSwipe) next(pos float64) int {
	for i, v := range s.points {
		if v >= pos-snapEpsilon {
			return i
		}
	}
	return len(s.points)
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

// around returns the points around x inside the bounds, or the start when
// it lies between points.
func (s *snapSwipe) around(x float64) (a, b float64, ok bool) {
	stops := slices.DeleteFunc(slices.Clone(s.points), func(v float64) bool { return v < s.lo || v > s.hi })
	if s.start > s.lo && s.start < s.hi && !slices.ContainsFunc(stops, func(v float64) bool { return math.Abs(v-s.start) <= snapEpsilon }) {
		stops = append(stops, s.start)
		slices.Sort(stops)
	}
	for i := 1; i < len(stops); i++ {
		if x <= stops[i] {
			a, b = stops[i-1], stops[i]
			return a, b, b-a > snapEpsilon
		}
	}
	return 0, 0, false
}

// end is the point the swipe settles on when the fingers lift (the
// start's closest when cancelled), and the view's speed for the spring
// there, in view units per second.
func (s *snapSwipe) end(cancelled bool, at time.Duration) (target, velocity float64) {
	// Idle time before the lift slows the swipe down.
	s.tracker.push(0, at)
	velocity = s.tracker.velocity() * s.scale * s.slope()
	if cancelled {
		return s.points[s.origin], velocity
	}
	lo, hi := s.points[max(s.origin-1, 0)], s.points[min(s.origin+1, len(s.points)-1)]
	v := s.tracker.velocity()
	if math.Abs(v) < snapVelocity {
		return min(max(s.points[s.closest(s.raw())], lo), hi), velocity
	}
	// A quick swipe lands on the first point at or past where its speed
	// would carry it, in its direction.
	proj := min(max(s.start+s.tracker.projectedEnd()*s.scale, lo), hi)
	if v > 0 {
		return s.points[min(s.next(proj), len(s.points)-1)], velocity
	}
	return s.points[max(s.previous(proj), 0)], velocity
}

// discreteSwipeMovement is the touchpad distance of one step of a swipe
// that runs an action instead of sliding the view.
const discreteSwipeMovement = 300.0

// newStepSwipe is a swipe that settles on a step of -1, 0 or 1.
func newStepSwipe() snapSwipe {
	return newSnapSwipe(0, 0, 1/discreteSwipeMovement, []float64{-1, 0, 1}, rubberBand{})
}

// step is where a step swipe settles when the fingers lift.
func (s *snapSwipe) step(cancelled bool, at time.Duration) int {
	target, _ := s.end(cancelled, at)
	return int(math.Round(target))
}
