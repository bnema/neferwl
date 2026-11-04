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
	"golang.org/x/sys/unix"
)

// Captures into client dmabufs (CaptureFrame.CopyTo). The slot's readback
// buffer already holds the frame as opaque sRGB8 BGRA, alpha 0xff (the
// compose pipelines never write alpha over the clear's 1; the HDR capture
// pass writes 1), so a copy is that buffer's region uploaded
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
	wait  vk.Semaphore // the destination's implicit fence, 0 when none
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
	// The slot holds opaque sRGB8 BGRA, or XBGR2101010 PQ words for a PQ
	// capture: either is copied as is into a destination of that layout.
	if f.pq && dst.Format != castFourccs[0] || !f.pq && dst.Format != captureFourccs[0] && dst.Format != captureFourccs[1] {
		return nil, fmt.Errorf("capture dmabuf format %#x unsupported for this capture (pq %v)", dst.Format, f.pq)
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
	// Implicit sync with the buffer's other users: the copy waits for what
	// they still do with it (a consumer reading the previous frame). When
	// that cannot be established nothing is written (fail closed).
	if c.wait, err = r.dmabufBusy(dst.Planes[0].File); err != nil {
		return nil, err
	}
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
	waitStage := vk.PipelineStageFlags(vk.PipelineStageTransferBit)
	if c.wait != 0 {
		submit.WaitSemaphoreCount, submit.WaitSemaphores, submit.WaitDstStageMask = 1, &c.wait, &waitStage
	}
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
	done := os.NewFile(uintptr(fd), "capture-dmabuf-done")
	// Implicit sync the other way: users that do not wait on done (a
	// PipeWire consumer) wait on the buffer's own fence.
	attachWriteFence(dst.Planes[0].File, done)
	return done, nil
}

// dmabufFences is the implicit-sync surface of CopyTo destinations: the
// dmabuf fence export and the wait semaphore it is imported into (a seam for
// tests; the device and the kernel implement it).
type dmabufFences interface {
	// exportSyncFile returns a sync file of every fence pending on the
	// dmabuf of f, for a writer (readers and writers); -1 when the kernel
	// reports none. The fd is the caller's.
	exportSyncFile(f *os.File) (int32, error)
	createSemaphore() (vk.Semaphore, error)
	// importSyncFD makes fd the temporary payload of s; on success Vulkan
	// owns fd, on failure it is still the caller's.
	importSyncFD(s vk.Semaphore, fd int32) vk.Result
	destroySemaphore(s vk.Semaphore)
}

// deviceFences is the dmabufFences of the renderer's device.
type deviceFences struct{ r *Renderer }

func (deviceFences) exportSyncFile(f *os.File) (int32, error) {
	arg := dmaBufSync{flags: dmaBufSyncWrite, fd: -1}
	if err := dmabufIoctl(f, ioctlExportSyncFile, &arg); err != nil {
		return -1, fmt.Errorf("export cast buffer fences: %w", err)
	}
	return arg.fd, nil
}

func (d deviceFences) createSemaphore() (vk.Semaphore, error) {
	var sem vk.Semaphore
	si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
	err := checked("vkCreateSemaphore(dmabuf wait)", d.r.gpu.dd.CreateSemaphore(d.r.gpu.device, &si, nil, &sem))
	return sem, err
}

func (d deviceFences) importSyncFD(s vk.Semaphore, fd int32) vk.Result {
	imp := vk.ImportSemaphoreFdInfoKHR{SType: vk.StructureTypeImportSemaphoreFDInfoKHR, Semaphore: s, Flags: vk.SemaphoreImportTemporaryBit, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit, Fd: fd}
	return d.r.gpu.dd.ImportSemaphoreFdKHR(d.r.gpu.device, &imp)
}

func (d deviceFences) destroySemaphore(s vk.Semaphore) {
	d.r.gpu.dd.DestroySemaphore(d.r.gpu.device, s, nil)
}

// fences is the dmabuf fence seam: the device unless a test replaced it.
func (r *Renderer) fences() dmabufFences {
	if r.dmaFences == nil {
		r.dmaFences = deviceFences{r}
	}
	return r.dmaFences
}

// dmabufBusy makes a wait semaphore of every fence pending on the dmabuf
// of f (DMA_BUF_IOCTL_EXPORT_SYNC_FILE for writing: readers and writers).
// 0 with no error only when the kernel reports no fence; any failure to
// export or import is an error, so the caller writes nothing. The exported
// sync file is closed on every path but a successful import.
func (r *Renderer) dmabufBusy(f *os.File) (vk.Semaphore, error) {
	fs := r.fences()
	fd, err := fs.exportSyncFile(f)
	if err != nil {
		return 0, err
	}
	if fd < 0 {
		return 0, nil
	}
	sem, err := fs.createSemaphore()
	if err != nil {
		_ = unix.Close(int(fd))
		return 0, err
	}
	if err := checked("vkImportSemaphoreFdKHR(dmabuf wait)", fs.importSyncFD(sem, fd)); err != nil {
		_ = unix.Close(int(fd))
		fs.destroySemaphore(sem)
		return 0, err
	}
	return sem, nil
}

// attachWriteFence adds the sync file done to the dmabuf of f as a write
// fence (DMA_BUF_IOCTL_IMPORT_SYNC_FILE): implicit-sync readers wait for it.
// Best effort: an older kernel leaves the reader to explicit sync.
func attachWriteFence(f, done *os.File) {
	sc, err := done.SyscallConn()
	if err != nil {
		return
	}
	_ = sc.Control(func(fd uintptr) {
		arg := dmaBufSync{flags: dmaBufSyncWrite, fd: int32(fd)}
		_ = dmabufIoctl(f, ioctlImportSyncFile, &arg)
	})
}

// dmabufIoctl runs a sync-file ioctl on the dmabuf of f.
func dmabufIoctl(f *os.File, req uintptr, arg *dmaBufSync) error {
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno unix.Errno
	if err := sc.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(arg)))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
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
	if c.wait != 0 {
		r.fences().destroySemaphore(c.wait)
	}
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
