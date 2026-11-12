package wayland

import "github.com/bnema/neferwl/internal/domain"

// contentRouter owns content delivery state on the display goroutine.
type contentRouter struct {
	server     *Server // Return posts back to the display through this server.
	links      []*contentLink
	latest     map[domain.WindowID]domain.SurfaceContent
	contentSeq map[domain.WindowID]uint64
	damage     map[domain.WindowID][]domain.SeqDamage
}

func newContentRouter(s *Server) contentRouter {
	return contentRouter{server: s, latest: make(map[domain.WindowID]domain.SurfaceContent), contentSeq: make(map[domain.WindowID]uint64), damage: make(map[domain.WindowID][]domain.SeqDamage)}
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
