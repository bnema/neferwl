package vulkan

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// Render targets (ADR 014). A DRM output scans out images the renderer
// draws into: ExportTargets allocates them with a DRM format modifier the
// display accepts and exports their memory as dmabufs, which the output
// turns into KMS framebuffers. Render then composes straight into the
// selected target and the output flips to it; nothing is copied by the
// CPU. In HDR an internal fp16 linear image keeps full-range composition,
// sampled by a full-screen GPU pass into the 10-bit target. Capture/Pixels
// convert to SDR sRGB only when requested.

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
// singlePlane keeps only one-plane modifiers (no DCC metadata planes), for
// displays that refuse the multi-plane framebuffers of the others.
func (r *Renderer) ExportTargets(n int, modifiers []uint64, singlePlane bool) ([]ports.DMABuf, error) {
	r.dropTargets()
	if n > 0 && r.hdrNits > 0 && r.hdrReadback && r.physical != 0 {
		// Readback targets (GPU tests, headless --screenshot-raw) are also
		// transfer sources, which changes the format requirements.
		r.hdrMods = r.probeModifiers(r.physical, vk.FormatA2r10g10b10UnormPack32)
	}
	if n > 0 && r.hdrNits > 0 && r.hdr.pipeline == 0 {
		return nil, fmt.Errorf("HDR transform unavailable: %w", r.hdrError)
	}
	if n == 0 {
		return nil, nil
	}
	if !r.dd.HasGetMemoryFdKHR() || !r.dd.HasGetImageDrmFormatModifierPropertiesEXT() || len(r.dmabuf.Formats) == 0 {
		return nil, errors.New("device cannot export dmabufs")
	}
	mods := r.exportModifiers(modifiers, singlePlane)
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

// SetVirtualOutput marks the output as having no display (headless): an
// empty display modifier list then accepts every exportable HDR modifier,
// as it does for SDR. DRM never sets it. Call before ExportTargets.
func (r *Renderer) SetVirtualOutput(on bool) { r.virtual = on }

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
// those supported by the display. HDR never assumes an unspecified list,
// except for a virtual output (no display) or a test readback. singlePlane
// drops the modifiers with more than one memory plane.
func (r *Renderer) exportModifiers(display []uint64, singlePlane bool) []uint64 {
	var out []uint64
	available := r.renderMods
	if r.hdrNits > 0 {
		available = r.hdrMods
	}
	for _, m := range available {
		if singlePlane && r.modPlanes[m] > 1 {
			continue
		}
		if (len(display) == 0 && (r.hdrNits == 0 || r.hdrReadback || r.virtual)) || slices.Contains(display, m) {
			out = append(out, m)
		}
	}
	return out
}

// probeRenderModifiers lists exportable SDR and HDR targets.
func (r *Renderer) probeRenderModifiers(physical vk.PhysicalDevice) {
	r.renderMods = r.probeModifiers(physical, vk.FormatB8g8r8a8Unorm)
	r.hdrMods = r.probeModifiers(physical, vk.FormatA2r10g10b10UnormPack32)
}

// probeModifiers lists the exportable modifiers of format (one to four memory
// planes: DCC adds metadata planes) that support the target usage, and
// records their plane counts in modPlanes. HDR targets need transfer-src only when
// hdrReadback is set (GPU tests, headless --screenshot-raw; never DRM).
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
	// SDR targets are composed and read back; HDR targets are color
	// attachments, and transfer sources only with hdrReadback.
	result, planes := filterModifiers(mods[:list.DrmFormatModifierCount], need, func(m uint64) bool { return r.exportable(physical, format, m) })
	if r.modPlanes == nil {
		r.modPlanes = make(map[uint64]uint32)
	}
	// A modifier has the same plane count for every format (DCC planes
	// come from the modifier), so SDR and HDR share the map.
	maps.Copy(r.modPlanes, planes)
	return result
}

// maxModifierPlanes is the DRM limit of planes in a framebuffer.
const maxModifierPlanes = 4

// filterModifiers keeps the modifiers with 1..4 memory planes whose tiling
// features include need and that pass exportable, and returns each kept
// modifier's plane count.
func filterModifiers(mods []vk.DrmFormatModifierPropertiesEXT, need vk.FormatFeatureFlags, exportable func(uint64) bool) ([]uint64, map[uint64]uint32) {
	var result []uint64
	planes := make(map[uint64]uint32, len(mods))
	for _, m := range mods {
		if m.DrmFormatModifierPlaneCount < 1 || m.DrmFormatModifierPlaneCount > maxModifierPlanes || m.DrmFormatModifierTilingFeatures&need != need || !exportable(m.DrmFormatModifier) {
			continue
		}
		result = append(result, m.DrmFormatModifier)
		planes[m.DrmFormatModifier] = m.DrmFormatModifierPlaneCount
	}
	return result, planes
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
	n := max(r.modPlanes[props.DrmFormatModifier], 1)
	var layouts [maxModifierPlanes]vk.SubresourceLayout
	for i := range min(n, maxModifierPlanes) {
		sub := vk.ImageSubresource{AspectMask: vk.ImageAspectMemoryPlane0BitEXT << i}
		d.GetImageSubresourceLayout(r.device, t.image, &sub, &layouts[i])
	}
	t.view, err = r.imageView(t.image, format)
	if err != nil {
		return nil, ports.DMABuf{}, err
	}
	var fd int32
	get := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFDInfoKHR, Memory: t.memory, HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	if err := checked("vkGetMemoryFdKHR", d.GetMemoryFdKHR(r.device, &get, &fd)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	// Every plane owns its file: the planes share the one memory object,
	// so planes 1.. are duplicates of the exported fd.
	planes := make([]ports.DMABufPlane, 0, n)
	closePlanes := func() {
		for _, p := range planes {
			_ = p.File.Close()
		}
	}
	first := os.NewFile(uintptr(fd), "neferwl-target")
	if first == nil {
		return nil, ports.DMABuf{}, fmt.Errorf("invalid exported fd %d", fd)
	}
	planes = append(planes, ports.DMABufPlane{File: first, Offset: uint32(layouts[0].Offset), Stride: uint32(layouts[0].RowPitch)})
	for i := uint32(1); i < n; i++ {
		dup, err := unix.FcntlInt(first.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			closePlanes()
			return nil, ports.DMABuf{}, fmt.Errorf("duplicate exported fd for plane %d: %w", i, err)
		}
		planes = append(planes, ports.DMABufPlane{File: os.NewFile(uintptr(dup), "neferwl-target"), Offset: uint32(layouts[i].Offset), Stride: uint32(layouts[i].RowPitch)})
	}
	r.log.Info().Uint64("modifier", props.DrmFormatModifier).Uint32("planes", n).Msg("render target exported")
	buf := ports.DMABuf{Width: r.width, Height: r.height, Format: fourcc, Modifier: props.DrmFormatModifier, Planes: planes}
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
	// New targets hold no HDR frame until the next Render.
	r.targets, r.current, r.lastHDRTarget = nil, 0, -1
}
