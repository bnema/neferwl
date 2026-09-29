package vulkan

import (
	"fmt"
	"image"
	"math"
	"os"
	"runtime"
	"strconv"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Renderer is owned by one goroutine; its methods must not be called concurrently.
type Renderer struct {
	width, height int
	// queuePriority is the global priority the queue got: realtime, high
	// or default.
	queuePriority string
	instance      vk.Instance
	id            *vk.InstanceDispatch
	device        vk.Device
	dd            *vk.DeviceDispatch
	queue         vk.Queue
	// own is the SDR composition image; HDR uses hdrOwn and PQ targets.
	own        target
	hdrOwn     target // linear BT.709 in units of SDR reference white
	targets    []*target
	current    int
	renderMods []uint64
	hdrMods    []uint64
	hdrNits    float64
	hdr        hdrPass
	hdrError   error
	// hdrReadback is used only by the GPU test to permit transfer from a 10-bit target.
	hdrReadback bool
	physical    vk.PhysicalDevice
	// One converted cursor image is kept; changes replace it in bounded memory.
	cursorCache cursorConversion
	// last is the target of the last frame; readback copies it into
	// buffer only when Pixels asks (stale until then).
	last          *target
	readBack      bool
	buffer        vk.Buffer
	bufferMemory  vk.DeviceMemory
	readbackBytes int // bytes per pixel allocated for the mapped readback buffer
	mapped        unsafe.Pointer
	pool          vk.CommandPool
	memory        vk.PhysicalDeviceMemoryProperties
	family        uint32
	// Frames in flight (frame.go): frame is the last frame recorded,
	// submitted the last submitted, completed the last known finished.
	slots                       [frameSlots]frameSlot
	frame, submitted, completed uint64
	deferred                    []deferredFree
	// syncFD: frames export a sync file (else Render waits for them).
	syncFD bool
	// dmabuf is what clients may send; imports are their buffers by ID.
	dmabuf  ports.DMABufSupport
	imports map[uint64]*imported
	// pools are client wl_shm pools mapped by pool ID.
	pools map[uint64]*mapping
	// retired are pool mappings replaced this frame, unmapped once the
	// frame's copies are done.
	retired [][]byte
	// shm are the GPU copies of wl_shm surfaces (shm.go); copied counts
	// the bytes copied into them, for tests.
	shm    map[shmKey]*shmSurface
	copied int
	// marks date the frames for Trim's wall-clock eviction (trim.go).
	marks []trimMark
	// redrawn counts the target pixels drawn, for tests.
	redrawn int
	// cursors are the exported cursor images (CursorBuffers).
	cursors [2]*cursorImage
	// compose draws every frame (compose.go); maxRange bounds one
	// storage buffer binding.
	compose  composer
	maxRange int
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
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func New(width, height int) (r *Renderer, err error) {
	if width <= 0 || height <= 0 || width > math.MaxUint32 || height > math.MaxUint32 || uint64(width)*uint64(height) > uint64(math.MaxInt/4) {
		return nil, fmt.Errorf("invalid renderer dimensions %d x %d", width, height)
	}
	r = &Renderer{width: width, height: height, imports: map[uint64]*imported{}, pools: map[uint64]*mapping{}, shm: map[shmKey]*shmSurface{}}
	defer func() {
		if err != nil {
			r.Close()
			r = nil
		}
	}()
	if err = vk.Init(); err != nil {
		return nil, fmt.Errorf("vk.Init: %w", err)
	}
	app := vk.ApplicationInfo{SType: vk.StructureTypeApplicationInfo, ApiVersion: vk.MakeVersion(1, 3, 0)}
	info := vk.InstanceCreateInfo{SType: vk.StructureTypeInstanceCreateInfo, ApplicationInfo: &app}
	if err = checked("vkCreateInstance", vk.Global().CreateInstance(&info, nil, &r.instance)); err != nil {
		return
	}
	r.id, err = vk.LoadInstanceDispatch(r.instance)
	if err != nil {
		err = fmt.Errorf("LoadInstanceDispatch: %w", err)
		return
	}
	var count uint32
	if err = checked("vkEnumeratePhysicalDevices(count)", r.id.EnumeratePhysicalDevices(r.instance, &count, nil)); err != nil {
		return
	}
	if count == 0 {
		err = fmt.Errorf("no Vulkan 1.3 device")
		return
	}
	devices := make([]vk.PhysicalDevice, count)
	if err = checked("vkEnumeratePhysicalDevices", r.id.EnumeratePhysicalDevices(r.instance, &count, &devices[0])); err != nil {
		return
	}
	index := -1
	if value, ok := os.LookupEnv("NEFERWL_VK_DEVICE"); ok {
		index, err = strconv.Atoi(value)
		if err != nil || index < 0 || index >= int(count) {
			err = fmt.Errorf("invalid NEFERWL_VK_DEVICE index %q", value)
			return
		}
	}
	var physical vk.PhysicalDevice
	var family uint32
	for i, device := range devices[:count] {
		if index >= 0 && i != index {
			continue
		}
		var properties vk.PhysicalDeviceProperties
		r.id.GetPhysicalDeviceProperties(device, &properties)
		if properties.ApiVersion < vk.MakeVersion(1, 3, 0) {
			continue
		}
		var n uint32
		r.id.GetPhysicalDeviceQueueFamilyProperties(device, &n, nil)
		if n == 0 {
			continue
		}
		families := make([]vk.QueueFamilyProperties, n)
		r.id.GetPhysicalDeviceQueueFamilyProperties(device, &n, &families[0])
		for j, f := range families[:n] {
			if f.QueueCount > 0 && f.QueueFlags&vk.QueueGraphicsBit != 0 {
				physical = device
				family = uint32(j)
				break
			}
		}
		if physical != 0 {
			break
		}
	}
	if physical == 0 {
		err = fmt.Errorf("no Vulkan 1.3 device with a graphics queue")
		return
	}
	r.id.GetPhysicalDeviceMemoryProperties(physical, &r.memory)
	var properties vk.PhysicalDeviceProperties
	r.id.GetPhysicalDeviceProperties(physical, &properties)
	r.maxRange = int(properties.Limits.MaxStorageBufferRange)
	r.family = family
	priority := float32(1)
	qi := vk.DeviceQueueCreateInfo{SType: vk.StructureTypeDeviceQueueCreateInfo, QueueFamilyIndex: family, QueueCount: 1, QueuePriorities: &priority}
	dynamicRendering := vk.PhysicalDeviceDynamicRenderingFeatures{SType: vk.StructureTypePhysicalDeviceDynamicRenderingFeatures, DynamicRendering: 1}
	di := vk.DeviceCreateInfo{SType: vk.StructureTypeDeviceCreateInfo, Next: unsafe.Pointer(&dynamicRendering), QueueCreateInfoCount: 1, QueueCreateInfos: &qi}
	// dmabuf import needs every extension; without them clients use wl_shm.
	var extNames [][]byte
	var extPtrs []*byte
	have := r.extensions(physical)
	dmabufSupported := true
	for _, e := range deviceExtensions {
		if !have[e] {
			dmabufSupported = false
		}
	}
	if dmabufSupported {
		for _, e := range deviceExtensions {
			extNames = append(extNames, append([]byte(e), 0))
			extPtrs = append(extPtrs, &extNames[len(extNames)-1][0])
		}
		di.EnabledExtensionCount = uint32(len(extPtrs))
		di.PpEnabledExtensionNames = &extPtrs[0]
	}
	globalExt := ""
	if have[vk.KHRGlobalPriorityExtensionName] {
		globalExt = vk.KHRGlobalPriorityExtensionName
	} else if have[vk.EXTGlobalPriorityExtensionName] {
		globalExt = vk.EXTGlobalPriorityExtensionName
	}
	var global vk.DeviceQueueGlobalPriorityCreateInfo
	chosen := "default"
	for _, attempt := range priorityAttempts(globalExt != "") {
		qi.Next = nil
		count := len(extPtrs)
		if attempt != 0 {
			global = vk.DeviceQueueGlobalPriorityCreateInfo{SType: vk.StructureTypeDeviceQueueGlobalPriorityCreateInfo, GlobalPriority: attempt}
			qi.Next = unsafe.Pointer(&global)
			extNames = append(extNames, append([]byte(globalExt), 0))
			extPtrs = append(extPtrs, &extNames[len(extNames)-1][0])
			chosen = "realtime"
			if attempt == vk.QueueGlobalPriorityHigh {
				chosen = "high"
			}
		} else {
			chosen = "default"
		}
		di.EnabledExtensionCount = uint32(len(extPtrs))
		if len(extPtrs) > 0 {
			di.PpEnabledExtensionNames = &extPtrs[0]
		} else {
			di.PpEnabledExtensionNames = nil
		}
		result := r.id.CreateDevice(physical, &di, nil, &r.device)
		runtime.KeepAlive(global)
		runtime.KeepAlive(qi)
		runtime.KeepAlive(extNames)
		runtime.KeepAlive(extPtrs)
		extPtrs = extPtrs[:count]
		extNames = extNames[:count]
		if result == vk.Success {
			err = nil
			break
		}
		err = checked("vkCreateDevice", result)
		if attempt == 0 {
			return
		}
	}
	r.queuePriority = chosen
	r.dd, err = vk.LoadDeviceDispatch(r.id, r.device)
	if err != nil {
		err = fmt.Errorf("LoadDeviceDispatch: %w", err)
		return
	}
	runtime.KeepAlive(extNames)
	runtime.KeepAlive(extPtrs)
	if dmabufSupported {
		// Clients need the render node to allocate on the right GPU.
		if sup := r.probeDMABuf(physical); sup.Device != 0 {
			r.dmabuf = sup
			r.probeRenderModifiers(physical)
			r.physical = physical
		}
		// VK_KHR_external_semaphore_fd (enabled above): frames export
		// their fence as a sync file.
		r.syncFD = r.dd.HasGetSemaphoreFdKHR()
	}
	r.dd.GetDeviceQueue(r.device, family, 0, &r.queue)
	extent := vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: extent, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: targetUsage | vk.ImageUsageSampledBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err = checked("vkCreateImage", r.dd.CreateImage(r.device, &ii, nil, &r.own.image)); err != nil {
		return
	}
	var req vk.MemoryRequirements
	r.dd.GetImageMemoryRequirements(r.device, r.own.image, &req)
	var kind uint32
	kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err = checked("vkAllocateMemory(image)", r.dd.AllocateMemory(r.device, &alloc, nil, &r.own.memory)); err != nil {
		return
	}
	if err = checked("vkBindImageMemory", r.dd.BindImageMemory(r.device, r.own.image, r.own.memory, 0)); err != nil {
		return
	}
	if err = r.createReadback(4); err != nil {
		return
	}
	pi := vk.CommandPoolCreateInfo{SType: vk.StructureTypeCommandPoolCreateInfo, Flags: vk.CommandPoolCreateResetCommandBufferBit, QueueFamilyIndex: family}
	if err = checked("vkCreateCommandPool", r.dd.CreateCommandPool(r.device, &pi, nil, &r.pool)); err != nil {
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
	runtime.KeepAlive(priority)
	return
}

// createReadback replaces the host buffer only while the device is idle (at
// construction or on an SDR→HDR transition). The current buffer stays in use
// until the replacement is mapped, so a failed transition keeps SDR readback.
func (r *Renderer) createReadback(bytesPerPixel int) (err error) {
	d := r.dd
	var (
		buffer vk.Buffer
		memory vk.DeviceMemory
		mapped unsafe.Pointer
	)
	defer func() {
		if err == nil {
			return
		}
		if buffer != 0 {
			d.DestroyBuffer(r.device, buffer, nil)
		}
		if memory != 0 {
			d.FreeMemory(r.device, memory, nil)
		}
	}()
	size := vk.DeviceSize(r.width) * vk.DeviceSize(r.height) * vk.DeviceSize(bytesPerPixel)
	bi := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: size, Usage: vk.BufferUsageTransferDstBit, SharingMode: vk.SharingModeExclusive}
	if err := checked("vkCreateBuffer", d.CreateBuffer(r.device, &bi, nil, &buffer)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetBufferMemoryRequirements(r.device, buffer, &req)
	// Readback (screenshots) reads this buffer: prefer cached host memory.
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit|vk.MemoryPropertyHostCachedBit)
	if err != nil {
		kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
	}
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(buffer)", d.AllocateMemory(r.device, &alloc, nil, &memory)); err != nil {
		return err
	}
	if err := checked("vkBindBufferMemory", d.BindBufferMemory(r.device, buffer, memory, 0)); err != nil {
		return err
	}
	if err := checked("vkMapMemory", d.MapMemory(r.device, memory, 0, size, 0, &mapped)); err != nil {
		return err
	}
	if r.mapped != nil {
		d.UnmapMemory(r.device, r.bufferMemory)
	}
	if r.buffer != 0 {
		d.DestroyBuffer(r.device, r.buffer, nil)
	}
	if r.bufferMemory != 0 {
		d.FreeMemory(r.device, r.bufferMemory, nil)
	}
	r.buffer, r.bufferMemory, r.mapped, r.readbackBytes = buffer, memory, mapped, bytesPerPixel
	return nil
}

func (r *Renderer) findMemoryType(bits uint32, props vk.MemoryPropertyFlags) (uint32, error) {
	for i := uint32(0); i < r.memory.MemoryTypeCount; i++ {
		if bits&(uint32(1)<<i) != 0 && r.memory.MemoryTypes[i].PropertyFlags&props == props {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no Vulkan memory type for bits %#x and properties %#x", bits, props)
}

// CopiedBytes reports cumulative wl_shm bytes copied into GPU storage.
// It is diagnostic only and must be read by the renderer's owner goroutine.
func (r *Renderer) CopiedBytes() int { return r.copied }

// QueuePriority is the global priority the queue got: realtime, high or
// default.
func (r *Renderer) QueuePriority() string { return r.queuePriority }

func (r *Renderer) Pixels() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	if r.mapped == nil || r.readback() != nil {
		return out
	}
	if r.last == &r.hdrOwn {
		r.hdrRegion(out.Pix, out.Stride, 0, 0, r.width, r.height, true)
		return out
	}
	src := unsafe.Slice((*byte)(r.mapped), len(out.Pix))
	for i := 0; i < len(src); i += 4 {
		// The output is opaque: x-format client buffers leave alpha undefined.
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = src[i+2], src[i+1], src[i], 255
	}
	return out
}

// Capture reads the last rendered image without changing the renderer's target.
func (r *Renderer) Capture(region image.Rectangle, dst []byte, stride int) error {
	bounds := image.Rect(0, 0, r.width, r.height)
	w, h := region.Dx(), region.Dy()
	if region.Empty() || !region.In(bounds) || stride < w*4 || len(dst) < (h-1)*stride+w*4 {
		return fmt.Errorf("invalid capture region or destination")
	}
	if r.mapped == nil || r.last == nil || r.last.image == 0 {
		return fmt.Errorf("no rendered frame")
	}
	if err := r.readback(); err != nil {
		return err
	}
	if r.last == &r.hdrOwn {
		r.hdrRegion(dst, stride, region.Min.X, region.Min.Y, w, h, false)
		return nil
	}
	src := unsafe.Slice((*byte)(r.mapped), r.width*r.height*4)
	for y := range h {
		start := ((region.Min.Y+y)*r.width + region.Min.X) * 4
		row := dst[y*stride : y*stride+w*4]
		copy(row, src[start:start+w*4])
		for x := 3; x < len(row); x += 4 {
			row[x] = 255
		}
	}
	return nil
}

// Missing from the bindings: VK_QUEUE_FAMILY_IGNORED, VK_WHOLE_SIZE.
const (
	queueFamilyIgnored = ^uint32(0)
	wholeSize          = ^vk.DeviceSize(0)
)

// readback copies the last frame into the host buffer, once per frame
// (screenshots only).
func (r *Renderer) readback() error {
	if r.readBack || r.last == nil || r.last.image == 0 {
		return nil
	}
	// The copy reads the frame: wait for it, and keep the ring ordered.
	if err := r.waitFrame(r.submitted); err != nil {
		return err
	}
	t := r.last
	err := r.oneShot(func(cmd vk.CommandBuffer) {
		d := r.dd
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: t.layout, NewLayout: vk.ImageLayoutTransferSrcOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: t.image, SubresourceRange: colorRange}
		if t.exported {
			b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vk.QueueFamilyForeignEXT, r.family
		}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTopOfPipeBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
		region := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
		d.CmdCopyImageToBuffer(cmd, t.image, vk.ImageLayoutTransferSrcOptimal, r.buffer, 1, &region)
		// Make the copy visible to host reads of the mapped buffer.
		hb := vk.BufferMemoryBarrier{SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: vk.AccessTransferWriteBit, DstAccessMask: vk.AccessHostReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Buffer: r.buffer, Size: wholeSize}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageHostBit, 0, 0, nil, 1, &hb, 0, nil)
		if t.exported {
			b.OldLayout, b.NewLayout = vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral
			b.SrcAccessMask, b.DstAccessMask = vk.AccessTransferReadBit, 0
			b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
			d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
		}
	})
	if err != nil {
		return err
	}
	if !t.exported {
		t.layout = vk.ImageLayoutTransferSrcOptimal
	}
	r.readBack = true
	return nil
}

func (r *Renderer) Close() {
	if r == nil {
		return
	}
	if r.dd != nil {
		d := r.dd
		if r.device != 0 {
			_ = checked("vkDeviceWaitIdle", d.DeviceWaitIdle(r.device))
			r.idle()
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
		if r.pool != 0 {
			d.DestroyCommandPool(r.device, r.pool, nil)
			r.pool = 0
		}
		if r.mapped != nil {
			d.UnmapMemory(r.device, r.bufferMemory)
			r.mapped = nil
		}
		if r.buffer != 0 {
			d.DestroyBuffer(r.device, r.buffer, nil)
			r.buffer = 0
		}
		if r.bufferMemory != 0 {
			d.FreeMemory(r.device, r.bufferMemory, nil)
			r.bufferMemory = 0
		}
		r.destroyComposer()
		r.freeTarget(&r.hdrOwn)
		r.freeTarget(&r.own)
		d.DestroyDevice(r.device, nil)
		r.device = 0
	} else if r.device != 0 && r.id != nil {
		if vk.VkDestroyDevice != nil {
			vk.VkDestroyDevice(r.device, nil)
		}
		r.device = 0
	}
	if r.instance != 0 {
		if r.id != nil {
			r.id.DestroyInstance(r.instance, nil)
		} else if vk.VkDestroyInstance != nil {
			vk.VkDestroyInstance(r.instance, nil)
		}
		r.instance = 0
	}
}
