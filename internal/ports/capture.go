package ports

import (
	"github.com/bnema/neferwl/internal/domain"
)

// Capture sessions, exclusion and workspace sources.
//
// Core owns every decision; wayland relays requests and what core decided.
// Registered sessions are the ext-image-copy-capture sessions
// (CaptureSessionOpen). One of them at most has an exclusion
// (neferwl_capture_exclusion_v1): the layer surfaces its HUD attached stay
// visible over a fullscreen window and are left out of that session's frames.
//
// Every capture is shown on screen by the compositor, never by the client
// (domain.CaptureIndicator): a border on the target of a recording session for as
// long as it lives, and on the target of every captured frame for
// domain.CaptureFlash after it. No capture image ever holds an indicator.

// CaptureSessionOpen carries wayland → core a new ext-image-copy-capture
// session. Its target is an output (Workspace 0), whole when Region is zero,
// else the part of it in Region (output-local logical pixels, clipped), or a
// workspace (ID of WorkspaceInfo.ID): its frame, on whichever output owns it
// now, or a window (Window not 0): its client area at its own size, wherever
// it is and whether or not it is on screen, always rendered off screen. ID is
// chosen by wayland and unique for the process lifetime.
type CaptureSessionOpen struct {
	ID        uint64
	Output    string
	Workspace uint64
	Window    domain.WindowID
	Region    domain.Rect
}

func (CaptureSessionOpen) clientEvent() {}

// CaptureSessionClose carries wayland → core the end of a session, for any
// reason. Unknown IDs are ignored.
type CaptureSessionClose struct{ ID uint64 }

func (CaptureSessionClose) clientEvent() {}

// CaptureFrameTaken carries wayland → core a capture accepted for rendering,
// from any protocol: a wlr-screencopy frame (Session 0) or a frame of an
// ext-image-copy-capture session. Output and Region, or Workspace, are the
// target as the client asked for it: an output (Region zero is the whole
// output, else output-local logical pixels, clipped by core) or a workspace
// (Workspace is its ID, Output and Region are empty). Core shows the
// indicator on that target for domain.CaptureFlash after it, and while Session
// lives when it is not 0.
type CaptureFrameTaken struct {
	Session   uint64
	Output    string
	Workspace uint64
	Window    domain.WindowID
	Region    domain.Rect
}

func (CaptureFrameTaken) clientEvent() {}

// CaptureExclusionBegin carries wayland → core the exclusion of a session.
// Wayland has already refused it when another one is live.
type CaptureExclusionBegin struct{ Session uint64 }

func (CaptureExclusionBegin) clientEvent() {}

// CaptureExclusionLayer carries wayland → core a layer surface the session's
// authorised HUD attached (or detached, when it was destroyed). Layer is the
// LayerSurface.ID, known before the surface maps. Wayland has already checked
// the layer is top or overlay and that it never takes the keyboard
// exclusively; at most domain.MaxExclusionLayers are attached.
type CaptureExclusionLayer struct {
	Session  uint64
	Layer    domain.WindowID
	Attached bool
}

func (CaptureExclusionLayer) clientEvent() {}

// CaptureExclusionEnd carries wayland → core the end of an exclusion whose
// session lives on (its object was destroyed). A session that closes ends
// its exclusion with it. The layers of an ended exclusion that a screen still
// lists stay excluded and fenced (CaptureSessionState.Exclusion, Revision)
// until none is listed, so no frame of the session shows the HUD afterwards.
type CaptureExclusionEnd struct{ Session uint64 }

func (CaptureExclusionEnd) clientEvent() {}

// CaptureSessionState carries core → wayland what core decided about a
// registered session, on every change of it. Output is the output that owns
// the target now, Rect the clipped target in output-local logical pixels (for
// a workspace, its frame) and Workspace the workspace ID, 0 for an output.
// Hidden means the workspace is not drawn as itself on screen: it is rendered
// for capture only (domain.Scene.CaptureScene) and frames must ask for it
// (CaptureRequest.Workspace). Active means frames are served; a hidden
// workspace is rendered for one session at a time in the compositor (its
// Workspace): another hidden one is not Active until that one is gone. A
// non-empty Reason ends the session (wayland stops it).
//
// Exclusion says core knows an exclusion of this session. Revision is its
// revision fence, from 1, and Layers the attached layer surfaces core knows
// (at most domain.MaxExclusionLayers); both are zero without an exclusion. Core
// bumps the revision whenever what an excluded frame must leave out changes:
// the start, an attached or detached layer, an excluded popup, the target. The
// scene carrying that change has the same domain.SceneCapture.Revision. Wayland
// confirms an attach to the client only once a state lists the layer, serves
// the session's frames with CaptureRequest.Exclude and stamps
// CaptureRevision with the last state's Revision; a renderer refuses the
// request unless its scene's Capture.Revision is at least that, so a scene
// published before the change is never used and no stale exclusion list can
// leak a HUD.
type CaptureSessionState struct {
	ID        uint64
	Output    string
	Rect      domain.Rect
	Workspace uint64
	Hidden    bool
	Active    bool
	Reason    domain.CaptureReason
	Exclusion bool
	Revision  uint64
	Layers    []domain.WindowID
}

func (CaptureSessionState) clientCommand() {}
