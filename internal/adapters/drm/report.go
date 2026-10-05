package drm

import (
	"maps"

	"github.com/bnema/neferwl/internal/ports"
)

// Reports to wayland: what the output shows and has read.

// report queues what the output shows and has read, so replaced client
// buffers can be released (see presented.Queue for merging).
func (o *Output) report(flip *ports.FlipInfo, seen map[ports.WindowID]uint64) {
	o.capped = false
	var reads map[ports.WindowID]uint64
	if o.capHidden != nil {
		seen, reads, o.capped = o.capHidden(seen)
	}
	if o.reports.Push(ports.OutputPresented{Output: o.conn.name, Flip: flip, Shown: o.shown, Queued: o.queued, Seen: seen, ChildReads: reads}) {
		o.log.Warn().Str("connector", o.conn.name).Msg("wayland is not reading output reports; flips merged")
	}
}

// shownBy returns an immutable snapshot of the content Seq of drawn windows.
// The scratch map is private to the output; a snapshot may still be read by
// wayland after a subsequent flip, so never refill a previously sent map.
func (o *Output) shownBy(s ports.Scene, seen map[ports.WindowID]uint64) map[ports.WindowID]uint64 {
	if o.showsScratch == nil {
		o.showsScratch = make(map[ports.WindowID]uint64)
	}
	clear(o.showsScratch)
	for id, seq := range seen {
		// A closed window drawn from its kept content is not shown to its
		// client: the empty content has no frame to present.
		if s.Shows(id) && (o.kept == nil || !o.kept(id)) {
			o.showsScratch[id] = seq
		}
	}
	if !maps.Equal(o.showsSnapshot, o.showsScratch) {
		o.showsSnapshot = maps.Clone(o.showsScratch)
	}
	return o.showsSnapshot
}

func (o *Output) directShownBy(id ports.WindowID, seq uint64) map[ports.WindowID]uint64 {
	if len(o.directShowsSnapshot) != 1 || o.directShowsSnapshot[id] != seq {
		o.directShowsSnapshot = map[ports.WindowID]uint64{id: seq}
	}
	return o.directShowsSnapshot
}
