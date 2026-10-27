package domain

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
