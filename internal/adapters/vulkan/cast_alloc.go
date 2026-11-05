package vulkan

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sync/atomic"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"

	vk "bnema.dev/purego-vulkan/vulkan"
)

// Screencast buffers (ports.CastBufferAllocator). The PipeWire producer
// offers only DMA-BUF buffers, allocated here: one exported image per
// buffer, of a modifier the device can export and copy captures into
// (CaptureFrame.CopyTo, a transfer destination). The GPU keeps the
// exporting image and memory, keyed by the buffer ID, so CopyTo writes
// into them without importing the planes again. The caller calls
// ReleaseCast before closing the plane files; outstanding copies defer the
// destruction of the image and memory until their fences are reclaimed.

// castFourccs are the cast formats, preferred first within an offer:
// XBGR2101010 carries the PQ words of an HDR frame, the 8-bit formats the
// opaque sRGB8 of BeginCapture.
var castFourccs = [...]uint32{fourcc('X', 'B', '3', '0'), fourcc('X', 'R', '2', '4'), fourcc('A', 'R', '2', '4')}

// maxCastModifiers is the most modifiers PipeWire takes per format.
const maxCastModifiers = 32

// castUsage is how a cast buffer is used: CopyTo writes it.
const castUsage = vk.ImageUsageTransferDstBit

// CastFormats lists, per cast format, the modifiers AllocCast exports and
// CopyTo writes into (transfer destinations, 1 to 4 memory planes, no AMD
// DCC retile). A format without one is left out; nothing when the device
// cannot export dmabufs or sync files.
func (g *GPU) CastFormats() []domain.DMABufFormat {
	if g.dd == nil || !g.syncFD || !g.dd.HasGetMemoryFdKHR() || !g.dd.HasGetImageDrmFormatModifierPropertiesEXT() {
		return nil
	}
	var out []domain.DMABufFormat
	for _, fc := range castFourccs {
		format, _, _ := vkFormat(fc)
		mods, _ := filterModifiers(g.formatModifiers(format), formatFeatureTransferDst, func(m uint64) bool {
			return g.castExportable(format, m)
		})
		for _, m := range mods[:min(len(mods), maxCastModifiers)] {
			out = append(out, domain.DMABufFormat{Format: fc, Modifier: m})
		}
	}
	return out
}

// formatModifiers lists the DRM modifiers the device knows for format.
func (g *GPU) formatModifiers(format vk.Format) []vk.DrmFormatModifierPropertiesEXT {
	list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
	props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
	g.inst.id.GetPhysicalDeviceFormatProperties2(g.physical, format, &props)
	if list.DrmFormatModifierCount == 0 {
		return nil
	}
	mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
	list.DrmFormatModifierProperties = &mods[0]
	g.inst.id.GetPhysicalDeviceFormatProperties2(g.physical, format, &props)
	return mods[:list.DrmFormatModifierCount]
}

// castExportable reports whether a cast image of format and modifier can
// be exported, and imported back as CopyTo's destination.
func (g *GPU) castExportable(format vk.Format, modifier uint64) bool {
	mod := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{SType: vk.StructureTypePhysicalDeviceImageDRMFormatModifierInfoEXT, DrmFormatModifier: modifier, SharingMode: vk.SharingModeExclusive}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, Next: unsafe.Pointer(&mod), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: castUsage}
	extOut := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&extOut)}
	if g.inst.id.GetPhysicalDeviceImageFormatProperties2(g.physical, &info, &out) != vk.Success {
		return false
	}
	need := vk.ExternalMemoryFeatureFlags(vk.ExternalMemoryFeatureExportableBit | vk.ExternalMemoryFeatureImportableBit)
	return extOut.ExternalMemoryProperties.ExternalMemoryFeatures&need == need
}

// errCastClosed: the GPU was closed (device loss or shutdown).
var errCastClosed = errors.New("vulkan: GPU closed")

// AllocCast exports a w×h image of a cast format with modifier, one listed
// by CastFormats, and returns it as a dmabuf whose plane files are the
// caller's (every plane a file of the one memory object). ReleaseCast must
// precede closing them. It may run on
// any goroutine.
func (g *GPU) AllocCast(format uint32, w, h int, modifier uint64) (domain.DMABuf, error) {
	if w <= 0 || h <= 0 || w > math.MaxUint16 || h > math.MaxUint16 {
		return domain.DMABuf{}, fmt.Errorf("invalid cast buffer size %dx%d", w, h)
	}
	if !slices.Contains(castFourccs[:], format) {
		return domain.DMABuf{}, fmt.Errorf("cast format %#x unsupported", format)
	}
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	if g.dd == nil || g.device == 0 {
		return domain.DMABuf{}, errCastClosed
	}
	vf, _, _ := vkFormat(format)
	planes, ok := g.supportsBlit(vf, modifier, formatFeatureTransferDst)
	if !ok || planes < 1 || planes > maxModifierPlanes || retilesDCC(modifier) || !g.castExportable(vf, modifier) {
		return domain.DMABuf{}, fmt.Errorf("cast format %#x modifier %#x not exportable", format, modifier)
	}
	return g.exportCast(vf, format, w, h, modifier, planes)
}

// exportCast creates and retains one cast image until ReleaseCast. g.poolMu is held.
func (g *GPU) exportCast(vf vk.Format, format uint32, w, h int, modifier uint64, n uint32) (domain.DMABuf, error) {
	d, dev := g.dd, g.device
	t := &target{}
	retained := false
	defer func() {
		if !retained {
			g.freeImage(t)
		}
	}()
	list := vk.ImageDrmFormatModifierListCreateInfoEXT{SType: vk.StructureTypeImageDRMFormatModifierListCreateInfoEXT, DrmFormatModifierCount: 1, DrmFormatModifiers: &modifier}
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, Next: unsafe.Pointer(&list), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: vf, Extent: vk.Extent3D{Width: uint32(w), Height: uint32(h), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: castUsage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(cast)", d.CreateImage(dev, &ii, nil, &t.image)); err != nil {
		return domain.DMABuf{}, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(dev, t.image, &req)
	kind, err := findCopyMemory(g, req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
	if err != nil {
		return domain.DMABuf{}, err
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.image}
	export := vk.ExportMemoryAllocateInfo{SType: vk.StructureTypeExportMemoryAllocateInfo, Next: unsafe.Pointer(&dedicated), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&export), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(cast)", d.AllocateMemory(dev, &alloc, nil, &t.memory)); err != nil {
		return domain.DMABuf{}, err
	}
	if err := checked("vkBindImageMemory(cast)", d.BindImageMemory(dev, t.image, t.memory, 0)); err != nil {
		return domain.DMABuf{}, err
	}
	props := vk.ImageDrmFormatModifierPropertiesEXT{SType: vk.StructureTypeImageDRMFormatModifierPropertiesEXT}
	if err := checked("vkGetImageDrmFormatModifierPropertiesEXT(cast)", d.GetImageDrmFormatModifierPropertiesEXT(dev, t.image, &props)); err != nil {
		return domain.DMABuf{}, err
	}
	if props.DrmFormatModifier != modifier {
		return domain.DMABuf{}, fmt.Errorf("cast image got modifier %#x, want %#x", props.DrmFormatModifier, modifier)
	}
	var layouts [maxModifierPlanes]vk.SubresourceLayout
	for i := range n {
		sub := vk.ImageSubresource{AspectMask: vk.ImageAspectMemoryPlane0BitEXT << i}
		d.GetImageSubresourceLayout(dev, t.image, &sub, &layouts[i])
	}
	fd := int32(-1)
	get := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFDInfoKHR, Memory: t.memory, HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	if err := checked("vkGetMemoryFdKHR(cast)", d.GetMemoryFdKHR(dev, &get, &fd)); err != nil {
		return domain.DMABuf{}, err
	}
	first := os.NewFile(uintptr(fd), "neferwl-cast")
	if first == nil {
		return domain.DMABuf{}, fmt.Errorf("invalid exported cast fd %d", fd)
	}
	planes := make([]domain.DMABufPlane, 0, n)
	planes = append(planes, domain.DMABufPlane{File: first, Offset: uint32(layouts[0].Offset), Stride: uint32(layouts[0].RowPitch)})
	for i := uint32(1); i < n; i++ {
		dup, err := dupFile(first)
		if err != nil {
			for _, p := range planes {
				_ = p.File.Close()
			}
			return domain.DMABuf{}, fmt.Errorf("duplicate cast fd for plane %d: %w", i, err)
		}
		planes = append(planes, domain.DMABufPlane{File: os.NewFile(uintptr(dup), "neferwl-cast"), Offset: uint32(layouts[i].Offset), Stride: uint32(layouts[i].RowPitch)})
	}
	buf := domain.DMABuf{ID: castBufferID.Add(1), Width: w, Height: h, Format: format, Modifier: modifier, Planes: planes}
	if g.castImages == nil {
		g.castImages = make(map[uint64]*castImage)
	}
	g.castImages[buf.ID] = &castImage{buffer: buf, img: t}
	retained = true
	return buf, nil
}

// castBufferID is process-wide, so cast identities cannot collide across GPUs.
var castBufferID atomic.Uint64

type castImage struct {
	buffer   domain.DMABuf
	img      *target
	refs     int
	released bool
}

// ReleaseCast is safe on the PipeWire owner while output owners finish copies.
func (g *GPU) ReleaseCast(buf domain.DMABuf) {
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	e := g.castImages[buf.ID]
	if e == nil {
		return
	}
	delete(g.castImages, buf.ID)
	e.released = true
	if e.refs == 0 && e.img != nil {
		g.freeImage(e.img)
		e.img = nil
	}
}

func (g *GPU) acquireCastImage(buf *domain.DMABuf) (*target, *castImage, error) {
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	e := g.castImages[buf.ID]
	if e == nil {
		img, err := g.importBlitImage(buf, buf.Width, buf.Height, vk.ImageUsageTransferDstBit, formatFeatureTransferDst)
		return img, nil, err
	}
	// Registered IDs name an immutable allocation, not arbitrary request geometry.
	if buf.Width != e.buffer.Width || buf.Height != e.buffer.Height || buf.Format != e.buffer.Format || buf.Modifier != e.buffer.Modifier || len(buf.Planes) != len(e.buffer.Planes) {
		return nil, nil, errImageGeometry
	}
	for i, p := range buf.Planes {
		if p != e.buffer.Planes[i] {
			return nil, nil, errImagePlanes
		}
	}

	e.refs++
	return e.img, e, nil
}

func (g *GPU) releaseCastImage(e *castImage) {
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	e.refs--
	if e.released && e.refs == 0 && e.img != nil {
		g.freeImage(e.img)
		e.img = nil
	}
}
