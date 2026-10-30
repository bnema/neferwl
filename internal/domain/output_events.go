package domain

import (
	"image"
	"time"
)

// OutputInfo describes a connected display. Sizes are physical pixels.
type OutputInfo struct {
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

// OutputPresented carries output → wayland what an output shows and reads.
// Flip is set when a page flip completed: frame callbacks of the surfaces
// on it are due. Shown and Queued are the DMABuf IDs scanned out directly
// (0: a composed image); Seen is the latest content Seq per window the
// output finished reading (the GPU is done with it). A replaced client
// buffer is released once every output that reports has seen a later
// content of its window and neither shows nor queues it.
type OutputPresented struct {
	Output        string
	Flip          *FlipInfo
	Shown, Queued uint64
	Seen          map[WindowID]uint64
	// ChildReads is the oldest content Seq per window still read by a child
	// renderer on a separate device. Unlike Seen, these holds never time out:
	// they end only with a completed render or device shutdown.
	ChildReads map[WindowID]uint64
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
