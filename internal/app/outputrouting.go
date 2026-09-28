package app

import (
	"context"

	"github.com/bnema/neferwl/internal/ports"
)

// outputChannels is the shared app-to-backend wiring. The backend loop remains
// the sole owner of outputSet and outputApply; routing does not introduce a
// forwarding queue.
type outputChannels struct {
	events        chan<- ports.OutputEvent
	scenes        <-chan []ports.Scene
	contents      <-chan ports.SurfaceContent
	cursorChanges <-chan ports.CursorChange
	presented     chan<- ports.OutputPresented
	captures      <-chan ports.CaptureRequest
	captured      chan<- ports.CaptureDone
	formats       chan<- ports.OutputFormats
	heads         chan<- ports.OutputHeads
	// reloads carries file configuration; configured carries the effective
	// configuration, overrides included, to core.
	reloads    <-chan ports.ConfigChanged
	configured chan<- ports.ConfigChanged
	requests   <-chan ports.OutputApply
	replies    chan<- ports.OutputApplied
}

// sendHeads publishes the inventory to Wayland clients; false means the
// context ended first.
func (ch outputChannels) sendHeads(ctx context.Context, inventory ports.OutputHeads) bool {
	select {
	case ch.heads <- inventory:
		return true
	case <-ctx.Done():
		return false
	}
}
