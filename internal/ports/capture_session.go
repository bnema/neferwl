package ports

import "time"

// Private capture sessions (neferwl_capture_manager_v1, see
// internal/adapters/wayland/capturesession/neferwl-capture-v1.xml).
//
// A session is opened by one client connection (the capture connection) for
// one workspace of one output. While core reports it Active, the layer
// surfaces another connection attached to it (the HUD, authorised by token
// and peer UID) stay visible over a fullscreen window and are left out of
// clean captures; screen captures requested by the session's own connection
// are tagged Clean. Every other capture keeps the HUD and the native border.
//
// Only one session is live in the compositor. Core owns the session state;
// wayland only relays requests and tells clients what core decided.

const (
	// MaxCaptureSessionLayers bounds the layer surfaces attached to a session.
	MaxCaptureSessionLayers = 4
	// MaxCaptureExcluded bounds SceneCapture.Excluded: attached layers and
	// their popup descendants.
	MaxCaptureExcluded = 128
	// CaptureSessionTimeout is how long core lets a session live without a
	// CaptureSessionPing (the explicit keep-alive) before it stops it.
	CaptureSessionTimeout = 5 * time.Second
	// CaptureBorderWidth and CaptureBorderColor are the native compositor
	// border of a recording session (CaptureSessionBegin.Record), in logical
	// pixels and #rrggbb.
	CaptureBorderWidth = 2
	CaptureBorderColor = "#ff3b30"
)

// CaptureReason says why a session is not active. The zero value is none.
type CaptureReason string

const (
	CaptureReasonNone CaptureReason = ""
	// Every reason ends the session: core forgets it and wayland stops it.
	CaptureReasonOutputGone    CaptureReason = "output-gone"
	CaptureReasonOutputOff     CaptureReason = "output-off"
	CaptureReasonPingTimeout   CaptureReason = "ping-timeout"
	CaptureReasonWorkspaceGone CaptureReason = "workspace-gone"
	CaptureReasonBusy          CaptureReason = "busy" // another session is live
	CaptureReasonInvalidRegion CaptureReason = "invalid-region"
	CaptureReasonUnauthorized  CaptureReason = "unauthorized" // wayland only: peer credentials unreadable or not the compositor's user
	// TooManyExcluded: the attached layers and their popups exceed
	// MaxCaptureExcluded; a capture could not leave them all out (fail closed).
	CaptureReasonTooManyExcluded CaptureReason = "too-many-excluded"
)

// Terminal reports whether a state with this reason ends the session.
func (r CaptureReason) Terminal() bool { return r != CaptureReasonNone }

// CaptureSessionBegin carries wayland → core a new session. There are two
// target kinds:
//   - Workspace 0 is an output target: whatever the output shows, bars and
//     margins included, overview too. A zero Region means the whole output;
//     a non-zero one is clipped to the output.
//   - Workspace is a ports.WorkspaceInfo.ID: that workspace's frame
//     (its own viewport, no bars). A zero Region means the whole frame; a
//     non-zero one is clipped to the frame. The workspace may be on screen or
//     not, and the overview does not matter: one that is not drawn as itself
//     is rendered for capture only (Scene.CaptureScene). It ends with
//     CaptureReasonWorkspaceGone when it is removed or moves to another
//     output; sliding never pauses it.
//
// Region is output-local logical pixels. Record asks for the native border.
// ID is chosen by wayland and unique for the process lifetime.
type CaptureSessionBegin struct {
	ID        uint64
	Output    string
	Workspace uint64
	Region    Rect
	Record    bool
}

func (CaptureSessionBegin) clientEvent() {}

// CaptureSessionLayer carries wayland → core a layer surface the session's
// authorised HUD attached (or detached, when it was destroyed). Layer is the
// LayerSurface.ID, known before the surface maps. Wayland has already checked
// the layer is top or overlay and that it never takes the keyboard
// exclusively; at most MaxCaptureSessionLayers are attached.
type CaptureSessionLayer struct {
	ID       uint64
	Layer    WindowID
	Attached bool
}

func (CaptureSessionLayer) clientEvent() {}

// CaptureSessionPing carries wayland → core the session owner's keep-alive.
type CaptureSessionPing struct{ ID uint64 }

func (CaptureSessionPing) clientEvent() {}

// CaptureSessionEnd carries wayland → core an explicit stop or the owner's
// disconnect. Unknown IDs are ignored.
type CaptureSessionEnd struct{ ID uint64 }

func (CaptureSessionEnd) clientEvent() {}

// CaptureSessionState carries core → wayland what core decided (wayland relays it as neferwl_capture_session_v1.state), on every
// change of it. Rect is the clipped target in output-local logical pixels and
// Workspace the real workspace ID. Active means captures are being served
// (and the scene carries SceneCapture); a non-empty Reason with Active false
// explains it, and every Reason ends the session. A live session is always
// Active: a slide or swipe does not pause it (a workspace target's geometry is
// its own viewport, unaffected), and a session that is lost is stopped, never
// paused.
//
// Revision is the session's revision fence, from 1. Core bumps it whenever
// what a clean capture must do changes: the start, an attached or detached
// layer, an excluded popup or the target. The scene carrying that
// change has the same SceneCapture.Revision. Layers lists the attached layer
// surfaces core knows (at most MaxCaptureSessionLayers): wayland confirms an
// attach to the client only once a state lists the layer.
//
// Wayland serves a clean capture only for a session Active in the last state
// it saw, stamps CaptureRequest.CaptureRevision with that state's Revision,
// and a renderer refuses the request unless its scene's Capture.Revision is
// at least that: a scene published before the change is never used, so no
// stale exclusion list can leak a HUD.
type CaptureSessionState struct {
	ID        uint64
	Output    string
	Rect      Rect
	Workspace uint64
	Active    bool
	Reason    CaptureReason
	Revision  uint64
	Layers    []WindowID
}

func (CaptureSessionState) clientCommand() {}

// SceneCapture carries core → renderer the active session on one output's
// scene (Scene.Capture; nil when none). TargetRect is output-local logical:
// the clipped region of the output, or of the workspace frame. Excluded lists
// the attached layer surfaces and their popup descendants (at most
// MaxCaptureExcluded), which clean captures leave out. BorderWidth and
// BorderColor are the native border of a recording session, zero and empty
// otherwise; it is drawn inside TargetRect, on its edge, in every capture
// that is not Clean, and only when the target is drawn on screen: a
// workspace rendered for capture only (Scene.CaptureScene) has none.
// Workspace is the real workspace ID, 0 for an output target. For such a
// hidden workspace TargetRect is relative to the origin of CaptureScene (the
// workspace viewport), the scene that capture renders; otherwise it is
// output-local.
type SceneCapture struct {
	Session     uint64
	TargetRect  Rect
	Excluded    []WindowID
	BorderWidth int
	BorderColor string
	Workspace   uint64
	// Revision is the CaptureSessionState.Revision this scene carries; a
	// clean CaptureRequest needs Revision >= its CaptureRevision.
	Revision uint64
}
