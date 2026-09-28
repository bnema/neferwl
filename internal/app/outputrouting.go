package app

import (
	"context"

	"github.com/bnema/neferwl/internal/ports"
)

// outputChannels is the shared app-to-backend wiring. The backend loop remains
// the sole owner of outputSet; routing does not introduce a forwarding queue.
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
	report        chan<- ports.OutputHeads
	configs       <-chan ports.Config
	applied       chan<- error
}

// sendInventory preserves heads-before-report delivery; false means the
// context interrupted either send.
func (ch outputChannels) sendInventory(ctx context.Context, inventory ports.OutputHeads) bool {
	select {
	case ch.heads <- inventory:
	case <-ctx.Done():
		return false
	}
	select {
	case ch.report <- inventory:
		return true
	case <-ctx.Done():
		return false
	}
}
