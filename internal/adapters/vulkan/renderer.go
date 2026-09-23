package vulkan

import (
	"fmt"
	"image"
	"math"
	"os"
	"runtime"
	"strconv"
	"unsafe"

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

func (r *Renderer) Clear(rgb [3]uint8) error {
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
