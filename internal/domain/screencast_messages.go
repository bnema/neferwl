package domain

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
