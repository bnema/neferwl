package vulkan

import (
	"fmt"
	"image"
	"math"
	"os"
	"runtime"
	"strconv"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Renderer is owned by one goroutine; its methods must not be called concurrently.
type Renderer struct {
	width, height int
	instance      vk.Instance
	id            *vk.InstanceDispatch
	device        vk.Device
	dd            *vk.DeviceDispatch
	queue         vk.Queue
	// own is the target until ExportTargets; targets are exported images
	// (targets.go), current the one the next frame draws into.
	own        target
	targets    []*target
	current    int
	renderMods []uint64
	// last is the target of the last frame; readback copies it into
	// buffer only when Pixels asks (stale until then).
	last         *target
	readBack     bool
	buffer       vk.Buffer
	bufferMemory vk.DeviceMemory
	mapped       unsafe.Pointer
	pool         vk.CommandPool
	memory       vk.PhysicalDeviceMemoryProperties
	family       uint32
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
	// redrawn counts the target pixels drawn, for tests.
	redrawn int
	// cursors are the exported cursor images (CursorBuffers).
	cursors [2]*cursorImage
	// compose draws every frame (compose.go); maxRange bounds one
	// storage buffer binding.
	compose  composer
	maxRange int
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
	if value, ok := os.LookupEnv("NEFERTTY_VK_DEVICE"); ok {
		index, err = strconv.Atoi(value)
		if err != nil || index < 0 || index >= int(count) {
			err = fmt.Errorf("invalid NEFERTTY_VK_DEVICE index %q", value)
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
	if r.hasExtensions(physical) {
		for _, e := range deviceExtensions {
			extNames = append(extNames, append([]byte(e), 0))
			extPtrs = append(extPtrs, &extNames[len(extNames)-1][0])
		}
		di.EnabledExtensionCount = uint32(len(extPtrs))
		di.PpEnabledExtensionNames = &extPtrs[0]
	}
	if err = checked("vkCreateDevice", r.id.CreateDevice(physical, &di, nil, &r.device)); err != nil {
		return
	}
	r.dd, err = vk.LoadDeviceDispatch(r.id, r.device)
	if err != nil {
		err = fmt.Errorf("LoadDeviceDispatch: %w", err)
		return
	}
	runtime.KeepAlive(extNames)
	runtime.KeepAlive(extPtrs)
	if len(extPtrs) > 0 {
		// Clients need the render node to allocate on the right GPU.
		if sup := r.probeDMABuf(physical); sup.Device != 0 {
			r.dmabuf = sup
			r.probeRenderModifiers(physical)
		}
		// VK_KHR_external_semaphore_fd (enabled above): frames export
		// their fence as a sync file.
		r.syncFD = r.dd.GetSemaphoreFdKHR != nil
	}
	r.dd.GetDeviceQueue(r.device, family, 0, &r.queue)
	extent := vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: extent, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: targetUsage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
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
	size := vk.DeviceSize(width) * vk.DeviceSize(height) * 4
	bi := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: size, Usage: vk.BufferUsageTransferDstBit, SharingMode: vk.SharingModeExclusive}
	if err = checked("vkCreateBuffer", r.dd.CreateBuffer(r.device, &bi, nil, &r.buffer)); err != nil {
		return
	}
	r.dd.GetBufferMemoryRequirements(r.device, r.buffer, &req)
	// Readback (screenshots) reads this buffer: uncached memory is ~100x slower.
	kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit|vk.MemoryPropertyHostCachedBit)
	if err != nil {
		kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
	}
	if err != nil {
		return
	}
	alloc = vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err = checked("vkAllocateMemory(buffer)", r.dd.AllocateMemory(r.device, &alloc, nil, &r.bufferMemory)); err != nil {
		return
	}
	if err = checked("vkBindBufferMemory", r.dd.BindBufferMemory(r.device, r.buffer, r.bufferMemory, 0)); err != nil {
		return
	}
	if err = checked("vkMapMemory", r.dd.MapMemory(r.device, r.bufferMemory, 0, size, 0, &r.mapped)); err != nil {
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

func (r *Renderer) findMemoryType(bits uint32, props vk.MemoryPropertyFlags) (uint32, error) {
	for i := uint32(0); i < r.memory.MemoryTypeCount; i++ {
		if bits&(uint32(1)<<i) != 0 && r.memory.MemoryTypes[i].PropertyFlags&props == props {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no Vulkan memory type for bits %#x and properties %#x", bits, props)
}

func parseColor(s string) [3]uint8 {
	var c [3]uint8
	if len(s) != 7 || s[0] != '#' {
		return c
	}
	for i := range c {
		v, e := strconv.ParseUint(s[1+i*2:3+i*2], 16, 8)
		if e != nil {
			return [3]uint8{}
		}
		c[i] = uint8(v)
	}
	return c
}

func windowColor(id ports.WindowID) [3]uint8 {
	h := math.Mod(float64(id)*0.618034, 1) * 6
	sector := int(h)
	f := h - float64(sector)
	p, q, t := 0.4, 0.8*(1-0.5*f), 0.8*(1-0.5*(1-f))
	var a, b, c float64
	switch sector {
	case 0:
		a, b, c = 0.8, t, p
	case 1:
		a, b, c = q, 0.8, p
	case 2:
		a, b, c = p, 0.8, t
	case 3:
		a, b, c = p, q, 0.8
	case 4:
		a, b, c = t, p, 0.8
	default:
		a, b, c = 0.8, p, q
	}
	return [3]uint8{uint8(a * 255), uint8(b * 255), uint8(c * 255)}
}

// draws walks the scene into quads in paint order, copying new wl_shm
// content into GPU buffers on the way.
func (r *Renderer) draws(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent, dmg *damageRegion) []draw {
	// Scene rects are logical; everything below works in physical pixels.
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	phys := func(v int) int { return int(math.Round(float64(v) * scale)) }
	physRect := func(x, y, w, h int) image.Rectangle { return image.Rect(phys(x), phys(y), phys(x+w), phys(y+h)) }
	var draws []draw
	bounds := image.Rect(0, 0, r.width, r.height)
	add := func(rect image.Rectangle, c [3]uint8) {
		rect = rect.Intersect(bounds)
		if rect.Empty() {
			return
		}
		draws = append(draws, r.fillDraw(rect, c))
	}
	// addContent draws the part of a buffer mapped onto full (physical
	// pixels) that dst shows. key names the surface, seq its content.
	addContent := func(dst, full image.Rectangle, content *ports.SurfaceContent, key shmKey, seq uint64) {
		rect := dst.Intersect(bounds)
		if rect.Empty() {
			return
		}
		if content.DMABuf != nil {
			im, err := r.importDMABuf(content.DMABuf)
			if err != nil {
				// The window shows its background until a buffer imports.
				return
			}
			dr := r.contentDraw(rect, full, content.Width, content.Height, modeImage, content.Opaque)
			dr.set, dr.im = im.set, im
			draws = append(draws, dr)
			return
		}
		b := content.SHM
		pixels, err := r.shmPixels(b, b.Offset+(content.Height-1)*b.Stride+content.Width*4)
		if err != nil {
			return
		}
		st := shmState{w: content.Width, h: content.Height, seq: seq}
		// Damage history is the root surface's: children copy in full.
		var damage func(uint64) ([]ports.Rect, bool)
		if key.index == 0 {
			damage = content.DamageSince
		}
		c, err := r.shmCopyFor(key, st, pixels, b.Offset, b.Stride, damage)
		if err != nil {
			return
		}
		dr := r.contentDraw(rect, full, content.Width, content.Height, modeBuffer, content.Opaque)
		dr.set = c.set
		dr.pc.buf = [4]uint32{0, uint32(content.Width), uint32(content.Height), 0}
		draws = append(draws, dr)
	}
	// drawSurface draws one surface buffer with its origin at (x, y)
	// logical, clipped to clip (logical).
	drawSurface := func(content *ports.SurfaceContent, x, y int, clip image.Rectangle, key shmKey, seq uint64) {
		lw, lh := content.LogicalW, content.LogicalH
		if lw <= 0 || lh <= 0 {
			lw, lh = content.Width, content.Height
		}
		if content.SHM == nil && content.DMABuf == nil || lw <= 0 || lh <= 0 || content.Width <= 0 || content.Height <= 0 {
			return
		}
		if content.DMABuf == nil && (content.SHM.Stride < content.Width*4 || content.SHM.Offset < 0) {
			return
		}
		full := physRect(x, y, lw, lh)
		// A buffer drawn at the physical size (fractional-scale clients round
		// w*scale, we round per edge) is copied 1:1, never resampled for 1px.
		near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 }
		if near(full.Dx(), content.Width) && near(full.Dy(), content.Height) {
			full.Max = full.Min.Add(image.Pt(content.Width, content.Height))
		}
		dst := full.Intersect(physRect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()))
		if dst.Empty() {
			return
		}
		if key.index == 0 {
			dmg.content(key.win, content, full, dst)
		}
		addContent(dst, full, content, key, seq)
	}
	// place draws a surface tree with its window geometry at (x, y)
	// logical, clipped to w×h logical: client shadows fall outside.
	// Every surface of the tree is keyed by its window and place in it;
	// the tree's Seq changes with any of them.
	place := func(id ports.WindowID, content *ports.SurfaceContent, x, y, w, h int) {
		clip := image.Rect(x, y, x+w, y+h)
		ox, oy := x-content.Geometry.X, y-content.Geometry.Y
		for i := range content.Children {
			if ch := &content.Children[i]; ch.Below {
				drawSurface(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip, shmKey{id, i + 1}, content.Seq)
			}
		}
		drawSurface(content, ox, oy, clip, shmKey{id, 0}, content.Seq)
		for i := range content.Children {
			if ch := &content.Children[i]; !ch.Below {
				drawSurface(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip, shmKey{id, i + 1}, content.Seq)
			}
		}
	}
	fullscreen := false
	for _, w := range s.Windows {
		fullscreen = fullscreen || (!w.Hidden && w.Fullscreen)
	}
	addLayers := func(afterWindows bool) {
		for _, layer := range s.Layers {
			if (layer.Layer >= ports.LayerTop) != afterWindows ||
				(fullscreen && (layer.Layer == ports.LayerBottom || layer.Layer == ports.LayerTop)) ||
				layer.Rect.W <= 0 || layer.Rect.H <= 0 {
				continue
			}
			content := contents[layer.ID]
			dmg.window(layer.ID, content, physRect(layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H))
			place(layer.ID, &content, layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H)
		}
	}
	addLayers(false)
	for _, w := range s.Windows {
		if w.Hidden || w.Rect.W <= 0 || w.Rect.H <= 0 {
			continue
		}
		x, y := w.Rect.X, w.Rect.Y
		if w.Popup {
			continue
		}
		// Content sits inside the border; core sized the client to match.
		b := 0
		if !w.Fullscreen && !w.Borderless {
			b = min(max(s.Border.Width, 0), w.Rect.W/2, w.Rect.H/2)
		}
		cx, cy, cw, ch := x+b, y+b, w.Rect.W-2*b, w.Rect.H-2*b
		body := physRect(cx, cy, cw, ch)
		content := contents[w.ID]
		dmg.window(w.ID, content, physRect(x, y, w.Rect.W, w.Rect.H))
		if content.Empty() {
			add(body, windowColor(w.ID))
		} else {
			add(body, parseColor(s.Background))
			place(w.ID, &content, cx, cy, cw, ch)
		}
		borderColor := s.Border.Inactive
		if w.Focused {
			borderColor = s.Border.Active
		}
		if b > 0 && borderColor != "" {
			rgb := parseColor(borderColor)
			outer, inner := physRect(x, y, w.Rect.W, w.Rect.H), physRect(cx, cy, cw, ch)
			for _, strip := range []image.Rectangle{
				image.Rect(outer.Min.X, outer.Min.Y, outer.Max.X, inner.Min.Y),
				image.Rect(outer.Min.X, inner.Max.Y, outer.Max.X, outer.Max.Y),
				image.Rect(outer.Min.X, inner.Min.Y, inner.Min.X, inner.Max.Y),
				image.Rect(inner.Max.X, inner.Min.Y, outer.Max.X, inner.Max.Y),
			} {
				add(strip, rgb)
			}
		}
	}
	// Popups draw only what the client drew, shadows clipped. Window popups
	// stay with the windows; a layer's go over every layer.
	addPopups := func(overLayers bool) {
		for _, w := range s.Windows {
			if w.Popup && w.OverLayers == overLayers && !w.Hidden && w.Rect.W > 0 && w.Rect.H > 0 {
				content := contents[w.ID]
				dmg.window(w.ID, content, physRect(w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H))
				place(w.ID, &content, w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H)
			}
		}
	}
	addPopups(false)
	addLayers(true)
	addPopups(true)
	return draws
}

func (r *Renderer) Pixels() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	if r.mapped == nil || r.readback() != nil {
		return out
	}
	src := unsafe.Slice((*byte)(r.mapped), len(out.Pix))
	for i := 0; i < len(src); i += 4 {
		// The output is opaque: x-format client buffers leave alpha undefined.
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = src[i+2], src[i+1], src[i], 255
	}
	return out
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
