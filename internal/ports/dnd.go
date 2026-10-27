package ports

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

// Data drag and drop (wl_data_device.start_drag). Wayland validates the
// client's request and tells core; core owns the pointer during the drag:
// it hit-tests, routes the drag to the surface under the pointer, drops on
// the release and cancels on Escape. Wayland speaks the protocol to the
// source and the targets. The icon is a client surface core draws over
// everything at the cursor.

// DataDragStarted carries wayland → core a client drag answering the
// client's current press. Button is the button of that press, whose
// release drops; Icon is the drag icon's ID (0: none). Core answers with
// DataDragCancel when it refuses the drag.
type DataDragStarted struct {
	Button uint32
	Icon   domain.WindowID
}

func (DataDragStarted) clientEvent() {}

// DataDragIcon carries wayland → core the drag icon's geometry after a
// commit: its top left corner relative to the cursor hotspot, and its
// size, in logical pixels. W or H 0 hides it.
type DataDragIcon struct {
	ID         domain.WindowID
	X, Y, W, H int
}

func (DataDragIcon) clientEvent() {}

// DataDragEnded carries wayland → core the end of a drag core did not
// end itself: the source died or the drag was cancelled.
type DataDragEnded struct{}

func (DataDragEnded) clientEvent() {}

// DataDragFocus carries core → wayland the surface under the pointer
// during a drag (0: none), at surface-local X, Y: the old target gets
// leave, the new one enter with an offer.
type DataDragFocus struct {
	ID   domain.WindowID
	X, Y float64
	Time time.Duration
}

func (DataDragFocus) clientCommand() {}

// DataDragMotion carries core → wayland the pointer over the drag target,
// in its surface coordinates.
type DataDragMotion struct {
	ID   domain.WindowID
	X, Y float64
	Time time.Duration
}

func (DataDragMotion) clientCommand() {}

// DataDragDrop carries core → wayland the release of the drag button: the
// target gets drop when it accepted the offer, else the drag is cancelled.
type DataDragDrop struct{}

func (DataDragDrop) clientCommand() {}

// DataDragCancel carries core → wayland a cancelled or refused drag.
type DataDragCancel struct{}

func (DataDragCancel) clientCommand() {}
