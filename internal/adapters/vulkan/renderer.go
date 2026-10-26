package vulkan

import (
	"fmt"
	"image"
	"math"
	"os"

	"github.com/bnema/neferwl/internal/adapters/hdrcursor"
	"github.com/bnema/neferwl/internal/adapters/hintlabel"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// DeviceInfo describes the selected Vulkan device and its startup cost.
type DeviceInfo struct {
	Name, Primary, Render string
	CreateMS              int64
}

// Renderer is owned by one goroutine; its methods must not be called concurrently.
type Renderer struct {
	width, height int
	gpu           *GPU
	q             *queue
	// legacyOwner is temporary: New owns its GPU/instance until P1.4 removes New.
	legacyOwner bool
	// own is the SDR composition image; HDR uses hdrOwn and PQ targets.
	own      target
	hdrOwn   target // linear BT.709 in units of SDR reference white
	targets  []*target
	current  int
	hdrNits  float64
	hdr      hdrPass
	hdrError error
	// log reports the chosen target modifier; the zero value discards.
	log zerowrap.Logger
	// traceDamage logs every frame's redrawn area (--debug=render-damage).
	traceDamage bool
	// hdrReadback makes exported HDR targets transfer sources, for GPU tests
	// and headless --screenshot-raw (SetHDRReadback); never DRM.
	hdrReadback bool
	// virtual: the output has no display (headless), so ExportTargets
	// accepts an empty modifier list for HDR (SetVirtualOutput).
	virtual bool
	// One converted cursor image is kept; changes replace it in bounded memory.
	cursorCache hdrcursor.Cache
	// lastHDRTarget is the index in targets of the exported PQ target the
	// last HDR frame rendered into; -1 when the current targets hold none.
	lastHDRTarget int
	// last is the target of the last frame: what captures copy.
	last *target
	// captures are the readback slots (capture.go), allocated on the
	// first capture; capturePass converts HDR frames into captureSDR.
	captures    []*captureSlot
	capturePass hdrPass
	captureSDR  target
	sync        captureSync
	pool        vk.CommandPool
	// Frames in flight (frame.go): frame is the last frame recorded,
	// submitted the last submitted, completed the last known finished.
	slots                       [frameSlots]frameSlot
	frame, submitted, completed uint64
	deferred                    []deferredFree
	imports                     map[uint64]*imported
	// pools are client wl_shm pools mapped by pool ID.
	pools map[uint64]*mapping
	// retired are pool mappings replaced this frame, unmapped once the
	// frame's copies are done.
	retired [][]byte
	// shm are the GPU copies of wl_shm surfaces (shm.go); copied counts
	// the bytes copied into them, for tests.
	shm    map[shmKey]*shmSurface
	copied int
	// labels rasterizes the hint labels (hint_labels.go); the zero value works.
	labels hintlabel.Raster
	// shmRects is scratch for shmCopyFor.
	shmRects []image.Rectangle
	// marks date the frames for Trim's wall-clock eviction (trim.go).
	marks []trimMark
	// hidden are the windows the last scene lists as Hidden (noteHidden).
	hidden map[ports.WindowID]struct{}
	// redrawn counts the target pixels drawn (TakeRedrawn).
	redrawn int
	// fullRedraws counts why whole targets or windows were redrawn; each
	// TakeRedrawn logs and resets it.
	fullRedraws fullRedraws
	// cursors are the exported cursor images (CursorBuffers).
	cursors [2]*cursorImage
	// compose draws every frame (compose.go); maxRange bounds one
	// storage buffer binding.
	compose composer
	// Scratch belongs to the renderer goroutine; neither slice is published.
	scratchDraws  []draw
	scratchCovers []image.Rectangle
	damage        damageRegion
	damageSeen    map[ports.WindowID]bool
	damageDrawn   map[ports.WindowID]heldWindow
	frameDMAs     []*imported
	frameAcquires map[*imported]*os.File
	frameWaits    []vk.Semaphore
	frameStages   []vk.PipelineStageFlags
	// call holds the structs a per-frame Vulkan call is handed by pointer.
	call callScratch
	// capCall holds the structs a capture hands Vulkan by pointer.
	capCall captureScratch
}

// priorityAttempts falls back to an ordinary queue when elevated priority is unavailable.
func priorityAttempts(supported bool) []vk.QueueGlobalPriority {
	if supported {
		return []vk.QueueGlobalPriority{vk.QueueGlobalPriorityRealtime, vk.QueueGlobalPriorityHigh, 0}
	}
	return []vk.QueueGlobalPriority{0}
}

func checked(name string, result vk.Result) error {
	if err := vk.Check(result); err != nil {
		if result == vk.ErrorDeviceLost {
			return fmt.Errorf("%s: %w: %w", name, err, ports.ErrDeviceLost)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// New is a temporary compatibility wrapper; new callers use GPU.NewRenderer.
func New(width, height int) (*Renderer, error) {
	inst, err := NewInstance(zerowrap.Logger{})
	if err != nil {
		return nil, err
	}
	g, err := inst.openGPU(ports.GPUInfo{}, true)
	if err != nil {
		inst.Close()
		return nil, err
	}
	renderer, err := g.NewRenderer(width, height, ports.RendererOptions{})
	if err != nil {
		g.Close()
		inst.Close()
		return nil, err
	}
	r := renderer.(*Renderer)
	r.legacyOwner = true
	return r, nil
}

// NewRenderer creates per-output resources on this GPU.
func (g *GPU) NewRenderer(width, height int, o ports.RendererOptions) (renderer ports.Renderer, err error) {
	if width <= 0 || height <= 0 || width > math.MaxUint32 || height > math.MaxUint32 || uint64(width)*uint64(height) > uint64(math.MaxInt/4) {
		return nil, fmt.Errorf("invalid renderer dimensions %d x %d", width, height)
	}
	r := &Renderer{gpu: g, q: g.acquireQueue(), width: width, height: height, lastHDRTarget: -1, imports: map[uint64]*imported{}, pools: map[uint64]*mapping{}, shm: map[shmKey]*shmSurface{}, hidden: map[ports.WindowID]struct{}{}, log: g.log}
	r.compose.resources, r.compose.indexing = g.resources, g.indexing
	r.SetVirtualOutput(o.Virtual)
	r.SetTraceDamage(o.TraceDamage)
	r.SetHDRReadback(o.Readback)
	renderer = r
	defer func() {
		if err != nil {
			r.Close()
			renderer = nil
		}
	}()
	extent := vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: extent, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: targetUsage | vk.ImageUsageSampledBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err = checked("vkCreateImage", r.gpu.dd.CreateImage(r.gpu.device, &ii, nil, &r.own.image)); err != nil {
		return
	}
	var req vk.MemoryRequirements
	r.gpu.dd.GetImageMemoryRequirements(r.gpu.device, r.own.image, &req)
	var kind uint32
	kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err = checked("vkAllocateMemory(image)", r.gpu.dd.AllocateMemory(r.gpu.device, &alloc, nil, &r.own.memory)); err != nil {
		return
	}
	if err = checked("vkBindImageMemory", r.gpu.dd.BindImageMemory(r.gpu.device, r.own.image, r.own.memory, 0)); err != nil {
		return
	}
	pi := vk.CommandPoolCreateInfo{SType: vk.StructureTypeCommandPoolCreateInfo, Flags: vk.CommandPoolCreateResetCommandBufferBit, QueueFamilyIndex: g.family}
	if err = checked("vkCreateCommandPool", r.gpu.dd.CreateCommandPool(r.gpu.device, &pi, nil, &r.pool)); err != nil {
		return
	}
	if err = r.createSlots(); err != nil {
		return
	}
	if err = r.createView(&r.own); err != nil {
		return
	}
	if err = r.createComposer(); err != nil {
		return
	}
	return r, nil
}

func (r *Renderer) findMemoryType(bits uint32, props vk.MemoryPropertyFlags) (uint32, error) {
	for i := uint32(0); i < r.gpu.memory.MemoryTypeCount; i++ {
		if bits&(uint32(1)<<i) != 0 && r.gpu.memory.MemoryTypes[i].PropertyFlags&props == props {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no Vulkan memory type for bits %#x and properties %#x", bits, props)
}

// CopiedBytes reports cumulative wl_shm bytes copied into GPU storage.
// It is diagnostic only and must be read by the renderer's owner goroutine.
func (r *Renderer) CopiedBytes() int { return r.copied }

// TakeRedrawn returns the target pixels drawn since the last call and
// resets the count.
func (r *Renderer) TakeRedrawn() int {
	n := r.redrawn
	r.redrawn = 0
	if f := r.fullRedraws.take(); f != (fullRedraws{}) {
		r.log.Debug().Int("target", f[fullTarget]).Int("scene", f[fullScene]).Int("new_window", f[fullNewWindow]).
			Int("empty", f[fullEmpty]).Int("children", f[fullChildren]).Int("history", f[fullHistory]).Msg("full redraws")
	}
	return n
}

// SetLogger sets the logger (with its component field) for render target
// events. Not on the port; the zero logger discards.
func (r *Renderer) SetLogger(l zerowrap.Logger) { r.log = l }

// SetTraceDamage logs the redrawn pixels, draw count and partial flag of
// every frame at debug level (--debug=render-damage). Off by default.
func (r *Renderer) SetTraceDamage(on bool) { r.traceDamage = on }

// DeviceInfo returns device identity and creation timing captured by New.
func (r *Renderer) DeviceInfo() DeviceInfo { return r.gpu.deviceInfo }

// QueuePriority is the global priority the queue got: realtime, high or
// default.
func (r *Renderer) QueuePriority() string { return r.gpu.queuePriority }

// Pixels reads the last frame back through a capture slot, waiting for
// the GPU on the calling goroutine (slot fence, no sync file needed):
// headless screenshots and tests only. Nil when no slot is free (both
// leased to captures) or the copy failed: never a silent black image.
func (r *Renderer) Pixels() *image.RGBA {
	cf, err := r.debugCapture()
	if err != nil {
		return nil
	}
	defer r.EndCapture(cf)
	out := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	if err := cf.Read(image.Rect(0, 0, r.width, r.height), out.Pix, out.Stride); err != nil {
		return nil
	}
	for i := 0; i < len(out.Pix); i += 4 {
		out.Pix[i], out.Pix[i+2] = out.Pix[i+2], out.Pix[i]
	}
	return out
}

// SetHDRReadback makes the exported HDR targets transfer sources so that
// HDRPixels can read them (they are not in production: the DRM path never
// sets it). Call before ExportTargets. Not on the port.
func (r *Renderer) SetHDRReadback(on bool) { r.hdrReadback = on }

// HDRPixels reads the last HDR frame's XRGB2101010 target back, each 10-bit
// code scaled to 16 bits (c<<6 | c>>4), alpha opaque. It waits for the GPU:
// debug only. Nil when the output is SDR, the target is not readable (no
// SetHDRReadback), no HDR frame was rendered, every slot is leased or the
// copy failed.
func (r *Renderer) HDRPixels() *image.RGBA64 {
	var out *image.RGBA64
	err := r.readHDRTarget(func(words []uint32) {
		out = image.NewRGBA64(image.Rect(0, 0, r.width, r.height))
		for i, w := range words {
			p := out.Pix[i*8 : i*8+8]
			for ch, c := range [3]uint32{w >> 20 & 1023, w >> 10 & 1023, w & 1023} {
				v := uint16(c<<6 | c>>4)
				p[ch*2], p[ch*2+1] = byte(v>>8), byte(v)
			}
			p[6], p[7] = 0xff, 0xff
		}
	})
	if err != nil {
		return nil
	}
	return out
}

// Missing from the bindings: VK_QUEUE_FAMILY_IGNORED, VK_WHOLE_SIZE.
const (
	queueFamilyIgnored = ^uint32(0)
	wholeSize          = ^vk.DeviceSize(0)
)

func (r *Renderer) Close() {
	if r == nil || r.q == nil {
		return
	}
	if r.gpu.dd != nil {
		d := r.gpu.dd
		if r.gpu.device != 0 {
			_ = r.waitOwnIdle()
		}
		r.dropTargets()
		r.destroyHDR()
		r.dropCursors()
		for id, im := range r.imports {
			r.release(im)
			delete(r.imports, id)
		}
		r.frame += importTTL + 1
		r.dropPools()
		r.dropShm()
		r.destroySlots()
		r.destroyCaptures()
		if r.pool != 0 {
			d.DestroyCommandPool(r.gpu.device, r.pool, nil)
			r.pool = 0
		}
		r.destroyComposer()
		r.freeTarget(&r.hdrOwn)
		r.freeTarget(&r.own)
	}
	g := r.gpu
	g.releaseQueue(r.q)
	r.q = nil
	if r.legacyOwner {
		g.Close()
		g.inst.Close()
	}
}
