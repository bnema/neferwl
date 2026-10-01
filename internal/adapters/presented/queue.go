// Package presented queues the OutputPresented reports an output owner sends
// to wayland: what the output read, held and flipped.
package presented

import (
	"context"
	"maps"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// MaxUnsent bounds reports waiting for wayland; past it the oldest flip is
// merged into the next one.
const MaxUnsent = 16

// Queue holds reports wayland has not read yet. Its owner goroutine is the
// only user. The Seen and ChildReads maps of a queued report are immutable
// snapshots: wayland may still read an earlier report on another goroutine,
// so a snapshot is replaced by a clone when its content changes and an
// unchanged report allocates nothing.
type Queue struct {
	unsent      []ports.OutputPresented
	seen, reads map[ports.WindowID]uint64
}

// Push queues r. Its Seen and ChildReads may be the caller's scratch maps:
// the queue keeps snapshots. A flip-less report replaces the newest queued
// one when that one has no flip, or keeps its flip; flips are merged only
// past MaxUnsent. Push reports whether a flip was merged that way.
func (q *Queue) Push(r ports.OutputPresented) (merged bool) {
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
		return false
	}
	q.unsent = append(q.unsent, r)
	if len(q.unsent) <= MaxUnsent {
		return false
	}
	if old, next := q.unsent[0].Flip, q.unsent[1].Flip; next != nil && old != nil {
		mergeFlip(old, next)
	}
	q.unsent = slices.Delete(q.unsent, 0, 1)
	return true
}

// mergeFlip folds old into next: next counts it as merged and shows what
// old showed, so feedbacks waiting on old are answered (as discarded).
func mergeFlip(old, next *ports.FlipInfo) {
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

// Flush sends queued reports without blocking.
func (q *Queue) Flush(ch chan<- ports.OutputPresented) {
	for len(q.unsent) > 0 {
		select {
		case ch <- q.unsent[0]:
			q.unsent = slices.Delete(q.unsent, 0, 1)
		default:
			return
		}
	}
}

// Drain sends every queued report, blocking until wayland reads them or ctx
// ends.
func (q *Queue) Drain(ctx context.Context, ch chan<- ports.OutputPresented) error {
	for len(q.unsent) > 0 {
		select {
		case ch <- q.unsent[0]:
			q.unsent = slices.Delete(q.unsent, 0, 1)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
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
