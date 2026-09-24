package ports

// OutputConfig configures one connector. Name is the connector (e.g. "DP-2").
// Mode is "WxH" (highest refresh) or "WxH@Hz" (closest refresh); empty picks the
// monitor's preferred mode. Off disables the connector.
// The first listed, connected, enabled output is used; without a match, the first
// connected output that is not off.
type OutputConfig struct {
	Name string
	Mode string
	Off  bool
}

// Config is the parsed compositor configuration (see the config adapter for keys).
type Config struct {
	Keyboard struct {
		Layout, Variant, Options string
		RepeatRate               int
		RepeatDelay              int
		CmdKey                   string
	}
	Terminal   struct{ Command []string }
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
	}
	// Outputs selects and configures physical displays (drm backend).
	Outputs []OutputConfig
	Binds   map[string]string
	Render  struct {
		DirectScanout bool
	}
	Log struct {
		Level string
		Debug []string
	}
}
