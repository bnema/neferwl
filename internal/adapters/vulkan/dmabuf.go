package vulkan

import (
	"fmt"
	"os"
	"slices"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// dmabuf import (linux-dmabuf clients): each client buffer becomes a
// VkImage bound to its imported memory, cached by buffer ID while the
// client keeps attaching it, and sampled by the compose shader. Sync is
// implicit: before sampling, the renderer waits on the fences the client's
// driver attached to the buffer (DMA_BUF_IOCTL_EXPORT_SYNC_FILE), imported
// as a semaphore.

// deviceExtensions are enabled when all present; without them the renderer
// has no dmabuf support and clients fall back to wl_shm.
var deviceExtensions = []string{
	vk.KHRExternalMemoryFDExtensionName,
	vk.EXTExternalMemoryDMABUFExtensionName,
	vk.EXTImageDRMFormatModifierExtensionName,
	vk.EXTQueueFamilyForeignExtensionName,
	vk.KHRExternalSemaphoreFDExtensionName,
	// The render node advertised to clients in dmabuf feedback.
	vk.EXTPhysicalDeviceDRMExtensionName,
}

// fourcc → Vulkan format. DRM formats name the packed pixel from the high
// byte, little-endian: ARGB8888 is B,G,R,A in memory, like B8G8R8A8.
var dmabufFormats = []struct {
	fourcc uint32
	format vk.Format
	opaque bool
}{
	{fourcc('A', 'R', '2', '4'), vk.FormatB8g8r8a8Unorm, false},
	{fourcc('X', 'R', '2', '4'), vk.FormatB8g8r8a8Unorm, true},
	{fourcc('A', 'B', '2', '4'), vk.FormatR8g8b8a8Unorm, false},
	{fourcc('X', 'B', '2', '4'), vk.FormatR8g8b8a8Unorm, true},
	// Vulkan's A2R10G10B10/A2B10G10R10 packed UNORM formats match
	// DRM's little-endian 2101010 layout; do not use an sRGB view for PQ.
	{fourcc('A', 'R', '3', '0'), vk.FormatA2r10g10b10UnormPack32, false},
	{fourcc('X', 'R', '3', '0'), vk.FormatA2r10g10b10UnormPack32, true},
	{fourcc('A', 'B', '3', '0'), vk.FormatA2b10g10r10UnormPack32, false},
	{fourcc('X', 'B', '3', '0'), vk.FormatA2b10g10r10UnormPack32, true},
	// Little-endian ABGR16161616F is RGBA16F; floating-point values
	// are linear electrical signals, not sRGB-encoded.
	{fourcc('A', 'B', '4', 'H'), vk.FormatR16g16b16a16Sfloat, false},
	{fourcc('X', 'B', '4', 'H'), vk.FormatR16g16b16a16Sfloat, true},
	{fourcc('N', 'V', '1', '2'), vk.Format(1000156003), true}, // VK_FORMAT_G8_B8R8_2PLANE_420_UNORM
	{fourcc('P', '0', '1', '0'), vk.Format(1000156013), true}, // VK_FORMAT_G10X6_B10X6R10X6_2PLANE_420_UNORM_3PACK16
}

func isYUVFormat(f uint32) bool {
	return f == fourcc('N', 'V', '1', '2') || f == fourcc('P', '0', '1', '0')
}

func fourcc(a, b, c, d byte) uint32 {
	return uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24
}

func vkFormat(f uint32) (vk.Format, bool, bool) {
	for _, v := range dmabufFormats {
		if v.fourcc == f {
			return v.format, v.opaque, true
		}
	}
	return 0, false, false
}

// extensions lists the device extensions supported by the selected GPU.
func (r *Renderer) extensions(physical vk.PhysicalDevice) map[string]bool {
	var n uint32
	if r.id.EnumerateDeviceExtensionProperties(physical, nil, &n, nil) != vk.Success || n == 0 {
		return nil
	}
	props := make([]vk.ExtensionProperties, n)
	if r.id.EnumerateDeviceExtensionProperties(physical, nil, &n, &props[0]) != vk.Success {
		return nil
	}
	have := map[string]bool{}
	for _, p := range props[:n] {
		have[cstring(p.ExtensionName[:])] = true
	}
	return have
}

func cstring(b []byte) string {
	if i := slices.Index(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// probeDMABuf lists the sampled fourcc/modifier pairs supported by the GPU.
// Multi-planar formats require mutable per-plane views; disjoint binding is
// optional and only used when the device supports it and planes differ.
func (r *Renderer) probeDMABuf(physical vk.PhysicalDevice) ports.DMABufSupport {
	var sup ports.DMABufSupport
	drm := vk.PhysicalDeviceDrmPropertiesEXT{SType: vk.StructureTypePhysicalDeviceDRMPropertiesEXT}
	props := vk.PhysicalDeviceProperties2{SType: vk.StructureTypePhysicalDeviceProperties2, Next: unsafe.Pointer(&drm)}
	r.id.GetPhysicalDeviceProperties2(physical, &props)
	if drm.HasRender != 0 {
		sup.Device = unix.Mkdev(uint32(drm.RenderMajor), uint32(drm.RenderMinor))
	}
	for _, f := range dmabufFormats {
		list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
		fp := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
		r.id.GetPhysicalDeviceFormatProperties2(physical, f.format, &fp)
		if list.DrmFormatModifierCount == 0 {
			continue
		}
		mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
		list.DrmFormatModifierProperties = &mods[0]
		r.id.GetPhysicalDeviceFormatProperties2(physical, f.format, &fp)
		for _, m := range mods[:list.DrmFormatModifierCount] {
			need := vk.FormatFeatureFlags(vk.FormatFeatureSampledImageBit | vk.FormatFeatureSampledImageFilterLinearBit)
			planes := uint32(1)
			if isYUVFormat(f.fourcc) {
				planes = 2
			}
			if m.DrmFormatModifierPlaneCount != planes || m.DrmFormatModifierTilingFeatures&need != need {
				continue
			}
			if r.importable(physical, f.format, m.DrmFormatModifier, isYUVFormat(f.fourcc), false) {
				sup.Formats = append(sup.Formats, ports.DMABufFormat{Format: f.fourcc, Modifier: m.DrmFormatModifier})
			}
		}
	}
	return sup
}

// importable asks whether an image of that format and modifier can be
// imported from a dmabuf.
func (r *Renderer) importable(physical vk.PhysicalDevice, format vk.Format, modifier uint64, yuv, disjoint bool) bool {
	mod := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{SType: vk.StructureTypePhysicalDeviceImageDRMFormatModifierInfoEXT, DrmFormatModifier: modifier, SharingMode: vk.SharingModeExclusive}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, Next: unsafe.Pointer(&mod), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	flags := vk.ImageCreateFlags(0)
	if yuv {
		flags = vk.ImageCreateMutableFormatBit
		if disjoint {
			flags |= imageDisjoint
			// Image-format queries alone do not imply the modifier supports
			// disjoint plane bindings. Check its tiling features as well.
			list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDRMFormatModifierPropertiesListEXT}
			properties := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, Next: unsafe.Pointer(&list)}
			r.id.GetPhysicalDeviceFormatProperties2(physical, format, &properties)
			if list.DrmFormatModifierCount == 0 {
				return false
			}
			mods := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
			list.DrmFormatModifierProperties = &mods[0]
			r.id.GetPhysicalDeviceFormatProperties2(physical, format, &properties)
			found := false
			for _, m := range mods[:list.DrmFormatModifierCount] {
				if m.DrmFormatModifier == modifier && m.DrmFormatModifierTilingFeatures&0x00400000 != 0 {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		views := yuvViewFormats(format)
		formats := imageFormatList{sType: structureImageFormatList, viewFormatCount: 3, viewFormats: &views[0]}
		mod.Next = unsafe.Pointer(&formats)
		info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Flags: flags, Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: vk.ImageUsageSampledBit}
		extOut := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
		out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&extOut)}
		return r.id.GetPhysicalDeviceImageFormatProperties2(physical, &info, &out) == vk.Success && extOut.ExternalMemoryProperties.ExternalMemoryFeatures&vk.ExternalMemoryFeatureImportableBit != 0
	}
	info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Flags: flags, Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: vk.ImageUsageSampledBit}
	extOut := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&extOut)}
	if r.id.GetPhysicalDeviceImageFormatProperties2(physical, &info, &out) != vk.Success {
		return false
	}
	return extOut.ExternalMemoryProperties.ExternalMemoryFeatures&vk.ExternalMemoryFeatureImportableBit != 0
}

// DMABuf reports the formats this renderer imports.
func (r *Renderer) DMABuf() ports.DMABufSupport { return r.dmabuf }

// imported is a client dmabuf bound to a VkImage, with the view and set
// the compose shader samples it through.
type imported struct {
	image      vk.Image
	memory     vk.DeviceMemory
	view       vk.ImageView
	chromaView vk.ImageView
	memories   [2]vk.DeviceMemory
	fds        [2]int
	yuv        bool
	// disjoint is available for this format/modifier; required only when
	// the two planes refer to different underlying DMA-BUF objects.
	disjoint bool
	pool     vk.DescriptorPool
	set      vk.DescriptorSet
	fd       int // our duplicate of plane 0, for implicit sync
	// last is the frame that drew it, for eviction.
	last uint64
}

// importTTL is how many frames an import survives undrawn: clients cycle
// through 2-4 buffers, and a destroyed buffer is freed shortly after.
const importTTL = 120

// importDMABuf returns the image of a client buffer, importing it once.
func (r *Renderer) importDMABuf(b *ports.DMABuf) (*imported, error) {
	if im := r.imports[b.ID]; im != nil {
		im.last = r.frame
		return im, nil
	}
	if len(r.dmabuf.Formats) == 0 {
		return nil, fmt.Errorf("dmabuf unsupported")
	}
	format, _, ok := vkFormat(b.Format)
	if !ok || b.Width <= 0 || b.Height <= 0 {
		return nil, fmt.Errorf("dmabuf format %#x with %d planes unsupported", b.Format, len(b.Planes))
	}
	if isYUVFormat(b.Format) {
		if len(b.Planes) != 2 || b.Width%2 != 0 || b.Height%2 != 0 {
			return nil, fmt.Errorf("invalid YUV buffer geometry or planes")
		}
		return r.importYUV(b, format)
	}
	if len(b.Planes) != 1 {
		return nil, fmt.Errorf("RGB buffer needs one plane")
	}
	fd, err := dupFile(b.Planes[0].File)
	if err != nil {
		return nil, err
	}
	im := &imported{fd: fd, last: r.frame}
	ok = false
	defer func() {
		if !ok {
			r.release(im)
		}
	}()
	d := r.dd
	layout := vk.SubresourceLayout{Offset: vk.DeviceSize(b.Planes[0].Offset), RowPitch: vk.DeviceSize(b.Planes[0].Stride)}
	explicit := vk.ImageDrmFormatModifierExplicitCreateInfoEXT{SType: vk.StructureTypeImageDRMFormatModifierExplicitCreateInfoEXT, DrmFormatModifier: b.Modifier, DrmFormatModifierPlaneCount: 1, PlaneLayouts: &layout}
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, Next: unsafe.Pointer(&explicit), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(b.Width), Height: uint32(b.Height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: vk.ImageUsageSampledBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
	if err := checked("vkCreateImage(dmabuf)", d.CreateImage(r.device, &ii, nil, &im.image)); err != nil {
		return nil, err
	}
	fdProps := vk.MemoryFdPropertiesKHR{SType: vk.StructureTypeMemoryFDPropertiesKHR}
	if err := checked("vkGetMemoryFdPropertiesKHR", d.GetMemoryFdPropertiesKHR(r.device, vk.ExternalMemoryHandleTypeDMABUFBitEXT, int32(fd), &fdProps)); err != nil {
		return nil, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.device, im.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits&fdProps.MemoryTypeBits, 0)
	if err != nil {
		return nil, err
	}
	// Vulkan owns the fd it imports on success: hand it its own duplicate.
	memFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: im.image}
	imp := vk.ImportMemoryFdInfoKHR{SType: vk.StructureTypeImportMemoryFDInfoKHR, Next: unsafe.Pointer(&dedicated), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT, Fd: int32(memFD)}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&imp), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(dmabuf)", d.AllocateMemory(r.device, &alloc, nil, &im.memory)); err != nil {
		unix.Close(memFD)
		return nil, err
	}
	if err := checked("vkBindImageMemory(dmabuf)", d.BindImageMemory(r.device, im.image, im.memory, 0)); err != nil {
		return nil, err
	}
	if im.view, err = r.imageView(im.image, format); err != nil {
		return nil, err
	}
	if im.pool, im.set, err = r.newSet(im.view, r.compose.dummyBuffer.buffer); err != nil {
		return nil, err
	}
	ok = true
	r.imports[b.ID] = im
	return im, nil
}

// dupFile duplicates the descriptor while the file is held open, so a close
// on the wayland goroutine cannot swap it for a reused number.
func dupFile(f *os.File) (int, error) {
	if f == nil {
		return -1, fmt.Errorf("dmabuf plane without file")
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd, dupErr := -1, error(nil)
	if err := raw.Control(func(v uintptr) { fd, dupErr = unix.FcntlInt(v, unix.F_DUPFD_CLOEXEC, 0) }); err != nil {
		return -1, err
	}
	return fd, dupErr
}

func (r *Renderer) release(im *imported) {
	if im.pool != 0 {
		r.dd.DestroyDescriptorPool(r.device, im.pool, nil)
	}
	if im.view != 0 {
		r.dd.DestroyImageView(r.device, im.view, nil)
	}
	if im.chromaView != 0 {
		r.dd.DestroyImageView(r.device, im.chromaView, nil)
	}
	if im.image != 0 {
		r.dd.DestroyImage(r.device, im.image, nil)
	}
	if im.memory != 0 {
		r.dd.FreeMemory(r.device, im.memory, nil)
	}
	for i := range im.memories {
		if im.memories[i] != 0 {
			r.dd.FreeMemory(r.device, im.memories[i], nil)
		}
	}
	if im.fd >= 0 {
		unix.Close(im.fd)
	}
	if im.yuv {
		for _, fd := range im.fds {
			if fd >= 0 {
				unix.Close(fd)
			}
		}
	}
}

// dropUnused frees imports not drawn for importTTL frames (the client
// moved on to other buffers or went away), once the last frame that drew
// them completed.
func (r *Renderer) dropUnused() {
	for id, im := range r.imports {
		if r.frame-im.last > importTTL {
			r.retire(im.last, func() { r.release(im) })
			delete(r.imports, id)
		}
	}
}

// dmaBufSync is struct dma_buf_export_sync_file.
type dmaBufSync struct {
	flags uint32
	fd    int32
}

// DMA_BUF_IOCTL_EXPORT_SYNC_FILE = _IOWR('b', 2, struct dma_buf_export_sync_file)
const ioctlExportSyncFile = 0xc0086202
const dmaBufSyncRead = 1

// appendReadFences waits on every distinct backing object's pending writes.
// Shared YUV planes have one implicit fence; disjoint planes have two.
func (r *Renderer) appendReadFences(waits []vk.Semaphore, im *imported) []vk.Semaphore {
	fds := [2]int{im.fd}
	count := 1
	if im.yuv {
		fds = im.fds
		if im.disjoint {
			count = 2
		}
	}
	for _, fd := range fds[:count] {
		arg := dmaBufSync{flags: dmaBufSyncRead, fd: -1}
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), ioctlExportSyncFile, uintptr(unsafe.Pointer(&arg))); errno != 0 || arg.fd < 0 {
			continue // Older kernels cannot export implicit fences.
		}
		if sem := r.importSyncFD(int(arg.fd)); sem != 0 {
			waits = append(waits, sem)
		}
	}
	return waits
}

// importFence makes a wait semaphore of a sync file. The file stays the
// caller's: Vulkan takes a duplicate, made while the file is held open.
func (r *Renderer) importFence(f *os.File) vk.Semaphore {
	fd, err := dupFile(f)
	if err != nil {
		return 0
	}
	return r.importSyncFD(fd)
}

// importSyncFD imports a sync file fd as a temporary semaphore payload;
// the fd is Vulkan's on success and closed on failure.
func (r *Renderer) importSyncFD(fd int) vk.Semaphore {
	var sem vk.Semaphore
	si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
	if r.dd.CreateSemaphore(r.device, &si, nil, &sem) != vk.Success {
		unix.Close(fd)
		return 0
	}
	imp := vk.ImportSemaphoreFdInfoKHR{SType: vk.StructureTypeImportSemaphoreFDInfoKHR, Semaphore: sem, Flags: vk.SemaphoreImportTemporaryBit, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit, Fd: int32(fd)}
	if r.dd.ImportSemaphoreFdKHR(r.device, &imp) != vk.Success {
		unix.Close(fd)
		r.dd.DestroySemaphore(r.device, sem, nil)
		return 0
	}
	return sem
}

// Probe reports the dmabuf formats of the device renderers use, or none
// when Vulkan or the extensions are missing.
func Probe() ports.DMABufSupport {
	r, err := New(1, 1)
	if err != nil {
		return ports.DMABufSupport{}
	}
	defer r.Close()
	return r.DMABuf()
}
