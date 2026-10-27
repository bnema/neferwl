package ports

import "github.com/bnema/neferwl/internal/domain"

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

// PopupRequest carries wayland → core a popup to place (xdg_popup), on
// its first commit and on reposition (Reposition, with the client's
// Token). Parent is a window or popup. Grab popups take the keyboard and
// close on a click elsewhere.
type PopupRequest struct {
	ID, Parent domain.WindowID
	Positioner domain.Positioner
	Grab       bool
	Reposition bool
	Token      uint32
}

func (PopupRequest) clientEvent() {}

// PopupMapped carries wayland → core a placed popup that now has content.
// It ends with WindowUnmapped.
type PopupMapped struct{ ID domain.WindowID }

func (PopupMapped) clientEvent() {}
