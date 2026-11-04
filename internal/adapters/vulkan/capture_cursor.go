package vulkan

import (
	_ "embed"
	"errors"
	"unsafe"

	vk "bnema.dev/purego-vulkan/vulkan"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

//go:generate glslc -O --target-env=vulkan1.3 shaders/capture_cursor.frag -o shaders/capture_cursor.frag.spv
//go:embed shaders/capture_cursor.frag.spv
var captureCursorFrag []byte

// Every upload, descriptor and target belongs to a readback slot. Reuse waits
// for both its readback and outstanding CopyTo operations, never the display.
type captureCursorPass struct {
	pass        hdrPass
	source, out target
	upload      gpuBuffer
	format      vk.Format
	pq          bool
}

func (r *Renderer) BeginCaptureCursor(c *domain.CaptureCursor, pq bool) (ports.CaptureFrame, error) {
	if c == nil || c.Image.W == 0 || c.Image.H == 0 {
		if pq {
			return r.BeginCapturePQ()
		}
		return r.BeginCapture()
	}
	return r.beginCaptureCursor(false, pq, c)
}

func (r *Renderer) freeCaptureCursor(p *captureCursorPass) {
	d := r.gpu.dd
	if p.pass.pipeline != 0 {
		d.DestroyPipeline(r.gpu.device, p.pass.pipeline, nil)
	}
	if p.pass.pool != 0 {
		d.DestroyDescriptorPool(r.gpu.device, p.pass.pool, nil)
	}
	if p.pass.sampler != 0 {
		d.DestroySampler(r.gpu.device, p.pass.sampler, nil)
	}
	if p.pass.layout != 0 {
		d.DestroyPipelineLayout(r.gpu.device, p.pass.layout, nil)
	}
	if p.pass.setLayout != 0 {
		d.DestroyDescriptorSetLayout(r.gpu.device, p.pass.setLayout, nil)
	}
	r.freeGPUBuffer(&p.upload)
	r.freeTarget(&p.source)
	r.freeTarget(&p.out)
	*p = captureCursorPass{}
}

func (r *Renderer) captureCursorTarget(t *target, format vk.Format, usage vk.ImageUsageFlags) error {
	d := r.gpu.dd
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: usage, SharingMode: vk.SharingModeExclusive}
	if err := checked("vkCreateImage(capture cursor)", d.CreateImage(r.gpu.device, &ii, nil, &t.image)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.gpu.device, t.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return err
	}
	ai := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err = checked("vkAllocateMemory(capture cursor)", d.AllocateMemory(r.gpu.device, &ai, nil, &t.memory)); err != nil {
		return err
	}
	if err = checked("vkBindImageMemory(capture cursor)", d.BindImageMemory(r.gpu.device, t.image, t.memory, 0)); err != nil {
		return err
	}
	t.view, err = r.imageView(t.image, format)
	return err
}

func (r *Renderer) prepareCaptureCursor(s *captureSlot, c *domain.CaptureCursor, pq bool) (err error) {
	im := c.Image
	if im.W <= 0 || im.H <= 0 || im.W > 256 || im.H > 256 || len(im.Pixels) < im.W*im.H*4 {
		return errors.New("invalid capture cursor image")
	}
	p := &s.cursor
	format := vk.Format(vk.FormatB8g8r8a8Unorm)
	if r.last == &r.hdrOwn {
		format = vk.FormatR16g16b16a16Sfloat
	}
	if p.pass.pipeline != 0 && (p.format != format || p.pq != pq) {
		r.freeCaptureCursor(p)
	}
	if p.pass.pipeline == 0 {
		defer func() {
			if err != nil {
				r.freeCaptureCursor(p)
			}
		}()
		p.format, p.pq = format, pq
		if err = r.captureCursorTarget(&p.source, format, vk.ImageUsageTransferDstBit|vk.ImageUsageSampledBit); err != nil {
			return err
		}
		outFormat := vk.Format(vk.FormatB8g8r8a8Unorm)
		if pq {
			outFormat = formatXB30
		}
		if err = r.captureCursorTarget(&p.out, outFormat, targetUsage); err != nil {
			return err
		}
		if err = r.newGPUBuffer(&p.upload, 256*256*4); err != nil {
			return err
		}
		d, h := r.gpu.dd, &p.pass
		bindings := []vk.DescriptorSetLayoutBinding{{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit}, {Binding: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit}}
		sli := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 2, Bindings: &bindings[0]}
		if err = checked("vkCreateDescriptorSetLayout(capture cursor)", d.CreateDescriptorSetLayout(r.gpu.device, &sli, nil, &h.setLayout)); err != nil {
			return err
		}
		push := vk.PushConstantRange{StageFlags: vk.ShaderStageFragmentBit, Size: 24}
		pli := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, SetLayouts: &h.setLayout, PushConstantRangeCount: 1, PushConstantRanges: &push}
		if err = checked("vkCreatePipelineLayout(capture cursor)", d.CreatePipelineLayout(r.gpu.device, &pli, nil, &h.layout)); err != nil {
			return err
		}
		si := vk.SamplerCreateInfo{SType: vk.StructureTypeSamplerCreateInfo, MagFilter: vk.FilterNearest, MinFilter: vk.FilterNearest, AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge}
		if err = checked("vkCreateSampler(capture cursor)", d.CreateSampler(r.gpu.device, &si, nil, &h.sampler)); err != nil {
			return err
		}
		sizes := []vk.DescriptorPoolSize{{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1}, {Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1}}
		dpi := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 2, PoolSizes: &sizes[0]}
		if err = checked("vkCreateDescriptorPool(capture cursor)", d.CreateDescriptorPool(r.gpu.device, &dpi, nil, &h.pool)); err != nil {
			return err
		}
		dai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: h.pool, DescriptorSetCount: 1, SetLayouts: &h.setLayout}
		if err = checked("vkAllocateDescriptorSets(capture cursor)", d.AllocateDescriptorSets(r.gpu.device, &dai, &h.set)); err != nil {
			return err
		}
		img := vk.DescriptorImageInfo{Sampler: h.sampler, ImageView: p.source.view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
		buf := vk.DescriptorBufferInfo{Buffer: p.upload.buffer, Range: vk.DeviceSize(p.upload.size)}
		writes := []vk.WriteDescriptorSet{{SType: vk.StructureTypeWriteDescriptorSet, DstSet: h.set, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &img}, {SType: vk.StructureTypeWriteDescriptorSet, DstSet: h.set, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, BufferInfo: &buf}}
		d.UpdateDescriptorSets(r.gpu.device, 2, &writes[0], 0, nil)
		if err = r.createGraphicsPipeline(hdrVert, captureCursorFrag, outFormat, h.layout, false, nil, &h.pipeline); err != nil {
			return err
		}
	}
	copy(unsafe.Slice((*byte)(p.upload.mapped), p.upload.size), im.Pixels[:im.W*im.H*4])
	return nil
}

type captureCursorParams struct {
	Rect [4]int32
	Mode int32
	Nits float32
}

func (r *Renderer) recordCaptureCursor(cmd vk.CommandBuffer, s *captureSlot, c *domain.CaptureCursor, pq bool) {
	d, p, t := r.gpu.dd, &s.cursor, r.last
	sc := &r.capCall
	barrier := func(img vk.Image, old, new vk.ImageLayout, src, dst vk.AccessFlags, ss, ds vk.PipelineStageFlags) {
		sc.b = vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: src, DstAccessMask: dst, OldLayout: old, NewLayout: new, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: img, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, ss, ds, 0, 0, nil, 0, nil, 1, &sc.b)
	}
	if t.exported {
		sc.b = vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: t.layout, NewLayout: t.layout, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.gpu.family, Image: t.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageAllCommandsBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &sc.b)
	}
	barrier(p.source.image, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal, 0, vk.AccessTransferWriteBit, vk.PipelineStageTopOfPipeBit, vk.PipelineStageTransferBit)
	offsets := [2]vk.Offset3D{{}, {X: int32(r.width), Y: int32(r.height), Z: 1}}
	sc.blit = vk.ImageBlit{SrcSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, DstSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, SrcOffsets: offsets, DstOffsets: offsets}
	d.CmdBlitImage(cmd, t.image, t.layout, p.source.image, vk.ImageLayoutTransferDstOptimal, 1, &sc.blit, vk.FilterNearest)
	if t.exported {
		sc.b = vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessTransferReadBit, OldLayout: t.layout, NewLayout: t.layout, SrcQueueFamilyIndex: r.gpu.family, DstQueueFamilyIndex: vk.QueueFamilyForeignEXT, Image: t.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &sc.b)
	}
	barrier(p.source.image, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.AccessTransferWriteBit, vk.AccessShaderReadBit, vk.PipelineStageTransferBit, vk.PipelineStageFragmentShaderBit)
	sc.hb = vk.BufferMemoryBarrier{SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: vk.AccessHostWriteBit, DstAccessMask: vk.AccessShaderReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Buffer: p.upload.buffer, Size: wholeSize}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageHostBit, vk.PipelineStageFragmentShaderBit, 0, 0, nil, 1, &sc.hb, 0, nil)
	barrier(p.out.image, vk.ImageLayoutUndefined, vk.ImageLayoutColorAttachmentOptimal, 0, vk.AccessColorAttachmentWriteBit, vk.PipelineStageTopOfPipeBit, vk.PipelineStageColorAttachmentOutputBit)
	sc.attachment = vk.RenderingAttachmentInfo{SType: vk.StructureTypeRenderingAttachmentInfo, ImageView: p.out.view, ImageLayout: vk.ImageLayoutColorAttachmentOptimal, LoadOp: vk.AttachmentLoadOpDontCare, StoreOp: vk.AttachmentStoreOpStore}
	sc.info = vk.RenderingInfo{SType: vk.StructureTypeRenderingInfo, RenderArea: vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}, LayerCount: 1, ColorAttachmentCount: 1, ColorAttachments: &sc.attachment}
	d.CmdBeginRendering(cmd, &sc.info)
	d.CmdBindPipeline(cmd, vk.PipelineBindPointGraphics, p.pass.pipeline)
	d.CmdBindDescriptorSets(cmd, vk.PipelineBindPointGraphics, p.pass.layout, 0, 1, &p.pass.set, 0, nil)
	sc.cursorParams = captureCursorParams{Rect: [4]int32{int32(c.X - c.Image.HotX), int32(c.Y - c.Image.HotY), int32(c.Image.W), int32(c.Image.H)}, Nits: float32(r.hdrNits)}
	if t == &r.hdrOwn {
		sc.cursorParams.Mode = 1
	}
	if pq {
		sc.cursorParams.Mode = 2
	}
	d.CmdPushConstants(cmd, p.pass.layout, vk.ShaderStageFragmentBit, 0, 24, unsafe.Pointer(&sc.cursorParams))
	d.CmdDraw(cmd, 3, 1, 0, 0)
	d.CmdEndRendering(cmd)
	barrier(p.out.image, vk.ImageLayoutColorAttachmentOptimal, vk.ImageLayoutTransferSrcOptimal, vk.AccessColorAttachmentWriteBit, vk.AccessTransferReadBit, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageTransferBit)
	r.recordCaptureCopy(cmd, &p.out, vk.ImageLayoutTransferSrcOptimal, s)
}
