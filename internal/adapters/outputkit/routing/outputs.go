package routing

import (
	"context"
	"fmt"
	"sync"

	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Run drives one output until ctx ends: it renders the scenes and
// surface contents it receives, with the cursor the client asks for.
type Run func(ctx context.Context, feed Feed) error

// Feed is what an output lifetime runs on. StartOff tells that the
// latest scene for its name turned it off: it starts off until core's first
// scene (a display reconnecting from deep sleep stays dark).
type Feed struct {
	Scenes   <-chan domain.Scene
	Contents <-chan *domain.ContentBatch
	Returns  ports.ContentReturn
	Cursor   <-chan domain.CursorChange
	Captures <-chan domain.CaptureRequest
	Security <-chan domain.SecurityState
	Instance domain.OutputInstance
	StartOff bool
}

// ContentAttacher connects an output to the surface contents: wayland feeds
// every attached output batches of window contents, starting with the latest
// content of each window, and takes the batches back through the returned
// ports.ContentReturn. AttachOutput may block on the display goroutine: call it
// from outside that goroutine. It returns a nil channel when wayland is gone.
// The returned ports.ContentReturn is also the handle of the feed: attaching
// a name that already has a feed replaces it, and DetachOutput(handle) stops
// that feed only, so a stale handle never stops a newer feed of the same
// name. Detach an output once it stopped.
type ContentAttacher interface {
	AttachOutput(name string) (<-chan *domain.ContentBatch, ports.ContentReturn)
	DetachOutput(feed ports.ContentReturn)
}

// Set owns the output lifetimes: start and stop, security
// registration and content feeds. Only the backend loop touches it. Scenes,
// cursor and captures reach the outputs through the router, its own
// goroutine, so lifecycle work here never delays another output's frames.
// Surface contents reach every output (windows move between outputs)
// straight from wayland through attach, which also replays the current
// windows to a new output.
type Set struct {
	Outs   map[string]*RunningOutput
	Router *Router
	// stopRouter ends the router (router.done closes once it returned).
	StopRouter context.CancelFunc
	// attach is nil when no wayland server feeds contents (tests).
	Attach         ContentAttacher
	Stopped        chan Stopped
	Captured       chan<- domain.CaptureDone
	Ctx            context.Context
	Quit           chan struct{} // closed by wait: nobody reads stopped any more
	Security       ports.SessionSecurity
	SecurityEvents chan<- ports.SecurityBackendEvent
	State          domain.SecurityState
	NextInstance   domain.OutputInstance
	StartErr       error
	Barriers       chan ports.SecurityBackendEvent
	Forwarders     sync.WaitGroup
}

// A stop is tied to a lifetime, never just a reusable connector name.
type Stopped struct {
	Name     string
	Instance domain.OutputInstance
}

// RunningOutput is one output lifetime: the Mailbox the router feeds, and
// what the backend loop owns.
type RunningOutput struct {
	*Mailbox
	Security       chan domain.SecurityState
	SecurityEvents chan ports.SecurityBackendEvent
	Err            error
	// feed is the content feed AttachOutput returned (nil: none), the handle
	// to detach once the output stopped.
	Feed ports.ContentReturn
}

// NewSet returns a set whose router feeds the outputs from in, until
// ctx ends or wait stops it.
func NewSet(ctx context.Context, captured chan<- domain.CaptureDone, in Routes) *Set {
	rctx, stopRouter := context.WithCancel(ctx)
	s := &Set{Captured: captured, Ctx: ctx, Outs: map[string]*RunningOutput{}, Router: NewRouter(ctx, captured, in), StopRouter: stopRouter, Stopped: make(chan Stopped), Quit: make(chan struct{})}
	go s.Router.Run(rctx)
	return s
}

// Start runs an output in its own goroutine, fed the window contents.
func (s *Set) Start(ctx context.Context, name string, run Run, wire ...*chan<- ports.SecurityBackendEvent) bool {
	// Exhaustion is permanent: never wrap to zero or reuse a lifetime.
	if s.NextInstance == ^domain.OutputInstance(0) {
		s.StartErr = fmt.Errorf("output instance exhausted")
		return false
	}
	// Register authoritatively before an owner can render or replay contents.
	s.NextInstance++
	instance := s.NextInstance
	if !s.SecurityEvent(ports.SecurityOutputAdded{Instance: instance, Output: name}) {
		return false
	}
	if s.Security != nil {
		s.SetSecurity(s.Security.Snapshot())
	}
	octx, stop := context.WithCancel(ctx)
	r := &RunningOutput{Mailbox: NewMailbox(instance, octx, stop, make(chan struct{})), Security: make(chan domain.SecurityState, 1)}
	r.Security <- s.State // available before any content replay
	r.SecurityEvents = s.ForwardSecurity()
	for _, destination := range wire {
		*destination = r.SecurityEvents
	}
	// After the security state above: the replay of the windows starts here.
	feed := Feed{Scenes: r.Scenes, Cursor: r.Cursor, Captures: r.Captures, Security: r.Security, Instance: instance}
	if s.Attach != nil {
		feed.Contents, feed.Returns = s.Attach.AttachOutput(name)
	}
	r.Feed = feed.Returns
	// The router feeds it from now on, current cursor first.
	off, ok := s.Router.Add(name, r.Mailbox)
	if !ok {
		stop()
		s.Detach(r)
		return false
	}
	feed.StartOff = off
	s.Outs[name] = r
	go func() {
		r.Err = run(octx, feed)
		if r.SecurityEvents != nil {
			close(r.SecurityEvents)
		}
		stop()
		close(r.Done)
		select {
		case s.Stopped <- Stopped{Name: name, Instance: instance}:
		case <-s.Quit:
		}
	}()
	return true
}

func (s *Set) WireSecurity(ch Channels) {
	s.Security, s.SecurityEvents = ch.Security, ch.SecurityEvents
	if ch.SecurityEvents != nil {
		s.Barriers = s.ForwardSecurity()
	}
	if s.Security != nil {
		s.SetSecurity(s.Security.Snapshot())
	}
}

// SecurityEvent registers/removes directly, while barriers use a bounded FIFO.
// No authoritative event is merged or discarded during an active backend.
func (s *Set) SecurityEvent(ev ports.SecurityBackendEvent) bool {
	if s.SecurityEvents == nil {
		return true // security disabled
	}
	destination := s.SecurityEvents
	if _, ok := ev.(ports.SecurityBackendBarrier); ok && s.Barriers != nil {
		destination = s.Barriers
	}
	select {
	case destination <- ev:
		return true
	case <-s.Ctx.Done():
		return false
	}
}

// Each producer has a finite, FIFO outbox. Saturation applies backpressure;
// events are never merged. Registration/removal bypass it for lifecycle ordering.
func (s *Set) ForwardSecurity() chan ports.SecurityBackendEvent {
	if s.SecurityEvents == nil {
		return nil
	}
	incoming := make(chan ports.SecurityBackendEvent, 16)
	s.Forwarders.Add(1)
	go func() {
		defer s.Forwarders.Done()
		for {
			select {
			case ev, ok := <-incoming:
				if !ok {
					return
				}
				select {
				case s.SecurityEvents <- ev:
				case <-s.Ctx.Done():
					return
				case <-s.Quit:
					return
				}
			case <-s.Ctx.Done():
				return
			case <-s.Quit:
				return
			}
		}
	}()
	return incoming
}

func (s *Set) SetSecurity(state domain.SecurityState) bool {
	if s.Security != nil {
		if newest := s.Security.Snapshot(); newest.Generation > state.Generation {
			state = newest
		}
	}
	if state.Generation < s.State.Generation || state == s.State {
		return false
	}
	s.State = state
	for _, r := range s.Outs {
		Latest(r.Security, state)
	}
	return true
}

// StoppedCurrent rejects delayed stop messages from a previous incarnation.
func (s *Set) StoppedCurrent(stopped Stopped) bool {
	r := s.Outs[stopped.Name]
	return r != nil && r.Instance == stopped.Instance
}

// Finish forgets a stopped output and returns its error.
func (s *Set) Finish(name string) error {
	r := s.Outs[name]
	if r == nil {
		return nil
	}
	<-r.Done
	r.Stop()
	// Once the router no longer feeds it, nothing else queues a capture.
	s.Router.Remove(name, r.Instance)
	failQueued(s.Ctx, r.Mailbox, s.Captured)
	s.Detach(r)
	delete(s.Outs, name)
	return r.Err
}

// Detach stops the content feed of an output that has stopped.
func (s *Set) Detach(r *RunningOutput) {
	if s.Attach != nil && r.Feed != nil {
		s.Attach.DetachOutput(r.Feed)
	}
}

// Wait stops every output and returns the first error.
func (s *Set) Wait() error {
	var first error = s.StartErr
	// Cancel producers before closing the forwarding quit path. Owners may
	// be backpressured on a full FIFO, and need their context to exit.
	for _, r := range s.Outs {
		r.Stop()
	}
	close(s.Quit)
	s.Forwarders.Wait()
	// No capture is routed past this point.
	s.StopRouter()
	<-s.Router.done
	for name, r := range s.Outs {
		r.Stop()
		<-r.Done
		failQueued(s.Ctx, r.Mailbox, s.Captured)
		s.Detach(r)
		if first == nil && r.Err != nil {
			first = r.Err
		}
		delete(s.Outs, name)
	}
	return first
}

// routeCapture transfers the request to its output or closes it on failure.
// failQueued fails the captures left in a stopped output's Mailbox.
func failQueued(ctx context.Context, m *Mailbox, captured chan<- domain.CaptureDone) {
	for {
		select {
		case req := <-m.Captures:
			capture.Fail(ctx, req, capture.ErrOutputStopped, captured)
		default:
			return
		}
	}
}

// DrainCaptures releases requests left in the app queue. Call it once the
// producer (the wayland server) has stopped, so none can arrive after it.
func DrainCaptures(ctx context.Context, incoming <-chan domain.CaptureRequest, replies chan<- domain.CaptureDone) {
	for {
		select {
		case q, ok := <-incoming:
			if !ok {
				return
			}
			capture.Fail(ctx, q, capture.ErrOutputStopped, replies)
		default:
			return
		}
	}
}
