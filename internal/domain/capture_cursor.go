package domain

// CaptureCursor is an immutable output-buffer-local cursor snapshot. Image is
// premultiplied sRGB BGRA, already scaled and transformed for the output; X,Y
// locate its hotspot in buffer pixels. Nil means no visible cursor. The output
// owner keeps Pixels alive until BeginCaptureCursor returns (the GPU upload
// belongs to the capture lease). It is never drawn into the display target.
type CaptureCursor struct {
	Image CursorImage
	X, Y  int
}
