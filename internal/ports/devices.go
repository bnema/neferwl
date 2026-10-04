package ports

import (
	"errors"
	"fmt"
	"image"
	"os"
	"time"
)

// MaxCaptureInflight bounds the capture requests admitted but not yet
// answered, across outputs. The reply channel of the production app has
// at least this capacity, so an output never blocks on a reply.
const MaxCaptureInflight = 32

// ErrCaptureTransient marks a capture failure that may clear on its own (the
// renderer busy, the indicator or an exclusion not shown yet): a protocol
// frame tries again shortly instead of failing. Test with errors.Is.
var ErrCaptureTransient = errors.New("capture: transient")

// ErrCaptureBusy: every capture slot of the renderer is leased.
var ErrCaptureBusy = fmt.Errorf("%w: no free slot", ErrCaptureTransient)

// CaptureFrame is a lease on one renderer readback slot holding a GPU
// copy of a rendered frame. Done and Read make no Vulkan call: a worker
// goroutine waits on Done, then reads. EndCapture returns the lease.
type CaptureFrame interface {
	// Done is the sync file signalled when the copy finished; nil means it
	// had finished when BeginCapture returned. The frame owns it.
	Done() *os.File
	// Read copies region (output pixels) as opaque sRGB8 BGRA rows into
	// dst, stride bytes per row. Valid after Done signalled and before
	// EndCapture.
	Read(region image.Rectangle, dst []byte, stride int) error
}

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
	// Pixels reads the last frame back (headless screenshots, tests). It
	// waits for the GPU: debug only. Nil when it cannot (every capture
	// slot leased, or the copy failed).
	Pixels() *image.RGBA
	// HDRPixels reads the last HDR frame's XRGB2101010 target back, each
	// 10-bit code scaled to 16 bits (c<<6 | c>>4), A = 0xffff. It waits for
	// the GPU: debug only. Nil when the output is SDR, the target is not
	// readable, a slot is busy or the copy failed.
	HDRPixels() *image.RGBA64
	// BeginCapture submits a GPU copy of the last rendered frame into a
	// free slot and returns without waiting. ErrCaptureBusy when every
	// slot is leased; another error when the copy cannot be tracked (no
	// sync file): the caller fails the request.
	BeginCapture() (CaptureFrame, error)
	// EndCapture returns a lease. It never waits: a slot whose copy may
	// still run is reused only once the renderer sees it finished.
	EndCapture(CaptureFrame)
	// Trim frees client buffer caches left undrawn for a while and what
	// finished frames held, without rendering. Outputs call it
	// periodically: an idle output renders no frame to free them.
	Trim(now time.Time) error
	// TakeRedrawn returns the target pixels drawn since the last call
	// (damage-limited frames count their damage only) and starts over.
	TakeRedrawn() int
	Close()
}

// Seat is the device broker (libseat) input devices are opened through.
type Seat interface {
	OpenDevice(path string) (int, error)
	CloseDevice(fd int)
	SwitchVT(vt int)
}
