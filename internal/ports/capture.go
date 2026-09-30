package ports

// Capture sessions, exclusion and workspace sources.
//
// Core owns every decision; wayland relays requests and what core decided.
// Registered sessions are the ext-image-copy-capture sessions
// (CaptureSessionOpen). One of them at most has an exclusion
// (neferwl_capture_exclusion_v1): the layer surfaces its HUD attached stay
// visible over a fullscreen window and are left out of that session's frames.

const (
	// MaxExclusionLayers bounds the layer surfaces attached to an exclusion.
	MaxExclusionLayers = 4
	// MaxCaptureExcluded bounds SceneCapture.Excluded: attached layers and
	// their popup descendants.
	MaxCaptureExcluded = 128
)

// CaptureReason says why a registered session ends. The zero value is none.
type CaptureReason string

const (
	CaptureReasonNone CaptureReason = ""
	// Every reason ends the session: core forgets it and wayland stops it.
	CaptureReasonOutputGone    CaptureReason = "output-gone"
	CaptureReasonWorkspaceGone CaptureReason = "workspace-gone"
	CaptureReasonInvalidRegion CaptureReason = "invalid-region" // nothing of the region is left on the output
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
// now. ID is chosen by wayland and unique for the process lifetime.
type CaptureSessionOpen struct {
	ID        uint64
	Output    string
	Workspace uint64
	Region    Rect
}

func (CaptureSessionOpen) clientEvent() {}

// CaptureSessionClose carries wayland → core the end of a session, for any
// reason. Unknown IDs are ignored.
type CaptureSessionClose struct{ ID uint64 }

func (CaptureSessionClose) clientEvent() {}

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
// hidden Workspace; else it fails, whatever wayland last believed.
type SceneCapture struct {
	Shown     uint64
	Session   uint64
	Excluded  []WindowID
	Revision  uint64
	Workspace uint64
}
