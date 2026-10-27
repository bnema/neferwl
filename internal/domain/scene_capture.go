package domain

import "time"

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
