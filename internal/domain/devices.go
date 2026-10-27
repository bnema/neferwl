package domain

// RendererOptions selects per-renderer output and diagnostic behavior.
type RendererOptions struct{ Virtual, Readback, TraceDamage bool }

// GPUInfo describes one DRM card. Primary/Render are dev_t (0 = absent).
type GPUInfo struct {
	Path     string // /dev/dri/cardN
	Primary  uint64
	Render   uint64
	PCI      string // "0000:03:00.0", "" if not PCI
	BootVGA  bool
	Internal bool   // a connected eDP/LVDS/DSI connector
	Driver   string // DRM driver name, e.g. "amdgpu", "nvidia-drm"
}

// GPUSelection identifies the render GPU and cards kept by selection.
type GPUSelection struct {
	Render  int      // index into candidates
	Keep    []int    // candidates to open, ascending
	Reasons []string // one human line per decision, logged by app
}
