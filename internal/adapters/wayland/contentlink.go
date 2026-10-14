package wayland

import (
	"slices"
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
//
// The link is also the ports.ContentReturn handed to the output, and the
// handle DetachOutput takes: a detach removes this link and no other, even
// when a later attach of the same name replaced it.
type contentLink struct {
	s    *Server
	name string
	in   chan *ports.ContentBatch
	free chan *ports.ContentBatch
	cur  *ports.ContentBatch
	// pending: cur holds contents that could not be sent yet (the channel is
	// full, or every batch is out). Return only wakes the display when it is
	// set, so a steady stream of frames costs the display no extra wake.
	pending atomic.Bool
}

// Return gives a consumed (reset) batch back and, when contents wait for one,
// has the display goroutine send them. Safe from any goroutine, also after
// the link was detached: the batch only goes back to its own link.
func (l *contentLink) Return(b *ports.ContentBatch) {
	l.free <- b // never full: the link owns batchesPerLink batches
	// The display stores pending before it looks at free once more, so
	// either it sees this batch or this load sees pending.
	if l.pending.Load() && l.s.flushPosted.CompareAndSwap(false, true) {
		l.s.display.Post(l.s.flushLinksFn)
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
// is replaced (dropped from the feed: its handle then detaches nothing). The
// returned ContentReturn is the link, and the handle for detachOutput. The
// first batch holds the latest content of every live window,
// so the output starts with all of them drawn. Display goroutine, or before
// Run.
func (s *Server) attachOutput(name string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
	for i, old := range s.links {
		if old.name == name {
			s.links = slices.Delete(s.links, i, i+1)
			break
		}
	}
	l := &contentLink{
		s:    s,
		name: name,
		in:   make(chan *ports.ContentBatch, 1),
		free: make(chan *ports.ContentBatch, batchesPerLink),
	}
	for range batchesPerLink - 1 {
		l.free <- &ports.ContentBatch{Items: make([]ports.SurfaceContent, 0, batchItems)}
	}
	l.cur = &ports.ContentBatch{Items: make([]ports.SurfaceContent, 0, max(batchItems, len(s.latest)))}
	for _, c := range s.latest {
		l.add(c)
	}
	s.links = append(s.links, l)
	s.flushLink(l)
	return l.in, l
}

// detachOutput stops feeding the link ret, the handle attachOutput returned.
// A handle whose link was replaced by a later attach of its name, or that is
// not a link, detaches nothing: a newer link is never removed. Display
// goroutine, or before Run. The output may still Return its last batch.
func (s *Server) detachOutput(ret ports.ContentReturn) {
	l, ok := ret.(*contentLink)
	if !ok {
		return
	}
	if i := slices.Index(s.links, l); i >= 0 {
		s.links = slices.Delete(s.links, i, i+1)
	}
}

// AttachOutput starts feeding the output called name with the contents of
// the windows, as batches on the returned channel, starting with the current
// content of every window. The output hands each batch back, reset, to the
// returned ContentReturn, which is also the handle for DetachOutput.
// Attaching a name that already has a feed replaces that feed. Call it from
// outside the display goroutine. It returns a nil channel and handle when
// the server is stopped.
func (s *Server) AttachOutput(name string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
	var in <-chan *ports.ContentBatch
	var ret ports.ContentReturn
	if !s.display.Do(func() { in, ret = s.attachOutput(name) }) {
		return nil, nil
	}
	return in, ret
}

// DetachOutput stops the feed ret, returned by AttachOutput. Only that feed
// stops: if a later AttachOutput of the same name replaced it, the newer
// feed keeps running. Call it from outside the display goroutine, once the
// output no longer reads its channel. A nil ret does nothing.
func (s *Server) DetachOutput(ret ports.ContentReturn) {
	if ret == nil {
		return
	}
	s.display.Do(func() { s.detachOutput(ret) })
}
