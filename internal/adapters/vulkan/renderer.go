package vulkan

import (
	"fmt"
	"image"
	"math"
	"os"
	"runtime"
	"slices"
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
	image         vk.Image
	imageMemory   vk.DeviceMemory
	buffer        vk.Buffer
	bufferMemory  vk.DeviceMemory
	staging       vk.Buffer
	stagingMemory vk.DeviceMemory
	stagingMapped unsafe.Pointer
	stagingSize   int
	mapped        unsafe.Pointer
	pool          vk.CommandPool
	command       vk.CommandBuffer
	fence         vk.Fence
	layout        vk.ImageLayout
	memory        vk.PhysicalDeviceMemoryProperties
	family        uint32
	// dmabuf is what clients may send; imports are their buffers by ID.
	dmabuf  ports.DMABufSupport
	imports map[uint64]*imported
	frame   uint64
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
	r = &Renderer{width: width, height: height, layout: vk.ImageLayoutUndefined, imports: map[uint64]*imported{}}
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
		}
	}
	r.dd.GetDeviceQueue(r.device, family, 0, &r.queue)
	extent := vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: extent, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: vk.ImageUsageTransferSrcBit | vk.ImageUsageTransferDstBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err = checked("vkCreateImage", r.dd.CreateImage(r.device, &ii, nil, &r.image)); err != nil {
		return
	}
	var req vk.MemoryRequirements
	r.dd.GetImageMemoryRequirements(r.device, r.image, &req)
	var kind uint32
	kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err = checked("vkAllocateMemory(image)", r.dd.AllocateMemory(r.device, &alloc, nil, &r.imageMemory)); err != nil {
		return
	}
	if err = checked("vkBindImageMemory", r.dd.BindImageMemory(r.device, r.image, r.imageMemory, 0)); err != nil {
		return
	}
	size := vk.DeviceSize(width) * vk.DeviceSize(height) * 4
	bi := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: size, Usage: vk.BufferUsageTransferDstBit, SharingMode: vk.SharingModeExclusive}
	if err = checked("vkCreateBuffer", r.dd.CreateBuffer(r.device, &bi, nil, &r.buffer)); err != nil {
		return
	}
	r.dd.GetBufferMemoryRequirements(r.device, r.buffer, &req)
	// The CPU reads this buffer every frame: uncached memory makes that ~100x slower.
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
	// Start with two frame-sized regions; later frames grow staging on demand.
	stagingSize := size * 2
	r.stagingSize = int(stagingSize)
	stagingInfo := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: stagingSize, Usage: vk.BufferUsageTransferSrcBit, SharingMode: vk.SharingModeExclusive}
	if err = checked("vkCreateBuffer(staging)", r.dd.CreateBuffer(r.device, &stagingInfo, nil, &r.staging)); err != nil {
		return
	}
	r.dd.GetBufferMemoryRequirements(r.device, r.staging, &req)
	kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
	if err != nil {
		return
	}
	alloc = vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err = checked("vkAllocateMemory(staging)", r.dd.AllocateMemory(r.device, &alloc, nil, &r.stagingMemory)); err != nil {
		return
	}
	if err = checked("vkBindBufferMemory(staging)", r.dd.BindBufferMemory(r.device, r.staging, r.stagingMemory, 0)); err != nil {
		return
	}
	if err = checked("vkMapMemory(staging)", r.dd.MapMemory(r.device, r.stagingMemory, 0, stagingSize, 0, &r.stagingMapped)); err != nil {
		return
	}
	pi := vk.CommandPoolCreateInfo{SType: vk.StructureTypeCommandPoolCreateInfo, Flags: vk.CommandPoolCreateResetCommandBufferBit, QueueFamilyIndex: family}
	if err = checked("vkCreateCommandPool", r.dd.CreateCommandPool(r.device, &pi, nil, &r.pool)); err != nil {
		return
	}
	ai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: r.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: 1}
	if err = checked("vkAllocateCommandBuffers", r.dd.AllocateCommandBuffers(r.device, &ai, &r.command)); err != nil {
		return
	}
	fi := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
	if err = checked("vkCreateFence", r.dd.CreateFence(r.device, &fi, nil, &r.fence)); err != nil {
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

// ensureStaging grows the mapped transfer buffer before recording any commands.
func (r *Renderer) ensureStaging(size int) error {
	if size <= r.stagingSize {
		return nil
	}
	d := r.dd
	capacity := size
	info := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: vk.DeviceSize(capacity), Usage: vk.BufferUsageTransferSrcBit, SharingMode: vk.SharingModeExclusive}
	var buffer vk.Buffer
	if err := checked("vkCreateBuffer(staging)", d.CreateBuffer(r.device, &info, nil, &buffer)); err != nil {
		return err
	}
	var memory vk.DeviceMemory
	defer func() {
		if buffer != 0 {
			d.DestroyBuffer(r.device, buffer, nil)
		}
		if memory != 0 {
			d.FreeMemory(r.device, memory, nil)
		}
	}()
	var req vk.MemoryRequirements
	d.GetBufferMemoryRequirements(r.device, buffer, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(staging)", d.AllocateMemory(r.device, &alloc, nil, &memory)); err != nil {
		return err
	}
	if err := checked("vkBindBufferMemory(staging)", d.BindBufferMemory(r.device, buffer, memory, 0)); err != nil {
		return err
	}
	var mapped unsafe.Pointer
	if err := checked("vkMapMemory(staging)", d.MapMemory(r.device, memory, 0, vk.DeviceSize(capacity), 0, &mapped)); err != nil {
		return err
	}
	d.UnmapMemory(r.device, r.stagingMemory)
	d.DestroyBuffer(r.device, r.staging, nil)
	d.FreeMemory(r.device, r.stagingMemory, nil)
	r.staging, r.stagingMemory, r.stagingMapped, r.stagingSize = buffer, memory, mapped, capacity
	buffer, memory = 0, 0
	return nil
}

func (r *Renderer) Clear(rgb [3]uint8) error {
	return r.Render(ports.Scene{Background: fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])}, nil)
}

func (r *Renderer) Render(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent) error {
	// Scene rects are logical; everything below works in physical pixels.
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	phys := func(v int) int { return int(math.Round(float64(v) * scale)) }
	physRect := func(x, y, w, h int) image.Rectangle { return image.Rect(phys(x), phys(y), phys(x+w), phys(y+h)) }
	type upload struct {
		rect   image.Rectangle
		offset int
		pixels []byte
		stride int
		// dst covers src; they differ in size when the buffer is not drawn at
		// the physical size (integer-scale clients, rounding).
		dst, src image.Rectangle
		color    [3]uint8
		opaque   bool
		// dma is a client GPU buffer, blitted instead of copied from staging.
		dma *imported
	}
	var uploads []upload
	used := 0
	bounds := image.Rect(0, 0, r.width, r.height)
	add := func(rect image.Rectangle, c [3]uint8) {
		rect = rect.Intersect(bounds)
		if rect.Empty() {
			return
		}
		uploads = append(uploads, upload{rect: rect, offset: used, color: c})
		used += rect.Dx() * rect.Dy() * 4
	}
	// addContent maps src (buffer pixels) onto dst (physical pixels).
	addContent := func(dst, src image.Rectangle, content *ports.SurfaceContent) {
		rect := dst.Intersect(bounds)
		if rect.Empty() || src.Empty() {
			return
		}
		if content.DMABuf != nil {
			im, err := r.importDMABuf(content.DMABuf)
			if err != nil {
				// The window shows its background until a buffer imports.
				return
			}
			uploads = append(uploads, upload{rect: rect, dst: dst, src: src, dma: im})
			return
		}
		uploads = append(uploads, upload{rect: rect, offset: used, pixels: content.Pixels, stride: content.Stride, opaque: content.Opaque, dst: dst, src: src})
		used += rect.Dx() * rect.Dy() * 4
	}
	// drawSurface draws one surface buffer with its origin at (x, y)
	// logical, clipped to clip (logical).
	drawSurface := func(content *ports.SurfaceContent, x, y int, clip image.Rectangle) {
		lw, lh := content.LogicalW, content.LogicalH
		if lw <= 0 || lh <= 0 {
			lw, lh = content.Width, content.Height
		}
		if content.Pixels == nil && content.DMABuf == nil || lw <= 0 || lh <= 0 || content.Width <= 0 || content.Height <= 0 {
			return
		}
		if content.DMABuf == nil && (content.Stride < content.Width*4 || len(content.Pixels) < (content.Height-1)*content.Stride+content.Width*4) {
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
		// The visible part of dst in buffer pixels.
		bx := func(v int) int { return (v - full.Min.X) * content.Width / full.Dx() }
		by := func(v int) int { return (v - full.Min.Y) * content.Height / full.Dy() }
		src := image.Rect(bx(dst.Min.X), by(dst.Min.Y), bx(dst.Max.X), by(dst.Max.Y))
		addContent(dst, src, content)
	}
	// place draws a surface tree with its window geometry at (x, y)
	// logical, clipped to w×h logical: client shadows fall outside.
	place := func(content *ports.SurfaceContent, x, y, w, h int) {
		clip := image.Rect(x, y, x+w, y+h)
		ox, oy := x-content.Geometry.X, y-content.Geometry.Y
		for i := range content.Children {
			if ch := &content.Children[i]; ch.Below {
				drawSurface(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip)
			}
		}
		drawSurface(content, ox, oy, clip)
		for i := range content.Children {
			if ch := &content.Children[i]; !ch.Below {
				drawSurface(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip)
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
			place(&content, layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H)
		}
	}
	addLayers(false)
	for _, w := range s.Windows {
		if w.Hidden || w.Rect.W <= 0 || w.Rect.H <= 0 {
			continue
		}
		x, y := w.Rect.X, w.Rect.Y
		// Content sits inside the border; core sized the client to match.
		b := 0
		if !w.Fullscreen && !w.Borderless {
			b = min(max(s.Border.Width, 0), w.Rect.W/2, w.Rect.H/2)
		}
		cx, cy, cw, ch := x+b, y+b, w.Rect.W-2*b, w.Rect.H-2*b
		body := physRect(cx, cy, cw, ch)
		content := contents[w.ID]
		if content.Empty() {
			add(body, windowColor(w.ID))
		} else {
			add(body, parseColor(s.Background))
			place(&content, cx, cy, cw, ch)
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
	addLayers(true)
	if err := r.ensureStaging(used); err != nil {
		return err
	}
	data := unsafe.Slice((*byte)(r.stagingMapped), r.stagingSize)
	for _, u := range uploads {
		if u.dma != nil {
			continue
		}
		dst := data[u.offset : u.offset+u.rect.Dx()*u.rect.Dy()*4]
		for row := 0; row < u.rect.Dy(); row++ {
			line := dst[row*u.rect.Dx()*4 : (row+1)*u.rect.Dx()*4]
			if u.pixels != nil {
				y := u.rect.Min.Y + row
				if u.dst.Size() == u.src.Size() {
					start := (u.src.Min.Y+y-u.dst.Min.Y)*u.stride + (u.src.Min.X+u.rect.Min.X-u.dst.Min.X)*4
					copy(line, u.pixels[start:start+len(line)])
				} else {
					// Nearest-neighbour resample, sampling pixel centres.
					sy := u.src.Min.Y + ((y-u.dst.Min.Y)*2+1)*u.src.Dy()/(u.dst.Dy()*2)
					src := u.pixels[sy*u.stride:]
					for i := 0; i < u.rect.Dx(); i++ {
						sx := u.src.Min.X + ((u.rect.Min.X+i-u.dst.Min.X)*2+1)*u.src.Dx()/(u.dst.Dx()*2)
						copy(line[i*4:i*4+4], src[sx*4:sx*4+4])
					}
				}
				if u.opaque {
					for i := 3; i < len(line); i += 4 {
						line[i] = 255
					}
				}
			} else {
				for i := 0; i < len(line); i += 4 {
					line[i], line[i+1], line[i+2], line[i+3] = u.color[2], u.color[1], u.color[0], 255
				}
			}
		}
	}
	// B8G8R8A8 pixels are copied unchanged; blending belongs to the compositing pipeline.
	rgb := parseColor(s.Background)
	d := r.dd
	if err := checked("vkResetCommandBuffer", d.ResetCommandBuffer(r.command, 0)); err != nil {
		return err
	}
	begin := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo}
	if err := checked("vkBeginCommandBuffer", d.BeginCommandBuffer(r.command, &begin)); err != nil {
		return err
	}
	rangeInfo := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectColorBit, LevelCount: 1, LayerCount: 1}
	srcStage := vk.PipelineStageFlags(vk.PipelineStageTopOfPipeBit)
	srcAccess := vk.AccessFlags(0)
	if r.layout == vk.ImageLayoutTransferSrcOptimal {
		srcStage = vk.PipelineStageTransferBit
		srcAccess = vk.AccessTransferReadBit
	}
	barrier := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: srcAccess, DstAccessMask: vk.AccessTransferWriteBit, OldLayout: r.layout, NewLayout: vk.ImageLayoutTransferDstOptimal, Image: r.image, SubresourceRange: rangeInfo}
	d.CmdPipelineBarrier(r.command, srcStage, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &barrier)
	// VkClearColorValue is a union; UNORM formats read it as float32.
	unorm := func(v uint8) uint32 { return math.Float32bits(float32(v) / 255) }
	color := vk.ClearColorValue{unorm(rgb[0]), unorm(rgb[1]), unorm(rgb[2]), math.Float32bits(1)}
	d.CmdClearColorImage(r.command, r.image, vk.ImageLayoutTransferDstOptimal, &color, 1, &rangeInfo)
	barrier.SrcAccessMask = vk.AccessTransferWriteBit
	barrier.DstAccessMask = vk.AccessTransferWriteBit
	barrier.OldLayout = vk.ImageLayoutTransferDstOptimal
	barrier.NewLayout = vk.ImageLayoutTransferDstOptimal
	d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &barrier)
	// Client buffers come from the foreign queue family (their driver) and
	// go back to it after the frame, so the next frame acquires them again.
	var dmas []*imported
	for _, u := range uploads {
		if u.dma != nil && !slices.Contains(dmas, u.dma) {
			dmas = append(dmas, u.dma)
		}
	}
	ownership := func(acquire bool) {
		for _, im := range dmas {
			b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, Image: im.image, SubresourceRange: rangeInfo}
			if acquire {
				// GENERAL, never UNDEFINED: the client's contents must survive.
				b.OldLayout, b.NewLayout = vk.ImageLayoutGeneral, vk.ImageLayoutTransferSrcOptimal
				b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vk.QueueFamilyForeignEXT, r.family
				b.DstAccessMask = vk.AccessTransferReadBit
			} else {
				b.OldLayout, b.NewLayout = vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral
				b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
				b.SrcAccessMask = vk.AccessTransferReadBit
			}
			d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
		}
	}
	ownership(true)
	for i, f := range uploads {
		if i > 0 {
			d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &barrier)
		}
		rect := f.rect
		if f.dma != nil {
			// Map the visible part of dst back onto the buffer.
			sx := func(x int) int32 { return int32(f.src.Min.X + (x-f.dst.Min.X)*f.src.Dx()/f.dst.Dx()) }
			sy := func(y int) int32 { return int32(f.src.Min.Y + (y-f.dst.Min.Y)*f.src.Dy()/f.dst.Dy()) }
			layers := vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}
			blit := vk.ImageBlit{
				SrcSubresource: layers,
				SrcOffsets:     [2]vk.Offset3D{{X: sx(rect.Min.X), Y: sy(rect.Min.Y)}, {X: sx(rect.Max.X), Y: sy(rect.Max.Y), Z: 1}},
				DstSubresource: layers,
				DstOffsets:     [2]vk.Offset3D{{X: int32(rect.Min.X), Y: int32(rect.Min.Y)}, {X: int32(rect.Max.X), Y: int32(rect.Max.Y), Z: 1}},
			}
			d.CmdBlitImage(r.command, f.dma.image, vk.ImageLayoutTransferSrcOptimal, r.image, vk.ImageLayoutTransferDstOptimal, 1, &blit, vk.FilterNearest)
			continue
		}
		region := vk.BufferImageCopy{BufferOffset: vk.DeviceSize(f.offset), ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageOffset: vk.Offset3D{X: int32(rect.Min.X), Y: int32(rect.Min.Y)}, ImageExtent: vk.Extent3D{Width: uint32(rect.Dx()), Height: uint32(rect.Dy()), Depth: 1}}
		d.CmdCopyBufferToImage(r.command, r.staging, r.image, vk.ImageLayoutTransferDstOptimal, 1, &region)
	}
	ownership(false)
	barrier.DstAccessMask = vk.AccessTransferReadBit
	barrier.OldLayout = vk.ImageLayoutTransferDstOptimal
	barrier.NewLayout = vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &barrier)
	copyRegion := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
	d.CmdCopyImageToBuffer(r.command, r.image, vk.ImageLayoutTransferSrcOptimal, r.buffer, 1, &copyRegion)
	if err := checked("vkEndCommandBuffer", d.EndCommandBuffer(r.command)); err != nil {
		return err
	}
	// Implicit sync: wait for the client's GPU writes to each buffer.
	var waits []vk.Semaphore
	for _, im := range dmas {
		if sem := r.readFence(im); sem != 0 {
			waits = append(waits, sem)
		}
	}
	defer func() {
		for _, sem := range waits {
			d.DestroySemaphore(r.device, sem, nil)
		}
	}()
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &r.command}
	stages := make([]vk.PipelineStageFlags, len(waits))
	if len(waits) > 0 {
		for i := range stages {
			stages[i] = vk.PipelineStageTransferBit
		}
		submit.WaitSemaphoreCount, submit.WaitSemaphores, submit.WaitDstStageMask = uint32(len(waits)), &waits[0], &stages[0]
	}
	if err := checked("vkQueueSubmit", d.QueueSubmit(r.queue, 1, &submit, r.fence)); err != nil {
		return err
	}
	runtime.KeepAlive(stages)
	r.layout = vk.ImageLayoutTransferSrcOptimal
	if err := checked("vkWaitForFences", d.WaitForFences(r.device, 1, &r.fence, 1, math.MaxUint64)); err != nil {
		return err
	}
	r.dropUnused()
	return checked("vkResetFences", d.ResetFences(r.device, 1, &r.fence))
}

func (r *Renderer) Pixels() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	if r.mapped == nil {
		return out
	}
	src := unsafe.Slice((*byte)(r.mapped), len(out.Pix))
	for i := 0; i < len(src); i += 4 {
		// The output is opaque: x-format client buffers leave alpha undefined.
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = src[i+2], src[i+1], src[i], 255
	}
	return out
}

// CopyBGRX writes the last frame into an XRGB8888 buffer with the given pitch.
// The image is B8G8R8A8, which is already XRGB8888 in memory.
func (r *Renderer) CopyBGRX(dst []byte, pitch int) {
	if r.mapped == nil {
		return
	}
	row := r.width * 4
	src := unsafe.Slice((*byte)(r.mapped), row*r.height)
	for y := 0; y < r.height; y++ {
		copy(dst[y*pitch:y*pitch+row], src[y*row:(y+1)*row])
	}
}

func (r *Renderer) Close() {
	if r == nil {
		return
	}
	if r.dd != nil {
		d := r.dd
		if r.device != 0 {
			_ = checked("vkDeviceWaitIdle", d.DeviceWaitIdle(r.device))
		}
		for id, im := range r.imports {
			r.release(im)
			delete(r.imports, id)
		}
		if r.fence != 0 {
			d.DestroyFence(r.device, r.fence, nil)
			r.fence = 0
		}
		if r.command != 0 {
			d.FreeCommandBuffers(r.device, r.pool, 1, &r.command)
			r.command = 0
		}
		if r.pool != 0 {
			d.DestroyCommandPool(r.device, r.pool, nil)
			r.pool = 0
		}
		if r.stagingMapped != nil {
			d.UnmapMemory(r.device, r.stagingMemory)
			r.stagingMapped = nil
		}
		if r.staging != 0 {
			d.DestroyBuffer(r.device, r.staging, nil)
			r.staging = 0
		}
		if r.stagingMemory != 0 {
			d.FreeMemory(r.device, r.stagingMemory, nil)
			r.stagingMemory = 0
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
		if r.image != 0 {
			d.DestroyImage(r.device, r.image, nil)
			r.image = 0
		}
		if r.imageMemory != 0 {
			d.FreeMemory(r.device, r.imageMemory, nil)
			r.imageMemory = 0
		}
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
