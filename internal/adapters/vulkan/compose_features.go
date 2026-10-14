package vulkan

import (
	"unsafe"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// VkPhysicalDeviceDescriptorIndexingFeatures is absent from the bindings.
// Its layout and sType are defined by Vulkan 1.2 (vulkan_core.h).
const descriptorIndexingFeaturesType vk.StructureType = 1000161001

type descriptorIndexingFeatures struct {
	SType                                              vk.StructureType
	Next                                               unsafe.Pointer
	ShaderInputAttachmentArrayDynamicIndexing          vk.Bool32
	ShaderUniformTexelBufferArrayDynamicIndexing       vk.Bool32
	ShaderStorageTexelBufferArrayDynamicIndexing       vk.Bool32
	ShaderUniformBufferArrayNonUniformIndexing         vk.Bool32
	ShaderSampledImageArrayNonUniformIndexing          vk.Bool32
	ShaderStorageBufferArrayNonUniformIndexing         vk.Bool32
	ShaderStorageImageArrayNonUniformIndexing          vk.Bool32
	ShaderInputAttachmentArrayNonUniformIndexing       vk.Bool32
	ShaderUniformTexelBufferArrayNonUniformIndexing    vk.Bool32
	ShaderStorageTexelBufferArrayNonUniformIndexing    vk.Bool32
	DescriptorBindingUniformBufferUpdateAfterBind      vk.Bool32
	DescriptorBindingSampledImageUpdateAfterBind       vk.Bool32
	DescriptorBindingStorageImageUpdateAfterBind       vk.Bool32
	DescriptorBindingStorageBufferUpdateAfterBind      vk.Bool32
	DescriptorBindingUniformTexelBufferUpdateAfterBind vk.Bool32
	DescriptorBindingStorageTexelBufferUpdateAfterBind vk.Bool32
	DescriptorBindingUpdateUnusedWhilePending          vk.Bool32
	DescriptorBindingPartiallyBound                    vk.Bool32
	DescriptorBindingVariableDescriptorCount           vk.Bool32
	RuntimeDescriptorArray                             vk.Bool32
}

// composeResourceLimit counts both image planes and the instance SSBO.
// No update-after-bind/partially-bound features are needed: tables are
// completely initialized and updated only after their frame-slot fence.
func composeResourceLimit(l vk.PhysicalDeviceLimits) uint32 {
	if l.MaxDescriptorSetStorageBuffers <= 1 || l.MaxPerStageResources == 0 {
		return 0
	}
	return min(uint32(maxComposeResources),
		l.MaxPerStageDescriptorSamplers/2, l.MaxDescriptorSetSamplers/2,
		l.MaxPerStageDescriptorSampledImages/2, l.MaxDescriptorSetSampledImages/2,
		l.MaxPerStageDescriptorStorageBuffers, l.MaxDescriptorSetStorageBuffers-1,
		(l.MaxPerStageResources-1)/3)
}
