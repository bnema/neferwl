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
	kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
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
	// Two frame-sized regions accommodate non-overlapping window bodies and their overlapping borders.
	stagingSize := size * 2
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

func (r *Renderer) Clear(rgb [3]uint8) error {
	return r.Render(ports.Scene{Background: fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])})
}

func (r *Renderer) Render(s ports.Scene) error {
	// Stage before recording: a full buffer is an error, not a partial submission.
	type fill struct {
		rect   image.Rectangle
		color  [3]uint8
		offset int
	}
	var fills []fill
	used := 0
	add := func(rect image.Rectangle, c [3]uint8) error {
		rect = rect.Intersect(image.Rect(0, 0, r.width, r.height))
		if rect.Empty() {
			return nil
		}
		n := rect.Dx() * rect.Dy() * 4
		if n > r.width*r.height*8-used {
			return fmt.Errorf("staging buffer full")
		}
		data := unsafe.Slice((*byte)(r.stagingMapped), r.width*r.height*8)[used : used+n]
		for i := 0; i < n; i += 4 {
			data[i], data[i+1], data[i+2], data[i+3] = c[2], c[1], c[0], 255
		}
		fills = append(fills, fill{rect, c, used})
		used += n
		return nil
	}
	for _, w := range s.Windows {
		if w.Hidden || w.Rect.W <= 0 || w.Rect.H <= 0 {
			continue
		}
		x, y := w.Rect.X, w.Rect.Y
		// Clip before converting coordinates to Vulkan's signed 32-bit offsets.
		body := image.Rect(x, y, x+w.Rect.W, y+w.Rect.H)
		if err := add(body, windowColor(w.ID)); err != nil {
			return err
		}
		if w.Focused {
			for _, strip := range []image.Rectangle{image.Rect(x, y, x+w.Rect.W, y+4), image.Rect(x, y+w.Rect.H-4, x+w.Rect.W, y+w.Rect.H), image.Rect(x, y+4, x+4, y+w.Rect.H-4), image.Rect(x+w.Rect.W-4, y+4, x+w.Rect.W, y+w.Rect.H-4)} {
				if err := add(strip, [3]uint8{255, 255, 255}); err != nil {
					return err
				}
			}
		}
	}
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
	for _, f := range fills {
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
