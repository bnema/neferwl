package ports

import "time"

// Capture sessions, exclusion and workspace sources.
//
// Core owns every decision; wayland relays requests and what core decided.
// Registered sessions are the ext-image-copy-capture sessions
// (CaptureSessionOpen). One of them at most has an exclusion
// (neferwl_capture_exclusion_v1): the layer surfaces its HUD attached stay
// visible over a fullscreen window and are left out of that session's frames.
//
// Every capture is shown on screen by the compositor, never by the client
// (CaptureIndicator): a border on the target of a recording session for as
// long as it lives, and on the target of every captured frame for
// CaptureFlash after it. No capture image ever holds an indicator.

const (
	// MaxExclusionLayers bounds the layer surfaces attached to an exclusion.
	MaxExclusionLayers = 4
	// MaxCaptureExcluded bounds SceneCapture.Excluded: attached layers and
	// their popup descendants.
	MaxCaptureExcluded = 128
	// CaptureBorderWidth and CaptureBorderColor are the indicator border, in
	// logical pixels and #rrggbb. CapturePillSize, CapturePillInset and
	// CapturePillRadius are the marker of a target that is not on screen
	// (a workspace rendered for capture only): a rounded square in the
	// top-right corner of its output, in logical pixels.
	CaptureBorderWidth = 2
	CaptureBorderColor = "#ff3b30"
	CapturePillSize    = 12
	CapturePillInset   = 8
	CapturePillRadius  = 3
	// CaptureFlash is how long the indicator of a captured frame stays on
	// screen after that frame.
	CaptureFlash = time.Second
)

// CaptureReason says why a registered session ends. The zero value is none.
type CaptureReason string

const (
	CaptureReasonNone CaptureReason = ""
	// Every reason ends the session: core forgets it and wayland stops it.
	CaptureReasonOutputGone    CaptureReason = "output-gone"
	CaptureReasonWorkspaceGone CaptureReason = "workspace-gone"
	CaptureReasonInvalidRegion CaptureReason = "invalid-region" // nothing of the region is left on the output
	CaptureReasonWindowGone    CaptureReason = "window-gone"
	// TooManyExcluded: the attached layers and their popups exceed
	// MaxCaptureExcluded; a capture could not leave them all out (fail closed).
	CaptureReasonTooManyExcluded CaptureReason = "too-many-excluded"
)

// Terminal reports whether a state with this reason ends the session.
func (r CaptureReason) Terminal() bool { return r != CaptureReasonNone }

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
	Window    WindowID
	Region    Rect
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
// indicator on that target for CaptureFlash after it, and while Session
// lives when it is not 0.
type CaptureFrameTaken struct {
	Session   uint64
	Output    string
	Workspace uint64
	Window    WindowID
	Region    Rect
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
// exclusively; at most MaxExclusionLayers are attached.
type CaptureExclusionLayer struct {
	Session  uint64
	Layer    WindowID
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
// for capture only (Scene.CaptureScene) and frames must ask for it
// (CaptureRequest.Workspace). Active means frames are served; a hidden
// workspace is rendered for one session at a time in the compositor (its
// Workspace): another hidden one is not Active until that one is gone. A
// non-empty Reason ends the session (wayland stops it).
//
// Exclusion says core knows an exclusion of this session. Revision is its
// revision fence, from 1, and Layers the attached layer surfaces core knows
// (at most MaxExclusionLayers); both are zero without an exclusion. Core
// bumps the revision whenever what an excluded frame must leave out changes:
// the start, an attached or detached layer, an excluded popup, the target. The
// scene carrying that change has the same SceneCapture.Revision. Wayland
// confirms an attach to the client only once a state lists the layer, serves
// the session's frames with CaptureRequest.Exclude and stamps
// CaptureRevision with the last state's Revision; a renderer refuses the
// request unless its scene's Capture.Revision is at least that, so a scene
// published before the change is never used and no stale exclusion list can
// leak a HUD.
type CaptureSessionState struct {
	ID        uint64
	Output    string
	Rect      Rect
	Workspace uint64
	Hidden    bool
	Active    bool
	Reason    CaptureReason
	Exclusion bool
	Revision  uint64
	Layers    []WindowID
}

func (CaptureSessionState) clientCommand() {}

// SceneCapture carries core → renderer the capture state of one output's
// scene (Scene.Capture; nil when there is none, which is the whole cost of
// the feature while no session exists).
//
// Session is the session that owns the exclusion, 0 without one; Excluded
// lists its attached layer surfaces and their popup descendants (at most
// MaxCaptureExcluded), which that session's frames leave out, and Revision is
// the fence of CaptureSessionState. Workspace is the ID of the hidden
// workspace CaptureScene draws, 0 when there is none on this output. Shown is
// the ID of the workspace this scene draws as itself (0 in the overview): a
// request for a workspace is served only when it is Shown here, or is the
// hidden Workspace; else it fails, whatever wayland last believed. Window is
// the window CaptureScene draws instead, alone at its client size, 0 when
// none: one of Workspace and Window at most is set.
type SceneCapture struct {
	Shown     uint64
	Session   uint64
	Excluded  []WindowID
	Revision  uint64
	Workspace uint64
	Window    WindowID
}

// CaptureIndicator carries core → renderer one mark of a capture on an
// output's scene (Scene.CaptureIndicators; nil while nothing is captured,
// which is the whole cost of the feature). The renderer draws it last, over
// everything, in logical pixels; it is only on the scene of the physical
// output and in no capture image (the capture pipeline draws captures
// without it).
//
// Without Pill the indicator is the border of a target on screen: Rect is
// the mark, output-local and clipped, and the border is drawn inside it, at
// most half its smaller side wide (CaptureBorderOf). Core never lists a mark
// thinner than MinCaptureMark unless the output itself is: a border of a
// 1-pixel target would have nothing to draw. With Pill it marks a target
// that is not on screen: Rect is the pill square, a rounded one.
type CaptureIndicator struct {
	Rect Rect
	Pill bool
}

// MinCaptureMark is the smallest side of a border mark: the border on both
// sides and one pixel between them.
const MinCaptureMark = 2*CaptureBorderWidth + 1

// CaptureBorderOf is the width of the border drawn inside a w×h mark: the
// indicator width, at most half of each side. Zero means nothing is drawn
// for a proper border; the renderer then fills the whole mark, and the fence
// refuses such a mark (the thin mark of a capture it cannot vouch for).
func CaptureBorderOf(w, h int) int { return min(CaptureBorderWidth, w/2, h/2) }

// Drawable reports whether the indicator draws a visible mark, wherever the
// output is: a non-empty rectangle whose border, for a border mark, has a
// width. A pill must also lie in the output (DrawableIn).
func (m CaptureIndicator) Drawable() bool {
	if m.Rect.W <= 0 || m.Rect.H <= 0 {
		return false
	}
	return m.Pill || CaptureBorderOf(m.Rect.W, m.Rect.H) > 0
}

// DrawableIn is Drawable on an output of w×h logical pixels: a pill must lie
// fully inside it (an empty or partly outside pill is cut by the renderer and
// may show nothing).
func (m CaptureIndicator) DrawableIn(w, h int) bool {
	if !m.Drawable() {
		return false
	}
	if !m.Pill {
		return true
	}
	r := m.Rect
	return r.X >= 0 && r.Y >= 0 && r.X+r.W <= w && r.Y+r.H <= h
}
