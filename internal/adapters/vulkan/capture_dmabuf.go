package vulkan

import (
	"errors"
	"fmt"
	"image"
	"os"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"

	vk "bnema.dev/purego-vulkan/vulkan"
)

// Captures into client dmabufs (CaptureFrame.CopyTo). The slot's readback
// buffer already holds the frame as opaque sRGB8 BGRA (HDR frames went
// through the capture pass), so a copy is that buffer's region uploaded
// into the imported client image, on the frame queue after the readback.
// Each copy has its own command buffer, fence and exported semaphore; the
// slot keeps it until its fence polls signalled, and is not reused before.

// VK_FORMAT_FEATURE_TRANSFER_DST_BIT (Vulkan 1.1), missing from the
// generated bindings.
const formatFeatureTransferDst = 1 << 15

// maxSlotCopies bounds the dmabuf copies of one lease (a batch's requests).
const maxSlotCopies = 8

// captureFourccs are the client formats CopyTo writes: B8G8R8A8 in memory.
var captureFourccs = [...]uint32{fourcc('X', 'R', '2', '4'), fourcc('A', 'R', '2', '4')}

// dmaCopy is one submitted copy into a client dmabuf.
type dmaCopy struct {
	cmd   vk.CommandBuffer
	fence vk.Fence
	done  vk.Semaphore
	img   *target
}

// CaptureFormats lists the single-plane XRGB8888/ARGB8888 modifiers a
// capture can be copied into: transfer destinations the device imports.
func (g *GPU) CaptureFormats() []domain.DMABufFormat {
	if g.dd == nil || !g.syncFD {
		return nil
	}
	var out []domain.DMABufFormat
	for _, fc := range captureFourccs {
		format, _, _ := vkFormat(fc)
		list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
		props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
		g.inst.id.GetPhysicalDeviceFormatProperties2(g.physical, format, &props)
		if list.DrmFormatModifierCount == 0 {
			continue
		}
		mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
		list.DrmFormatModifierProperties = &mods[0]
		g.inst.id.GetPhysicalDeviceFormatProperties2(g.physical, format, &props)
		for _, m := range mods[:list.DrmFormatModifierCount] {
			if m.DrmFormatModifierPlaneCount == 1 && m.DrmFormatModifierTilingFeatures&formatFeatureTransferDst != 0 && g.copyImportable(format, m.DrmFormatModifier, vk.ImageUsageTransferDstBit) {
				out = append(out, domain.DMABufFormat{Format: fc, Modifier: m.DrmFormatModifier})
			}
		}
	}
	return out
}

// CopyTo submits the copy of region into dst and returns its sync file.
func (f *captureFrame) CopyTo(region image.Rectangle, dst domain.DMABuf) (*os.File, error) {
	if !f.valid {
		return nil, errors.New("capture frame ended")
	}
	r, s := f.r, f.slot
	if r.gpu.device == 0 || r.gpu.dd == nil {
		return nil, errors.New("renderer closed")
	}
	if region.Empty() || !region.In(image.Rect(0, 0, f.w, f.h)) || dst.Width != region.Dx() || dst.Height != region.Dy() {
		return nil, errors.New("invalid capture region or destination")
	}
	if dst.Format != captureFourccs[0] && dst.Format != captureFourccs[1] {
		return nil, fmt.Errorf("capture dmabuf format %#x unsupported", dst.Format)
	}
	if !r.syncFD || !r.gpu.dd.HasGetSemaphoreFdKHR() {
		return nil, errors.New("capture needs sync file export")
	}
	if err := r.reapCopies(s); err != nil {
		return nil, err
	}
	if len(s.copies) >= maxSlotCopies {
		return nil, ports.ErrCaptureBusy
	}
	img, err := r.gpu.importBlitImage(&dst, dst.Width, dst.Height, vk.ImageUsageTransferDstBit, formatFeatureTransferDst)
	if err != nil {
		return nil, err
	}
	c := &dmaCopy{img: img}
	submitted := false
	defer func() {
		if !submitted {
			r.freeDMACopy(c)
		}
	}()
	d, dev := r.gpu.dd, r.gpu.device
	ai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: r.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: 1}
	if err := checked("vkAllocateCommandBuffers(capture dmabuf)", d.AllocateCommandBuffers(dev, &ai, &c.cmd)); err != nil {
		return nil, err
	}
	fi := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
	if err := checked("vkCreateFence(capture dmabuf)", d.CreateFence(dev, &fi, nil, &c.fence)); err != nil {
		return nil, err
	}
	if c.done, err = (deviceSync{r}).newSemaphore(); err != nil {
		return nil, err
	}
	begin := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageOneTimeSubmitBit}
	if err := checked("vkBeginCommandBuffer(capture dmabuf)", d.BeginCommandBuffer(c.cmd, &begin)); err != nil {
		return nil, err
	}
	// The readback, submitted earlier on this queue, wrote the buffer.
	hb := vk.BufferMemoryBarrier{SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: vk.AccessTransferWriteBit, DstAccessMask: vk.AccessTransferReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Buffer: s.buffer, Size: wholeSize}
	// Acquire the client image from the foreign family; its content is replaced.
	ib := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferWriteBit, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutTransferDstOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.gpu.family, Image: img.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(c.cmd, vk.PipelineStageTransferBit, vk.PipelineStageTransferBit, 0, 0, nil, 1, &hb, 1, &ib)
	region0 := vk.BufferImageCopy{
		BufferOffset:     vk.DeviceSize((region.Min.Y*f.w + region.Min.X) * captureBytesPerPixel),
		BufferRowLength:  uint32(f.w),
		ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1},
		ImageExtent:      vk.Extent3D{Width: uint32(dst.Width), Height: uint32(dst.Height), Depth: 1},
	}
	d.CmdCopyBufferToImage(c.cmd, s.buffer, img.image, vk.ImageLayoutTransferDstOptimal, 1, &region0)
	ib.SrcAccessMask, ib.DstAccessMask = vk.AccessTransferWriteBit, 0
	ib.OldLayout, ib.NewLayout = vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutGeneral
	ib.SrcQueueFamilyIndex, ib.DstQueueFamilyIndex = r.gpu.family, vk.QueueFamilyForeignEXT
	d.CmdPipelineBarrier(c.cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &ib)
	if err := checked("vkEndCommandBuffer(capture dmabuf)", d.EndCommandBuffer(c.cmd)); err != nil {
		return nil, err
	}
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &c.cmd, SignalSemaphoreCount: 1, SignalSemaphores: &c.done}
	if err := r.q.submit(1, &submit, c.fence); err != nil {
		return nil, err
	}
	// From here the copy is tracked by its fence, whatever happens.
	submitted = true
	s.copies = append(s.copies, c)
	get := vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFDInfoKHR, Semaphore: c.done, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit}
	fd := int32(-1)
	if err := checked("vkGetSemaphoreFdKHR(capture dmabuf)", d.GetSemaphoreFdKHR(dev, &get, &fd)); err != nil {
		return nil, err
	}
	if fd < 0 {
		return nil, nil
	}
	return os.NewFile(uintptr(fd), "capture-dmabuf-done"), nil
}

// reapCopies frees the slot's copies whose fence polls signalled.
func (r *Renderer) reapCopies(s *captureSlot) error {
	kept := s.copies[:0]
	var err error
	for _, c := range s.copies {
		switch res := r.gpu.dd.WaitForFences(r.gpu.device, 1, &c.fence, 1, 0); res {
		case vk.Success:
			r.freeDMACopy(c)
		case vk.Timeout, vk.NotReady:
			kept = append(kept, c)
		default:
			kept = append(kept, c)
			if err == nil {
				err = checked("vkWaitForFences(capture dmabuf poll)", res)
			}
		}
	}
	clear(s.copies[len(kept):])
	s.copies = kept
	return err
}

// freeDMACopy destroys a copy the device is done with (or never ran).
func (r *Renderer) freeDMACopy(c *dmaCopy) {
	d, dev := r.gpu.dd, r.gpu.device
	if c.done != 0 {
		d.DestroySemaphore(dev, c.done, nil)
	}
	if c.fence != 0 {
		d.DestroyFence(dev, c.fence, nil)
	}
	if c.cmd != 0 {
		d.FreeCommandBuffers(dev, r.pool, 1, &c.cmd)
	}
	if c.img != nil {
		r.gpu.freeImage(c.img)
	}
	*c = dmaCopy{}
}
