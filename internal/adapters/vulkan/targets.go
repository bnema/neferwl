package vulkan

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Render targets (ADR 014). A DRM output scans out images the renderer
// draws into: ExportTargets allocates them with a DRM format modifier the
// display accepts and exports their memory as dmabufs, which the output
// turns into KMS framebuffers. Render then composes straight into the
// selected target and the output flips to it; nothing is copied by the
// CPU. In HDR the internal image retains the SDR scene, sampled by a
// full-screen GPU pass into the 10-bit target. Pixels reads internal SDR.

// target is an image frames are drawn into.
type target struct {
	image  vk.Image
	memory vk.DeviceMemory
	layout vk.ImageLayout
	view   vk.ImageView // color attachment for blended draws
	// exported images are shared with the display: ownership goes to the
	// foreign queue family after each frame, and KMS reads them.
	exported bool
	// What the image holds (damage.go): the scene Seq and each window's
	// content Seq; valid once a frame was drawn whole.
	valid    bool
	sceneSeq uint64
	windows  map[ports.WindowID]heldWindow
}

// fourccXRGB is DRM_FORMAT_XRGB8888: B8G8R8A8 in memory, alpha ignored.
const fourccXRGB = 'X' | 'R'<<8 | '2'<<16 | '4'<<24

// fourccXR30 is DRM_FORMAT_XRGB2101010 (A2R10G10B10 packed).
const fourccXR30 = 'X' | 'R'<<8 | '3'<<16 | '0'<<24

// ExportTargets allocates n exported images of the output size, replacing
// the previous ones; n = 0 only drops them (back to the internal image).
func (r *Renderer) ExportTargets(n int, modifiers []uint64) ([]ports.DMABuf, error) {
	r.dropTargets()
	if n > 0 && r.hdrNits > 0 && r.hdr.pipeline == 0 {
		return nil, fmt.Errorf("HDR transform unavailable: %w", r.hdrError)
	}
	if n == 0 {
		return nil, nil
	}
	if r.dd.GetMemoryFdKHR == nil || r.dd.GetImageDrmFormatModifierPropertiesEXT == nil || len(r.dmabuf.Formats) == 0 {
		return nil, errors.New("device cannot export dmabufs")
	}
	mods := r.exportModifiers(modifiers)
	if len(mods) == 0 {
		if r.hdrNits == 0 {
			return nil, errors.New("no XRGB8888 modifier both the device and the display accept")
		}
		return nil, errors.New("no XRGB2101010 modifier both the device and the display accept")
	}
	var out []ports.DMABuf
	for range n {
		t, buf, err := r.exportTarget(mods)
		if err != nil {
			r.dropTargets()
			for _, b := range out {
				for _, p := range b.Planes {
					p.File.Close()
				}
			}
			return nil, err
		}
		r.targets = append(r.targets, t)
		out = append(out, buf)
	}
	r.current = 0
	return out, nil
}

// UseTarget selects the exported image the next frames draw into.
func (r *Renderer) UseTarget(i int) {
	if i >= 0 && i < len(r.targets) {
		r.current = i
	}
}

// target is the image the next frame draws into.
func (r *Renderer) target() *target {
	if len(r.targets) > 0 {
		return r.targets[r.current]
	}
	return &r.own
}

// exportModifiers intersects the selected signal format modifiers with
// those supported by the display. HDR never assumes an unspecified list.
func (r *Renderer) exportModifiers(display []uint64) []uint64 {
	var out []uint64
	available := r.renderMods
	if r.hdrNits > 0 {
		available = r.hdrMods
	}
	for _, m := range available {
		if (len(display) == 0 && r.hdrNits == 0) || slices.Contains(display, m) {
			out = append(out, m)
		}
	}
	return out
}

// probeRenderModifiers lists single-plane, exportable SDR and HDR targets.
func (r *Renderer) probeRenderModifiers(physical vk.PhysicalDevice) {
	r.renderMods = r.probeModifiers(physical, vk.FormatB8g8r8a8Unorm)
	r.hdrMods = r.probeModifiers(physical, vk.FormatA2r10g10b10UnormPack32)
}

func (r *Renderer) probeModifiers(physical vk.PhysicalDevice, format vk.Format) []uint64 {
	list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
	fp := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
	r.id.GetPhysicalDeviceFormatProperties2(physical, format, &fp)
	if list.DrmFormatModifierCount == 0 {
		return nil
	}
	mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
	list.DrmFormatModifierProperties = &mods[0]
	r.id.GetPhysicalDeviceFormatProperties2(physical, format, &fp)
	need := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit)
	if format == vk.FormatB8g8r8a8Unorm {
		need |= formatFeatureTransferSrc | vk.FormatFeatureColorAttachmentBlendBit
	} else if r.hdrReadback {
		need |= formatFeatureTransferSrc
	}
	var result []uint64
	// SDR targets are composed and read back; HDR targets are color attachments.
	for _, m := range mods[:list.DrmFormatModifierCount] {
		if m.DrmFormatModifierPlaneCount == 1 && m.DrmFormatModifierTilingFeatures&need == need && r.exportable(physical, format, m.DrmFormatModifier) {
			result = append(result, m.DrmFormatModifier)
		}
	}
	return result
}

func (r *Renderer) exportable(physical vk.PhysicalDevice, format vk.Format, modifier uint64) bool {
	mod := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{SType: vk.StructureTypePhysicalDeviceImageDRMFormatModifierInfoEXT, DrmFormatModifier: modifier, SharingMode: vk.SharingModeExclusive}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, Next: unsafe.Pointer(&mod), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: exportUsage(format, r.hdrReadback)}
	extOut := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&extOut)}
	if r.id.GetPhysicalDeviceImageFormatProperties2(physical, &info, &out) != vk.Success {
		return false
	}
	return extOut.ExternalMemoryProperties.ExternalMemoryFeatures&vk.ExternalMemoryFeatureExportableBit != 0
}

// VK_FORMAT_FEATURE_TRANSFER_SRC_BIT (Vulkan 1.1), missing from the
// generated bindings.
const formatFeatureTransferSrc = 1 << 14

// Exported SDR targets compose directly; HDR targets receive the final pass.
const targetUsage = vk.ImageUsageTransferSrcBit | vk.ImageUsageColorAttachmentBit

// Production HDR targets need only a color attachment; test readback opts
// into transfer source before export and skips if the driver refuses it.
func exportUsage(format vk.Format, readback bool) vk.ImageUsageFlags {
	if format == vk.FormatA2r10g10b10UnormPack32 && !readback {
		return vk.ImageUsageColorAttachmentBit
	}
	return targetUsage
}

// exportTarget creates one exported image with a modifier from mods.
func (r *Renderer) exportTarget(mods []uint64) (*target, ports.DMABuf, error) {
	d := r.dd
	t := &target{exported: true, layout: vk.ImageLayoutUndefined}
	format, fourcc := vk.Format(vk.FormatB8g8r8a8Unorm), uint32(fourccXRGB)
	if r.hdrNits > 0 {
		format, fourcc = vk.FormatA2r10g10b10UnormPack32, fourccXR30
	}
	ok := false
	defer func() {
		if !ok {
			r.freeTarget(t)
		}
	}()
	list := vk.ImageDrmFormatModifierListCreateInfoEXT{SType: vk.StructureTypeImageDRMFormatModifierListCreateInfoEXT, DrmFormatModifierCount: uint32(len(mods)), DrmFormatModifiers: &mods[0]}
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, Next: unsafe.Pointer(&list), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: exportUsage(format, r.hdrReadback), SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(target)", d.CreateImage(r.device, &ii, nil, &t.image)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.device, t.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return nil, ports.DMABuf{}, err
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.image}
	export := vk.ExportMemoryAllocateInfo{SType: vk.StructureTypeExportMemoryAllocateInfo, Next: unsafe.Pointer(&dedicated), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&export), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(target)", d.AllocateMemory(r.device, &alloc, nil, &t.memory)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	if err := checked("vkBindImageMemory(target)", d.BindImageMemory(r.device, t.image, t.memory, 0)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	props := vk.ImageDrmFormatModifierPropertiesEXT{SType: vk.StructureTypeImageDRMFormatModifierPropertiesEXT}
	if err := checked("vkGetImageDrmFormatModifierPropertiesEXT", d.GetImageDrmFormatModifierPropertiesEXT(r.device, t.image, &props)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	var layout vk.SubresourceLayout
	sub := vk.ImageSubresource{AspectMask: vk.ImageAspectMemoryPlane0BitEXT}
	d.GetImageSubresourceLayout(r.device, t.image, &sub, &layout)
	t.view, err = r.imageView(t.image, format)
	if err != nil {
		return nil, ports.DMABuf{}, err
	}
	var fd int32
	get := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFDInfoKHR, Memory: t.memory, HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	if err := checked("vkGetMemoryFdKHR", d.GetMemoryFdKHR(r.device, &get, &fd)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	f := os.NewFile(uintptr(fd), "neferwl-target")
	if f == nil {
		return nil, ports.DMABuf{}, fmt.Errorf("invalid exported fd %d", fd)
	}
	buf := ports.DMABuf{Width: r.width, Height: r.height, Format: fourcc, Modifier: props.DrmFormatModifier, Planes: []ports.DMABufPlane{{File: f, Offset: uint32(layout.Offset), Stride: uint32(layout.RowPitch)}}}
	ok = true
	return t, buf, nil
}

func (r *Renderer) freeTarget(t *target) {
	if t.view != 0 {
		r.dd.DestroyImageView(r.device, t.view, nil)
		t.view = 0
	}
	if t.image != 0 {
		r.dd.DestroyImage(r.device, t.image, nil)
		t.image = 0
	}
	if t.memory != 0 {
		r.dd.FreeMemory(r.device, t.memory, nil)
		t.memory = 0
	}
}

func (r *Renderer) dropTargets() {
	if len(r.targets) > 0 && r.device != 0 {
		_ = checked("vkDeviceWaitIdle", r.dd.DeviceWaitIdle(r.device))
		r.idle()
	}
	for _, t := range r.targets {
		if r.last == t {
			r.last = nil
		}
		r.freeTarget(t)
	}
	r.targets, r.current = nil, 0
}
