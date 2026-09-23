package ports

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
	Binds  map[string]string `toml:"binds"`
	Render struct {
		DirectScanout bool `toml:"direct_scanout"`
	} `toml:"render"`
	Log struct {
		Level string
		Debug []string
	} `toml:"log"`
}
