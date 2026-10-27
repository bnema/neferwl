package cursors

import (
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Router routes pointer moves from the input goroutine to the cursor of
// the output under the pointer and hides the others. After hideAfter
// without motion it hides that cursor too; the next motion shows it.
type Router struct {
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

// New constructs a synchronous cursor router using clock for idle hiding.
func New(clock ports.Clock, hideAfter time.Duration) *Router {
	return &Router{all: map[string]cursor{}, clock: clock, hideAfter: hideAfter}
}

// SetHideAfter changes the idle delay; 0 never hides the cursor. A cursor
// already hidden stays hidden until the next motion.
func (c *Router) SetHideAfter(d time.Duration) {
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

// Stop cancels a pending hide; the router is not used after.
func (c *Router) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disarm()
}

// disarm stops the idle timer. Callers hold mu.
func (c *Router) disarm() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.armed = false
}

// arm starts the idle timer for d. Callers hold mu.
func (c *Router) arm(d time.Duration) {
	c.armed = true
	if c.timer == nil {
		c.timer = c.clock.AfterFunc(d, c.idle)
		return
	}
	c.timer.Reset(d)
}

// idle hides the cursor once hideAfter passed since the last move, or
// re-arms the timer for the time left.
func (c *Router) idle() {
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

// Set registers the cursor of an output; nil removes it.
func (c *Router) Set(output string, cur cursor) {
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

// Move places the cursor on output at target pixels (x, y): physical, after
// the output transform. motion is false
// when the pointer is only placed again (layout or constraint change): it
// neither shows an idle-hidden cursor nor delays the hide.
func (c *Router) Move(output string, x, y float64, motion bool) {
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
