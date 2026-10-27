package domain

import "time"

// CastCommandKind selects the CastCommand fields in use.
type CastCommandKind uint8

const (
	// CastOpen starts cast ID on Output or Window.
	CastOpen CastCommandKind = iota + 1
	// CastFrame asks for one frame into buffer Buf.
	CastFrame
	// CastClose ends cast ID. Wayland sends no CastStopped for it; a frame
	// still in flight is answered with its CastFrameDone as usual.
	CastClose
)

// CastEventKind selects the CastEvent fields in use.
type CastEventKind uint8

const (
	// CastSize: the target is now Width x Height; the stream must renegotiate.
	CastSize CastEventKind = iota + 1
	// CastDamage: the target changed and a frame may be requested. Time is
	// the flip time.
	CastDamage
	// CastFrameDone: the frame into Buf finished. Err is nil when Data holds
	// a complete image; Time is the capture time.
	CastFrameDone
	// CastStopped: the cast ended (Reason, Err); no event follows.
	CastStopped
)

// CastCommand carries screencast → wayland: open or close a cast, or request
// one frame into a PipeWire buffer.
type CastCommand struct {
	Kind CastCommandKind
	ID   uint64
	// CastOpen: the target. A window cast sets Window and leaves Output empty.
	Output string
	Window WindowID
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
	Kind          CastEventKind
	ID            uint64
	Width, Height int
	Buf           int
	Err           error
	Time          time.Time
	Reason        CaptureReason
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
	ID           WindowID
	AppID, Title string
}

// CastInventory is the set a cast target is picked from.
type CastInventory struct {
	Outputs []CastOutput
	Windows []CastWindow
	Focused string // name of the focused output
}
