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
	r = &Renderer{width: width, height: height, layout: vk.ImageLayoutUndefined}
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
	priority := float32(1)
	qi := vk.DeviceQueueCreateInfo{SType: vk.StructureTypeDeviceQueueCreateInfo, QueueFamilyIndex: family, QueueCount: 1, QueuePriorities: &priority}
	dynamicRendering := vk.PhysicalDeviceDynamicRenderingFeatures{SType: vk.StructureTypePhysicalDeviceDynamicRenderingFeatures, DynamicRendering: 1}
	di := vk.DeviceCreateInfo{SType: vk.StructureTypeDeviceCreateInfo, Next: unsafe.Pointer(&dynamicRendering), QueueCreateInfoCount: 1, QueueCreateInfos: &qi}
	if err = checked("vkCreateDevice", r.id.CreateDevice(physical, &di, nil, &r.device)); err != nil {
		return
	}
	r.dd, err = vk.LoadDeviceDispatch(r.id, r.device)
	if err != nil {
		err = fmt.Errorf("LoadDeviceDispatch: %w", err)
		return
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
	type upload struct {
		rect                     image.Rectangle
		offset                   int
		pixels                   []byte
		stride, sourceX, sourceY int
		color                    [3]uint8
		opaque                   bool
	}
	var uploads []upload
	used := 0
	bounds := image.Rect(0, 0, r.width, r.height)
	add := func(rect image.Rectangle, c [3]uint8, content *ports.SurfaceContent, origin image.Point) {
		rect = rect.Intersect(bounds)
		if rect.Empty() {
			return
		}
		n := rect.Dx() * rect.Dy() * 4
		u := upload{rect: rect, offset: used, color: c}
		if content != nil {
			u.pixels, u.stride, u.opaque = content.Pixels, content.Stride, content.Opaque
			u.sourceX, u.sourceY = rect.Min.X-origin.X, rect.Min.Y-origin.Y
		}
		uploads = append(uploads, u)
		used += n
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
			width, height := min(content.Width, layer.Rect.W), min(content.Height, layer.Rect.H)
			if content.Pixels == nil || width <= 0 || height <= 0 || content.Stride < width*4 || len(content.Pixels) < (height-1)*content.Stride+width*4 {
				continue
			}
			x, y := layer.Rect.X, layer.Rect.Y
			add(image.Rect(x, y, x+width, y+height), [3]uint8{}, &content, image.Pt(x, y))
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
		if !w.Fullscreen {
			b = min(max(s.Border.Width, 0), w.Rect.W/2, w.Rect.H/2)
		}
		cx, cy, cw, ch := x+b, y+b, w.Rect.W-2*b, w.Rect.H-2*b
		body := image.Rect(cx, cy, cx+cw, cy+ch)
		content := contents[w.ID]
		if content.Pixels == nil {
			add(body, windowColor(w.ID), nil, image.Point{})
		} else {
			add(body, parseColor(s.Background), nil, image.Point{})
			width, height := min(content.Width, cw), min(content.Height, ch)
			if width > 0 && height > 0 && content.Stride >= width*4 && len(content.Pixels) >= (height-1)*content.Stride+width*4 {
				add(image.Rect(cx, cy, cx+width, cy+height), [3]uint8{}, &content, image.Pt(cx, cy))
			}
		}
		borderColor := s.Border.Inactive
		if w.Focused {
			borderColor = s.Border.Active
		}
		if b > 0 && borderColor != "" {
			rgb := parseColor(borderColor)
			for _, strip := range []image.Rectangle{image.Rect(x, y, x+w.Rect.W, y+b), image.Rect(x, y+w.Rect.H-b, x+w.Rect.W, y+w.Rect.H), image.Rect(x, y+b, x+b, y+w.Rect.H-b), image.Rect(x+w.Rect.W-b, y+b, x+w.Rect.W, y+w.Rect.H-b)} {
				add(strip, rgb, nil, image.Point{})
			}
		}
	}
	addLayers(true)
	if err := r.ensureStaging(used); err != nil {
		return err
	}
	data := unsafe.Slice((*byte)(r.stagingMapped), r.stagingSize)
	for _, u := range uploads {
		dst := data[u.offset : u.offset+u.rect.Dx()*u.rect.Dy()*4]
		for row := 0; row < u.rect.Dy(); row++ {
			line := dst[row*u.rect.Dx()*4 : (row+1)*u.rect.Dx()*4]
			if u.pixels != nil {
				start := (u.sourceY+row)*u.stride + u.sourceX*4
				copy(line, u.pixels[start:start+len(line)])
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
	for i, f := range uploads {
		if i > 0 {
			d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &barrier)
		}
		rect := f.rect
		region := vk.BufferImageCopy{BufferOffset: vk.DeviceSize(f.offset), ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageOffset: vk.Offset3D{X: int32(rect.Min.X), Y: int32(rect.Min.Y)}, ImageExtent: vk.Extent3D{Width: uint32(rect.Dx()), Height: uint32(rect.Dy()), Depth: 1}}
		d.CmdCopyBufferToImage(r.command, r.staging, r.image, vk.ImageLayoutTransferDstOptimal, 1, &region)
	}
	barrier.DstAccessMask = vk.AccessTransferReadBit
	barrier.OldLayout = vk.ImageLayoutTransferDstOptimal
	barrier.NewLayout = vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &barrier)
	copyRegion := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
	d.CmdCopyImageToBuffer(r.command, r.image, vk.ImageLayoutTransferSrcOptimal, r.buffer, 1, &copyRegion)
	if err := checked("vkEndCommandBuffer", d.EndCommandBuffer(r.command)); err != nil {
		return err
	}
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &r.command}
	if err := checked("vkQueueSubmit", d.QueueSubmit(r.queue, 1, &submit, r.fence)); err != nil {
		return err
	}
	r.layout = vk.ImageLayoutTransferSrcOptimal
	if err := checked("vkWaitForFences", d.WaitForFences(r.device, 1, &r.fence, 1, math.MaxUint64)); err != nil {
		return err
	}
	return checked("vkResetFences", d.ResetFences(r.device, 1, &r.fence))
}

func (r *Renderer) Pixels() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	if r.mapped == nil {
		return out
	}
	src := unsafe.Slice((*byte)(r.mapped), len(out.Pix))
	for i := 0; i < len(src); i += 4 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
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
