package domain

// OverlayKind is what an Overlay draws.
type OverlayKind uint8

const (
	// OverlayLabel is a one-line text label (Text, Typed).
	OverlayLabel OverlayKind = iota + 1
	// OverlayPanel is a multi-line text panel with a frame-time graph (Panel).
	OverlayPanel
)

// OverlayAnchor is how the renderer places an Overlay relative to its Rect.
type OverlayAnchor uint8

const (
	// AnchorOutsideTopLeft places the overlay outside Rect's top-left corner,
	// overlapping it by a quarter of the overlay height, kept inside the output.
	AnchorOutsideTopLeft OverlayAnchor = iota + 1
	// AnchorTopCenter centres horizontally in the attached window (or output
	// when Window is 0), Rect.Y below its top edge; Rect.X is ignored.
	AnchorTopCenter
)
