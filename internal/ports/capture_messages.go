package ports

import (
	"image"
	"time"
)

// CaptureRequest transfers ownership of Dst.File to the output goroutine.
type CaptureRequest struct {
	ID                    uint64
	Output                string
	Region                image.Rectangle // clipped output buffer coordinates
	Cursor                bool
	Dst                   SHMBuffer
	Width, Height, Stride int
	Format                uint32
	// Session is the registered capture session (CaptureSessionOpen) the
	// request belongs to, 0 for wlr-screencopy. It is informational, except
	// with Exclude.
	Session uint64
	// Exclude asks for a capture without the exclusion's HUD layers and
	// popups (SceneCapture.Excluded). Wayland sets it, with Session and
	// CaptureRevision, only on requests of the session that owns the
	// exclusion. A renderer fails such a request unless its scene's
	// Capture.Session is Session and Capture.Revision at least
	// CaptureRevision (CaptureSessionState).
	Exclude         bool
	CaptureRevision uint64
	// Workspace is not 0 for the capture of a workspace's frame. On screen
	// (OffScreen false) it is an ordinary region of the displayed frame, and
	// fails unless the scene's Capture.Shown is Workspace. OffScreen is set
	// when the workspace was not on screen when the request was made: it is
	// served from Scene.CaptureScene, whole (Region is the whole child
	// image), and fails unless the scene's Capture.Workspace is Workspace.
	// Either way a workspace that moved since fails instead of returning
	// another crop.
	Workspace uint64
	OffScreen bool
	// Window is not 0 for the capture of a window: always OffScreen, served
	// whole from Scene.CaptureScene, and failed unless the scene's
	// Capture.Window is Window.
	Window WindowID
	// Indicate is set by wayland on every capture it requests: core shows the
	// capture indicator for it (CaptureFrameTaken), and the output owner
	// serves the request only from a scene that shows the mark it needs (a
	// pill for OffScreen, else a border covering Region), so the indicator is
	// on screen in the same frame as the capture or an earlier one. It holds
	// the request for a bounded time, then fails it.
	Indicate bool
	// Since is when the output owner took the request (monotonic), to bound
	// that wait. Only the output owner sets it.
	Since time.Time
}

// CaptureDone is attempted once after the output closes the destination.
// Time holds CLOCK_MONOTONIC seconds and nanoseconds since boot, not wall time.
type CaptureDone struct {
	ID     uint64
	Output string
	Err    error
	Time   time.Time
}
