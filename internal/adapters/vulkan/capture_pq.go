package vulkan

import (
	"errors"
	"unsafe"

	vk "bnema.dev/purego-vulkan/vulkan"
	"github.com/bnema/neferwl/internal/ports"
)

// PQ capture: an HDR frame read back as the display receives it, PQ-encoded
// BT.2020 without tone mapping, in XBGR2101010 (DRM 'XB30', SPA
// xBGR_210LE, Vulkan A2B10G10R10: R in bits 0-9, G 10-19, B 20-29). The
// display target is XRGB2101010 and is not a transfer source in production,
// so the pass re-encodes hdrOwn with the display's own PQ shader (hdr.frag)
// into a private A2B10G10R10 image: the words equal the display target's
// with R and B swapped (SwizzleXR30ToXB30).

// capturePQPass is the private image and its pipeline; the pipeline reuses
// the display PQ pass's layout and descriptor set (r.hdr).
type capturePQPass struct {
	pipeline vk.Pipeline
	out      target
}

const formatXB30 = vk.FormatA2b10g10r10UnormPack32

// SwizzleXR30ToXB30 converts an XRGB2101010 word (R in bits 20-29) to
// XBGR2101010 (R in bits 0-9); the two padding bits are cleared.
func SwizzleXR30ToXB30(w uint32) uint32 {
	return (w>>20)&0x3ff | w&(0x3ff<<10) | (w&0x3ff)<<20
}

// PackXB30 packs 10-bit channels into an XBGR2101010 word.
func PackXB30(r, g, b uint16) uint32 {
	return uint32(r&0x3ff) | uint32(g&0x3ff)<<10 | uint32(b&0x3ff)<<20
}

// BeginCapturePQ is BeginCapture for the last HDR frame as XBGR2101010 PQ
// words: Read copies them as is (4 bytes per pixel, no alpha forced). It
// fails when the last frame was not HDR; the lease is as BeginCapture's.
func (r *Renderer) BeginCapturePQ() (ports.CaptureFrame, error) {
	f, err := r.beginCaptureAs(false, true)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// createCapturePQ makes the image and pipeline once, on the first PQ capture.
func (r *Renderer) createCapturePQ() (err error) {
	p := &r.capturePQ
	if p.pipeline != 0 {
		return nil
	}
	if r.hdr.pipeline == 0 || r.hdrOwn.view == 0 {
		return errors.New("HDR pass unavailable")
	}
	defer func() {
		if err != nil {
			r.destroyCapturePQ()
		}
	}()
	props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2}
	r.gpu.inst.id.GetPhysicalDeviceFormatProperties2(r.gpu.physical, formatXB30, &props)
	need := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit | formatFeatureTransferSrc)
	if props.FormatProperties.OptimalTilingFeatures&need != need {
		return errors.New("A2B10G10R10 lacks attachment/readback features")
	}
	d, t := r.gpu.dd, &p.out
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: formatXB30, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: targetUsage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(PQ capture)", d.CreateImage(r.gpu.device, &ii, nil, &t.image)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.gpu.device, t.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(PQ capture)", d.AllocateMemory(r.gpu.device, &alloc, nil, &t.memory)); err != nil {
		return err
	}
	if err := checked("vkBindImageMemory(PQ capture)", d.BindImageMemory(r.gpu.device, t.image, t.memory, 0)); err != nil {
		return err
	}
	if t.view, err = r.imageView(t.image, formatXB30); err != nil {
		return err
	}
	// The pipeline is last: it marks the pass complete.
	return r.createGraphicsPipeline(hdrVert, hdrFrag, formatXB30, r.hdr.layout, false, nil, &p.pipeline)
}

// destroyCapturePQ frees the pass (device idle). It runs before the display
// PQ pass whose layout it borrows is destroyed.
func (r *Renderer) destroyCapturePQ() {
	if r.gpu == nil || r.gpu.dd == nil {
		return
	}
	p := &r.capturePQ
	if p.pipeline != 0 {
		r.gpu.dd.DestroyPipeline(r.gpu.device, p.pipeline, nil)
	}
	r.freeTarget(&p.out)
	*p = capturePQPass{}
}

// recordCapturePQ encodes hdrOwn (TRANSFER_SRC after an HDR frame) to PQ in
// the private image, then copies that into the slot. hdrOwn is put back in
// TRANSFER_SRC, as the next frame expects.
func (r *Renderer) recordCapturePQ(cmd vk.CommandBuffer, s *captureSlot) {
	d, h, out, sc := r.gpu.dd, &r.hdr, &r.capturePQ.out, &r.capCall
	in, b := &sc.hdrIn, &sc.hdrB
	*in = vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessTransferReadBit, DstAccessMask: vk.AccessShaderReadBit, OldLayout: vk.ImageLayoutTransferSrcOptimal, NewLayout: vk.ImageLayoutShaderReadOnlyOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: r.hdrOwn.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageFragmentShaderBit, 0, 0, nil, 0, nil, 1, in)
	*b = vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessTransferReadBit, DstAccessMask: vk.AccessColorAttachmentWriteBit, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutColorAttachmentOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: out.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageColorAttachmentOutputBit, 0, 0, nil, 0, nil, 1, b)
	sc.attachment = vk.RenderingAttachmentInfo{SType: vk.StructureTypeRenderingAttachmentInfo, ImageView: out.view, ImageLayout: vk.ImageLayoutColorAttachmentOptimal, LoadOp: vk.AttachmentLoadOpDontCare, StoreOp: vk.AttachmentStoreOpStore}
	sc.info = vk.RenderingInfo{SType: vk.StructureTypeRenderingInfo, RenderArea: vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}, LayerCount: 1, ColorAttachmentCount: 1, ColorAttachments: &sc.attachment}
	d.CmdBeginRendering(cmd, &sc.info)
	d.CmdBindPipeline(cmd, vk.PipelineBindPointGraphics, r.capturePQ.pipeline)
	d.CmdBindDescriptorSets(cmd, vk.PipelineBindPointGraphics, h.layout, 0, 1, &h.set, 0, nil)
	// Push constants are copied at record time; the frame's field is free.
	r.call.hdrNits = float32(r.hdrNits)
	d.CmdPushConstants(cmd, h.layout, vk.ShaderStageFragmentBit, 0, 4, unsafe.Pointer(&r.call.hdrNits))
	d.CmdDraw(cmd, 3, 1, 0, 0)
	d.CmdEndRendering(cmd)
	in.SrcAccessMask, in.DstAccessMask = vk.AccessShaderReadBit, vk.AccessTransferReadBit
	in.OldLayout, in.NewLayout = vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(cmd, vk.PipelineStageFragmentShaderBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, in)
	b.SrcAccessMask, b.DstAccessMask = vk.AccessColorAttachmentWriteBit, vk.AccessTransferReadBit
	b.OldLayout, b.NewLayout = vk.ImageLayoutColorAttachmentOptimal, vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(cmd, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, b)
	r.recordCaptureCopy(cmd, out, vk.ImageLayoutTransferSrcOptimal, s)
}
