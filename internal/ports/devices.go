package ports

import "image"

// Renderer draws scenes for an output. One goroutine owns it. Frames go
// to its own image until ExportTargets gives it scanout images; then each
// Render draws into the target chosen by UseTarget (ADR 014: no CPU copy).
type Renderer interface {
	Render(Scene, map[WindowID]SurfaceContent) error
	// ExportTargets allocates n images of the renderer's size that the
	// display can scan out, with one of the given XRGB8888 modifiers
	// (none: any the device exports), and returns them as dmabufs.
	ExportTargets(n int, modifiers []uint64) ([]DMABuf, error)
	// UseTarget selects the exported image the next Render draws into.
	UseTarget(i int)
	// Pixels reads the last frame back (headless screenshots, tests).
	Pixels() *image.RGBA
	// CopyBGRX reads the last frame back as XRGB8888 rows of the given
	// pitch: the logged fallback when no image can be exported.
	CopyBGRX(dst []byte, pitch int)
	Close()
}

// Seat is the device broker (libseat) input devices are opened through.
type Seat interface {
	OpenDevice(path string) (int, error)
	CloseDevice(fd int)
	SwitchVT(vt int)
}
