package ports

import (
	"image"
	"os"
	"time"
)

// Renderer draws scenes for an output. One goroutine owns it. Frames go
// to its own image until ExportTargets gives it scanout images; then each
// Render draws into the target chosen by UseTarget (ADR 014: no CPU pass).
type Renderer interface {
	// SetHDR selects the HDR10 output transform (0 disables it).
	// The DRM output calls this before ExportTargets.
	SetHDR(sdrNits float64)
	// Render draws the scene. done is a sync file signalled when the GPU
	// finished the frame (nil: already finished); the caller closes it.
	Render(Scene, map[WindowID]SurfaceContent) (done *os.File, err error)
	// ExportTargets allocates n images of the renderer's size that the
	// display can scan out, with one of the given XRGB8888 (SDR) or
	// XRGB2101010 (HDR) modifiers (none: any the device exports),
	// and returns them as dmabufs.
	// n = 0 drops the targets.
	ExportTargets(n int, modifiers []uint64) ([]DMABuf, error)
	// UseTarget selects the exported image the next Render draws into.
	UseTarget(i int)
	// CursorBuffers allocates two linear ARGB8888 images of size×size the
	// display can show on its cursor plane, and returns them as dmabufs.
	CursorBuffers(size int) ([2]DMABuf, error)
	// WriteCursor fills cursor image i with premultiplied ARGB8888 pixels
	// (w×h, w*4 per row), cropped to the image and cleared around it.
	WriteCursor(i int, pixels []byte, w, h int) error
	// Pixels reads the last frame back (headless screenshots, tests).
	Pixels() *image.RGBA
	// Capture copies a region of the last rendered frame as opaque BGRA.
	Capture(region image.Rectangle, dst []byte, stride int) error
	// Trim frees client buffer caches left undrawn for a while and what
	// finished frames held, without rendering. Outputs call it
	// periodically: an idle output renders no frame to free them.
	Trim(now time.Time) error
	Close()
}

// Seat is the device broker (libseat) input devices are opened through.
type Seat interface {
	OpenDevice(path string) (int, error)
	CloseDevice(fd int)
	SwitchVT(vt int)
}
