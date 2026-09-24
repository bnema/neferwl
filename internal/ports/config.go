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

// Config is the parsed compositor configuration.
type Config struct {
	Keyboard struct {
		Layout, Variant, Options string
		RepeatRate               int    `toml:"repeat_rate"`
		RepeatDelay              int    `toml:"repeat_delay"`
		CmdKey                   string `toml:"cmd_key"`
	} `toml:"keyboard"`
	Terminal   struct{ Command []string } `toml:"terminal"`
	Background struct{ Color string }     `toml:"background"`
	Layout     struct {
		Gaps               int
		DefaultColumnWidth string `toml:"default_column_width"`
		Presets            []string
	} `toml:"layout"`
	// Outputs selects and configures physical displays (drm backend).
	Outputs []OutputConfig    `toml:"output"`
	Binds   map[string]string `toml:"binds"`
	Render  struct {
		DirectScanout bool `toml:"direct_scanout"`
	} `toml:"render"`
	Log struct {
		Level string
		Debug []string
	} `toml:"log"`
}
