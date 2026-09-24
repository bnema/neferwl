package app

import (
	"context"
	"sync"

	"github.com/bnema/nefertty/internal/ports"
)

// outputRun drives one output until ctx ends: it renders the scenes and
// surface contents it receives.
type outputRun func(ctx context.Context, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent) error

// outputSet owns the running outputs and feeds them. Core sends one scene
// per output; each goes to its output, latest first. Surface contents go to
// every output, since windows move between outputs; the latest content of
// each window is kept so a new output starts with every window drawn.
// Only the goroutine running loop touches the set.
type outputSet struct {
	outs    map[string]*runningOutput
	latest  map[ports.WindowID]ports.SurfaceContent
	stopped chan string
	quit    chan struct{} // closed by wait: nobody reads stopped any more
}

type runningOutput struct {
	scenes   chan ports.Scene
	contents chan ports.SurfaceContent
	ctx      context.Context
	stop     context.CancelFunc
	err      error
	done     chan struct{}
}

func newOutputSet() *outputSet {
	return &outputSet{outs: map[string]*runningOutput{}, latest: map[ports.WindowID]ports.SurfaceContent{}, stopped: make(chan string), quit: make(chan struct{})}
}

// start runs an output in its own goroutine and replays the window contents.
func (s *outputSet) start(ctx context.Context, name string, run outputRun) {
	octx, stop := context.WithCancel(ctx)
	r := &runningOutput{scenes: make(chan ports.Scene, 1), contents: make(chan ports.SurfaceContent, 64), ctx: octx, stop: stop, done: make(chan struct{})}
	s.outs[name] = r
	go func() {
		r.err = run(octx, r.scenes, r.contents)
		close(r.done)
		select {
		case s.stopped <- name:
		case <-s.quit:
		}
	}()
	for _, c := range s.latest {
		r.send(c)
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
	if c.Pixels == nil {
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
	// Before the first move nothing is shown yet: hiding would stick.
	if c.last != "" && output != c.last {
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
