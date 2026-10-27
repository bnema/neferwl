package domain

// Layer is a wlr-layer-shell stacking layer.
type Layer uint32

const (
	LayerBackground Layer = iota
	LayerBottom
	LayerTop
	LayerOverlay
)

// LayerSurface is the committed state of one mapped layer surface. Width and
// Height are the committed surface size in logical pixels. Margin is top, right, bottom, left.
// IDs share the WindowID space with windows and never collide.
type LayerSurface struct {
	ID            WindowID
	Layer         Layer
	Anchor        uint32
	ExclusiveZone int32
	Margin        [4]int32
	Width, Height int
	Namespace     string
	// Keyboard is the requested interactivity: 0 none, 1 exclusive, 2 on-demand.
	Keyboard uint32
	// Output is the connector the surface is on.
	Output string
}
