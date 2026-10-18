package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"

	"github.com/bnema/neferwl/internal/ports"
)

// outputRun drives one output until ctx ends: it renders the scenes and
// surface contents it receives, with the cursor the client asks for.
type outputRun func(ctx context.Context, feed outputFeed) error

// outputFeed is what an output lifetime runs on. StartOff tells that the
// latest scene for its name turned it off: it starts off until core's first
// scene (a display reconnecting from deep sleep stays dark).
type outputFeed struct {
	Scenes   <-chan ports.Scene
	Contents <-chan *ports.ContentBatch
	Returns  ports.ContentReturn
	Cursor   <-chan ports.CursorChange
	Captures <-chan ports.CaptureRequest
	Security <-chan ports.SecurityState
	Instance ports.OutputInstance
	StartOff bool
}

// contentAttacher connects an output to the surface contents: wayland feeds
// every attached output batches of window contents, starting with the latest
// content of each window, and takes the batches back through the returned
// ports.ContentReturn. AttachOutput may block on the display goroutine: call it
// from outside that goroutine. It returns a nil channel when wayland is gone.
// The returned ports.ContentReturn is also the handle of the feed: attaching
// a name that already has a feed replaces it, and DetachOutput(handle) stops
// that feed only, so a stale handle never stops a newer feed of the same
// name. Detach an output once it stopped.
type contentAttacher interface {
	AttachOutput(name string) (<-chan *ports.ContentBatch, ports.ContentReturn)
	DetachOutput(feed ports.ContentReturn)
}

// outputSet owns the output lifetimes: start and stop, security
// registration and content feeds. Only the backend loop touches it. Scenes,
// cursor and captures reach the outputs through the router, its own
// goroutine, so lifecycle work here never delays another output's frames.
// Surface contents reach every output (windows move between outputs)
// straight from wayland through attach, which also replays the current
// windows to a new output.
type outputSet struct {
	outs   map[string]*runningOutput
	router *outputRouter
	// stopRouter ends the router (router.done closes once it returned).
	stopRouter context.CancelFunc
	// attach is nil when no wayland server feeds contents (tests).
	attach         contentAttacher
	stopped        chan outputStopped
	captured       chan<- ports.CaptureDone
	ctx            context.Context
	quit           chan struct{} // closed by wait: nobody reads stopped any more
	security       ports.SessionSecurity
	securityEvents chan<- ports.SecurityBackendEvent
	state          ports.SecurityState
	nextInstance   ports.OutputInstance
	startErr       error
	barriers       chan ports.SecurityBackendEvent
	forwarders     sync.WaitGroup
}

// A stop is tied to a lifetime, never just a reusable connector name.
type outputStopped struct {
	name     string
	instance ports.OutputInstance
}

// runningOutput is one output lifetime: the mailbox the router feeds, and
// what the backend loop owns.
type runningOutput struct {
	*mailbox
	security       chan ports.SecurityState
	securityEvents chan ports.SecurityBackendEvent
	err            error
	// feed is the content feed AttachOutput returned (nil: none), the handle
	// to detach once the output stopped.
	feed ports.ContentReturn
}

// newOutputSet returns a set whose router feeds the outputs from in, until
// ctx ends or wait stops it.
func newOutputSet(ctx context.Context, captured chan<- ports.CaptureDone, in outputRoutes) *outputSet {
	rctx, stopRouter := context.WithCancel(ctx)
	s := &outputSet{captured: captured, ctx: ctx, outs: map[string]*runningOutput{}, router: newOutputRouter(ctx, captured, in), stopRouter: stopRouter, stopped: make(chan outputStopped), quit: make(chan struct{})}
	go s.router.run(rctx)
	return s
}

// start runs an output in its own goroutine, fed the window contents.
func (s *outputSet) start(ctx context.Context, name string, run outputRun, wire ...*chan<- ports.SecurityBackendEvent) bool {
	// Exhaustion is permanent: never wrap to zero or reuse a lifetime.
	if s.nextInstance == ^ports.OutputInstance(0) {
		s.startErr = fmt.Errorf("output instance exhausted")
		return false
	}
	// Register authoritatively before an owner can render or replay contents.
	s.nextInstance++
	instance := s.nextInstance
	if !s.securityEvent(ports.SecurityOutputAdded{Instance: instance, Output: name}) {
		return false
	}
	if s.security != nil {
		s.setSecurity(s.security.Snapshot())
	}
	octx, stop := context.WithCancel(ctx)
	r := &runningOutput{mailbox: newMailbox(instance, octx, stop, make(chan struct{})), security: make(chan ports.SecurityState, 1)}
	r.security <- s.state // available before any content replay
	r.securityEvents = s.forwardSecurity()
	for _, destination := range wire {
		*destination = r.securityEvents
	}
	// After the security state above: the replay of the windows starts here.
	feed := outputFeed{Scenes: r.scenes, Cursor: r.cursor, Captures: r.captures, Security: r.security, Instance: instance}
	if s.attach != nil {
		feed.Contents, feed.Returns = s.attach.AttachOutput(name)
	}
	r.feed = feed.Returns
	// The router feeds it from now on, current cursor first.
	off, ok := s.router.add(name, r.mailbox)
	if !ok {
		stop()
		s.detach(r)
		return false
	}
	feed.StartOff = off
	s.outs[name] = r
	go func() {
		r.err = run(octx, feed)
		if r.securityEvents != nil {
			close(r.securityEvents)
		}
		stop()
		close(r.done)
		select {
		case s.stopped <- outputStopped{name: name, instance: instance}:
		case <-s.quit:
		}
	}()
	return true
}

func (s *outputSet) wireSecurity(ch outputChannels) {
	s.security, s.securityEvents = ch.security, ch.securityEvents
	if ch.securityEvents != nil {
		s.barriers = s.forwardSecurity()
	}
	if s.security != nil {
		s.setSecurity(s.security.Snapshot())
	}
}

// securityEvent registers/removes directly, while barriers use a bounded FIFO.
// No authoritative event is merged or discarded during an active backend.
func (s *outputSet) securityEvent(ev ports.SecurityBackendEvent) bool {
	if s.securityEvents == nil {
		return true // security disabled
	}
	destination := s.securityEvents
	if _, ok := ev.(ports.SecurityBackendBarrier); ok && s.barriers != nil {
		destination = s.barriers
	}
	select {
	case destination <- ev:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// Each producer has a finite, FIFO outbox. Saturation applies backpressure;
// events are never merged. Registration/removal bypass it for lifecycle ordering.
func (s *outputSet) forwardSecurity() chan ports.SecurityBackendEvent {
	if s.securityEvents == nil {
		return nil
	}
	incoming := make(chan ports.SecurityBackendEvent, 16)
	s.forwarders.Add(1)
	go func() {
		defer s.forwarders.Done()
		for {
			select {
			case ev, ok := <-incoming:
				if !ok {
					return
				}
				select {
				case s.securityEvents <- ev:
				case <-s.ctx.Done():
					return
				case <-s.quit:
					return
				}
			case <-s.ctx.Done():
				return
			case <-s.quit:
				return
			}
		}
	}()
	return incoming
}

func (s *outputSet) setSecurity(state ports.SecurityState) bool {
	if s.security != nil {
		if newest := s.security.Snapshot(); newest.Generation > state.Generation {
			state = newest
		}
	}
	if state.Generation < s.state.Generation || state == s.state {
		return false
	}
	s.state = state
	for _, r := range s.outs {
		latest(r.security, state)
	}
	return true
}

// stoppedCurrent rejects delayed stop messages from a previous incarnation.
func (s *outputSet) stoppedCurrent(stopped outputStopped) bool {
	r := s.outs[stopped.name]
	return r != nil && r.instance == stopped.instance
}

// finish forgets a stopped output and returns its error.
func (s *outputSet) finish(name string) error {
	r := s.outs[name]
	if r == nil {
		return nil
	}
	<-r.done
	r.stop()
	// Once the router no longer feeds it, nothing else queues a capture.
	s.router.remove(name, r.instance)
	failQueued(s.ctx, r.mailbox, s.captured)
	s.detach(r)
	delete(s.outs, name)
	return r.err
}

// detach stops the content feed of an output that has stopped.
func (s *outputSet) detach(r *runningOutput) {
	if s.attach != nil && r.feed != nil {
		s.attach.DetachOutput(r.feed)
	}
}

// wait stops every output and returns the first error.
func (s *outputSet) wait() error {
	var first error = s.startErr
	// Cancel producers before closing the forwarding quit path. Owners may
	// be backpressured on a full FIFO, and need their context to exit.
	for _, r := range s.outs {
		r.stop()
	}
	close(s.quit)
	s.forwarders.Wait()
	// No capture is routed past this point.
	s.stopRouter()
	<-s.router.done
	for name, r := range s.outs {
		r.stop()
		<-r.done
		failQueued(s.ctx, r.mailbox, s.captured)
		s.detach(r)
		if first == nil && r.err != nil {
			first = r.err
		}
		delete(s.outs, name)
	}
	return first
}

// cursors routes pointer moves from the input goroutine to the cursor of
// the output under the pointer and hides the others. After hideAfter
// without motion it hides that cursor too; the next motion shows it.
type cursors struct {
	mu    sync.Mutex
	all   map[string]cursor
	last  string
	clock ports.Clock
	// hideAfter is 0 when the cursor never hides. lastMove is when the
	// latest motion came; armed tells the timer runs; hidden tells the
	// cursor is idle-hidden. The timer is not reset on every motion: when
	// it fires early it re-arms for the time left.
	hideAfter time.Duration
	lastMove  time.Time
	timer     ports.Timer
	armed     bool
	hidden    bool
}

// cursor is a hardware or software cursor of one output.
type cursor interface {
	Move(x, y float64)
	Hide()
}

func newCursors(clock ports.Clock, hideAfter time.Duration) *cursors {
	return &cursors{all: map[string]cursor{}, clock: clock, hideAfter: hideAfter}
}

// setHideAfter changes the idle delay; 0 never hides the cursor. A cursor
// already hidden stays hidden until the next motion.
func (c *cursors) setHideAfter(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d == c.hideAfter {
		return
	}
	c.hideAfter = d
	c.disarm()
	if d > 0 && c.last != "" && !c.hidden {
		c.lastMove = c.clock.Now()
		c.arm(d)
	}
}

// stop cancels a pending hide; the router is not used after.
func (c *cursors) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disarm()
}

// disarm stops the idle timer. Callers hold mu.
func (c *cursors) disarm() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.armed = false
}

// arm starts the idle timer for d. Callers hold mu.
func (c *cursors) arm(d time.Duration) {
	c.armed = true
	if c.timer == nil {
		c.timer = c.clock.AfterFunc(d, c.idle)
		return
	}
	c.timer.Reset(d)
}

// idle hides the cursor once hideAfter passed since the last move, or
// re-arms the timer for the time left.
func (c *cursors) idle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.armed || c.hideAfter <= 0 {
		return
	}
	if left := c.hideAfter - c.clock.Now().Sub(c.lastMove); left > 0 {
		c.timer.Reset(left)
		return
	}
	c.armed, c.hidden = false, true
	if cur := c.all[c.last]; cur != nil {
		cur.Hide()
	}
}

// set registers the cursor of an output; nil removes it.
func (c *cursors) set(output string, cur cursor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur == nil {
		delete(c.all, output)
		return
	}
	c.all[output] = cur
	// Only the output under the pointer shows it; before the first move,
	// none does (a new cursor would show at its top-left corner). An idle
	// hidden cursor stays hidden.
	if output != c.last || c.hidden {
		cur.Hide()
	}
}

// move places the cursor on output at target pixels (x, y): physical, after
// the output transform. motion is false
// when the pointer is only placed again (layout or constraint change): it
// neither shows an idle-hidden cursor nor delays the hide.
func (c *cursors) move(output string, x, y float64, motion bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if motion {
		c.hidden = false
	}
	if output != c.last {
		for name, cur := range c.all {
			if name != output {
				cur.Hide()
			}
		}
		c.last = output
	}
	if c.hidden {
		return // the next motion moves it with fresh coordinates
	}
	if cur := c.all[output]; cur != nil {
		cur.Move(x, y)
	}
	if c.hideAfter > 0 && (motion || !c.armed) {
		c.lastMove = c.clock.Now()
		if !c.armed {
			c.arm(c.hideAfter)
		}
	}
}

// routeCapture transfers the request to its output or closes it on failure.
// failQueued fails the captures left in a stopped output's mailbox.
func failQueued(ctx context.Context, m *mailbox, captured chan<- ports.CaptureDone) {
	for {
		select {
		case req := <-m.captures:
			capture.Fail(ctx, req, capture.ErrOutputStopped, captured)
		default:
			return
		}
	}
}

// drainCaptures releases requests left in the app queue. Call it once the
// producer (the wayland server) has stopped, so none can arrive after it.
func drainCaptures(ctx context.Context, incoming <-chan ports.CaptureRequest, replies chan<- ports.CaptureDone) {
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
