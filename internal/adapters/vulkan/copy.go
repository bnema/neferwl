package vulkan

import (
	"errors"
	"fmt"
	"math"
	"os"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// Copier owns transfer imports and commands on one GPU; one goroutine uses it.
type Copier struct {
	gpu              *GPU
	q                *queue
	width, height    int
	pool             vk.CommandPool
	cmds             [3]vk.CommandBuffer
	sources, targets []*target
}

var _ ports.Copier = (*Copier)(nil)

// NewCopier allocates a fixed three-command-buffer transfer ring.
func (g *GPU) NewCopier(w, h int) (ports.Copier, error) {
	if w <= 0 || h <= 0 || uint64(w) > math.MaxUint32 || uint64(h) > math.MaxUint32 {
		return nil, fmt.Errorf("invalid copier dimensions %dx%d", w, h)
	}
	c := &Copier{gpu: g, q: g.acquireQueue(), width: w, height: h}
	info := vk.CommandPoolCreateInfo{SType: vk.StructureTypeCommandPoolCreateInfo, Flags: vk.CommandPoolCreateResetCommandBufferBit, QueueFamilyIndex: g.family}
	if err := checked("vkCreateCommandPool(copy)", g.dd.CreateCommandPool(g.device, &info, nil, &c.pool)); err != nil {
		c.Close()
		return nil, err
	}
	ai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: c.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: uint32(len(c.cmds))}
	if err := checked("vkAllocateCommandBuffers(copy)", g.dd.AllocateCommandBuffers(g.device, &ai, &c.cmds[0])); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func findCopyMemory(g *GPU, bits uint32, props vk.MemoryPropertyFlags) (uint32, error) {
	for i := uint32(0); i < g.memory.MemoryTypeCount; i++ {
		if bits&(1<<i) != 0 && g.memory.MemoryTypes[i].PropertyFlags&props == props {
			return i, nil
		}
	}
	return 0, errors.New("no compatible copy memory type")
}

func (c *Copier) importImage(b *ports.DMABuf, usage vk.ImageUsageFlags) (*target, error) {
	format, _, known := vkFormat(b.Format)
	n := len(b.Planes)
	if !known || isYUVFormat(b.Format) || b.Width != c.width || b.Height != c.height {
		return nil, errImageGeometry
	}
	if n < 1 || n > maxModifierPlanes {
		return nil, errImagePlanes
	}
	if !c.gpu.copyImportable(format, b.Modifier, usage) {
		return nil, errImageModifier
	}
	d := c.gpu.dd
	if err := sameDMABuf(b.Planes); err != nil {
		return nil, err
	}
	fd, err := dupFile(b.Planes[0].File)
	if err != nil {
		return nil, err
	}
	// Vulkan owns the fd it imports on success: hand it its own duplicate.
	// Closed here on every path but the successful import.
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	t := &target{exported: true, layout: vk.ImageLayoutUndefined}
	ok := false
	defer func() {
		if !ok {
			c.freeImage(t)
		}
	}()
	var layouts [maxModifierPlanes]vk.SubresourceLayout
	for i, p := range b.Planes {
		layouts[i] = vk.SubresourceLayout{Offset: vk.DeviceSize(p.Offset), RowPitch: vk.DeviceSize(p.Stride)}
	}
	explicit := vk.ImageDrmFormatModifierExplicitCreateInfoEXT{SType: vk.StructureTypeImageDRMFormatModifierExplicitCreateInfoEXT, DrmFormatModifier: b.Modifier, DrmFormatModifierPlaneCount: uint32(n), PlaneLayouts: &layouts[0]}
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, Next: unsafe.Pointer(&explicit), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(c.width), Height: uint32(c.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(imported target)", d.CreateImage(c.gpu.device, &ii, nil, &t.image)); err != nil {
		return nil, err
	}
	fdProps := vk.MemoryFdPropertiesKHR{SType: vk.StructureTypeMemoryFDPropertiesKHR}
	if err := checked("vkGetMemoryFdPropertiesKHR", d.GetMemoryFdPropertiesKHR(c.gpu.device, vk.ExternalMemoryHandleTypeDMABUFBitEXT, int32(fd), &fdProps)); err != nil {
		return nil, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(c.gpu.device, t.image, &req)
	bits := req.MemoryTypeBits & fdProps.MemoryTypeBits
	kind, err := findCopyMemory(c.gpu, bits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		if kind, err = findCopyMemory(c.gpu, bits, 0); err != nil {
			return nil, err
		}
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.image}
	imp := vk.ImportMemoryFdInfoKHR{SType: vk.StructureTypeImportMemoryFDInfoKHR, Next: unsafe.Pointer(&dedicated), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT, Fd: int32(fd)}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&imp), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(imported target)", d.AllocateMemory(c.gpu.device, &alloc, nil, &t.memory)); err != nil {
		return nil, err
	}
	fd = -1 // Vulkan owns it now.
	if err := checked("vkBindImageMemory(imported target)", d.BindImageMemory(c.gpu.device, t.image, t.memory, 0)); err != nil {
		return nil, err
	}
	ok = true
	return t, nil
}

func (g *GPU) copyImportable(format vk.Format, modifier uint64, usage vk.ImageUsageFlags) bool {
	mod := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{SType: vk.StructureTypePhysicalDeviceImageDRMFormatModifierInfoEXT, DrmFormatModifier: modifier, SharingMode: vk.SharingModeExclusive}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, Next: unsafe.Pointer(&mod), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: usage}
	external := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&external)}
	return g.inst.id.GetPhysicalDeviceImageFormatProperties2(g.physical, &info, &out) == vk.Success && external.ExternalMemoryProperties.ExternalMemoryFeatures&vk.ExternalMemoryFeatureImportableBit != 0
}

// SourceModifiers returns importable RGB modifiers supporting transfer/blit sources.
func (c *Copier) SourceModifiers(formats []ports.DMABufFormat) []ports.DMABufFormat {
	var out []ports.DMABufFormat
	for _, f := range formats {
		format, _, ok := vkFormat(f.Format)
		if !ok || isYUVFormat(f.Format) {
			continue
		}
		list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
		props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
		c.gpu.inst.id.GetPhysicalDeviceFormatProperties2(c.gpu.physical, format, &props)
		if list.DrmFormatModifierCount == 0 {
			continue
		}
		mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
		list.DrmFormatModifierProperties = &mods[0]
		c.gpu.inst.id.GetPhysicalDeviceFormatProperties2(c.gpu.physical, format, &props)
		for _, m := range mods[:list.DrmFormatModifierCount] {
			if m.DrmFormatModifier == f.Modifier && m.DrmFormatModifierTilingFeatures&(formatFeatureTransferSrc|vk.FormatFeatureBlitSrcBit) == (formatFeatureTransferSrc|vk.FormatFeatureBlitSrcBit) && c.gpu.copyImportable(format, f.Modifier, vk.ImageUsageTransferSrcBit) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func (c *Copier) importImages(bufs []ports.DMABuf, usage vk.ImageUsageFlags, dst *[]*target) error {
	for _, im := range *dst {
		c.freeImage(im)
	}
	*dst = nil
	for i := range bufs {
		im, err := c.importImage(&bufs[i], usage)
		if err != nil {
			for _, old := range *dst {
				c.freeImage(old)
			}
			*dst = nil
			return err
		}
		*dst = append(*dst, im)
	}
	return nil
}

// ImportSources replaces the transfer-source imports. The caller retains buffers.
func (c *Copier) ImportSources(bufs []ports.DMABuf) error {
	return c.importImages(bufs, vk.ImageUsageTransferSrcBit, &c.sources)
}

// ImportTargets replaces the transfer-destination imports.
func (c *Copier) ImportTargets(bufs []ports.DMABuf) error {
	return c.importImages(bufs, vk.ImageUsageTransferDstBit, &c.targets)
}

// Copy is implemented by the next transfer submission phase.
func (c *Copier) Copy(src, dst int, acquire *os.File) (*os.File, error) {
	return nil, errors.New("copy not implemented")
}

func (c *Copier) freeImage(t *target) {
	if t.image != 0 {
		c.gpu.dd.DestroyImage(c.gpu.device, t.image, nil)
		t.image = 0
	}
	if t.memory != 0 {
		c.gpu.dd.FreeMemory(c.gpu.device, t.memory, nil)
		t.memory = 0
	}
}

// Close releases all imports, commands and the acquired queue. It is idempotent.
func (c *Copier) Close() {
	if c == nil || c.q == nil {
		return
	}
	for _, im := range c.sources {
		c.freeImage(im)
	}
	for _, im := range c.targets {
		c.freeImage(im)
	}
	c.sources, c.targets = nil, nil
	if c.pool != 0 {
		c.gpu.dd.DestroyCommandPool(c.gpu.device, c.pool, nil)
		c.pool = 0
	}
	c.gpu.releaseQueue(c.q)
	c.q = nil
}
