package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/bnema/neferwl/internal/adapters/capture"

	"github.com/bnema/neferwl/internal/ports"
)

// outputRun drives one output until ctx ends: it renders the scenes and
// surface contents it receives, with the cursor the client asks for.
type outputRun func(ctx context.Context, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, captures <-chan ports.CaptureRequest) error

// outputSet owns the running outputs and feeds them. Core sends one scene
// per output; each goes to its output, latest first. Surface contents go to
// every output, since windows move between outputs; the latest content of
// each window is kept so a new output starts with every window drawn.
// Only the goroutine running loop touches the set.
type outputSet struct {
	outs     map[string]*runningOutput
	latest   map[ports.WindowID]ports.SurfaceContent
	cursor   ports.CursorChange
	stopped  chan string
	captured chan<- ports.CaptureDone
	ctx      context.Context
	quit     chan struct{} // closed by wait: nobody reads stopped any more
}

type runningOutput struct {
	scenes   chan ports.Scene
	contents chan ports.SurfaceContent
	cursor   chan ports.CursorChange
	captures chan ports.CaptureRequest
	ctx      context.Context
	stop     context.CancelFunc
	err      error
	done     chan struct{}
}

func newOutputSet(ctx context.Context, captured chan<- ports.CaptureDone) *outputSet {
	return &outputSet{captured: captured, ctx: ctx, outs: map[string]*runningOutput{}, latest: map[ports.WindowID]ports.SurfaceContent{}, stopped: make(chan string), quit: make(chan struct{})}
}

// start runs an output in its own goroutine and replays the window contents.
func (s *outputSet) start(ctx context.Context, name string, run outputRun) {
	octx, stop := context.WithCancel(ctx)
	r := &runningOutput{scenes: make(chan ports.Scene, 1), contents: make(chan ports.SurfaceContent, 64), cursor: make(chan ports.CursorChange, 1), captures: make(chan ports.CaptureRequest, 16), ctx: octx, stop: stop, done: make(chan struct{})}
	s.outs[name] = r
	go func() {
		r.err = run(octx, r.scenes, r.contents, r.cursor, r.captures)
		stop()
		close(r.done)
		select {
		case s.stopped <- name:
		case <-s.quit:
		}
	}()
	r.cursor <- s.cursor
	for _, c := range s.latest {
		r.send(c)
	}
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

// send hands content to the output unless it has stopped.
func (r *runningOutput) send(c ports.SurfaceContent) {
	select {
	case r.contents <- c:
	case <-r.ctx.Done():
	case <-r.done:
	}
}

// scenes routes each scene to its output, replacing an unread one.
func (s *outputSet) scenes(set []ports.Scene) {
	for _, sc := range set {
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
		r.send(c)
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
	var first error
	close(s.quit)
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
// the output under the pointer and hides the others.
type cursors struct {
	mu   sync.Mutex
	all  map[string]cursor
	last string
}

// cursor is a hardware or software cursor of one output.
type cursor interface {
	Move(x, y float64)
	Hide()
}

func newCursors() *cursors { return &cursors{all: map[string]cursor{}} }

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
	// none does (a new cursor would show at its top-left corner).
	if output != c.last {
		cur.Hide()
	}
}

// move places the cursor on output at physical (x, y).
func (c *cursors) move(output string, x, y float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if output != c.last {
		for name, cur := range c.all {
			if name != output {
				cur.Hide()
			}
		}
		c.last = output
	}
	if cur := c.all[output]; cur != nil {
		cur.Move(x, y)
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
