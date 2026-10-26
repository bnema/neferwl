package ports

// OverlayKind is what an Overlay draws.
type OverlayKind uint8

const (
	// OverlayLabel is a one-line text label (Text, Typed).
	OverlayLabel OverlayKind = iota + 1
)

// OverlayAnchor is how the renderer places an Overlay relative to its Rect.
type OverlayAnchor uint8

const (
	// AnchorOutsideTopLeft places the overlay outside Rect's top-left corner,
	// overlapping it by a quarter of the overlay height, kept inside the output.
	AnchorOutsideTopLeft OverlayAnchor = iota + 1
)

// OverlayStyle carries reloadable overlay settings.
type OverlayStyle struct {
	Size                   float64 // logical font size, 0: default
	Background, Foreground string  // #rrggbb, "": derived from the active border colour
}

// Overlay is compositor-drawn content over everything on an output. It is
// comparable. ID is stable while the overlay lives and never 0; Seq changes
// whenever its pixels change for the same scene scale and style, so a
// renderer may keep one cached image per ID.
type Overlay struct {
	ID     uint64
	Seq    uint64
	Kind   OverlayKind
	Anchor OverlayAnchor
	Rect   Rect // logical, output-local; W and H may be 0: sized from content
	// Capture keeps the overlay in captures requested by Wayland clients.
	// Captures requested by the compositor itself never contain overlays.
	Capture bool
	Style   OverlayStyle
	Text    string // OverlayLabel
	Typed   int    // OverlayLabel: leading characters already typed, drawn dimmed
}

// OutputPhysical is an output's untransformed mode size in pixels and
// physical size in millimetres.
type OutputPhysical struct{ Width, Height, MMWidth, MMHeight int }
