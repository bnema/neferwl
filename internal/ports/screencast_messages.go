package ports

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

// The cast messages are concrete structs, not sealed interfaces, so a send
// on a channel never boxes them (zero allocation per frame).

// CastCommand carries screencast → wayland: open or close a cast, or request
// one frame into a PipeWire buffer.
type CastCommand struct {
	Kind domain.CastCommandKind
	ID   uint64
	// CastOpen: the target. A window cast sets Window and leaves Output empty.
	Output string
	Window domain.WindowID
	Cursor bool
	// CastFrame: Buf is the buffer slot. Data is the buffer's mapping,
	// borrowed until the CastFrameDone of Buf: the sender neither reuses nor
	// unmaps it before then.
	Buf           int
	Data          []byte
	Stride        int
	Width, Height int
	Format        uint32 // wl_shm format
}

// CastEvent carries wayland → screencast through CastSink.Push.
type CastEvent struct {
	Kind          domain.CastEventKind
	ID            uint64
	Width, Height int
	Buf           int
	Err           error
	Time          time.Time
	Reason        domain.CaptureReason
}

// CastOutput is one output of the inventory.
type CastOutput struct {
	Info    OutputInfo
	Modes   []OutputMode
	Current int // index into Modes, -1 when none
	// X, Y, Width, Height are the logical placement.
	X, Y, Width, Height int
	Scale               float64
	Transform           BufferTransform
}

// CastWindow is one window of the inventory.
type CastWindow struct {
	ID           domain.WindowID
	AppID, Title string
}

// CastInventory is the set a cast target is picked from.
type CastInventory struct {
	Outputs []CastOutput
	Windows []CastWindow
	Focused string // name of the focused output
}
