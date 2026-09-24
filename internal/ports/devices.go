package ports

import "image"

// Renderer draws scenes for an output. One goroutine owns it.
type Renderer interface {
	Render(Scene, map[WindowID]SurfaceContent) error
	// Pixels returns the last frame (headless screenshots).
	Pixels() *image.RGBA
	// CopyBGRX writes the last frame as XRGB8888 rows of the given pitch (DRM scanout).
	CopyBGRX(dst []byte, pitch int)
	Close()
}

// Seat is the device broker (libseat) input devices are opened through.
type Seat interface {
	OpenDevice(path string) (int, error)
	CloseDevice(fd int)
	SwitchVT(vt int)
}
