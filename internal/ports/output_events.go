package ports

import (
	"sync/atomic"

	"github.com/bnema/neferwl/internal/domain"
)

// OutputEvent carries output → core notifications.
type OutputEvent interface{ outputEvent() }

// OutputAdded carries output → core a new display, or a new mode for a known
// connector.
type OutputAdded struct{ Info domain.OutputInfo }

func (OutputAdded) outputEvent() {}

// OutputHDRCaps carries confirmed HDR facts in output lifecycle order.
type OutputHDRCaps struct{ domain.OutputHDRCaps }

func (OutputHDRCaps) outputEvent() {}

// PresentationSink receives output → wayland reports. Push never blocks;
// it wakes the consumer. Owner goroutines of outputs call it. After Push the
// report belongs to the sink: its maps are read-only snapshots, and Flip must
// be fresh for every push, since the sink may update Merged and replace Shows
// when it merges flips. The sink must release a non-nil Snapshot exactly once
// after its final read (including any retained latest-output report).
type PresentationSink interface{ Push(OutputPresented) }

// OutputRemoved carries output → core an unplugged display.
type OutputRemoved struct{ Name string }

func (OutputRemoved) outputEvent() {}

// OutputPresented carries output → wayland what an output shows and reads.
// Flip is set when a page flip completed: frame callbacks of the surfaces
// on it are due. Shown and Queued are the DMABuf IDs scanned out directly
// (0: a composed image); Seen is the latest content Seq per window the
// output finished reading (the GPU is done with it); entries of unmapped
// windows disappear after a delay. A replaced client
// buffer is released once every output that reports has seen a later
// content of its window and neither shows nor queues it.
type OutputPresented struct {
	Output        string
	Flip          *domain.FlipInfo
	Shown, Queued uint64
	Seen          map[domain.WindowID]uint64
	// ChildReads is the oldest content Seq per window still read by a child
	// renderer on a separate device. Unlike Seen, these holds never time out:
	// they end only with a completed render or device shutdown.
	ChildReads map[domain.WindowID]uint64
	// Snapshot owns Flip, Shows, Seen and ChildReads when non-nil. Consumers must release
	// it exactly once after their final read; copies do not acquire ownership.
	Snapshot *PresentationSnapshot
}

// PresentationSnapshot leases report maps and flip storage across goroutines.
// Only the output owner writes them after receiving the last reference back.
type PresentationSnapshot struct {
	Seen, ChildReads map[domain.WindowID]uint64
	Flip             domain.FlipInfo
	Shows            map[domain.WindowID]uint64
	refs             atomic.Int32
	returned         chan<- *PresentationSnapshot
}

// Init starts an owner reference to a returned (or new) snapshot.
func (s *PresentationSnapshot) Init(returned chan<- *PresentationSnapshot) {
	s.returned = returned
	s.refs.Store(1)
}

// Retain acquires a reference while the caller already owns one.
func (s *PresentationSnapshot) Retain() { s.refs.Add(1) }

// Release ends one reference. A full return queue drops the unused snapshot.
func (s *PresentationSnapshot) Release() {
	if s == nil {
		return
	}
	refs := s.refs.Add(-1)
	if refs < 0 {
		panic("ports.PresentationSnapshot.Release: reference count underflow")
	}
	if refs == 0 {
		select {
		case s.returned <- s:
		default:
		}
	}
}

// PresentationMessage seals the report and its ownership token to this package.
type PresentationMessage interface{ presentationMessage() }

func (OutputPresented) presentationMessage()       {}
func (*PresentationSnapshot) presentationMessage() {}
