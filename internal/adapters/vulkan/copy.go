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
	slots            [3]copySlot
	next             int
	sources, targets []*target
}

var _ ports.Copier = (*Copier)(nil)

// NewCopier allocates a fixed three-command-buffer transfer ring.
func (g *GPU) NewCopier(w, h int) (ports.Copier, error) {
	if w <= 0 || h <= 0 || uint64(w) > math.MaxUint32 || uint64(h) > math.MaxUint32 {
		return nil, fmt.Errorf("invalid copier dimensions %dx%d", w, h)
	}
	if !g.syncFD {
		return nil, errors.New("GPU copy requires sync_fd export")
	}
	c := &Copier{gpu: g, q: g.acquireQueue(), width: w, height: h}
	if c.q == nil {
		return nil, errors.New("vulkan: GPU closed")
	}
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
	need := vk.FormatFeatureFlags(vk.FormatFeatureBlitSrcBit)
	if usage == vk.ImageUsageTransferDstBit {
		need = vk.FormatFeatureBlitDstBit
	}
	planes, supported := c.supportsBlit(format, b.Modifier, need)
	if !supported {
		return nil, fmt.Errorf("copy format %#x modifier %#x lacks blit feature %#x", b.Format, b.Modifier, need)
	}
	if uint32(n) != planes {
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
	if c.q == nil {
		return errors.New("copier closed")
	}
	if err := c.waitAll(); err != nil {
		return err
	}
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

type copySlot struct {
	fence      vk.Fence
	busy       bool
	wait, done vk.Semaphore
	// stale: done was signalled but not exported; recreate it before reuse.
	stale bool
}

func (c *Copier) waitSlot(s *copySlot) error {
	if s.busy {
		if err := checked("vkWaitForFences(copy)", c.gpu.dd.WaitForFences(c.gpu.device, 1, &s.fence, 1, math.MaxUint64)); err != nil {
			return err
		}
		if err := checked("vkResetFences(copy)", c.gpu.dd.ResetFences(c.gpu.device, 1, &s.fence)); err != nil {
			return err
		}
		s.busy = false
	}
	if s.wait != 0 {
		c.gpu.dd.DestroySemaphore(c.gpu.device, s.wait, nil)
		s.wait = 0
	}
	if s.stale {
		c.destroySlotSemaphores(s)
	}
	return nil
}

// destroySlotSemaphores destroys the slot's wait and done semaphores.
func (c *Copier) destroySlotSemaphores(s *copySlot) {
	if s.wait != 0 {
		c.gpu.dd.DestroySemaphore(c.gpu.device, s.wait, nil)
		s.wait = 0
	}
	if s.done != 0 {
		c.gpu.dd.DestroySemaphore(c.gpu.device, s.done, nil)
		s.done = 0
	}
	s.stale = false
}

func (c *Copier) waitAll() error {
	for i := range c.slots {
		if err := c.waitSlot(&c.slots[i]); err != nil {
			return err
		}
	}
	return nil
}

// Copy submits a 1:1 nearest blit. The acquire file remains caller-owned.
func (c *Copier) Copy(src, dst int, acquire *os.File) (*os.File, error) {
	if c.q == nil {
		return nil, errors.New("copier closed")
	}
	if src < 0 || src >= len(c.sources) || dst < 0 || dst >= len(c.targets) {
		return nil, errors.New("copy image index out of range")
	}
	s := &c.slots[c.next]
	cmd := c.cmds[c.next]
	if err := c.waitSlot(s); err != nil {
		return nil, err
	}
	d, dev := c.gpu.dd, c.gpu.device
	if s.fence == 0 {
		fi := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
		if err := checked("vkCreateFence(copy)", d.CreateFence(dev, &fi, nil, &s.fence)); err != nil {
			return nil, err
		}
	}
	if s.done == 0 {
		// Exporting a sync_fd resets the semaphore to unsignalled: reused.
		export := vk.ExportSemaphoreCreateInfo{SType: vk.StructureTypeExportSemaphoreCreateInfo, HandleTypes: vk.ExternalSemaphoreHandleTypeSyncFDBit}
		si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo, Next: unsafe.Pointer(&export)}
		if err := checked("vkCreateSemaphore(copy)", d.CreateSemaphore(dev, &si, nil, &s.done)); err != nil {
			return nil, err
		}
	}
	if acquire != nil {
		fd, err := dupFile(acquire)
		if err != nil {
			return nil, err
		}
		s.wait, err = c.gpu.importCopyFence(fd)
		if err != nil {
			return nil, err
		}
	}
	if err := checked("vkResetCommandBuffer(copy)", d.ResetCommandBuffer(cmd, 0)); err != nil {
		return nil, err
	}
	begin := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo}
	if err := checked("vkBeginCommandBuffer(copy)", d.BeginCommandBuffer(cmd, &begin)); err != nil {
		return nil, err
	}
	source, target := c.sources[src], c.targets[dst]
	barriers := [2]vk.ImageMemoryBarrier{
		{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: vk.ImageLayoutGeneral, NewLayout: vk.ImageLayoutTransferSrcOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: c.gpu.family, Image: source.image, SubresourceRange: colorRange},
		{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferWriteBit, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutTransferDstOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: c.gpu.family, Image: target.image, SubresourceRange: colorRange},
	}
	// The submit waits on the acquire fence at the transfer stage: the
	// ownership transfer must come after that wait (as in Renderer.ownership).
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 2, &barriers[0])
	layers := vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}
	offsets := [2]vk.Offset3D{{}, {X: int32(c.width), Y: int32(c.height), Z: 1}}
	blit := vk.ImageBlit{SrcSubresource: layers, SrcOffsets: offsets, DstSubresource: layers, DstOffsets: offsets}
	// purego-vulkan v0.6.0 lacks CmdCopyImage; 1:1 NEAREST blit is an exact copy.
	d.CmdBlitImage(cmd, source.image, vk.ImageLayoutTransferSrcOptimal, target.image, vk.ImageLayoutTransferDstOptimal, 1, &blit, vk.FilterNearest)
	for i := range barriers {
		b := &barriers[i]
		b.SrcAccessMask = b.DstAccessMask
		b.DstAccessMask = 0
		b.OldLayout = b.NewLayout
		b.NewLayout = vk.ImageLayoutGeneral
		b.SrcQueueFamilyIndex = c.gpu.family
		b.DstQueueFamilyIndex = vk.QueueFamilyForeignEXT
	}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 2, &barriers[0])
	if err := checked("vkEndCommandBuffer(copy)", d.EndCommandBuffer(cmd)); err != nil {
		return nil, err
	}
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &cmd, SignalSemaphoreCount: 1, SignalSemaphores: &s.done}
	stage := vk.PipelineStageFlags(vk.PipelineStageTransferBit)
	if s.wait != 0 {
		submit.WaitSemaphoreCount = 1
		submit.WaitSemaphores = &s.wait
		submit.WaitDstStageMask = &stage
	}
	if err := c.q.submit(1, &submit, s.fence); err != nil {
		return nil, err
	}
	s.busy = true
	c.next = (c.next + 1) % len(c.cmds)
	get := vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFDInfoKHR, Semaphore: s.done, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit}
	fd := int32(-1)
	if err := checked("vkGetSemaphoreFdKHR(copy)", d.GetSemaphoreFdKHR(dev, &get, &fd)); err != nil {
		s.stale = true // signalled, never exported
		return nil, err
	}
	if fd < 0 {
		return nil, nil
	} // Already signalled; no CPU wait/readback fallback.
	return os.NewFile(uintptr(fd), "copy-done"), nil
}

func (g *GPU) importCopyFence(fd int) (vk.Semaphore, error) {
	var sem vk.Semaphore
	si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
	if err := checked("vkCreateSemaphore(copy acquire)", g.dd.CreateSemaphore(g.device, &si, nil, &sem)); err != nil {
		unix.Close(fd)
		return 0, err
	}
	imp := vk.ImportSemaphoreFdInfoKHR{SType: vk.StructureTypeImportSemaphoreFDInfoKHR, Semaphore: sem, Flags: vk.SemaphoreImportTemporaryBit, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit, Fd: int32(fd)}
	if err := checked("vkImportSemaphoreFdKHR(copy)", g.dd.ImportSemaphoreFdKHR(g.device, &imp)); err != nil {
		unix.Close(fd)
		g.dd.DestroySemaphore(g.device, sem, nil)
		return 0, err
	}
	return sem, nil
}

func (c *Copier) supportsBlit(format vk.Format, modifier uint64, need vk.FormatFeatureFlags) (uint32, bool) {
	list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
	props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
	c.gpu.inst.id.GetPhysicalDeviceFormatProperties2(c.gpu.physical, format, &props)
	if list.DrmFormatModifierCount == 0 {
		return 0, false
	}
	mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
	list.DrmFormatModifierProperties = &mods[0]
	c.gpu.inst.id.GetPhysicalDeviceFormatProperties2(c.gpu.physical, format, &props)
	for _, m := range mods[:list.DrmFormatModifierCount] {
		if m.DrmFormatModifier == modifier {
			return m.DrmFormatModifierPlaneCount, m.DrmFormatModifierTilingFeatures&need == need
		}
	}
	return 0, false
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
	for i := range c.slots {
		s := &c.slots[i]
		if err := c.waitSlot(s); err != nil {
			c.gpu.log.Error().Err(err).Int("slot", i).Msg("wait for GPU copy on close")
		}
		c.destroySlotSemaphores(s)
		if s.fence != 0 {
			c.gpu.dd.DestroyFence(c.gpu.device, s.fence, nil)
			s.fence = 0
		}
		s.busy = false
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
