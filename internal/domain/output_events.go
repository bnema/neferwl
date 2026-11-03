package domain

import (
	"image"
	"time"
)

// OutputInfo describes a connected display. Sizes are physical pixels.
type OutputInfo struct {
	// Virtual distinguishes adapter-hosted outputs from physical connectors.
	Virtual bool
	// Name is the connector (e.g. DP-2), matched against output.<name>.* config.
	Name string
	// Make, Model and Serial come from EDID; empty when unknown.
	Make, Model, Serial  string
	Width, Height        int
	RefreshMilli         int
	PhysicalW, PhysicalH int // millimetres
}

// Key identifies the monitor across connectors: make, model and serial, or
// the connector name when EDID has no serial.
func (i OutputInfo) Key() string {
	if i.Serial == "" {
		return i.Name
	}
	return i.Make + " " + i.Model + " " + i.Serial
}

// OutputHead describes a connected connector even when it is disabled.
// Current is the active mode; it is nil for disabled or not-yet-running heads.
type OutputHead struct {
	Info    OutputInfo
	Modes   []OutputMode
	Current *OutputMode
	Enabled bool
}

// OutputHeads is a complete backend inventory, delivered to Wayland after a scan.
type OutputHeads struct{ Heads []OutputHead }

// OutputWarning is a non-protocol diagnostic about desired output configuration.
type OutputWarning struct {
	Name    string
	Message string
}

// HeadChange is the complete desired state of one head in a configuration.
type HeadChange struct {
	Name    string
	Enabled bool
	Mode    *OutputMode
	Pos     *image.Point
	Scale   float64
	// Transform is nil to leave the transform unchanged.
	Transform *BufferTransform
}

// OutputApply asks the app owner to validate or apply a runtime configuration.
type OutputApply struct {
	ID    uint64
	Test  bool
	Heads []HeadChange
}

// OutputApplied reports completion (or failure) of a runtime configuration.
type OutputApplied struct {
	ID  uint64
	Err error
}

// OutputFormats carries output → wayland the dmabuf formats an output can
// scan out directly (its primary plane's, that the renderer also samples,
// so a refused buffer can still be composed). Device is the KMS device
// (dev_t) clients allocate scanout buffers for. Sent after every successful
// modeset, including startup and VT resume; empty Formats means no direct
// scanout. HDR is non-nil only when the output's HDR10 modeset succeeded;
// nil means SDR, regardless of the monitor's HDR capability.
type OutputFormats struct {
	Output  string
	Device  uint64
	Formats []DMABufFormat
	HDR     *OutputHDR
}

// OutputHDR carries the active HDR10 output's EDID luminances in nits.
// These describe the display, not any client's content metadata.
type OutputHDR struct {
	MaxLuminance, MaxFrameAverage, MinLuminance float64
}

// FlipInfo is one completed page flip. When is its CLOCK_MONOTONIC time
// (hardware clock on DRM), Seq the output's vblank counter, Refresh the
// mode refresh period (the shortest period under variable refresh).
// VariableRate records whether VRR was active for this frame. ZeroCopy is the window
// whose buffer was shown without composition (direct scanout or overlay
// plane; 0: none), Async set for a tearing flip. ScanoutReason and
// OverlayReason say why the output refused direct scanout and the overlay
// plane for this frame ("" when used, or on outputs that do not report it).
// Shows is the content Seq per window the flipped frame shows. Merged
// counts earlier flips folded into this one when the reader fell behind:
// their presentation feedback is discarded.
type FlipInfo struct {
	When         time.Duration
	Seq          uint64
	Refresh      time.Duration
	ZeroCopy     WindowID
	Async        bool
	VariableRate bool
	// ScanoutReason and OverlayReason: why direct scanout and the overlay
	// plane were refused for this frame ("" when used, or on outputs that do
	// not report it). ScanoutReason "overlays" means compositor overlays
	// are the only failed scene-candidate check; it does not promise that
	// the buffer passes subsequent hardware format, fence or KMS checks.
	// Other candidate failures take precedence over compositor overlays.
	ScanoutReason, OverlayReason string
	HardwareClock                bool
	Merged                       int
	Shows                        map[WindowID]uint64
}

// OutputFrame carries output → core a completed flip of an animating scene
// (Scene.Animating): the next frame is due. Sends never block; a full
// channel drops it.
type OutputFrame struct{ Output string }

// ConfigChanged carries config → core reloads.
type ConfigChanged struct{ Config Config }

// ScaleChanged carries core → persistence a scale set by a scale bind.
type ScaleChanged struct {
	Output string
	Scale  float64
}
