package ports

// OutputConfig configures one connector. Name is the connector (e.g. "DP-2").
// Mode is "WxH" (highest refresh) or "WxH@Hz" (closest refresh); empty picks the
// monitor's preferred mode. Off disables the connector.
// Every connected output that is not off is used. Outputs are placed left to
// right in config order, then in connection order.
// Scale is the output scale (0 means 1); layout works in logical pixels,
// physical = logical × Scale.
// Primary gets the focus and the pointer at startup, wherever it is placed.
// ScaleOnly marks an entry set by output.<name>.scale or .primary alone: it
// does not select the connector.
type OutputConfig struct {
	Name      string
	Mode      string
	Off       bool
	Scale     float64
	Primary   bool
	ScaleOnly bool
}

// Config is the parsed compositor configuration (see the config adapter for keys).
type Config struct {
	Keyboard struct {
		Layout, Variant, Options string
		RepeatRate               int
		RepeatDelay              int
		CmdKey                   string
	}
	Terminal struct{ Command []string }
	// Xwayland is the xwayland-satellite binary serving X11 clients; empty
	// disables X11.
	Xwayland string
	// Startup are commands run once when the session starts, in order.
	Startup    [][]string
	Background struct{ Color string }
	// Border is drawn inside the window edge; Width 0 disables it.
	Border struct {
		Width    int
		Active   string
		Inactive string
	}
	Layout struct {
		Gaps int
		// MaxColumns is how many columns share the screen before scrolling.
		MaxColumns int
		Presets    []string
		// Overflow is "scroll" or "fixed" (see WorkspaceConfig).
		Overflow string
	}
	Focus struct {
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
	}
	Log struct {
		Level string
		Debug []string
	}
}

// WorkspaceConfig declares a named workspace. Hidden ones are not numbered and
// only reachable through a `workspace <name>` bind, which toggles them.
// Zero MaxColumns and an empty Overflow use the layout.* defaults.
type WorkspaceConfig struct {
	Name string
	// Monitor is the home monitor: a connector name (DP-2) or a monitor key
	// ("make model serial"); empty means the first output.
	Monitor string
	// Slots are the declared columns (workspace.<name>.column.N), by N.
	Slots      []SlotConfig
	Hidden     bool
	MaxColumns int
	Overflow   string
}

// SlotConfig reserves column N of a workspace for the window of one command.
// nefertty spawns the command at startup; the window it opens goes to the
// slot. Width is a layout width (fraction, percentage or pixels).
type SlotConfig struct {
	Index int
	Width string
	Argv  []string
}
