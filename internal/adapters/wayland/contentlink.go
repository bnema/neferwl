package wayland

import (
	"sync/atomic"

	"github.com/bnema/neferwl/internal/ports"
)

// batchesPerLink is how many batches circulate between wayland and one
// output: one wayland fills, one waits in the channel, one the output reads.
const batchesPerLink = 3

// batchItems is the initial capacity of a batch: windows beyond it grow the
// batch once, and it keeps the capacity.
const batchItems = 8

// contentLink is the content path to one running output. The display
// goroutine owns cur and the sending side of in; the output owns the batch it
// received until it returns it. free holds the batches nobody uses: it has
// room for all of them, so Return never blocks.
type contentLink struct {
	name string
	in   chan *ports.ContentBatch
	free chan *ports.ContentBatch
	cur  *ports.ContentBatch
	// pending: cur holds contents that could not be sent yet (the channel is
	// full, or every batch is out). Return only wakes the display when it is
	// set, so a steady stream of frames costs the display no extra wake.
	pending atomic.Bool
	ret     *contentReturn
}

// contentReturn is the ports.ContentReturn of one link.
type contentReturn struct {
	s *Server
	l *contentLink
}

// Return gives a consumed (reset) batch back and, when contents wait for one,
// has the display goroutine send them. Safe from any goroutine, also after
// the link was detached: the batch only goes back to its own link.
func (r *contentReturn) Return(b *ports.ContentBatch) {
	r.l.free <- b // never full: the link owns batchesPerLink batches
	// The display stores pending before it looks at free once more, so
	// either it sees this batch or this load sees pending.
	if r.l.pending.Load() && r.s.flushPosted.CompareAndSwap(false, true) {
		r.s.display.Post(r.s.flushLinksFn)
	}
}

// add puts c in the batch being filled, replacing the window's earlier
// content: the output only needs the newest one (see ports.ContentBatch).
func (l *contentLink) add(c ports.SurfaceContent) {
	items := l.cur.Items
	for i := range items {
		if items[i].ID == c.ID {
			items[i] = c
			return
		}
	}
	l.cur.Items = append(items, c)
}

// try sends the batch being filled when the channel is empty and a batch is
// free to fill next. Display goroutine only.
func (l *contentLink) try() bool {
	if len(l.in) != 0 {
		return false
	}
	select {
	case next := <-l.free:
		l.in <- l.cur // empty, and only this goroutine sends
		l.cur = next
		return true
	default:
		return false
	}
}

// flushLink sends l's batch when the output can take it; otherwise l keeps
// filling it and asks Return for a wake.
func (s *Server) flushLink(l *contentLink) {
	if len(l.cur.Items) == 0 {
		l.pending.Store(false)
		return
	}
	if l.try() {
		l.pending.Store(false)
		return
	}
	l.pending.Store(true)
	if l.try() { // a Return may have slipped in before pending was set
		l.pending.Store(false)
	}
}

// flushLinks retries every link that waits for a free batch. Display
// goroutine; posted by Return.
func (s *Server) flushLinks() {
	s.flushPosted.Store(false)
	for _, l := range s.links {
		if l.pending.Load() {
			s.flushLink(l)
		}
	}
}

// attachOutput starts feeding the output called name; a link of the same name
// is replaced. The first batch holds the latest content of every live window,
// so the output starts with all of them drawn. Display goroutine, or before
// Run.
func (s *Server) attachOutput(name string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
	s.detachOutput(name)
	l := &contentLink{
		name: name,
		in:   make(chan *ports.ContentBatch, 1),
		free: make(chan *ports.ContentBatch, batchesPerLink),
	}
	l.ret = &contentReturn{s: s, l: l}
	for range batchesPerLink - 1 {
		l.free <- &ports.ContentBatch{Items: make([]ports.SurfaceContent, 0, batchItems)}
	}
	l.cur = &ports.ContentBatch{Items: make([]ports.SurfaceContent, 0, max(batchItems, len(s.latest)))}
	for _, c := range s.latest {
		l.add(c)
	}
	s.links = append(s.links, l)
	s.flushLink(l)
	return l.in, l.ret
}

// detachOutput stops feeding the output called name. Display goroutine, or
// before Run. The output may still Return its last batch.
func (s *Server) detachOutput(name string) {
	for i, l := range s.links {
		if l.name == name {
			s.links[i] = nil
			s.links = append(s.links[:i], s.links[i+1:]...)
			return
		}
	}
}

// AttachOutput starts feeding the output called name with the contents of
// the windows, as batches on the returned channel, starting with the current
// content of every window. The output hands each batch back, reset, to the
// returned ContentReturn. Call it from outside the display goroutine. It
// returns a nil channel when the server is stopped.
func (s *Server) AttachOutput(name string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
	var in <-chan *ports.ContentBatch
	var ret ports.ContentReturn
	if !s.display.Do(func() { in, ret = s.attachOutput(name) }) {
		return nil, nil
	}
	return in, ret
}

// DetachOutput stops the feed of the output called name. Call it from outside
// the display goroutine, once the output no longer reads its channel.
func (s *Server) DetachOutput(name string) {
	s.display.Do(func() { s.detachOutput(name) })
}
