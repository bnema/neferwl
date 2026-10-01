package core

import (
	"math"
	"time"
)

// Touchpad motion follows GNOME Shell and niri: a swipe tracker measures the
// fingers' speed, the gesture lands where that speed would decelerate to a
// halt, and a spring carries the view there from where the fingers left it.

// trackerHistory is how far back the swipe speed is measured.
const trackerHistory = 150 * time.Millisecond

// touchpadDeceleration is the per-millisecond speed ratio of a swipe left
// to decelerate after the fingers lift.
const touchpadDeceleration = 0.997

type trackerEvent struct {
	delta float64
	at    time.Duration
}

// swipeTracker adds up one swipe axis. Times are event timestamps (the
// device clock), so the speed does not depend on when events are read.
type swipeTracker struct {
	history []trackerEvent
	pos     float64
}

// push adds a movement. An event older than the last one is ignored.
func (t *swipeTracker) push(delta float64, at time.Duration) {
	if n := len(t.history); n > 0 && at < t.history[n-1].at {
		return
	}
	t.history = append(t.history, trackerEvent{delta: delta, at: at})
	t.pos += delta
	i := 0
	for i < len(t.history) && at > t.history[i].at+trackerHistory {
		i++
	}
	t.history = t.history[i:]
}

// velocity is the speed over the recent history, in units per second.
func (t *swipeTracker) velocity() float64 {
	if len(t.history) < 2 {
		return 0
	}
	span := (t.history[len(t.history)-1].at - t.history[0].at).Seconds()
	if span <= 0 {
		return 0
	}
	sum := 0.0
	for _, e := range t.history {
		sum += e.delta
	}
	return sum / span
}

// projectedEnd is where the swipe stops once its speed decelerates.
func (t *swipeTracker) projectedEnd() float64 {
	return t.pos - t.velocity()/(1000*math.Log(touchpadDeceleration))
}

// spring is a damped spring from From to To (libadwaita's model, as in
// niri). Velocity is the initial speed in units per second.
type spring struct {
	From, To, Velocity float64
	DampingRatio       float64
	Stiffness          float64
	Epsilon            float64
}

// viewSpring moves a scrolled view; workspaceSpring switches workspaces.
// Both are critically damped: fast, with no overshoot.
func viewSpring(from, velocity float64) spring {
	return spring{From: from, Velocity: velocity, DampingRatio: 1, Stiffness: 800, Epsilon: 0.0001}
}

func workspaceSpring(from, velocity float64) spring {
	return spring{From: from, Velocity: velocity, DampingRatio: 1, Stiffness: 1000, Epsilon: 0.0001}
}

// f32Epsilon compares damping terms: float64's epsilon is too small for
// them (libadwaita, niri).
const f32Epsilon = 1.1920929e-07

func (s spring) coefficients() (beta, omega0 float64) {
	const mass = 1.0
	k := max(s.Stiffness, 0)
	damping := max(s.DampingRatio, 0) * 2 * math.Sqrt(mass*k)
	return damping / (2 * mass), math.Sqrt(k / mass)
}

// valueAt is the spring position t after it started.
func (s spring) valueAt(t time.Duration) float64 {
	sec := t.Seconds()
	beta, omega0 := s.coefficients()
	x0 := s.From - s.To
	envelope := math.Exp(-beta * sec)
	switch {
	case math.Abs(beta-omega0) <= f32Epsilon:
		// Critically damped.
		return s.To + envelope*(x0+(beta*x0+s.Velocity)*sec)
	case beta < omega0:
		w := math.Sqrt(omega0*omega0 - beta*beta)
		return s.To + envelope*(x0*math.Cos(w*sec)+((beta*x0+s.Velocity)/w)*math.Sin(w*sec))
	default:
		w := math.Sqrt(beta*beta - omega0*omega0)
		return s.To + envelope*(x0*math.Cosh(w*sec)+((beta*x0+s.Velocity)/w)*math.Sinh(w*sec))
	}
}

// duration is how long the spring takes to settle within Epsilon.
func (s spring) duration() time.Duration {
	const step = 0.001
	beta, omega0 := s.coefficients()
	if beta <= 0 {
		return time.Duration(math.MaxInt64)
	}
	if math.Abs(s.To-s.From) <= math.SmallestNonzeroFloat64 && s.Velocity == 0 {
		return 0
	}
	x0 := -math.Log(max(s.Epsilon, math.SmallestNonzeroFloat64)) / beta
	if math.Abs(beta-omega0) <= f32Epsilon || beta < omega0 {
		return seconds(x0)
	}
	// Overdamped: the envelope decays faster than the motion; find where
	// the motion itself settles (Newton's method).
	at := func(sec float64) float64 { return s.valueAt(seconds(sec)) }
	y0 := at(x0)
	m := (at(x0+step) - y0) / step
	x1 := (s.To - y0 + m*x0) / m
	y1 := at(x1)
	for i := 0; math.Abs(s.To-y1) > s.Epsilon; i++ {
		if i > 1000 {
			return 0
		}
		x0, y0 = x1, y1
		m = (at(x0+step) - y0) / step
		x1 = (s.To - y0 + m*x0) / m
		y1 = at(x1)
		if math.IsNaN(y1) || math.IsInf(y1, 0) {
			return seconds(x0)
		}
	}
	return seconds(x1)
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// motion is a spring started at a given time; a zero start begins at the
// first frame.
type motion struct {
	spring spring
	start  time.Time
	end    time.Duration
}

func newMotion(s spring, now time.Time) *motion {
	return &motion{spring: s, start: now, end: s.duration()}
}

// at is the position at now and whether the motion has settled.
func (m *motion) at(now time.Time) (float64, bool) {
	if m.start.IsZero() {
		m.start = now
	}
	t := now.Sub(m.start)
	if t >= m.end {
		return m.spring.To, true
	}
	return m.spring.valueAt(max(t, 0)), false
}

// rubberBand resists movement past the edges: the further out, the less
// it moves, never more than limit.
type rubberBand struct{ stiffness, limit float64 }

// workspaceBand holds a swipe past the steps it may reach, in steps: a
// twentieth of a workspace at most. Column swipes scale it to pixels.
var workspaceBand = rubberBand{stiffness: 0.5, limit: 0.05}

// scaled is the band measured in units k times smaller.
func (r rubberBand) scaled(k float64) rubberBand {
	return rubberBand{stiffness: r.stiffness, limit: r.limit * k}
}

func (r rubberBand) band(x float64) float64 {
	return (1 - 1/(x*r.stiffness/r.limit+1)) * r.limit
}

func (r rubberBand) derivative(x float64) float64 {
	d := r.stiffness*x + r.limit
	return r.stiffness * r.limit * r.limit / (d * d)
}

// clamp keeps x in [lo, hi], letting it stretch past the edges.
func (r rubberBand) clamp(lo, hi, x float64) float64 {
	c := min(max(x, lo), hi)
	if x < c {
		return c - r.band(c-x)
	}
	return c + r.band(x-c)
}

// clampDerivative is the slope of clamp at x: 1 inside the edges.
func (r rubberBand) clampDerivative(lo, hi, x float64) float64 {
	if lo <= x && x <= hi {
		return 1
	}
	c := min(max(x, lo), hi)
	return r.derivative(math.Abs(x - c))
}
