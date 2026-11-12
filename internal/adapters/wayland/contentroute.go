package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"time"
)

// contentRouter owns content delivery state on the display goroutine.
type contentRouter struct {
	held       []heldBuffer
	heldLive   []ports.OutputPresented
	reports    map[string]ports.OutputPresented
	release    func(*wayland.Buffer, syncHold)
	referenced func(*wayland.Buffer) bool
	live       func(string) bool
	server     *Server // Return posts back to the display through this server.
	links      []*contentLink
	latest     map[domain.WindowID]domain.SurfaceContent
	contentSeq map[domain.WindowID]uint64
	damage     map[domain.WindowID][]domain.SeqDamage
}

func newContentRouter(s *Server, release func(*wayland.Buffer, syncHold), referenced func(*wayland.Buffer) bool, live func(string) bool) contentRouter {
	return contentRouter{server: s, release: release, referenced: referenced, live: live, reports: make(map[string]ports.OutputPresented), latest: make(map[domain.WindowID]domain.SurfaceContent), contentSeq: make(map[domain.WindowID]uint64), damage: make(map[domain.WindowID][]domain.SeqDamage)}
}

func (r *contentRouter) seq(id domain.WindowID) uint64 { return r.contentSeq[id] }

// publish stamps immutable damage history and feeds every attached link.
//
//hexcheck:ignore suspicious-business-logic-in-adapter Moves adapter-owned content delivery bookkeeping on the display owner goroutine; no policy decision.
func (r *contentRouter) publish(c domain.SurfaceContent, d damage) domain.SurfaceContent {
	r.contentSeq[c.ID]++
	c.Seq = r.contentSeq[c.ID]
	// Published histories are immutable: an output may hold one for as long
	// as it likes. The history grows inside one backing array, appended past
	// the end of every published slice (never over an element a reader can
	// see), and moves to a fresh array only when that one is full. Readers
	// get a capacity-clipped slice, so they cannot append into the spare.
	old := r.damage[c.ID]
	if len(old) >= damageHistory {
		old = old[len(old)-damageHistory+1:]
	}
	if len(old) == cap(old) {
		grown := make([]domain.SeqDamage, len(old), damageHistoryCap)
		copy(grown, old)
		old = grown
	}
	h := append(old, domain.SeqDamage{Seq: c.Seq, Full: d.full, Rects: d.rects})
	c.DamageHistory = h[:len(h):len(h)]
	if c.Empty() {
		delete(r.damage, c.ID)
		delete(r.latest, c.ID)
	} else {
		r.damage[c.ID] = h
		r.latest[c.ID] = c
	}
	for _, l := range r.links {
		l.add(c)
		r.flushLink(l)
	}
	return c
}

func (r *contentRouter) hold(h heldBuffer) { r.held = append(r.held, h) }

// settle records the output reports and releases the buffers no
// output reads. Every output reads every window's content, so a buffer
// waits until no live output shows or queues it and all of them have seen
// a later content of its window (or heldTimeout passed, without child reads).
// It reports
// whether buffers are still held.
func (r *contentRouter) settle(now time.Time, reports []ports.OutputPresented) bool {
	for _, report := range reports {
		r.reports[report.Output].Snapshot.Release()
		r.reports[report.Output] = report
	}
	live := r.heldLive[:0]
	for name := range r.reports {
		if !r.live(name) {
			r.reports[name].Snapshot.Release()
			delete(r.reports, name)
		}
	}
	for _, r := range r.reports {
		live = append(live, r)
	}
	// keepHeld only reads live: the scratch holds nothing across calls.
	r.heldLive = live
	kept := r.held[:0]
	for _, h := range r.held {
		if r.referenced(h.res) || keepHeld(h, live, now) {
			kept = append(kept, h)
			continue
		}
		r.release(h.res, h.sync)
	}
	clear(r.held[len(kept):])
	r.held = kept
	clear(live) // the reports carry maps: do not retain them
	return len(r.held) > 0
}
