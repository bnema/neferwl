package drm

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Reports to wayland: what the output shows and has read.

// report queues what the output shows and has read, so replaced client
// buffers can be released (see presented.Queue for coalescing).
func (o *Output) report(flip *ports.FlipInfo, seen map[domain.WindowID]uint64) {
	o.capped = false
	var reads map[domain.WindowID]uint64
	if o.capHidden != nil {
		seen, reads, o.capped = o.capHidden(seen)
	}
	o.reports.Push(ports.OutputPresented{Output: o.conn.name, Flip: flip, Shown: o.shown, Queued: o.queued, Seen: seen, ChildReads: reads})
}

// shownBy returns an immutable snapshot of the content Seq of drawn windows
// (see surfaces.ShowsSnapshot). A closed window drawn from its kept content is
// not shown to its client: the empty content has no frame to present.
func (o *Output) shownBy(s ports.Scene, seen map[domain.WindowID]uint64) map[domain.WindowID]uint64 {
	return o.shows.FromSeen(s, seen, o.kept)
}

func (o *Output) directShownBy(id domain.WindowID, seq uint64) map[domain.WindowID]uint64 {
	if len(o.directShowsSnapshot) != 1 || o.directShowsSnapshot[id] != seq {
		o.directShowsSnapshot = map[domain.WindowID]uint64{id: seq}
	}
	return o.directShowsSnapshot
}
