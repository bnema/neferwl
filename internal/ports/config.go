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
// Every connected output that is not off is used. Outputs are placed left to
// right in config order, then in connection order.
// Scale is the output scale (0 means 1); layout works in logical pixels,
// physical = logical × Scale.
// Primary gets the focus and the pointer at startup, wherever it is placed.
// ScaleOnly marks an entry set only by output.<name> subkeys: it
// does not select the connector.
type OutputConfig struct {
	Name  string
	Mode  string
	Off   bool
	Scale float64
	// Pos is an explicit logical placement set at runtime; nil uses automatic layout.
	Pos           *image.Point
	Primary       bool
	HDR           bool // opt-in to HDR10 on capable outputs
	SDRBrightness int  // nits; default DefaultSDRBrightness
	ScaleOnly     bool
}

// LayoutRules are the layout settings shared by the defaults, screens and
// named workspaces. Zero MaxColumns and an empty Overflow inherit.
type LayoutRules struct {
	// MaxColumns is how many columns share the screen at once.
	MaxColumns int
	// Overflow is "scroll" (further columns scroll) or "fixed" (they split
	// the last one).
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
	// usable width; Dim darkens the neighbors, 0 to 1.
	Stash struct {
		Width, Gap int
		Dim        float64
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
	Touchpad TouchpadConfig
	Focus    struct {
		// FollowMove shows the target workspace after a column or window
		// moves to it.
		FollowMove bool
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

// TouchpadConfig configures touchpads. NaturalScroll moves the content with
// the fingers, for two-finger scroll and three-finger swipes. Tap clicks on
// a tap: one finger left, two right, three middle. AccelSpeed (-1 to 1) and
// AccelProfile (AccelAdaptive or AccelFlat) set the pointer speed;
// ScrollFactor multiplies two-finger scroll.
type TouchpadConfig struct {
	NaturalScroll bool
	Tap           bool
	AccelSpeed    float64
	AccelProfile  string
	ScrollFactor  float64
}

// Touchpad pointer acceleration profiles.
const (
	AccelAdaptive = "adaptive"
	AccelFlat     = "flat"
)

// WorkspaceConfig declares a bind-only named workspace, outside the numbered list.
// Zero MaxColumns and an empty Overflow use the layout.* defaults.
type WorkspaceConfig struct {
	Name string
	// Monitor is the home monitor: a connector name (DP-2) or a monitor key
	// ("make model serial"); empty means the first output.
	Monitor string
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
