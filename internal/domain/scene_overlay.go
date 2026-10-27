package domain

// OverlayStyle carries reloadable overlay settings.
type OverlayStyle struct {
	Size                   float64 // logical font size, 0: default
	Background, Foreground string  // #rrggbb, "": derived from the active border colour
}

// Overlay is compositor-drawn content. Unattached overlays draw over everything
// on an output. It is
// comparable. ID is stable while the overlay lives, never 0 and unique among
// the overlays of a scene (renderers cache by it); Seq changes
// whenever its pixels change for the same scene scale and style, so a
// renderer may keep one cached image per ID.
type Overlay struct {
	// Window, when non-zero, attaches the overlay to that scene window: drawn
	// right after its content in stacking order, clipped to it and faded with
	// it. Rect is relative to its top-left. If the window is not drawn in the
	// scene, neither is the overlay.
	Window WindowID
	ID     uint64
	Seq    uint64
	Kind   OverlayKind
	Anchor OverlayAnchor
	Rect   Rect // logical, output-local (window-local when attached); W and H may be 0: sized from content
	// Capture keeps the overlay in captures requested by Wayland clients.
	// Captures requested by the compositor itself never contain overlays.
	Capture bool
	Style   OverlayStyle
	Text    string // OverlayLabel
	Typed   int    // OverlayLabel: leading characters already typed, drawn dimmed
	// Panel is OverlayPanel content, immutable once published. The pointer keeps
	// Overlay comparable: new content has a new pointer and a new Seq.
	Panel *PanelContent
}

// OutputPhysical is an output's untransformed mode size in pixels and
// physical size in millimetres.
type OutputPhysical struct{ Width, Height, MMWidth, MMHeight int }

// PanelContent holds a multi-line panel and its frame-time graph.
type PanelContent struct {
	Lines [4]string
	Graph [StatsGraph]uint16
}
