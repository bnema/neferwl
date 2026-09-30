package vulkan

import (
	_ "embed"
	"fmt"
	"unsafe"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

var (
	//go:embed shaders/hdr.vert.spv
	hdrVert []byte
	//go:embed shaders/hdr.frag.spv
	hdrFrag []byte
)

type hdrPass struct {
	setLayout vk.DescriptorSetLayout
	layout    vk.PipelineLayout
	pipeline  vk.Pipeline
	sampler   vk.Sampler
	pool      vk.DescriptorPool
	set       vk.DescriptorSet
}

// SetHDR prepares the output transform before ExportTargets. The renderer
// remains owner-goroutine confined; all GPU objects are reused each frame.
func (r *Renderer) SetHDR(nits float64) {
	if nits != r.hdrNits {
		r.cursorCache = cursorConversion{}
		if r.hdrNits > 0 && nits > 0 {
			r.hdrOwn.valid = false
		}
		if (nits > 0) != (r.hdrNits > 0) && r.device != 0 {
			_ = checked("vkDeviceWaitIdle", r.dd.DeviceWaitIdle(r.device))
			r.idle()
			r.hdrOwn.valid = false
			r.own.valid = false
			for _, t := range r.targets {
				t.valid = false
			}
			if r.last == &r.hdrOwn {
				r.last = nil
			}
		}
	}
	r.hdrNits = nits
	if nits <= 0 {
		r.cursorCache = cursorConversion{}
		r.hdrError = nil
		return
	}
	if r.hdrOwn.image == 0 {
		r.hdrError = r.createHDROwn()
		if r.hdrError != nil {
			r.dropHDROwn()
			return
		}
	}
	if r.compose.hdrPipeline == 0 {
		r.hdrError = r.createGraphicsPipeline(composeVert, composeHDRFrag, vk.FormatR16g16b16a16Sfloat, r.compose.layout, true, &r.compose.hdrPipeline)
		if r.hdrError != nil {
			r.dropHDROwn()
			return
		}
	}
	if r.hdr.pipeline != 0 {
		return
	}
	// The port has no error return: ExportTargets fails if preparation fails.
	r.hdrError = r.createHDR()
	if r.hdrError != nil {
		r.destroyHDR()
		r.dropHDROwn()
	}
}

// createHDROwn allocates the fp16 linear-light composition image once per
// renderer; toggling HDR reuses it after the previous frames complete.
func (r *Renderer) createHDROwn() error {
	// An HDR compositor needs an fp16 blendable color attachment that can
	// also be sampled by the PQ pass and copied only for capture.
	if r.physical == 0 {
		return fmt.Errorf("HDR fp16 device unavailable")
	}
	props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2}
	r.id.GetPhysicalDeviceFormatProperties2(r.physical, vk.FormatR16g16b16a16Sfloat, &props)
	need := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit | vk.FormatFeatureColorAttachmentBlendBit | vk.FormatFeatureSampledImageBit | formatFeatureTransferSrc)
	if props.FormatProperties.OptimalTilingFeatures&need != need {
		return fmt.Errorf("HDR fp16 composition format lacks attachment/blend/sample/readback features")
	}
	d, t := r.dd, &r.hdrOwn
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatR16g16b16a16Sfloat, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: targetUsage | vk.ImageUsageSampledBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(HDR composition)", d.CreateImage(r.device, &ii, nil, &t.image)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.device, t.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(HDR composition)", d.AllocateMemory(r.device, &alloc, nil, &t.memory)); err != nil {
		return err
	}
	if err := checked("vkBindImageMemory(HDR composition)", d.BindImageMemory(r.device, t.image, t.memory, 0)); err != nil {
		return err
	}
	t.view, err = r.imageView(t.image, vk.FormatR16g16b16a16Sfloat)
	return err
}

func (r *Renderer) createHDR() error {
	d, h := r.dd, &r.hdr
	binding := vk.DescriptorSetLayoutBinding{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit}
	sli := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 1, Bindings: &binding}
	if err := checked("vkCreateDescriptorSetLayout(HDR)", d.CreateDescriptorSetLayout(r.device, &sli, nil, &h.setLayout)); err != nil {
		return err
	}
	push := vk.PushConstantRange{StageFlags: vk.ShaderStageFragmentBit, Size: 4}
	pli := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, SetLayouts: &h.setLayout, PushConstantRangeCount: 1, PushConstantRanges: &push}
	if err := checked("vkCreatePipelineLayout(HDR)", d.CreatePipelineLayout(r.device, &pli, nil, &h.layout)); err != nil {
		return err
	}
	if err := r.createGraphicsPipeline(hdrVert, hdrFrag, vk.FormatA2r10g10b10UnormPack32, h.layout, false, &h.pipeline); err != nil {
		return err
	}
	si := vk.SamplerCreateInfo{SType: vk.StructureTypeSamplerCreateInfo, MagFilter: vk.FilterNearest, MinFilter: vk.FilterNearest, AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge}
	if err := checked("vkCreateSampler(HDR)", d.CreateSampler(r.device, &si, nil, &h.sampler)); err != nil {
		return err
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1}
	dpi := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 1, PoolSizes: &size}
	if err := checked("vkCreateDescriptorPool(HDR)", d.CreateDescriptorPool(r.device, &dpi, nil, &h.pool)); err != nil {
		return err
	}
	dai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: h.pool, DescriptorSetCount: 1, SetLayouts: &h.setLayout}
	if err := checked("vkAllocateDescriptorSets(HDR)", d.AllocateDescriptorSets(r.device, &dai, &h.set)); err != nil {
		return err
	}
	img := vk.DescriptorImageInfo{Sampler: h.sampler, ImageView: r.hdrOwn.view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	write := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: h.set, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &img}
	d.UpdateDescriptorSets(r.device, 1, &write, 0, nil)
	return nil
}

func (r *Renderer) destroyHDR() {
	if r.dd == nil {
		return
	}
	d, h := r.dd, &r.hdr
	if h.pool != 0 {
		d.DestroyDescriptorPool(r.device, h.pool, nil)
	}
	if h.sampler != 0 {
		d.DestroySampler(r.device, h.sampler, nil)
	}
	if h.pipeline != 0 {
		d.DestroyPipeline(r.device, h.pipeline, nil)
	}
	if h.layout != 0 {
		d.DestroyPipelineLayout(r.device, h.layout, nil)
	}
	if h.setLayout != 0 {
		d.DestroyDescriptorSetLayout(r.device, h.setLayout, nil)
	}
	*h = hdrPass{}
}

// recordHDR converts the fully composed linear HDR image in the same command buffer.
func (r *Renderer) recordHDR(cmd vk.CommandBuffer, out *target) {
	d, h := r.dd, &r.hdr
	in := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessColorAttachmentWriteBit, DstAccessMask: vk.AccessShaderReadBit, OldLayout: vk.ImageLayoutColorAttachmentOptimal, NewLayout: vk.ImageLayoutShaderReadOnlyOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: r.hdrOwn.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageFragmentShaderBit, 0, 0, nil, 0, nil, 1, &in)
	b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessColorAttachmentWriteBit, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutColorAttachmentOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.family, Image: out.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTopOfPipeBit, vk.PipelineStageColorAttachmentOutputBit, 0, 0, nil, 0, nil, 1, &b)
	attachment := vk.RenderingAttachmentInfo{SType: vk.StructureTypeRenderingAttachmentInfo, ImageView: out.view, ImageLayout: vk.ImageLayoutColorAttachmentOptimal, LoadOp: vk.AttachmentLoadOpDontCare, StoreOp: vk.AttachmentStoreOpStore}
	info := vk.RenderingInfo{SType: vk.StructureTypeRenderingInfo, RenderArea: vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}, LayerCount: 1, ColorAttachmentCount: 1, ColorAttachments: &attachment}
	d.CmdBeginRendering(cmd, &info)
	d.CmdBindPipeline(cmd, vk.PipelineBindPointGraphics, h.pipeline)
	d.CmdBindDescriptorSets(cmd, vk.PipelineBindPointGraphics, h.layout, 0, 1, &h.set, 0, nil)
	nits := float32(r.hdrNits)
	d.CmdPushConstants(cmd, h.layout, vk.ShaderStageFragmentBit, 0, 4, unsafe.Pointer(&nits))
	d.CmdDraw(cmd, 3, 1, 0, 0)
	d.CmdEndRendering(cmd)
	b.SrcAccessMask, b.DstAccessMask = vk.AccessColorAttachmentWriteBit, 0
	b.OldLayout, b.NewLayout = vk.ImageLayoutColorAttachmentOptimal, vk.ImageLayoutGeneral
	b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
	d.CmdPipelineBarrier(cmd, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
	in.SrcAccessMask, in.DstAccessMask = vk.AccessShaderReadBit, vk.AccessTransferReadBit
	in.OldLayout, in.NewLayout = vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(cmd, vk.PipelineStageFragmentShaderBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &in)
}

// dropHDROwn frees the HDR composition image with the capture pass that
// samples it (its descriptor set refers to the view). Pending captures
// finish first: this runs on SetHDR failures, never per frame.
func (r *Renderer) dropHDROwn() {
	if r.capturePass.pipeline != 0 {
		for _, s := range r.captures {
			if s.pending {
				_ = checked("vkDeviceWaitIdle", r.dd.DeviceWaitIdle(r.device))
				r.idle()
				break
			}
		}
		r.destroyCapturePass()
	}
	r.freeTarget(&r.hdrOwn)
}
