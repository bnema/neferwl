package ports

import (
	"github.com/bnema/neferwl/internal/domain"
)

// OutputEvent carries output → core notifications.
type OutputEvent interface{ outputEvent() }

// OutputAdded carries output → core a new display, or a new mode for a known
// connector.
type OutputAdded struct{ Info domain.OutputInfo }

func (OutputAdded) outputEvent() {}

// PresentationSink receives output → wayland reports. Push never blocks;
// it wakes the consumer. Owner goroutines of outputs call it. After Push the
// report belongs to the sink: its maps are read-only snapshots, and Flip must
// be fresh for every push, since the sink may update Merged and replace Shows
// when it merges flips.
type PresentationSink interface{ Push(domain.OutputPresented) }

// OutputRemoved carries output → core an unplugged display.
type OutputRemoved struct{ Name string }

func (OutputRemoved) outputEvent() {}
