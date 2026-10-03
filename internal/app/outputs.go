package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/capture"

	"github.com/bnema/neferwl/internal/ports"
)

// outputRun drives one output until ctx ends: it renders the scenes and
// surface contents it receives, with the cursor the client asks for.
type outputRun func(ctx context.Context, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, captures <-chan ports.CaptureRequest, security <-chan ports.SecurityState, instance ports.OutputInstance) error

// outputSet owns the running outputs and feeds them. Core sends one scene
// per output; each goes to its output, latest first. Surface contents go to
// every output, since windows move between outputs; the latest content of
// each window is kept so a new output starts with every window drawn.
// Only the goroutine running loop touches the set.
type outputSet struct {
	outs   map[string]*runningOutput
	latest map[ports.WindowID]ports.SurfaceContent
	// off is the latest Scene.Off per output name, kept after the output
	// goes: it reconnects off until core's first scene (Output.StartOff).
	off            map[string]bool
	cursor         ports.CursorChange
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

type runningOutput struct {
	instance       ports.OutputInstance
	security       chan ports.SecurityState
	securityEvents chan ports.SecurityBackendEvent
	pending        []ports.SurfaceContent
	scenes         chan ports.Scene
	contents       chan ports.SurfaceContent
	cursor         chan ports.CursorChange
	captures       chan ports.CaptureRequest
	ctx            context.Context
	stop           context.CancelFunc
	err            error
	done           chan struct{}
}

func newOutputSet(ctx context.Context, captured chan<- ports.CaptureDone) *outputSet {
	return &outputSet{captured: captured, ctx: ctx, outs: map[string]*runningOutput{}, latest: map[ports.WindowID]ports.SurfaceContent{}, off: map[string]bool{}, stopped: make(chan outputStopped), quit: make(chan struct{})}
}

// start runs an output in its own goroutine and replays the window contents.
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
	r := &runningOutput{instance: instance, security: make(chan ports.SecurityState, 1), scenes: make(chan ports.Scene, 1), contents: make(chan ports.SurfaceContent, 64), cursor: make(chan ports.CursorChange, 1), captures: make(chan ports.CaptureRequest, 16), ctx: octx, stop: stop, done: make(chan struct{})}
	r.security <- s.state // available before any old scene/content replay
	r.securityEvents = s.forwardSecurity()
	for _, destination := range wire {
		*destination = r.securityEvents
	}
	for _, c := range s.latest {
		select {
		case r.contents <- c:
		default:
			r.pending = append(r.pending, c)
		}
	}
	s.outs[name] = r
	go func() {
		r.err = run(octx, r.scenes, r.contents, r.cursor, r.captures, r.security, r.instance)
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
	r.cursor <- s.cursor
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
		select {
		case <-r.security:
		default:
		}
		r.security <- state
	}
	return true
}

// stoppedCurrent rejects delayed stop messages from a previous incarnation.
func (s *outputSet) stoppedCurrent(stopped outputStopped) bool {
	r := s.outs[stopped.name]
	return r != nil && r.instance == stopped.instance
}

// setCursor gives every output the latest cursor, replacing an unread one.
func (s *outputSet) setCursor(c ports.CursorChange) {
	s.cursor = c
	for _, r := range s.outs {
		select {
		case <-r.cursor:
		default:
		}
		r.cursor <- c
	}
}

// scenes routes each scene to its output, replacing an unread one.
func (s *outputSet) scenes(set []ports.Scene) {
	for _, sc := range set {
		if sc.Off {
			s.off[sc.Output] = true
		} else {
			delete(s.off, sc.Output)
		}
		if r := s.outs[sc.Output]; r != nil {
			select {
			case <-r.scenes:
			default:
			}
			r.scenes <- sc
		}
	}
}

func (s *outputSet) content(c ports.SurfaceContent) {
	if c.Empty() {
		delete(s.latest, c.ID)
	} else {
		s.latest[c.ID] = c
	}
	for _, r := range s.outs {
		if r.ctx.Err() != nil {
			continue
		}
		if len(r.pending) == 0 {
			select {
			case r.contents <- c:
				continue
			default:
			}
		}
		// The backend disabled intake until the preceding broadcast drained.
		if cap(r.pending) == 0 {
			r.pending = make([]ports.SurfaceContent, 0, 1)
		}
		r.pending = append(r.pending, c)
	}
}

// At most one admitted broadcast waits behind each owner's finite replay.
// Disable new content intake until all copies have been delivered.
func (s *outputSet) contentOut(incoming <-chan ports.SurfaceContent) (<-chan ports.SurfaceContent, chan<- ports.SurfaceContent, ports.SurfaceContent, *runningOutput) {
	for _, r := range s.outs {
		if len(r.pending) != 0 && r.ctx.Err() == nil {
			return nil, r.contents, r.pending[0], r
		}
	}
	return incoming, nil, ports.SurfaceContent{}, nil
}

func (r *runningOutput) contentSent() {
	r.pending[0] = ports.SurfaceContent{}
	if len(r.pending) == 1 {
		r.pending = r.pending[:0] // retain the one-slot broadcast buffer
	} else {
		r.pending = r.pending[1:]
	}
}

// finish forgets a stopped output and returns its error.
func (s *outputSet) finish(name string) error {
	r := s.outs[name]
	if r == nil {
		return nil
	}
	<-r.done
	r.stop()
	s.failQueued(r)
	delete(s.outs, name)
	return r.err
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
	for name, r := range s.outs {
		r.stop()
		<-r.done
		s.failQueued(r)
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

// move places the cursor on output at physical (x, y). motion is false
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
func (s *outputSet) routeCapture(req ports.CaptureRequest) {
	r := s.outs[req.Output]
	if r == nil {
		capture.Fail(s.ctx, req, fmt.Errorf("output %q unavailable", req.Output), s.captured)
		return
	}
	select {
	case r.captures <- req:
	case <-r.done:
		r.stop()
		capture.Fail(s.ctx, req, fmt.Errorf("output %q stopped", req.Output), s.captured)
	case <-r.ctx.Done():
		capture.Fail(s.ctx, req, fmt.Errorf("output %q stopped", req.Output), s.captured)
	default:
		capture.Fail(s.ctx, req, fmt.Errorf("output %q capture queue full", req.Output), s.captured)
	}
}

func (s *outputSet) failQueued(r *runningOutput) {
	for {
		select {
		case req := <-r.captures:
			capture.Fail(s.ctx, req, fmt.Errorf("output stopped"), s.captured)
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
			capture.Fail(ctx, q, fmt.Errorf("output stopped"), replies)
		default:
			return
		}
	}
}
