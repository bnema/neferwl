package ports

import (
	"image"
	"regexp"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/domain"
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

// OutputAnchor places an output against another connected one, named by To.
// Offset is in logical pixels, along the shared edge: y for right-of and
// left-of (positive is down), x for above and below (positive is right).
type OutputAnchor struct {
	Relation domain.OutputRelation
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
	// One startup line may hold several, split by SplitCommands.
	Startup    [][]string
	Background struct{ Color string }
	Floating   struct{ Dim float64 }
	// Stash is the strip of windows set aside by toggle-window-stash:
	// Width is the width of its selected window (10 to 90) and Gap the
	// space between it and its neighbors (0 to 10), in percent of the
	// usable width; Dim darkens the neighbors, 0 to 1. Capture puts new
	// windows in the stash while it is shown. SpawnTerminal makes
	// toggle-stash-visible on an empty stash open the terminal in it.
	Stash struct {
		Width, Gap    int
		Dim           float64
		Capture       bool
		SpawnTerminal bool
	}
	// Border is drawn inside the window edge; Width 0 disables it. Radius is
	// in logical pixels, >= 0 (at most 64 from the file): it rounds floats
	// always and tiles when gaps > 0; the renderer uses SceneWindow.Radius.
	Border struct {
		Radius   int
		Width    int
		Active   string
		Inactive string
	}
	Layout struct {
		Gaps int
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
	// Slowdown scales every spring's duration, 0.1 to
	// 10 (the config's animations.speed maps to it).
	Animations struct {
		On       bool
		Slowdown float64
	}
	Focus struct {
		// FollowMove shows the target workspace after a column or window
		// moves to it.
		FollowMove bool
		// Flash briefly draws an inner border on the entity an action changed
		// (window or column). FlashWidth is in logical pixels (1 to 8),
		// FlashOpacity the starting opacity (0.1 to 1), FlashDuration the fade
		// (50ms to 1s, not scaled by Animations.Slowdown), FlashColor #rrggbb or
		// empty for Border.Active.
		PositionHint  bool
		Flash         bool
		FlashWidth    int
		FlashOpacity  float64
		FlashDuration time.Duration
		FlashColor    string
	}
	// OSD is the window info overlay: holding the cmd key shows it,
	// double-tapping cmd or the osd-pin action keeps it shown.
	OSD struct {
		// Enable (osd.enable) turns the hold, the double tap and osd-pin on.
		Enable bool
	}
	// Hint configures keyboard hint mode (action hint).
	Hint struct {
		// Enable turns the experimental hint mode on (hint.enable). It is
		// read at startup: a reload keeps the startup value.
		Enable    bool
		Sources   []domain.HintSource // ordered startup-only chain
		LinksOnly bool                // hint.targets
		TextClick domain.Mods         // hint.text-click
		// Capture shows the labels in the captures requested by Wayland
		// clients (hint.capture); the compositor's own scans never see them.
		Capture                bool
		Size                   float64 // logical pixels; zero selects automatic density sizing
		Background, Foreground string  // empty derives from Border.Active
		// Detector is the nefer-hints binary (hint.detector): a path, or a
		// name looked up in PATH. Model is the directory of its model files
		// (hint.model), with a leading ~/ already expanded. Threshold is the
		// minimum detection score (hint.threshold, 0.01 to 0.99); zero leaves
		// it unset, using model.json or the runner default. All three
		// are read at startup.
		Detector, Model string
		Threshold       float64
	}
	// Workspaces are declared with workspace.<name>.* keys, in first-seen order.
	Workspaces []WorkspaceConfig
	// Rules are the window rules (rule.<name>.* keys), in first-seen order.
	Rules []WindowRule
	// Outputs selects and configures physical displays (drm backend).
	Outputs []OutputConfig
	Binds   map[string]string
	// DRM selects devices at startup; changes require a restart.
	DRM    struct{ Devices, Ignore []string }
	Render struct {
		// Device and MultiGPU are startup-only; changes require a restart.
		Device, MultiGPU string
		DirectScanout    bool
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

// CommandSeparator separates commands in config values that run programs
// (startup, spawn binds): "waybar; mako" starts both, without waiting.
const CommandSeparator = ";"

// SplitCommands splits v on CommandSeparator, then each command on
// whitespace. Empty commands are skipped; no shell is involved.
func SplitCommands(v string) [][]string {
	var commands [][]string
	for part := range strings.SplitSeq(v, CommandSeparator) {
		if argv := strings.Fields(part); len(argv) > 0 {
			commands = append(commands, argv)
		}
	}
	return commands
}

// SlotConfig reserves column N of a workspace for the window of one command.
// neferwl spawns the command at startup; the window it opens goes to the
// slot. Cells is its width in cells, a cell being one max-columns-th of
// the usable width (1 to 16; 0 shares the width with the other columns).
type SlotConfig struct {
	Index int
	Cells int
	Argv  []string
}

// WindowRule places the windows whose app ID it matches when they first map.
// Matching rules merge in config order, a later rule winning per field.
type WindowRule struct {
	Name string
	// AppID matches the whole app ID (the config anchors it).
	AppID *regexp.Regexp
	// Floating forces a window to float (true) or to tile (false); nil leaves it.
	Floating *bool
	// Workspace is a 1-based number of a numbered workspace or the name of a
	// declared one; empty leaves it.
	Workspace string
	// Monitor is a connector (DP-2) or a monitor key ("make model serial").
	Monitor string
	// Cells is the width in cells of the new column of a tiled window
	// (1 to 16; 0 leaves it automatic).
	Cells int
}
