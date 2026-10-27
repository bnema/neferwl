package app

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// outputChannels is the shared app-to-backend wiring. The backend loop remains
// the sole owner of outputSet and outputApply. Security producers use bounded
// lossless FIFO forwarders; contents are not routed here: each output is
// attached to wayland's content feed (contentAttacher).
// heads, configured and replies are fed from the outputApply outbox.
type outputChannels struct {
	security        ports.SessionSecurity
	securityChanges <-chan domain.SecurityState
	securityEvents  chan<- ports.SecurityBackendEvent
	leaseRequests   <-chan ports.LeaseMessage
	leaseEvents     chan<- ports.LeaseMessage
	events          chan<- ports.OutputEvent
	scenes          <-chan []domain.Scene
	content         contentAttacher
	cursorChanges   <-chan domain.CursorChange
	presented       ports.PresentationSink
	frames          chan<- domain.OutputFrame
	captures        <-chan domain.CaptureRequest
	captured        chan<- domain.CaptureDone
	formats         chan<- domain.OutputFormats
	heads           chan<- domain.OutputHeads
	// reloads carries file configuration; configured carries the effective
	// configuration, overrides included, to core.
	reloads    <-chan domain.ConfigChanged
	configured chan<- domain.ConfigChanged
	requests   <-chan domain.OutputApply
	replies    chan<- domain.OutputApplied
}

// routes are the inputs the output router feeds the outputs from.
func (ch outputChannels) routes() outputRoutes {
	return outputRoutes{scenes: ch.scenes, cursor: ch.cursorChanges, captures: ch.captures}
}
