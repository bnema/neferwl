package ports

// WindowStats carries wayland → core the frame statistics of the watched
// window (WatchStats). It is comparable. FPS figures are over the frames
// recorded so far (at most StatsFrames); 0 when fewer than two frames.
type WindowStats struct {
	Window WindowID
	// Client frames are root-surface commits; shown frames are flips that
	// showed a new commit of the window.
	ClientFPS, ShownFPS float32
	// Avg, Min, Max, Low1 and Low01 are client FPS: mean, slowest frame,
	// fastest frame, mean of the slowest 1 % and of the slowest 0.1 % frames.
	Avg, Min, Max, Low1, Low01 float32
	// Frametimes are the last StatsGraph client frame times in microseconds,
	// oldest first, capped at 65535; 0 where no frame was recorded.
	Frametimes    [StatsGraph]uint16
	Width, Height int    // last committed buffer size
	Format        uint32 // DRM fourcc of the last DMA-BUF, 0 for wl_shm
	Modifier      uint64
	DMABuf        bool
	ExplicitSync  bool // last commit used wp_linux_drm_syncobj
	Tearing       bool // last commit asked for async presentation
	// Last flip that showed the window: scanned out without composition
	// (direct scanout or overlay plane), variable refresh on, torn.
	Scanout, VRR, Async bool
	// ScanoutReason and OverlayReason are why the output last refused direct
	// scanout and the overlay plane ("" when used or unknown). ScanoutReason
	// "overlays" means the window is otherwise scene-candidate eligible with
	// compositor overlays hidden, not that hardware acceptance is guaranteed
	// (see FlipInfo). Other candidate failures take precedence.
	ScanoutReason, OverlayReason string
}

func (WindowStats) clientEvent() {}

const (
	StatsFrames = 1024 // frame times kept per watched window
	StatsGraph  = 64   // frame times reported for a graph
)
