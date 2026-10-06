package ports

import (
	"image"
	"time"
)

// DefaultSDRBrightness is the BT.2408 SDR reference white in HDR, in nits.
const DefaultSDRBrightness = 203

// OutputConfig configures one connector. Name is the connector (e.g. "DP-2").
// Mode is "WxH" (highest refresh) or "WxH@Hz" (closest refresh); empty picks the
// monitor's preferred mode. Off disables the connector.
// Every connected output that is not off is used.
// Placement, by precedence: Pos (set at runtime by wlr-output-management),
// then Anchor against another connected output, then automatic: right of
// everything placed so far (never left of 0), at y=0, in config order, then in
// connection order.
// Scale is the output scale (0 means 1); layout works in logical pixels,
// physical = logical × Scale. Placement and offsets use the transformed
// logical size.
// Primary gets the focus and the pointer at startup, wherever it is placed.
// ScaleOnly marks an entry set only by output.<name> subkeys: it
// does not select the connector.
type OutputConfig struct {
	Name  string
	Mode  string
	Off   bool
	Scale float64
	// Transform is output.<name>.transform; logical size swaps for 90/270.
	Transform BufferTransform
	// Pos is an explicit logical placement set at runtime; nil uses Anchor, else automatic layout.
	Pos *image.Point
	// Anchor places the output against another connected one.
	Anchor        OutputAnchor // RelationNone: automatic
	Primary       bool
	HDR           bool // opt-in to HDR10 on capable outputs
	SDRBrightness int  // nits; default DefaultSDRBrightness
	ScaleOnly     bool
}

// OutputRelation is the side of another output where an output is placed.
type OutputRelation uint8

const (
	RelationNone OutputRelation = iota
	RelationRightOf
	RelationLeftOf
	RelationAbove
	RelationBelow
)

// OutputAnchor places an output against another connected one, named by To.
// Offset is in logical pixels, along the shared edge: y for right-of and
// left-of (positive is down), x for above and below (positive is right).
type OutputAnchor struct {
	Relation OutputRelation
	To       string
	Offset   int
}

// LayoutRules are the layout settings shared by the defaults, screens and
// named workspaces. Zero MaxColumns and an empty Overflow inherit.
type LayoutRules struct {
	// MaxColumns is how many columns share the screen at once.
	MaxColumns int
	// Overflow is "scroll" (horizontal scrolling), "fixed" (spiral splits),
	// or "cascade" (vertically scrolling bands of columns).
	Overflow string
}

// Over fills the unset rules of r from base.
func (r LayoutRules) Over(base LayoutRules) LayoutRules {
	if r.MaxColumns == 0 {
		r.MaxColumns = base.MaxColumns
	}
	if r.Overflow == "" {
		r.Overflow = base.Overflow
	}
	return r
}

// OutputLayout is the layout of one screen, matched by connector (DP-2) or
// monitor key.
type OutputLayout struct {
	Output string
	LayoutRules
}

// Focus indicator: the animation (Config.Focus.Animation) shapes in time
// the visual effect (Config.Focus.Effect) drawn on a newly focused window.
const (
	FocusAnimationOff   = "off"
	FocusAnimationPulse = "pulse"
	FocusEffectScreen   = "screen"
)

// Config is the parsed compositor configuration (see the config adapter for keys).
type Config struct {
	Keyboard struct {
		Layout, Variant, Options string
		RepeatRate               int
		RepeatDelay              int
		CmdKey                   string
	}
	Terminal struct {
		Command  []string
		AutoOpen string
	}
	// Xwayland is the xwayland-satellite binary serving X11 clients; empty
	// disables X11.
	Xwayland string
	// Startup are commands run once when the session starts, in order.
	Startup    [][]string
	Background struct{ Color string }
	Floating   struct{ Dim float64 }
	// Stash is the strip of windows set aside by toggle-window-stash:
	// Width is the width of its selected window (10 to 90) and Gap the
	// space between it and its neighbors (0 to 10), in percent of the
	// usable width; Dim darkens the neighbors, 0 to 1. Capture puts new
	// windows in the stash while it is shown.
	Stash struct {
		Width, Gap int
		Dim        float64
		Capture    bool
	}
	// Border is drawn inside the window edge; Width 0 disables it.
	Border struct {
		Width    int
		Active   string
		Inactive string
	}
	Layout struct {
		Gaps    int
		Presets []string
		// LayoutRules are the defaults; Outputs override them per screen and
		// named workspaces override them again.
		LayoutRules
		Outputs []OutputLayout
	}
	// InputDevicesConfig holds Touchpad and Mouse, promoted.
	InputDevicesConfig
	// Cursor.HideAfter hides the pointer cursor after this long without
	// motion; the next motion shows it. 0 never hides it.
	Cursor struct{ HideAfter time.Duration }
	// Animations: On enables motion (off lands everything instantly);
	// Slowdown scales every spring's duration (not the focus pulse), 0.1 to
	// 10 (the config's animations.speed maps to it).
	Animations struct {
		On       bool
		Slowdown float64
	}
	Focus struct {
		// FollowMove shows the target workspace after a column or window
		// moves to it.
		FollowMove bool
		// Animation and Effect mark a window that just got the focus:
		// FocusAnimation* (off disables it) and FocusEffect*. Strength is
		// the effect's peak, 0.01 to 0.2.
		Animation, Effect string
		Strength          float64
	}
	// Workspaces are declared with workspace.<name>.* keys, in first-seen order.
	Workspaces []WorkspaceConfig
	// Outputs selects and configures physical displays (drm backend).
	Outputs []OutputConfig
	Binds   map[string]string
	Render  struct {
		DirectScanout bool
		// Tearing honours wp_tearing_control_v1 in direct scanout; VRR
		// turns variable refresh on while a buffer is scanned out.
		Tearing, VRR bool
		// VRRFlipGap is the minimum time between a game frame's flip and
		// the next frame commit under VRR (0: off). It works around
		// flips that land at the panel's slowest refresh.
		VRRFlipGap time.Duration
	}
	Performance struct{ Realtime bool }
	Log         struct {
		Level string
		Debug []string
	}
}

// PointerConfig holds the settings mice and touchpads share. NaturalScroll
// moves the content with the fingers or the wheel instead of against them.
// AccelSpeed (-1 to 1) and AccelProfile (AccelAdaptive or AccelFlat) set the
// pointer speed. LeftHanded swaps the left and right buttons.
type PointerConfig struct {
	NaturalScroll bool
	AccelSpeed    float64
	AccelProfile  string
	LeftHanded    bool
}

// TouchpadConfig configures touchpads: the shared pointer settings
// (NaturalScroll also applies to two-finger scroll and swipes), plus Tap,
// which clicks on a tap (one finger left, two right, three middle), and
// ScrollFactor, which multiplies two-finger scroll.
type TouchpadConfig struct {
	PointerConfig
	Tap          bool
	ScrollFactor float64
}

// InputDevicesConfig is the live configuration of the input devices, sent to
// the input adapter as one value. It is comparable.
type InputDevicesConfig struct {
	Touchpad TouchpadConfig
	Mouse    PointerConfig
}

// Pointer acceleration profiles, for mice and touchpads.
const (
	AccelAdaptive = "adaptive"
	AccelFlat     = "flat"
)

// WorkspaceConfig declares a bind-only named workspace, outside the numbered list.
// Zero MaxColumns and an empty Overflow use the layout.* defaults.
type WorkspaceConfig struct {
	Name string
	// Monitor is the home monitor: a connector name (DP-2) or a monitor key
	// ("make model serial"); empty follows the focused output when shown.
	Monitor string
	// Size overrides the workspace's logical width and height. Zero inherits
	// the monitor; its scale is always inherited.
	Size [2]int
	// Slots are the declared columns (workspace.<name>.column.N), by N.
	Slots []SlotConfig
	LayoutRules
}

// SlotConfig reserves column N of a workspace for the window of one command.
// neferwl spawns the command at startup; the window it opens goes to the
// slot. Width is a layout width (fraction, percentage or pixels).
type SlotConfig struct {
	Index int
	Width string
	Argv  []string
}
