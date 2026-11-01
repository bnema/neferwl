package routing

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Channels is the shared app-to-backend wiring. The backend loop remains
// the sole owner of Set and outputApply. Security producers use bounded
// lossless FIFO forwarders; contents are not routed here: each output is
// attached to wayland's content feed (ContentAttacher).
// heads, configured and replies are fed from the outputApply outbox.
type Channels struct {
	Security        ports.SessionSecurity
	SecurityChanges <-chan domain.SecurityState
	SecurityEvents  chan<- ports.SecurityBackendEvent
	LeaseRequests   <-chan ports.LeaseMessage
	LeaseEvents     chan<- ports.LeaseMessage
	Events          chan<- ports.OutputEvent
	Scenes          <-chan []domain.Scene
	Content         ContentAttacher
	CursorChanges   <-chan domain.CursorChange
	Presented       ports.PresentationSink
	Frames          chan<- domain.OutputFrame
	Captures        <-chan domain.CaptureRequest
	Captured        chan<- domain.CaptureDone
	Formats         chan<- domain.OutputFormats
	Heads           chan<- domain.OutputHeads
	// reloads carries file configuration; configured carries the effective
	// configuration, overrides included, to core.
	Reloads    <-chan domain.ConfigChanged
	Configured chan<- domain.ConfigChanged
	Requests   <-chan domain.OutputApply
	Replies    chan<- domain.OutputApplied
}

// routes are the inputs the output router feeds the outputs from.
func (ch Channels) Routes() Routes {
	return Routes{Scenes: ch.Scenes, Cursor: ch.CursorChanges, Captures: ch.Captures}
}
