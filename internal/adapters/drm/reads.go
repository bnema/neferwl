package drm

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Fence holds: client buffers the GPU may still read.

// dupFences duplicates the non-nil fences.
func dupFences(fs ...*os.File) []*os.File {
	var out []*os.File
	for _, f := range fs {
		if d := dupFence(f); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// signalled reports whether every fence signalled (readable); an fd that
// cannot be polled counts as signalled.
func signalled(fs []*os.File) bool {
	if len(fs) == 0 {
		return true
	}
	pfds := make([]unix.PollFd, len(fs))
	for i, f := range fs {
		pfds[i] = unix.PollFd{Fd: int32(f.Fd()), Events: unix.POLLIN}
	}
	if _, err := unix.Poll(pfds, 0); err != nil {
		return true
	}
	for _, p := range pfds {
		if p.Revents == 0 {
			return false
		}
	}
	return true
}

// holdRead keeps the fences of a frame that was not committed.
func (o *Output) holdRead(fs ...*os.File) {
	o.readFences = append(o.readFences, dupFences(fs...)...)
}

// readDone reports whether no uncommitted frame may still read client
// buffers, closing the fences that signalled.
func (o *Output) readDone() bool {
	kept := o.readFences[:0]
	for _, f := range o.readFences {
		if signalled([]*os.File{f}) {
			f.Close()
			continue
		}
		kept = append(kept, f)
	}
	clear(o.readFences[len(kept):])
	o.readFences = kept
	return len(kept) == 0
}

// dropRead closes the fences of uncommitted frames.
func (o *Output) dropRead() {
	for _, f := range o.readFences {
		f.Close()
	}
	o.readFences = nil
}

// expire applies a lifecycle deadline and reports whether KMS needs a modeset.
// Fence polling and logging stay at the output boundary, not in the value
// transition; the lifecycle owns the decision and closes pending fences.
func (o *Output) expire() bool {
	now := time.Now()
	serial, frame, age, busy, fences := o.frame.deadlineInfo(now)
	ready := serial == 0 || !o.frame.deadlineReached(now) || signalled(fences)
	switch o.frame.timeout(now, ready) {
	case timeoutFence:
		o.log.Warn().Str("connector", o.conn.name).Uint64("serial", serial).Dur("age", age).Msg("frame waits for GPU fence")
	case timeoutBusy:
		o.log.Warn().Str("connector", o.conn.name).Str("kind", "busy").Dur("age", busy).Msg("commits refused as busy; modeset")
		return true
	case timeoutMissing:
		kind := "state"
		if frame {
			kind = "frame"
		}
		o.log.Warn().Str("connector", o.conn.name).Uint64("serial", serial).Str("kind", kind).Dur("age", age).Msg("commit event missing; modeset")
		return true
	}
	return false
}
