package core

import (
	"context"
	"sync"

	"github.com/bnema/neferwl/internal/ports"
)

// outbox carries commands from the owner goroutine to the sender goroutine
// without ever blocking the owner. It is a transport queue, not window
// state: the mutex guards only the slices. Commands leave in the order they
// were queued; the only merge is of adjacent pointer motions (see push).
//
// The wayland display goroutine can be busy for a while. Sending from the
// owner would stall keyboard, scenes and frame wakes behind it, so the owner
// queues and the sender goroutine does the blocking send.
type outbox struct {
	// wake is signalled (never blocking) after each push.
	wake chan struct{}

	mu           sync.Mutex
	queue, spare []outCmd
	// busy is set while the sender holds a batch it has not finished
	// sending: a command pushed then must queue behind it.
	busy bool
}

// outCmd is a queued command. A pointer motion is kept unboxed so that a
// merge boxes the sum once, when it leaves, not at every merge.
type outCmd struct {
	cmd ports.ClientCommand
	// state is the owner envelope; secured is false without a security gate.
	state   ports.SecurityState
	secured bool
	// motion is cmd when it is a pointer motion. merged means cmd is stale
	// and motion carries the sum.
	motion   ports.PointerMotionTo
	isMotion bool
	merged   bool
}

func newOutbox() outbox { return outbox{wake: make(chan struct{}, 1)} }

// command returns what the sender delivers.
func (o *outCmd) command() ports.ClientCommand {
	v := o.cmd
	if o.merged {
		v = o.motion
	}
	if o.secured {
		return ports.SecurityCommand{State: o.state, Command: v}
	}
	return v
}

// push delivers v, wrapped in an envelope for state when secured. With
// nothing queued or in flight and room in out it sends at once, which keeps
// the display's latency when it keeps up; otherwise it queues v. A pointer
// motion for the same window and envelope as the one queued last replaces
// it: the position is the latest and the relative deltas add up, so locked
// pointers (games) lose no movement. Nothing else is merged or reordered:
// buttons, focus, keys and gestures depend on their order.
func (o *outbox) push(out chan<- ports.ClientCommand, v ports.ClientCommand, state ports.SecurityState, secured bool) {
	e := outCmd{cmd: v, state: state, secured: secured}
	e.motion, e.isMotion = v.(ports.PointerMotionTo)
	o.mu.Lock()
	if !o.busy && len(o.queue) == 0 {
		// A send that cannot block is safe under the lock.
		select {
		case out <- e.command():
			o.mu.Unlock()
			return
		default:
		}
	}
	if n := len(o.queue); n > 0 && e.isMotion {
		last := &o.queue[n-1]
		if last.isMotion && last.motion.ID == e.motion.ID && last.secured == secured && last.state == state {
			e.motion.DX += last.motion.DX
			e.motion.DY += last.motion.DY
			e.motion.UnaccelDX += last.motion.UnaccelDX
			e.motion.UnaccelDY += last.motion.UnaccelDY
			e.merged = true
			*last = e
			o.mu.Unlock()
			return
		}
	}
	o.queue = append(o.queue, e)
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// take returns the queued commands and leaves an empty queue. The caller
// hands the batch back with recycle once it has sent it.
func (o *outbox) take() []outCmd {
	o.mu.Lock()
	batch := o.queue
	o.queue, o.spare = o.spare[:0], nil
	o.busy = len(batch) > 0
	o.mu.Unlock()
	return batch
}

// recycle keeps the batch's storage for the next take.
func (o *outbox) recycle(batch []outCmd) {
	// Do not retain what the commands reference in the spare batch.
	clear(batch)
	o.mu.Lock()
	o.spare = batch[:0]
	o.busy = false
	o.mu.Unlock()
}

// run delivers the queue to out in order until ctx ends. A command whose
// send is waiting is not merged any more, but what queues behind it is.
func (o *outbox) run(ctx context.Context, out chan<- ports.ClientCommand) {
	for {
		batch := o.take()
		if len(batch) == 0 {
			o.recycle(batch)
			select {
			case <-o.wake:
			case <-ctx.Done():
				return
			}
			continue
		}
		for i := range batch {
			select {
			case out <- batch[i].command():
			case <-ctx.Done():
				return
			}
		}
		o.recycle(batch)
	}
}
