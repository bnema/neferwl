// Package presented queues the OutputPresented reports an output owner sends
// to wayland: what the output read, held and flipped.
package presented

import (
	"maps"

	"github.com/bnema/neferwl/internal/ports"
)

// Queue holds the reports an output has not pushed to wayland yet. The owner
// goroutine flushes it every loop turn, so it holds the few reports of one
// turn; the sink alone bounds what waits for the display loop (and merges
// flips when that stalls). Its owner goroutine is the only user. The Seen and
// ChildReads maps of a queued report are immutable snapshots: wayland may
// still read an earlier report on another goroutine, so a snapshot is
// replaced by a clone when its content changes and an unchanged report
// allocates nothing.
type Queue struct {
	unsent      []ports.OutputPresented
	seen, reads map[ports.WindowID]uint64
}

// Push queues r. Its Seen and ChildReads may be the caller's scratch maps:
// the queue keeps snapshots. A flip-less report replaces the newest queued
// one when that one has no flip, or keeps its flip; every flip is queued.
func (q *Queue) Push(r ports.OutputPresented) {
	if !maps.Equal(q.seen, r.Seen) {
		q.seen = maps.Clone(r.Seen)
	}
	if !maps.Equal(q.reads, r.ChildReads) {
		q.reads = maps.Clone(r.ChildReads)
	}
	r.Seen, r.ChildReads = q.seen, q.reads
	if n := len(q.unsent); n > 0 && (r.Flip == nil || q.unsent[n-1].Flip == nil) {
		if r.Flip == nil {
			r.Flip = q.unsent[n-1].Flip
		}
		q.unsent[n-1] = r
		return
	}
	q.unsent = append(q.unsent, r)
}

// Merge folds old into next: next counts it as merged and shows what
// old showed, so feedbacks waiting on old are answered (as discarded).
// Wayland's presentation sink uses it when its ring overflows.
func Merge(old, next *ports.FlipInfo) {
	next.Merged += 1 + old.Merged
	// A snapshot can be shared with previously sent flips. Clone only when
	// a merge actually needs to add an absent window.
	cloned := false
	for id, seq := range old.Shows {
		if _, ok := next.Shows[id]; ok {
			continue
		}
		if !cloned {
			next.Shows = maps.Clone(next.Shows)
			cloned = true
		}
		if next.Shows == nil {
			next.Shows = make(map[ports.WindowID]uint64)
		}
		next.Shows[id] = seq
	}
}

// Flush pushes every queued report to sink, oldest first, and empties the
// queue. Push never blocks.
func (q *Queue) Flush(sink ports.PresentationSink) {
	for _, r := range q.unsent {
		sink.Push(r)
	}
	q.Reset()
}

// Len is the number of queued reports.
func (q *Queue) Len() int { return len(q.unsent) }

// Unsent returns the queued reports, oldest first. Callers must not modify
// them.
func (q *Queue) Unsent() []ports.OutputPresented { return q.unsent }

// Seen is the Seen snapshot of the last pushed report.
func (q *Queue) Seen() map[ports.WindowID]uint64 { return q.seen }

// Reset drops queued reports, e.g. when a security epoch makes them stale.
// Snapshots stay: they may be shared with reports already sent.
func (q *Queue) Reset() {
	clear(q.unsent)
	q.unsent = q.unsent[:0]
}
