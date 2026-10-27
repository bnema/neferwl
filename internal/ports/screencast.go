package ports

import "github.com/bnema/neferwl/internal/domain"

// CastSink receives wayland → screencast messages. Push never blocks:
// the screencast adapter queues the event for its owner goroutine, and drops
// nothing that releases a buffer or ends a cast (CastFrameDone, CastStopped).
// Inventory publishes the current outputs and windows after each change; the
// value and its slices belong to the receiver afterwards.
type CastSink interface {
	Push(domain.CastEvent)
	Inventory(domain.CastInventory)
}
