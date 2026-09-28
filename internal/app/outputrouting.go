package app

import "github.com/bnema/neferwl/internal/ports"

// outputChannels is the shared app-to-backend wiring. The backend loop remains
// the sole owner of outputSet and outputApply; routing does not introduce a
// forwarding queue. heads, configured and replies are fed from the
// outputApply outbox.
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
