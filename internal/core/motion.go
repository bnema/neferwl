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
	// Snap, when set, settles the motion as soon as it can no longer
	// stray that far from To: a position in pixels whose rounded value
	// is already the settled one draws no different frame, so the tail
	// of the spring (sub-pixel, hundreds of frames with nothing to show)
	// is cut. Fades and veils keep Epsilon alone. Critically damped
	// springs only (settled).
	Snap float64
}

// pixelSnap is the Snap of a spring in logical pixels: offsets are rounded
// to whole logical pixels before the renderer scales them (rectMotion.apply,
// Workspace.shiftPixels), so a spring that stays under half a pixel from
// its target draws the settled rect from then on, at any output scale.
const pixelSnap = 0.5

// viewSpring moves a scrolled view, or a rect offset, in logical pixels;
// workspaceSpring switches workspaces, in workspaces (the caller sets its
// Snap from the output height). Both are critically damped: fast, with no
// overshoot.
func viewSpring(from, velocity float64) spring {
	return spring{From: from, Velocity: velocity, DampingRatio: 1, Stiffness: 800, Epsilon: 0.0001, Snap: pixelSnap}
}

// levelSpring is viewSpring for a level in 0..1 (a fade, a veil): no snap,
// a fraction of a level still shows.
func levelSpring(from, velocity float64) spring {
	s := viewSpring(from, velocity)
	s.Snap = 0
	return s
}

func workspaceSpring(from, velocity float64) spring {
	return spring{From: from, Velocity: velocity, DampingRatio: 1, Stiffness: 1000, Epsilon: 0.0001}
}

// settled reports whether a critically damped spring at pos with speed vel
// stays within Snap of To from now on. From there the offset is
// x(t) = e^(-βt)(x + (βx+v)t), bounded by |x| + (β|x|+|v|)·max(t·e^(-βt))
// = |x|(1+1/e) + |v|/(βe) (the peak is at t = 1/β).
func (s spring) settled(pos, vel float64) bool {
	if s.Snap <= 0 {
		return false
	}
	beta, omega0 := s.coefficients()
	if math.Abs(beta-omega0) > f32Epsilon || beta <= 0 {
		return false
	}
	x := math.Abs(pos - s.To)
	return x*(1+1/math.E)+math.Abs(vel)/(beta*math.E) < s.Snap
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

// velocityAt is the spring speed t after it started, in units per second.
func (s spring) velocityAt(t time.Duration) float64 {
	sec := t.Seconds()
	beta, omega0 := s.coefficients()
	x0 := s.From - s.To
	envelope := math.Exp(-beta * sec)
	switch {
	case math.Abs(beta-omega0) <= f32Epsilon:
		// Critically damped.
		return envelope * (s.Velocity - beta*(beta*x0+s.Velocity)*sec)
	case beta < omega0:
		w := math.Sqrt(omega0*omega0 - beta*beta)
		b := (beta*x0 + s.Velocity) / w
		cos, sin := math.Cos(w*sec), math.Sin(w*sec)
		return envelope * (-beta*(x0*cos+b*sin) + w*(b*cos-x0*sin))
	default:
		w := math.Sqrt(beta*beta - omega0*omega0)
		b := (beta*x0 + s.Velocity) / w
		cosh, sinh := math.Cosh(w*sec), math.Sinh(w*sec)
		return envelope * (-beta*(x0*cosh+b*sinh) + w*(x0*sinh+b*cosh))
	}
}

// motion is a spring started at a given time; a zero start begins at the
// first frame. It is a value: on tells whether one runs. slow stretches
// the spring's time (a slowdown of 2 takes twice as long) and v is the
// current speed in units per second, so a retarget can keep it.
type motion struct {
	spring spring
	start  time.Time
	end    time.Duration
	slow   float64
	v      float64
	on     bool
}

// newMotion starts s at now, slowed down slow times (slow <= 0 is 1). The
// spring's Velocity is in units per second of real time.
func newMotion(s spring, now time.Time, slow float64) motion {
	if slow <= 0 {
		slow = 1
	}
	v := s.Velocity
	s.Velocity *= slow
	// The slowdown is capped by the config, so this cannot overflow.
	end := time.Duration(float64(s.duration()) * slow)
	return motion{spring: s, start: now, end: end, slow: slow, v: v, on: true}
}

// at is the position at now and whether the motion has settled.
func (m *motion) at(now time.Time) (float64, bool) {
	if m.start.IsZero() {
		m.start = now
	}
	t := now.Sub(m.start)
	if t >= m.end {
		m.v = 0
		return m.spring.To, true
	}
	tau := time.Duration(float64(max(t, 0)) / m.slow)
	pos, vel := m.spring.valueAt(tau), m.spring.velocityAt(tau)
	if m.spring.settled(pos, vel) {
		m.v = 0
		return m.spring.To, true
	}
	m.v = vel / m.slow
	return pos, false
}

// sampleAt is the position and speed m has at now, in units per second,
// without moving m: m is a copy, so at's bookkeeping (start, v) stays out.
func (m motion) sampleAt(now time.Time) (pos, vel float64) {
	pos, _ = m.at(now)
	return pos, m.v
}

// scale multiplies the motion's distance and speed by k: the spring is
// linear in its start offset and speed, so the progress is unchanged.
func (m *motion) scale(k float64) {
	if !m.on {
		return
	}
	m.spring.From *= k
	m.spring.Velocity *= k
	m.v *= k
}

// velocity is the speed of the last sample, in units per second.
func (m motion) velocity() float64 { return m.v }

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
