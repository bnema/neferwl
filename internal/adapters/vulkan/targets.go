package vulkan

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"
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
	// What the image holds (damage.go): the scene, its Seq and each
	// window's content Seq; valid once a frame was drawn whole. Scenes are
	// immutable for core, but output adapters reuse their slices in place
	// (DRM overlay frames, capture), so the compared slices of scene point
	// into the target's own buffers, copied by hold.
	valid          bool
	sceneSeq       uint64
	scene          ports.Scene
	heldWindows    []ports.SceneWindow
	heldSeparators []ports.Separator
	heldLayers     []ports.SceneLayer
	heldHints      []domain.Rect
	heldIndicators []ports.CaptureIndicator
	heldOverlays   []ports.Overlay
	windows        map[domain.WindowID]heldWindow
}

// fourccXRGB is DRM_FORMAT_XRGB8888: B8G8R8A8 in memory, alpha ignored.
const fourccXRGB = 'X' | 'R'<<8 | '2'<<16 | '4'<<24

// fourccXR30 is DRM_FORMAT_XRGB2101010 (A2R10G10B10 packed).
const fourccXR30 = 'X' | 'R'<<8 | '3'<<16 | '0'<<24

// ExportTargets allocates n exported images of the output size, replacing
// the previous ones; n = 0 only drops them (back to the internal image).
// singlePlane keeps only one-plane modifiers (no DCC metadata planes), for
// displays that refuse the multi-plane framebuffers of the others.
func (r *Renderer) ExportTargets(n int, modifiers []uint64, singlePlane bool) ([]domain.DMABuf, error) {
	r.dropTargets()
	if n > 0 && r.hdrNits > 0 && r.hdr.pipeline == 0 {
		return nil, fmt.Errorf("HDR transform unavailable: %w", r.hdrError)
	}
	if n == 0 {
		return nil, nil
	}
	if !r.gpu.dd.HasGetMemoryFdKHR() || !r.gpu.dd.HasGetImageDrmFormatModifierPropertiesEXT() || len(r.gpu.dmabuf.Formats) == 0 {
		return nil, errors.New("device cannot export dmabufs")
	}
	mods := r.exportModifiers(modifiers, singlePlane)
	if len(mods) == 0 {
		if r.hdrNits == 0 {
			return nil, errors.New("no XRGB8888 modifier both the device and the display accept")
		}
		return nil, errors.New("no XRGB2101010 modifier both the device and the display accept")
	}
	var out []domain.DMABuf
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

// ImportTargets makes the images the display allocated (GBM dmabufs, one
// memory object for all planes) the render targets, replacing the previous
// ones; no buffers only drops them. The caller keeps its files: the
// renderer imports duplicates. Each buffer must have the renderer's size and
// target format and a modifier from TargetModifiers. A refusal (the driver
// rejecting the explicit layout) returns an error and leaves no targets.
func (r *Renderer) ImportTargets(bufs []domain.DMABuf) error {
	r.dropTargets()
	if len(bufs) == 0 {
		return nil
	}
	if r.hdrNits > 0 && r.hdr.pipeline == 0 {
		return fmt.Errorf("HDR transform unavailable: %w", r.hdrError)
	}
	if !r.gpu.dd.HasGetMemoryFdPropertiesKHR() || !r.gpu.dd.HasGetImageDrmFormatModifierPropertiesEXT() || len(r.gpu.dmabuf.Formats) == 0 {
		return errors.New("device cannot import dmabufs")
	}
	for i := range bufs {
		t, err := r.importTarget(&bufs[i])
		if err != nil {
			r.dropTargets()
			return err
		}
		r.targets = append(r.targets, t)
	}
	r.current = 0
	return nil
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
	available := r.gpu.renderMods
	if r.hdrNits > 0 {
		available = r.gpu.hdrMods
		if r.hdrReadback {
			available = r.gpu.hdrReadbackMods
		}
	}
	return r.intersectModifiers(available, display, singlePlane)
}

// intersectModifiers keeps the available modifiers the display lists (an
// empty display list accepts all of them, for SDR and for HDR only on a
// virtual output or a test readback), without the multi-plane ones when
// singlePlane.
func (r *Renderer) intersectModifiers(available, display []uint64, singlePlane bool) []uint64 {
	var out []uint64
	for _, m := range available {
		planes, _ := r.gpu.modifierPlanes(m)
		if singlePlane && planes > 1 {
			continue
		}
		if (len(display) == 0 && (r.hdrNits == 0 || r.hdrReadback || r.virtual)) || slices.Contains(display, m) {
			out = append(out, m)
		}
	}
	return out
}

// targetFormat is the Vulkan format and fourcc of the targets: XRGB8888, or
// XRGB2101010 in HDR.
func (r *Renderer) targetFormat() (vk.Format, uint32) {
	if r.hdrNits > 0 {
		return vk.FormatA2r10g10b10UnormPack32, fourccXR30
	}
	return vk.FormatB8g8r8a8Unorm, fourccXRGB
}

// TargetModifiers lists the modifiers an image allocated by the display
// (GBM) can have to be imported as a target: those the device renders into
// and imports for the target format, that the display lists (empty: any),
// without multi-plane ones when singlePlane. Call after SetHDR.
func (r *Renderer) TargetModifiers(display []uint64, singlePlane bool) []uint64 {
	if r.gpu.dmabuf.Device == 0 || r.gpu.dd == nil || !r.gpu.dd.HasGetMemoryFdPropertiesKHR() || !r.gpu.dd.HasGetImageDrmFormatModifierPropertiesEXT() {
		return nil
	}
	format, _ := r.targetFormat()
	return r.intersectModifiers(r.gpu.importMods[importKey{format, r.hdrReadback}], display, singlePlane)
}

// probeRenderModifiers computes all export/import tables before GPU publication.
func (g *GPU) probeRenderModifiers() {
	g.renderMods = g.probeTargetModifiers(vk.FormatB8g8r8a8Unorm, vk.ExternalMemoryFeatureExportableBit, false)
	g.hdrMods = g.probeTargetModifiers(vk.FormatA2r10g10b10UnormPack32, vk.ExternalMemoryFeatureExportableBit, false)
	g.hdrReadbackMods = g.probeTargetModifiers(vk.FormatA2r10g10b10UnormPack32, vk.ExternalMemoryFeatureExportableBit, true)
	g.importMods = make(map[importKey][]uint64)
	for _, format := range []vk.Format{vk.FormatB8g8r8a8Unorm, vk.FormatA2r10g10b10UnormPack32} {
		for _, readback := range []bool{false, true} {
			g.importMods[importKey{format, readback}] = g.probeTargetModifiers(format, vk.ExternalMemoryFeatureImportableBit, readback)
		}
	}
}

// importKey is what the importable modifiers of a target depend on: its
// format and whether test readback adds the transfer-source requirement.
type importKey struct {
	format   vk.Format
	readback bool
}

func (g *GPU) probeTargetModifiers(format vk.Format, feature vk.ExternalMemoryFeatureFlags, readback bool) []uint64 {
	list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
	fp := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
	g.inst.id.GetPhysicalDeviceFormatProperties2(g.physical, format, &fp)
	if list.DrmFormatModifierCount == 0 {
		return nil
	}
	mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
	list.DrmFormatModifierProperties = &mods[0]
	g.inst.id.GetPhysicalDeviceFormatProperties2(g.physical, format, &fp)
	need := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit)
	if format == vk.FormatB8g8r8a8Unorm {
		need |= formatFeatureTransferSrc | vk.FormatFeatureColorAttachmentBlendBit
	} else if readback {
		need |= formatFeatureTransferSrc
	}
	// SDR targets are composed and read back; HDR targets are color
	// attachments, and transfer sources only with hdrReadback.
	result, planes := filterModifiers(mods[:list.DrmFormatModifierCount], need, func(m uint64) bool { return g.externalTarget(format, m, feature, readback) })
	if g.modPlanes == nil {
		g.modPlanes = make(map[uint64]uint32)
	}
	// A modifier has the same plane count for every format (DCC planes
	// come from the modifier), so SDR and HDR share the map.
	maps.Copy(g.modPlanes, planes)
	return result
}

// maxModifierPlanes is the DRM limit of planes in a framebuffer.
const maxModifierPlanes = 4

// AMD format modifiers (drm_fourcc.h): the vendor is in bits 56-63, and a DCC
// modifier with the retile bit keeps a second, display-aligned copy of the
// DCC metadata.
const (
	modVendorShift = 56
	modVendorAMD   = 0x02
	amdModRetile   = 1 << 14 // AMD_FMT_MOD_DCC_RETILE
)

// retilesDCC reports whether m is an AMD DCC modifier with the retile bit.
// RADV renders such an image into pipe-aligned DCC and runs a compute pass
// that rewrites the display copy: an extra full-surface GPU pass per frame,
// so these modifiers are never offered for targets. Plain DCC (pipe-aligned
// or not) costs no such pass.
func retilesDCC(m uint64) bool {
	return m>>modVendorShift == modVendorAMD && m&amdModRetile != 0
}

// filterModifiers keeps the modifiers with 1..4 memory planes whose tiling
// features include need, that are not DCC retile modifiers and that pass
// exportable, and returns each kept modifier's plane count.
func filterModifiers(mods []vk.DrmFormatModifierPropertiesEXT, need vk.FormatFeatureFlags, exportable func(uint64) bool) ([]uint64, map[uint64]uint32) {
	var result []uint64
	planes := make(map[uint64]uint32, len(mods))
	for _, m := range mods {
		if m.DrmFormatModifierPlaneCount < 1 || m.DrmFormatModifierPlaneCount > maxModifierPlanes || m.DrmFormatModifierTilingFeatures&need != need || retilesDCC(m.DrmFormatModifier) || !exportable(m.DrmFormatModifier) {
			continue
		}
		result = append(result, m.DrmFormatModifier)
		planes[m.DrmFormatModifier] = m.DrmFormatModifierPlaneCount
	}
	return result, planes
}

// externalTarget asks whether a target image of format and modifier with
// the target usage supports the external memory feature (exportable: the
// renderer allocates it; importable: the display allocator does).
func (g *GPU) externalTarget(format vk.Format, modifier uint64, feature vk.ExternalMemoryFeatureFlags, readback bool) bool {
	mod := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{SType: vk.StructureTypePhysicalDeviceImageDRMFormatModifierInfoEXT, DrmFormatModifier: modifier, SharingMode: vk.SharingModeExclusive}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, Next: unsafe.Pointer(&mod), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: exportUsage(format, readback)}
	extOut := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&extOut)}
	if g.inst.id.GetPhysicalDeviceImageFormatProperties2(g.physical, &info, &out) != vk.Success {
		return false
	}
	return extOut.ExternalMemoryProperties.ExternalMemoryFeatures&feature != 0
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
func (r *Renderer) exportTarget(mods []uint64) (*target, domain.DMABuf, error) {
	d := r.gpu.dd
	t := &target{exported: true, layout: vk.ImageLayoutUndefined}
	format, fourcc := r.targetFormat()
	ok := false
	defer func() {
		if !ok {
			r.freeTarget(t)
		}
	}()
	list := vk.ImageDrmFormatModifierListCreateInfoEXT{SType: vk.StructureTypeImageDRMFormatModifierListCreateInfoEXT, DrmFormatModifierCount: uint32(len(mods)), DrmFormatModifiers: &mods[0]}
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, Next: unsafe.Pointer(&list), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: exportUsage(format, r.hdrReadback), SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(target)", d.CreateImage(r.gpu.device, &ii, nil, &t.image)); err != nil {
		return nil, domain.DMABuf{}, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.gpu.device, t.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return nil, domain.DMABuf{}, err
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.image}
	export := vk.ExportMemoryAllocateInfo{SType: vk.StructureTypeExportMemoryAllocateInfo, Next: unsafe.Pointer(&dedicated), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&export), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(target)", d.AllocateMemory(r.gpu.device, &alloc, nil, &t.memory)); err != nil {
		return nil, domain.DMABuf{}, err
	}
	if err := checked("vkBindImageMemory(target)", d.BindImageMemory(r.gpu.device, t.image, t.memory, 0)); err != nil {
		return nil, domain.DMABuf{}, err
	}
	props := vk.ImageDrmFormatModifierPropertiesEXT{SType: vk.StructureTypeImageDRMFormatModifierPropertiesEXT}
	if err := checked("vkGetImageDrmFormatModifierPropertiesEXT", d.GetImageDrmFormatModifierPropertiesEXT(r.gpu.device, t.image, &props)); err != nil {
		return nil, domain.DMABuf{}, err
	}
	planeCount, _ := r.gpu.modifierPlanes(props.DrmFormatModifier)
	n := min(max(planeCount, 1), maxModifierPlanes)
	var layouts [maxModifierPlanes]vk.SubresourceLayout
	for i := range n {
		sub := vk.ImageSubresource{AspectMask: vk.ImageAspectMemoryPlane0BitEXT << i}
		d.GetImageSubresourceLayout(r.gpu.device, t.image, &sub, &layouts[i])
	}
	t.view, err = r.imageView(t.image, format)
	if err != nil {
		return nil, domain.DMABuf{}, err
	}
	var fd int32
	get := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFDInfoKHR, Memory: t.memory, HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	if err := checked("vkGetMemoryFdKHR", d.GetMemoryFdKHR(r.gpu.device, &get, &fd)); err != nil {
		return nil, domain.DMABuf{}, err
	}
	// Every plane owns its file: the planes share the one memory object,
	// so planes 1.. are duplicates of the exported fd.
	planes := make([]domain.DMABufPlane, 0, n)
	closePlanes := func() {
		for _, p := range planes {
			_ = p.File.Close()
		}
	}
	first := os.NewFile(uintptr(fd), "neferwl-target")
	if first == nil {
		return nil, domain.DMABuf{}, fmt.Errorf("invalid exported fd %d", fd)
	}
	planes = append(planes, domain.DMABufPlane{File: first, Offset: uint32(layouts[0].Offset), Stride: uint32(layouts[0].RowPitch)})
	for i := uint32(1); i < n; i++ {
		dup, err := dupFile(first)
		if err != nil {
			closePlanes()
			return nil, domain.DMABuf{}, fmt.Errorf("duplicate exported fd for plane %d: %w", i, err)
		}
		planes = append(planes, domain.DMABufPlane{File: os.NewFile(uintptr(dup), "neferwl-target"), Offset: uint32(layouts[i].Offset), Stride: uint32(layouts[i].RowPitch)})
	}
	r.log.Debug().Uint64("modifier", props.DrmFormatModifier).Uint32("planes", n).Msg("render target exported")
	buf := domain.DMABuf{Width: r.width, Height: r.height, Format: fourcc, Modifier: props.DrmFormatModifier, Planes: planes}
	ok = true
	return t, buf, nil
}

func (r *Renderer) freeTarget(t *target) {
	if t.view != 0 {
		r.gpu.dd.DestroyImageView(r.gpu.device, t.view, nil)
		t.view = 0
	}
	if t.image != 0 {
		r.gpu.dd.DestroyImage(r.gpu.device, t.image, nil)
		t.image = 0
	}
	if t.memory != 0 {
		r.gpu.dd.FreeMemory(r.gpu.device, t.memory, nil)
		t.memory = 0
	}
}

func (r *Renderer) dropTargets() {
	if len(r.targets) > 0 && r.gpu.device != 0 {
		_ = r.waitOwnIdle()
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

// What ImportTargets refuses an image for, before asking the driver.
var (
	errImageGeometry = errors.New("image size or format does not match the target")
	errImageModifier = errors.New("image modifier is not an importable target modifier")
	errImagePlanes   = errors.New("image plane count does not match its modifier")
	errImageDMABuf   = errors.New("image planes are not in one dmabuf")
)

// importTarget makes a target of an image allocated outside the renderer.
// The target is shared with the display like an exported one: frames
// acquire it from and release it to the foreign queue family.
func (r *Renderer) importTarget(b *domain.DMABuf) (*target, error) {
	d := r.gpu.dd
	format, fourcc := r.targetFormat()
	n := len(b.Planes)
	want, known := r.gpu.modifierPlanes(b.Modifier)
	switch {
	case b.Format != fourcc || b.Width != r.width || b.Height != r.height:
		return nil, fmt.Errorf("%w: image %dx%d format %#x, target %dx%d format %#x", errImageGeometry, b.Width, b.Height, b.Format, r.width, r.height, fourcc)
	case !known:
		return nil, fmt.Errorf("%w: %#x is not a target modifier of the device", errImageModifier, b.Modifier)
	case n < 1 || n > maxModifierPlanes || want != uint32(n):
		return nil, fmt.Errorf("%w: modifier %#x: image has %d planes, device expects %d", errImagePlanes, b.Modifier, n, want)
	case !r.gpu.externalTarget(format, b.Modifier, vk.ExternalMemoryFeatureImportableBit, r.hdrReadback):
		return nil, fmt.Errorf("%w: %#x cannot be imported as a target", errImageModifier, b.Modifier)
	}
	if err := sameDMABuf(b.Planes); err != nil {
		return nil, err
	}
	fd, err := dupFile(b.Planes[0].File)
	if err != nil {
		return nil, err
	}
	// Vulkan owns the fd it imports on success: hand it its own duplicate.
	// Closed here on every path but the successful import.
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	t := &target{exported: true, layout: vk.ImageLayoutUndefined}
	ok := false
	defer func() {
		if !ok {
			r.freeTarget(t)
		}
	}()
	var layouts [maxModifierPlanes]vk.SubresourceLayout
	for i, p := range b.Planes {
		layouts[i] = vk.SubresourceLayout{Offset: vk.DeviceSize(p.Offset), RowPitch: vk.DeviceSize(p.Stride)}
	}
	explicit := vk.ImageDrmFormatModifierExplicitCreateInfoEXT{SType: vk.StructureTypeImageDRMFormatModifierExplicitCreateInfoEXT, DrmFormatModifier: b.Modifier, DrmFormatModifierPlaneCount: uint32(n), PlaneLayouts: &layouts[0]}
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, Next: unsafe.Pointer(&explicit), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: exportUsage(format, r.hdrReadback), SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(imported target)", d.CreateImage(r.gpu.device, &ii, nil, &t.image)); err != nil {
		return nil, err
	}
	fdProps := vk.MemoryFdPropertiesKHR{SType: vk.StructureTypeMemoryFDPropertiesKHR}
	if err := checked("vkGetMemoryFdPropertiesKHR", d.GetMemoryFdPropertiesKHR(r.gpu.device, vk.ExternalMemoryHandleTypeDMABUFBitEXT, int32(fd), &fdProps)); err != nil {
		return nil, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.gpu.device, t.image, &req)
	bits := req.MemoryTypeBits & fdProps.MemoryTypeBits
	kind, err := r.findMemoryType(bits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		if kind, err = r.findMemoryType(bits, 0); err != nil {
			return nil, err
		}
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.image}
	imp := vk.ImportMemoryFdInfoKHR{SType: vk.StructureTypeImportMemoryFDInfoKHR, Next: unsafe.Pointer(&dedicated), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT, Fd: int32(fd)}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&imp), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(imported target)", d.AllocateMemory(r.gpu.device, &alloc, nil, &t.memory)); err != nil {
		return nil, err
	}
	fd = -1 // Vulkan owns it now.
	if err := checked("vkBindImageMemory(imported target)", d.BindImageMemory(r.gpu.device, t.image, t.memory, 0)); err != nil {
		return nil, err
	}
	if t.view, err = r.imageView(t.image, format); err != nil {
		return nil, err
	}
	r.log.Debug().Uint64("modifier", b.Modifier).Int("planes", n).Msg("render target imported")
	ok = true
	return t, nil
}

// sameDMABuf fails unless every plane file is the same dmabuf (same device
// and inode): the target is imported from the first plane's memory only, so
// planes in other buffers would read the wrong memory.
func sameDMABuf(planes []domain.DMABufPlane) error {
	var first unix.Stat_t
	for i, p := range planes {
		fd, err := dupFile(p.File)
		if err != nil {
			return fmt.Errorf("plane %d: %w", i, err)
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		_ = unix.Close(fd)
		if err != nil {
			return fmt.Errorf("stat plane %d: %w", i, err)
		}
		if i == 0 {
			first = st
		} else if st.Dev != first.Dev || st.Ino != first.Ino {
			return fmt.Errorf("%w: plane %d is not in the dmabuf of plane 0", errImageDMABuf, i)
		}
	}
	return nil
}
