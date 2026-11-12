package surfaces

import "time"

// routeWait bounds how long an output holds a frame for the contents a route
// change promised: after it the output renders anyway.
const routeWait = 100 * time.Millisecond

// RouteGate holds a scene back until the batch carrying its route generation
// (domain.Scene.Routes, domain.ContentBatch.Routes) has arrived, so that a
// window newly routed to the output is never drawn without its content. It is
// owned by the output goroutine; the zero value is ready and never gates.
type RouteGate struct {
	seen, need, warned uint64
	deadline           time.Time
}

// Scene records the generation the accepted scene needs. A generation above
// the newest batch seen arms the deadline once.
func (g *RouteGate) Scene(gen uint64, now time.Time) {
	if gen > g.seen && gen != g.need {
		g.deadline = now.Add(routeWait)
	}
	g.need = gen
}

// Batch records the generation a received batch carried.
func (g *RouteGate) Batch(gen uint64) { g.seen = max(g.seen, gen) }

// Wait reports whether the frame must wait, and for how long at most.
func (g *RouteGate) Wait(now time.Time) (time.Duration, bool) {
	if g.need <= g.seen || !now.Before(g.deadline) {
		return 0, false
	}
	return g.deadline.Sub(now), true
}

// Late reports, once per expiry, that the deadline passed with the batch
// still missing; the caller renders anyway and may warn.
func (g *RouteGate) Late(now time.Time) bool {
	if g.need <= g.seen || now.Before(g.deadline) || g.warned == g.need {
		return false
	}
	g.warned = g.need
	return true
}
