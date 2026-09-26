package vulkan

import (
	_ "embed"
	"encoding/binary"
	"unsafe"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Translucent client pixels are blended over what is below them; opaque
// ones keep the plain copy. The blend pipeline draws one quad per upload
// and its fragment shader reads the staged pixels straight from the
// staging buffer: no texture, no sampler. Wayland buffers are
// premultiplied, hence ONE, ONE_MINUS_SRC_ALPHA.
//
// Limits: dmabuf buffers are still blitted, alpha ignored (blending them
// needs a sampled-image variant). The opaque region is not honoured, so
// an ARGB buffer blends even where it is opaque.

//go:generate glslc -O --target-env=vulkan1.3 shaders/blend.vert -o shaders/blend.vert.spv
//go:generate glslc -O --target-env=vulkan1.3 shaders/blend.frag -o shaders/blend.frag.spv

var (
	//go:embed shaders/blend.vert.spv
	blendVert []byte
	//go:embed shaders/blend.frag.spv
	blendFrag []byte
)

// drawSize is one entry of the draw table (shaders: struct Draw).
const drawSize = 32

// blendPipeline is the graphics state for blended uploads.
type blendPipeline struct {
	setLayout vk.DescriptorSetLayout
	layout    vk.PipelineLayout
	pipeline  vk.Pipeline
	pool      vk.DescriptorPool
	set       vk.DescriptorSet
}

func (r *Renderer) createBlend() error {
	d, b := r.dd, &r.blend
	bindings := []vk.DescriptorSetLayoutBinding{
		{Binding: 0, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit},
		{Binding: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit},
	}
	sli := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 2, Bindings: &bindings[0]}
	if err := checked("vkCreateDescriptorSetLayout", d.CreateDescriptorSetLayout(r.device, &sli, nil, &b.setLayout)); err != nil {
		return err
	}
	pli := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, SetLayouts: &b.setLayout}
	if err := checked("vkCreatePipelineLayout", d.CreatePipelineLayout(r.device, &pli, nil, &b.layout)); err != nil {
		return err
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: 2}
	dpi := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 1, PoolSizes: &size}
	if err := checked("vkCreateDescriptorPool", d.CreateDescriptorPool(r.device, &dpi, nil, &b.pool)); err != nil {
		return err
	}
	dai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: b.pool, DescriptorSetCount: 1, SetLayouts: &b.setLayout}
	if err := checked("vkAllocateDescriptorSets", d.AllocateDescriptorSets(r.device, &dai, &b.set)); err != nil {
		return err
	}
	vert, err := r.shaderModule(blendVert)
	if err != nil {
		return err
	}
	defer d.DestroyShaderModule(r.device, vert, nil)
	frag, err := r.shaderModule(blendFrag)
	if err != nil {
		return err
	}
	defer d.DestroyShaderModule(r.device, frag, nil)
	main := []byte("main\x00")
	stages := []vk.PipelineShaderStageCreateInfo{
		{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageVertexBit, Module: vert, Name: &main[0]},
		{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageFragmentBit, Module: frag, Name: &main[0]},
	}
	vertexInput := vk.PipelineVertexInputStateCreateInfo{SType: vk.StructureTypePipelineVertexInputStateCreateInfo}
	assembly := vk.PipelineInputAssemblyStateCreateInfo{SType: vk.StructureTypePipelineInputAssemblyStateCreateInfo, Topology: vk.PrimitiveTopologyTriangleList}
	// The target size is fixed for the renderer's life: static viewport.
	viewport := vk.Viewport{Width: float32(r.width), Height: float32(r.height), MaxDepth: 1}
	scissor := vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}
	viewportState := vk.PipelineViewportStateCreateInfo{SType: vk.StructureTypePipelineViewportStateCreateInfo, ViewportCount: 1, Viewports: &viewport, ScissorCount: 1, Scissors: &scissor}
	raster := vk.PipelineRasterizationStateCreateInfo{SType: vk.StructureTypePipelineRasterizationStateCreateInfo, PolygonMode: vk.PolygonModeFill, CullMode: vk.CullModeNone, FrontFace: vk.FrontFaceCounterClockwise, LineWidth: 1}
	multisample := vk.PipelineMultisampleStateCreateInfo{SType: vk.StructureTypePipelineMultisampleStateCreateInfo, RasterizationSamples: vk.SampleCount1Bit}
	attachment := vk.PipelineColorBlendAttachmentState{
		BlendEnable:         1,
		SrcColorBlendFactor: vk.BlendFactorOne, DstColorBlendFactor: vk.BlendFactorOneMinusSrcAlpha, ColorBlendOp: vk.BlendOpAdd,
		SrcAlphaBlendFactor: vk.BlendFactorOne, DstAlphaBlendFactor: vk.BlendFactorOneMinusSrcAlpha, AlphaBlendOp: vk.BlendOpAdd,
		ColorWriteMask: vk.ColorComponentRBit | vk.ColorComponentGBit | vk.ColorComponentBBit | vk.ColorComponentABit,
	}
	blendState := vk.PipelineColorBlendStateCreateInfo{SType: vk.StructureTypePipelineColorBlendStateCreateInfo, AttachmentCount: 1, Attachments: &attachment}
	format := vk.Format(vk.FormatB8g8r8a8Unorm)
	rendering := vk.PipelineRenderingCreateInfo{SType: vk.StructureTypePipelineRenderingCreateInfo, ColorAttachmentCount: 1, ColorAttachmentFormats: &format}
	gpi := vk.GraphicsPipelineCreateInfo{
		SType: vk.StructureTypeGraphicsPipelineCreateInfo, Next: unsafe.Pointer(&rendering),
		StageCount: 2, Stages: &stages[0], VertexInputState: &vertexInput, InputAssemblyState: &assembly,
		ViewportState: &viewportState, RasterizationState: &raster, MultisampleState: &multisample,
		ColorBlendState: &blendState, Layout: b.layout, BasePipelineIndex: -1,
	}
	return checked("vkCreateGraphicsPipelines", d.CreateGraphicsPipelines(r.device, 0, 1, &gpi, nil, &b.pipeline))
}

func (r *Renderer) shaderModule(code []byte) (vk.ShaderModule, error) {
	// SPIR-V is read as words: copy into a uint32-aligned slice.
	words := make([]uint32, len(code)/4)
	for i := range words {
		words[i] = binary.LittleEndian.Uint32(code[i*4:])
	}
	info := vk.ShaderModuleCreateInfo{SType: vk.StructureTypeShaderModuleCreateInfo, CodeSize: uintptr(len(words) * 4), Code: &words[0]}
	var m vk.ShaderModule
	return m, checked("vkCreateShaderModule", r.dd.CreateShaderModule(r.device, &info, nil, &m))
}

func (r *Renderer) destroyBlend() {
	d, b := r.dd, &r.blend
	if b.pipeline != 0 {
		d.DestroyPipeline(r.device, b.pipeline, nil)
	}
	if b.pool != 0 {
		d.DestroyDescriptorPool(r.device, b.pool, nil)
	}
	if b.layout != 0 {
		d.DestroyPipelineLayout(r.device, b.layout, nil)
	}
	if b.setLayout != 0 {
		d.DestroyDescriptorSetLayout(r.device, b.setLayout, nil)
	}
	*b = blendPipeline{}
}

// bindStaging points the set at the staging buffer: blended pixels in
// [0, tableOffset), the draw table from tableOffset.
func (r *Renderer) bindStaging(tableOffset int) {
	infos := []vk.DescriptorBufferInfo{
		{Buffer: r.staging, Range: vk.DeviceSize(max(tableOffset, 4))},
		{Buffer: r.staging, Offset: vk.DeviceSize(tableOffset), Range: wholeSize},
	}
	writes := []vk.WriteDescriptorSet{
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: r.blend.set, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, BufferInfo: &infos[0]},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: r.blend.set, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, BufferInfo: &infos[1]},
	}
	r.dd.UpdateDescriptorSets(r.device, 2, &writes[0], 0, nil)
}

// putDraw writes draw entry i of table.
func (r *Renderer) putDraw(table []byte, i int, u *upload) {
	e := table[i*drawSize : (i+1)*drawSize]
	for j, v := range []uint32{
		uint32(u.rect.Min.X), uint32(u.rect.Min.Y), uint32(u.rect.Max.X), uint32(u.rect.Max.Y),
		uint32(u.offset / 4), uint32(u.rect.Dx()), uint32(r.width), uint32(r.height),
	} {
		binary.LittleEndian.PutUint32(e[j*4:], v)
	}
}

// drawBlended draws uploads[first:last], all blended, into view: one
// instanced draw. The image is TRANSFER_DST before and after.
func (r *Renderer) drawBlended(image vk.Image, view vk.ImageView, first, last int) {
	d := r.dd
	rangeInfo := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectColorBit, LevelCount: 1, LayerCount: 1}
	b := vk.ImageMemoryBarrier{
		SType: vk.StructureTypeImageMemoryBarrier, Image: image, SubresourceRange: rangeInfo,
		SrcAccessMask: vk.AccessTransferWriteBit, DstAccessMask: vk.AccessColorAttachmentReadBit | vk.AccessColorAttachmentWriteBit,
		OldLayout: vk.ImageLayoutTransferDstOptimal, NewLayout: vk.ImageLayoutColorAttachmentOptimal,
	}
	d.CmdPipelineBarrier(r.command, vk.PipelineStageTransferBit, vk.PipelineStageColorAttachmentOutputBit, 0, 0, nil, 0, nil, 1, &b)
	attachment := vk.RenderingAttachmentInfo{SType: vk.StructureTypeRenderingAttachmentInfo, ImageView: view, ImageLayout: vk.ImageLayoutColorAttachmentOptimal, LoadOp: vk.AttachmentLoadOpLoad, StoreOp: vk.AttachmentStoreOpStore}
	info := vk.RenderingInfo{SType: vk.StructureTypeRenderingInfo, RenderArea: vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}, LayerCount: 1, ColorAttachmentCount: 1, ColorAttachments: &attachment}
	d.CmdBeginRendering(r.command, &info)
	d.CmdBindPipeline(r.command, vk.PipelineBindPointGraphics, r.blend.pipeline)
	d.CmdBindDescriptorSets(r.command, vk.PipelineBindPointGraphics, r.blend.layout, 0, 1, &r.blend.set, 0, nil)
	d.CmdDraw(r.command, 6, uint32(last-first), 0, uint32(first))
	d.CmdEndRendering(r.command)
	b.SrcAccessMask, b.DstAccessMask = vk.AccessColorAttachmentWriteBit, vk.AccessTransferWriteBit|vk.AccessTransferReadBit
	b.OldLayout, b.NewLayout = vk.ImageLayoutColorAttachmentOptimal, vk.ImageLayoutTransferDstOptimal
	d.CmdPipelineBarrier(r.command, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
}

// createView makes the color attachment view of a target image.
func (r *Renderer) createView(t *target) error {
	info := vk.ImageViewCreateInfo{
		SType: vk.StructureTypeImageViewCreateInfo, Image: t.image, ViewType: vk.ImageViewType2d, Format: vk.FormatB8g8r8a8Unorm,
		SubresourceRange: vk.ImageSubresourceRange{AspectMask: vk.ImageAspectColorBit, LevelCount: 1, LayerCount: 1},
	}
	return checked("vkCreateImageView", r.dd.CreateImageView(r.device, &info, nil, &t.view))
}
