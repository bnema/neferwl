package motion

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

// Spring is a damped spring from From to To (libadwaita's model, as in
// niri). Velocity is the initial speed in units per second.
type Spring struct {
	// From and To are the starting and target positions.
	From, To float64
	// Velocity is the initial speed in units per second.
	Velocity float64
	// DampingRatio controls damping relative to critical damping (1).
	DampingRatio float64
	// Stiffness is the spring constant for a unit mass.
	Stiffness float64
	// Epsilon is the settling tolerance used to determine duration.
	Epsilon float64
	// Snap, when set, settles the motion as soon as it can no longer
	// stray that far from To: a position in pixels whose rounded value
	// is already the settled one draws no different frame, so the tail
	// of the spring (sub-pixel, hundreds of frames with nothing to show)
	// is cut. Fades and veils keep Epsilon alone. Critically damped
	// springs only (settled).
	Snap float64
}

// PixelSnap is the Snap of a spring in logical pixels: offsets are rounded
// to whole logical pixels before the renderer scales them (rectMotion.apply,
// Workspace.shiftPixels), so a spring that stays under half a pixel from
// its target draws the settled rect from then on, at any output scale.
const PixelSnap = 0.5

// ViewSpring moves a scrolled view, or a rect offset, in logical pixels;
// WorkspaceSpring switches workspaces, in workspaces (the caller sets its
// Snap from the output height). Both are critically damped: fast, with no
// overshoot.
func ViewSpring(from, velocity float64) Spring {
	return Spring{From: from, Velocity: velocity, DampingRatio: 1, Stiffness: 800, Epsilon: 0.0001, Snap: PixelSnap}
}

// LevelSpring is ViewSpring for a level in 0..1 (a fade, a veil): no snap,
// a fraction of a level still shows.
func LevelSpring(from, velocity float64) Spring {
	s := ViewSpring(from, velocity)
	s.Snap = 0
	return s
}

// WorkspaceSpring switches workspaces with critical damping, in workspace units.
// The caller sets Snap from the output span.
func WorkspaceSpring(from, velocity float64) Spring {
	return Spring{From: from, Velocity: velocity, DampingRatio: 1, Stiffness: 1000, Epsilon: 0.0001}
}

// settled reports whether a critically damped spring at pos with speed vel
// stays within Snap of To from now on. From there the offset is
// x(t) = e^(-βt)(x + (βx+v)t), bounded by |x| + (β|x|+|v|)·max(t·e^(-βt))
// = |x|(1+1/e) + |v|/(βe) (the peak is at t = 1/β).
func (s Spring) settled(pos, vel float64) bool {
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

func (s Spring) coefficients() (beta, omega0 float64) {
	const mass = 1.0
	k := max(s.Stiffness, 0)
	damping := max(s.DampingRatio, 0) * 2 * math.Sqrt(mass*k)
	return damping / (2 * mass), math.Sqrt(k / mass)
}

// valueAt is the spring position t after it started.
func (s Spring) valueAt(t time.Duration) float64 {
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
func (s Spring) duration() time.Duration {
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
func (s Spring) velocityAt(t time.Duration) float64 {
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

// Motion is a spring started at a given time; a zero start begins at the
// first frame. It remains a value, not an interface or a shared animation.
// It is named Motion to distinguish it from the package name.
type Motion struct {
	// Spring is the underlying spring, with velocity in slowed spring time.
	Spring Spring
	start  time.Time
	end    time.Duration
	// Slow stretches spring time: 2 takes twice as long.
	Slow float64
	v    float64
	// On reports whether this value holds a running motion. The owner clears it.
	On bool
}

// NewMotion starts s at now, slowed down slow times (slow <= 0 is 1). The
// spring's Velocity is in units per second of real time.
func NewMotion(s Spring, now time.Time, slow float64) Motion {
	if slow <= 0 {
		slow = 1
	}
	v := s.Velocity
	s.Velocity *= slow
	// The slowdown is capped by the config, so this cannot overflow.
	end := time.Duration(float64(s.duration()) * slow)
	return Motion{Spring: s, start: now, end: end, Slow: slow, v: v, On: true}
}

// At is the position at now and whether the motion has settled.
func (m *Motion) At(now time.Time) (float64, bool) {
	if m.start.IsZero() {
		m.start = now
	}
	t := now.Sub(m.start)
	if t >= m.end {
		m.v = 0
		return m.Spring.To, true
	}
	tau := time.Duration(float64(max(t, 0)) / m.Slow)
	pos, vel := m.Spring.valueAt(tau), m.Spring.velocityAt(tau)
	if m.Spring.settled(pos, vel) {
		m.v = 0
		return m.Spring.To, true
	}
	m.v = vel / m.Slow
	return pos, false
}

// SampleAt is the position and speed m has at now, in units per second,
// without moving m: m is a copy, so At's bookkeeping (start, v) stays out.
func (m Motion) SampleAt(now time.Time) (pos, vel float64) {
	pos, _ = m.At(now)
	return pos, m.v
}

// Scale multiplies the motion's distance and speed by k: the spring is
// linear in its start offset and speed, so the progress is unchanged.
func (m *Motion) Scale(k float64) {
	if !m.On {
		return
	}
	m.Spring.From *= k
	m.Spring.Velocity *= k
	m.v *= k
}

// Velocity is the speed of the last sample, in units per second.
func (m Motion) Velocity() float64 { return m.v }

// RubberBand resists movement past the edges: the further out, the less
// it moves, never more than limit.
type RubberBand struct{ stiffness, limit float64 }

// WorkspaceBand holds a swipe past the steps it may reach, in steps: a
// twentieth of a workspace at most. Column swipes scale it to pixels.
var WorkspaceBand = RubberBand{stiffness: 0.5, limit: 0.05}

// Scaled is the band measured in units k times smaller.
func (r RubberBand) Scaled(k float64) RubberBand {
	return RubberBand{stiffness: r.stiffness, limit: r.limit * k}
}

func (r RubberBand) band(x float64) float64 {
	return (1 - 1/(x*r.stiffness/r.limit+1)) * r.limit
}

func (r RubberBand) derivative(x float64) float64 {
	d := r.stiffness*x + r.limit
	return r.stiffness * r.limit * r.limit / (d * d)
}

// clamp keeps x in [lo, hi], letting it stretch past the edges.
func (r RubberBand) clamp(lo, hi, x float64) float64 {
	c := min(max(x, lo), hi)
	if x < c {
		return c - r.band(c-x)
	}
	return c + r.band(x-c)
}

// clampDerivative is the slope of clamp at x: 1 inside the edges.
func (r RubberBand) clampDerivative(lo, hi, x float64) float64 {
	if lo <= x && x <= hi {
		return 1
	}
	c := min(max(x, lo), hi)
	return r.derivative(math.Abs(x - c))
}
