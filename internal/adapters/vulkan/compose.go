package vulkan

import (
	_ "embed"
	"encoding/binary"
	"image"
	"math"
	"os"
	"unsafe"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Composition is one graphics pipeline drawing every piece of a frame as
// a quad, in paint order, inside one dynamic rendering pass: solid fills,
// client dmabufs sampled as images, and client wl_shm pixels read from a
// GPU buffer (shm.go). Push constants carry each draw; the descriptor set
// holds its image and buffer. Client pixels are premultiplied, hence the
// blend ONE, ONE_MINUS_SRC_ALPHA; opaque draws force alpha to 1.

//go:generate glslc -O --target-env=vulkan1.3 shaders/compose.vert -o shaders/compose.vert.spv
//go:generate glslc -O --target-env=vulkan1.3 shaders/compose.frag -o shaders/compose.frag.spv
//go:generate glslc -O --target-env=vulkan1.3 shaders/compose_hdr.frag -o shaders/compose_hdr.frag.spv
//go:generate glslc -O --target-env=vulkan1.3 shaders/hdr.vert -o shaders/hdr.vert.spv
//go:generate glslc -O --target-env=vulkan1.3 shaders/hdr.frag -o shaders/hdr.frag.spv

var (
	//go:embed shaders/compose.vert.spv
	composeVert []byte
	//go:embed shaders/compose.frag.spv
	composeFrag []byte
	//go:embed shaders/compose_hdr.frag.spv
	composeHDRFrag []byte
)

// Draw modes and flags (shaders: modeSolid, modeImage, flag*).
const (
	modeSolid  = 0
	modeImage  = 1
	modeBuffer = 2

	flagOpaque         = 1
	flagExact          = 2
	flagPQ             = 4
	flagExtendedLinear = 8
	flagYUV            = 16
	flagP010           = 32
)

// pushConstants is struct Draw of the shaders (std430 push constant block).
type pushConstants struct {
	rect  [4]int32
	mapv  [4]float32
	color [4]float32
	crop  [4]float32
	buf   [4]uint32
	misc  [4]uint32
}

// draw is one quad of a frame.
type draw struct {
	pc  pushConstants
	set vk.DescriptorSet
	im  *imported // sampled client dmabuf, for ownership barriers
	// acquire is the content's explicit-sync fence (nil: implicit).
	acquire *os.File
}

// composer is the pipeline state and the objects every draw can bind.
type composer struct {
	setLayout   vk.DescriptorSetLayout
	layout      vk.PipelineLayout
	pipeline    vk.Pipeline
	hdrPipeline vk.Pipeline
	sampler     vk.Sampler
	// Placeholders for the binding a draw does not use: every binding
	// the shader declares must be valid.
	dummyImage  vk.Image
	dummyMemory vk.DeviceMemory
	dummyView   vk.ImageView
	dummyBuffer gpuBuffer
	dummyPool   vk.DescriptorPool
	dummySet    vk.DescriptorSet
}

func (r *Renderer) createComposer() error {
	d, c := r.dd, &r.compose
	bindings := []vk.DescriptorSetLayoutBinding{
		{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit},
		{Binding: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit},
		{Binding: 2, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit},
	}
	sli := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: uint32(len(bindings)), Bindings: &bindings[0]}
	if err := checked("vkCreateDescriptorSetLayout", d.CreateDescriptorSetLayout(r.device, &sli, nil, &c.setLayout)); err != nil {
		return err
	}
	pcr := vk.PushConstantRange{StageFlags: vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit, Size: uint32(unsafe.Sizeof(pushConstants{}))}
	pli := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, SetLayouts: &c.setLayout, PushConstantRangeCount: 1, PushConstantRanges: &pcr}
	if err := checked("vkCreatePipelineLayout", d.CreatePipelineLayout(r.device, &pli, nil, &c.layout)); err != nil {
		return err
	}
	si := vk.SamplerCreateInfo{SType: vk.StructureTypeSamplerCreateInfo, MagFilter: vk.FilterLinear, MinFilter: vk.FilterLinear, AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge}
	if err := checked("vkCreateSampler", d.CreateSampler(r.device, &si, nil, &c.sampler)); err != nil {
		return err
	}
	if err := r.createPipeline(); err != nil {
		return err
	}
	return r.createDummies()
}

func (r *Renderer) createPipeline() error {
	if err := r.createGraphicsPipeline(composeVert, composeFrag, vk.FormatB8g8r8a8Unorm, r.compose.layout, true, &r.compose.pipeline); err != nil {
		return err
	}
	return nil
}

func (r *Renderer) createGraphicsPipeline(vertex, fragment []byte, format vk.Format, layout vk.PipelineLayout, blend bool, pipeline *vk.Pipeline) error {
	d := r.dd
	vert, err := r.shaderModule(vertex)
	if err != nil {
		return err
	}
	defer d.DestroyShaderModule(r.device, vert, nil)
	frag, err := r.shaderModule(fragment)
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
	if !blend {
		attachment.BlendEnable = 0
	}
	blendState := vk.PipelineColorBlendStateCreateInfo{SType: vk.StructureTypePipelineColorBlendStateCreateInfo, AttachmentCount: 1, Attachments: &attachment}
	rendering := vk.PipelineRenderingCreateInfo{SType: vk.StructureTypePipelineRenderingCreateInfo, ColorAttachmentCount: 1, ColorAttachmentFormats: &format}
	gpi := vk.GraphicsPipelineCreateInfo{
		SType: vk.StructureTypeGraphicsPipelineCreateInfo, Next: unsafe.Pointer(&rendering),
		StageCount: 2, Stages: &stages[0], VertexInputState: &vertexInput, InputAssemblyState: &assembly,
		ViewportState: &viewportState, RasterizationState: &raster, MultisampleState: &multisample,
		ColorBlendState: &blendState, Layout: layout, BasePipelineIndex: -1,
	}
	return checked("vkCreateGraphicsPipelines", d.CreateGraphicsPipelines(r.device, 0, 1, &gpi, nil, pipeline))
}

// createDummies makes the placeholder image (1×1, shader-read layout) and
// buffer, and the set fills bind.
func (r *Renderer) createDummies() error {
	d, c := r.dd, &r.compose
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: vk.Extent3D{Width: 1, Height: 1, Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: vk.ImageUsageSampledBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(dummy)", d.CreateImage(r.device, &ii, nil, &c.dummyImage)); err != nil {
		return err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.device, c.dummyImage, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return err
	}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(dummy)", d.AllocateMemory(r.device, &alloc, nil, &c.dummyMemory)); err != nil {
		return err
	}
	if err := checked("vkBindImageMemory(dummy)", d.BindImageMemory(r.device, c.dummyImage, c.dummyMemory, 0)); err != nil {
		return err
	}
	if c.dummyView, err = r.imageView(c.dummyImage, vk.FormatB8g8r8a8Unorm); err != nil {
		return err
	}
	if err := r.newGPUBuffer(&c.dummyBuffer, 16); err != nil {
		return err
	}
	if c.dummyPool, c.dummySet, err = r.newSet(c.dummyView, c.dummyBuffer.buffer); err != nil {
		return err
	}
	// Move the placeholder image to the layout every draw declares.
	return r.oneShot(func(cmd vk.CommandBuffer) {
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutShaderReadOnlyOptimal, DstAccessMask: vk.AccessShaderReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: c.dummyImage, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTopOfPipeBit, vk.PipelineStageFragmentShaderBit, 0, 0, nil, 0, nil, 1, &b)
	})
}

// newSet allocates a descriptor set from its own pool (the bindings lack
// vkFreeDescriptorSets: the pool is destroyed with the object).
func (r *Renderer) newSet(view vk.ImageView, buffer vk.Buffer, chroma ...vk.ImageView) (vk.DescriptorPool, vk.DescriptorSet, error) {
	d := r.dd
	sizes := []vk.DescriptorPoolSize{{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 2}, {Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1}}
	dpi := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 2, PoolSizes: &sizes[0]}
	var pool vk.DescriptorPool
	if err := checked("vkCreateDescriptorPool", d.CreateDescriptorPool(r.device, &dpi, nil, &pool)); err != nil {
		return 0, 0, err
	}
	var set vk.DescriptorSet
	dai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: pool, DescriptorSetCount: 1, SetLayouts: &r.compose.setLayout}
	if err := checked("vkAllocateDescriptorSets", d.AllocateDescriptorSets(r.device, &dai, &set)); err != nil {
		d.DestroyDescriptorPool(r.device, pool, nil)
		return 0, 0, err
	}
	img := vk.DescriptorImageInfo{Sampler: r.compose.sampler, ImageView: view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	uv := img
	if len(chroma) > 0 {
		uv.ImageView = chroma[0]
	}
	buf := vk.DescriptorBufferInfo{Buffer: buffer, Range: wholeSize}
	writes := []vk.WriteDescriptorSet{
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: set, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &img},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: set, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, BufferInfo: &buf},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: set, DstBinding: 2, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &uv},
	}
	d.UpdateDescriptorSets(r.device, uint32(len(writes)), &writes[0], 0, nil)
	return pool, set, nil
}

func (r *Renderer) destroyComposer() {
	d, c := r.dd, &r.compose
	if c.dummyPool != 0 {
		d.DestroyDescriptorPool(r.device, c.dummyPool, nil)
	}
	r.freeGPUBuffer(&c.dummyBuffer)
	if c.dummyView != 0 {
		d.DestroyImageView(r.device, c.dummyView, nil)
	}
	if c.dummyImage != 0 {
		d.DestroyImage(r.device, c.dummyImage, nil)
	}
	if c.dummyMemory != 0 {
		d.FreeMemory(r.device, c.dummyMemory, nil)
	}
	if c.sampler != 0 {
		d.DestroySampler(r.device, c.sampler, nil)
	}
	if c.pipeline != 0 {
		d.DestroyPipeline(r.device, c.pipeline, nil)
	}
	if c.hdrPipeline != 0 {
		d.DestroyPipeline(r.device, c.hdrPipeline, nil)
	}
	if c.layout != 0 {
		d.DestroyPipelineLayout(r.device, c.layout, nil)
	}
	if c.setLayout != 0 {
		d.DestroyDescriptorSetLayout(r.device, c.setLayout, nil)
	}
	*c = composer{}
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

var colorRange = vk.ImageSubresourceRange{AspectMask: vk.ImageAspectColorBit, LevelCount: 1, LayerCount: 1}

// imageView makes a 2D color view of an image.
func (r *Renderer) imageView(img vk.Image, format vk.Format) (vk.ImageView, error) {
	info := vk.ImageViewCreateInfo{SType: vk.StructureTypeImageViewCreateInfo, Image: img, ViewType: vk.ImageViewType2d, Format: format, SubresourceRange: colorRange}
	var v vk.ImageView
	return v, checked("vkCreateImageView", r.dd.CreateImageView(r.device, &info, nil, &v))
}

// createView makes the color attachment view of a target image.
func (r *Renderer) createView(t *target) (err error) {
	t.view, err = r.imageView(t.image, vk.FormatB8g8r8a8Unorm)
	return err
}

// fillDraw is a solid rect of c.
func (r *Renderer) fillDraw(rect image.Rectangle, c [3]uint8) draw {
	dr := draw{set: r.compose.dummySet}
	dr.pc.rect = [4]int32{int32(rect.Min.X), int32(rect.Min.Y), int32(rect.Max.X), int32(rect.Max.Y)}
	dr.pc.color = [4]float32{float32(c[0]) / 255, float32(c[1]) / 255, float32(c[2]) / 255, 1}
	dr.pc.misc = [4]uint32{modeSolid, 0, uint32(r.width), uint32(r.height)}
	return dr
}

// contentDraw draws the visible rect of a w×h buffer mapped onto full
// (target pixels). exact: the buffer is drawn at its size.
func (r *Renderer) contentDraw(rect, full image.Rectangle, w, h int, source [4]float32, mode uint32, opaque bool) draw {
	var dr draw
	if source[2] <= 0 {
		source = [4]float32{0, 0, float32(w), float32(h)}
	}
	sx, sy := source[2]/float32(full.Dx()), source[3]/float32(full.Dy())
	dr.pc.rect = [4]int32{int32(rect.Min.X), int32(rect.Min.Y), int32(rect.Max.X), int32(rect.Max.Y)}
	dr.pc.mapv = [4]float32{source[0] - float32(full.Min.X)*sx, source[1] - float32(full.Min.Y)*sy, sx, sy}
	dr.pc.crop = [4]float32{source[0], source[1], source[0] + source[2], source[1] + source[3]}
	flags := uint32(0)
	if opaque {
		flags |= flagOpaque
	}
	if full.Dx() == int(source[2]) && full.Dy() == int(source[3]) && source[2] == float32(int(source[2])) && source[3] == float32(int(source[3])) {
		flags |= flagExact
	}
	dr.pc.misc = [4]uint32{mode, flags, uint32(r.width), uint32(r.height)}
	return dr
}

// recordDraws draws ds in one rendering pass that clears view to bg, or
// keeps its contents (keep).
func (r *Renderer) recordDraws(cmd vk.CommandBuffer, view vk.ImageView, bg [3]uint8, ds []draw, keep bool) {
	d, c := r.dd, &r.compose
	clear := vk.ClearValue{math.Float32bits(float32(bg[0]) / 255), math.Float32bits(float32(bg[1]) / 255), math.Float32bits(float32(bg[2]) / 255), math.Float32bits(1)}
	if r.hdrNits > 0 {
		for i := 0; i < 3; i++ {
			clear[i] = math.Float32bits(float32(srgbToLinear(float64(bg[i]) / 255)))
		}
	}
	attachment := vk.RenderingAttachmentInfo{SType: vk.StructureTypeRenderingAttachmentInfo, ImageView: view, ImageLayout: vk.ImageLayoutColorAttachmentOptimal, LoadOp: vk.AttachmentLoadOpClear, StoreOp: vk.AttachmentStoreOpStore, ClearValue: clear}
	if keep {
		attachment.LoadOp = vk.AttachmentLoadOpLoad
	}
	info := vk.RenderingInfo{SType: vk.StructureTypeRenderingInfo, RenderArea: vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}, LayerCount: 1, ColorAttachmentCount: 1, ColorAttachments: &attachment}
	d.CmdBeginRendering(cmd, &info)
	pipeline := c.pipeline
	if r.hdrNits > 0 {
		pipeline = c.hdrPipeline
	}
	d.CmdBindPipeline(cmd, vk.PipelineBindPointGraphics, pipeline)
	var bound vk.DescriptorSet
	for i := range ds {
		dr := &ds[i]
		if dr.set != bound {
			d.CmdBindDescriptorSets(cmd, vk.PipelineBindPointGraphics, c.layout, 0, 1, &dr.set, 0, nil)
			bound = dr.set
		}
		d.CmdPushConstants(cmd, c.layout, vk.ShaderStageVertexBit|vk.ShaderStageFragmentBit, 0, uint32(unsafe.Sizeof(dr.pc)), unsafe.Pointer(&dr.pc))
		d.CmdDraw(cmd, 6, 1, 0, 0)
	}
	d.CmdEndRendering(cmd)
}
