package domain

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
