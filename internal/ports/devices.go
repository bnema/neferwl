package ports

import (
	"errors"
	"fmt"
	"image"
	"os"
	"time"

	"github.com/bnema/neferwl/internal/domain"
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
	// had finished when BeginCapture returned. The frame owns it: it stays
	// valid until EndCapture, and the reader must not close it.
	Done() *os.File
	// Read copies region (output pixels) as opaque sRGB8 BGRA rows into
	// dst, stride bytes per row. Valid after Done signalled and before
	// EndCapture.
	Read(region image.Rectangle, dst []byte, stride int) error
}

// Renderer draws scenes for an output. One goroutine owns it. Frames go
// to its own image until ExportTargets or ImportTargets gives it scanout
// images; then each Render draws into the target chosen by UseTarget
// (ADR 014: no CPU pass).
type Renderer interface {
	// SetHDR selects the HDR10 output transform (0 disables it).
	// The DRM output calls this before ExportTargets or ImportTargets.
	SetHDR(sdrNits float64)
	// Render draws the scene. done is a sync file signalled when the GPU
	// finished the frame (nil: already finished); the caller closes it.
	Render(Scene, map[domain.WindowID]SurfaceContent) (done *os.File, err error)
	// ExportTargets allocates n images of the renderer's size that the
	// display can scan out, with one of the given XRGB8888 (SDR) or
	// XRGB2101010 (HDR) modifiers (none: any the device exports),
	// and returns them as dmabufs; a modifier may have several memory
	// planes (DCC metadata), each plane owning its file. singlePlane
	// restricts the choice to one-plane modifiers.
	// n = 0 drops the targets.
	ExportTargets(n int, modifiers []uint64, singlePlane bool) ([]DMABuf, error)
	// TargetModifiers lists the modifiers an image allocated outside the
	// renderer (by the display's allocator) can have to be imported as a
	// target: those the device renders into and imports for the XRGB8888
	// (SDR) or XRGB2101010 (HDR) format, that the display lists (empty: any),
	// without multi-plane ones when singlePlane. Empty when the device
	// cannot import dmabufs.
	TargetModifiers(display []uint64, singlePlane bool) []uint64
	// ImportTargets makes images allocated outside the renderer its
	// targets, replacing the previous ones (no buffers: drops them). Each
	// is one memory object of the renderer's size and target format, with
	// a modifier from TargetModifiers: every plane file is the same
	// dmabuf (the planes differ by offset), and disjoint planes are
	// refused. The caller keeps its files and must keep the memory alive
	// while the targets are in use. An error leaves the renderer without
	// targets.
	ImportTargets(bufs []DMABuf) error
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

// ErrDeviceLost marks a Vulkan device loss requiring device recreation.
var ErrDeviceLost = errors.New("render device lost")

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

// RenderDevice owns one GPU's rendering and copy resources.
type RenderDevice interface {
	NewRenderer(w, h int, o RendererOptions) (Renderer, error)
	NewCopier(w, h int) (Copier, error)
	DMABuf() DMABufSupport
	Node() (primary, render uint64)
	Close()
}

// RenderDeviceOpener opens a GPU; zero GPUInfo selects the first usable device.
type RenderDeviceOpener interface {
	Open(g GPUInfo) (RenderDevice, error)
}

// Copier copies a rendered image from another GPU into a scanout buffer, on
// the display GPU. One goroutine owns it.
type Copier interface {
	ImportSources(bufs []DMABuf) error
	ImportTargets(bufs []DMABuf) error
	SourceModifiers(formats []DMABufFormat) []DMABufFormat // what it can sample
	Copy(src, dst int, acquire *os.File) (done *os.File, err error)
	Close()
}

// GPUSelection identifies the render GPU and cards kept by selection.
type GPUSelection struct {
	Render  int      // index into candidates
	Keep    []int    // candidates to open, ascending
	Reasons []string // one human line per decision, logged by app
}
