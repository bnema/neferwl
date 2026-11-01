// Package presented queues the OutputPresented reports an output owner sends

// to wayland: what the output read, held and flipped.
package presented

import (
	"maps"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Queue holds reports awaiting transport. Its owner alone calls its methods.
// Maps remain immutable until all report consumers release their snapshots.
type Queue struct {
	unsent   []ports.OutputPresented
	latest   *ports.PresentationSnapshot
	returned chan *ports.PresentationSnapshot
}

// Push queues r. Its Seen and ChildReads may be the caller's scratch maps:
// the queue keeps snapshots. A flip-less report replaces the newest queued
// one when that one has no flip, or keeps its flip; every flip is queued.
func (q *Queue) Push(r ports.OutputPresented) {
	if q.latest == nil || !maps.Equal(q.latest.Seen, r.Seen) || !maps.Equal(q.latest.ChildReads, r.ChildReads) {
		if q.returned == nil {
			q.returned = make(chan *ports.PresentationSnapshot, 128)
		}
		var snapshot *ports.PresentationSnapshot
		select {
		case snapshot = <-q.returned:
		default:
			snapshot = &ports.PresentationSnapshot{}
		}
		snapshot.Init(q.returned)
		snapshot.Seen = copyMap(snapshot.Seen, r.Seen)
		snapshot.ChildReads = copyMap(snapshot.ChildReads, r.ChildReads)
		q.latest.Release()
		q.latest = snapshot
	}
	q.latest.Retain()
	r.Seen, r.ChildReads, r.Snapshot = q.latest.Seen, q.latest.ChildReads, q.latest
	if n := len(q.unsent); n > 0 && (r.Flip == nil || q.unsent[n-1].Flip == nil) {
		if r.Flip == nil {
			r.Flip = q.unsent[n-1].Flip
		}
		q.unsent[n-1].Snapshot.Release()
		q.unsent[n-1] = r
		return
	}
	q.unsent = append(q.unsent, r)
}

// Merge folds old into next: next counts it as merged and shows what
// old showed, so feedbacks waiting on old are answered (as discarded).
// Wayland's presentation sink uses it when its ring overflows.
func Merge(old, next *domain.FlipInfo) {
	next.Merged += 1 + old.Merged
	// The reasons, like ZeroCopy and Async, stay the newest flip's.
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
			next.Shows = make(map[domain.WindowID]uint64)
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
	clear(q.unsent)
	q.unsent = q.unsent[:0]
}

// Len is the number of queued reports.
func (q *Queue) Len() int { return len(q.unsent) }

// Unsent returns the queued reports, oldest first. Callers must not modify
// them.
func (q *Queue) Unsent() []ports.OutputPresented { return q.unsent }

// Seen is the Seen snapshot of the last pushed report.
func (q *Queue) Seen() map[domain.WindowID]uint64 {
	if q.latest == nil {
		return nil
	}
	return q.latest.Seen
}

// Reset drops queued reports, e.g. when a security epoch makes them stale.
// The owner retains the latest snapshot; sent reports retain their own reference.
func (q *Queue) Reset() {
	for _, r := range q.unsent {
		r.Snapshot.Release()
	}
	clear(q.unsent)
	q.unsent = q.unsent[:0]
}

func copyMap(dst, src map[domain.WindowID]uint64) map[domain.WindowID]uint64 {
	if dst == nil && len(src) > 0 {
		dst = make(map[domain.WindowID]uint64, len(src))
	}
	clear(dst)
	maps.Copy(dst, src)
	return dst
}
