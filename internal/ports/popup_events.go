package ports

// Popup anchors and gravities, as xdg_positioner values.
const (
	EdgeNone uint32 = iota
	EdgeTop
	EdgeBottom
	EdgeLeft
	EdgeRight
	EdgeTopLeft
	EdgeBottomLeft
	EdgeTopRight
	EdgeBottomRight
)

// Popup constraint adjustments, as xdg_positioner values.
const (
	AdjustSlideX uint32 = 1 << iota
	AdjustSlideY
	AdjustFlipX
	AdjustFlipY
	AdjustResizeX
	AdjustResizeY
)

// Positioner places a popup relative to its parent's window geometry:
// the Anchor point of AnchorRect, moved by Offset, with the popup extending
// towards Gravity. Adjust says how core may move it to stay on screen.
type Positioner struct {
	Width, Height    int
	AnchorRect       Rect
	Anchor, Gravity  uint32
	OffsetX, OffsetY int
	Adjust           uint32
}

// PopupRequest carries wayland → core a popup to place (xdg_popup), on
// its first commit and on reposition (Reposition, with the client's
// Token). Parent is a window or popup. Grab popups take the keyboard and
// close on a click elsewhere.
type PopupRequest struct {
	ID, Parent WindowID
	Positioner Positioner
	Grab       bool
	Reposition bool
	Token      uint32
}

func (PopupRequest) clientEvent() {}

// PopupMapped carries wayland → core a placed popup that now has content.
// It ends with WindowUnmapped.
type PopupMapped struct{ ID WindowID }

func (PopupMapped) clientEvent() {}
