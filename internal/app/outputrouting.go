package app

import "github.com/bnema/neferwl/internal/ports"

// outputChannels is the shared app-to-backend wiring. The backend loop remains
// the sole owner of outputSet and outputApply. Security producers use bounded
// lossless FIFO forwarders; contents are not routed here: each output is
// attached to wayland's content feed (contentAttacher).
// heads, configured and replies are fed from the outputApply outbox.
type outputChannels struct {
	security        ports.SessionSecurity
	securityChanges <-chan ports.SecurityState
	securityEvents  chan<- ports.SecurityBackendEvent
	leaseRequests   <-chan ports.LeaseMessage
	leaseEvents     chan<- ports.LeaseMessage
	events          chan<- ports.OutputEvent
	scenes          <-chan []ports.Scene
	content         contentAttacher
	cursorChanges   <-chan ports.CursorChange
	presented       ports.PresentationSink
	frames          chan<- ports.OutputFrame
	captures        <-chan ports.CaptureRequest
	captured        chan<- ports.CaptureDone
	formats         chan<- ports.OutputFormats
	heads           chan<- ports.OutputHeads
	// reloads carries file configuration; configured carries the effective
	// configuration, overrides included, to core.
	reloads    <-chan ports.ConfigChanged
	configured chan<- ports.ConfigChanged
	requests   <-chan ports.OutputApply
	replies    chan<- ports.OutputApplied
}
