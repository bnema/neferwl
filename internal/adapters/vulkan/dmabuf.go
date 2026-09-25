package vulkan

import (
	"fmt"
	"os"
	"slices"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

// dmabuf import (linux-dmabuf clients): each client buffer becomes a
// VkImage bound to its imported memory, cached by buffer ID while the
// client keeps attaching it, and blitted into the output image. Sync is
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

// hasExtensions reports whether the device offers every dmabuf extension.
func (r *Renderer) hasExtensions(physical vk.PhysicalDevice) bool {
	var n uint32
	if r.id.EnumerateDeviceExtensionProperties(physical, nil, &n, nil) != vk.Success || n == 0 {
		return false
	}
	props := make([]vk.ExtensionProperties, n)
	if r.id.EnumerateDeviceExtensionProperties(physical, nil, &n, &props[0]) != vk.Success {
		return false
	}
	have := map[string]bool{}
	for _, p := range props[:n] {
		have[cstring(p.ExtensionName[:])] = true
	}
	for _, e := range deviceExtensions {
		if !have[e] {
			return false
		}
	}
	return true
}

func cstring(b []byte) string {
	if i := slices.Index(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// probeDMABuf lists the fourcc/modifier pairs the device imports as
// single-plane blit sources, and the render node.
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
			// Multi-plane modifiers (compression metadata) are left to a
			// later step; the blit needs a source.
			if m.DrmFormatModifierPlaneCount != 1 || m.DrmFormatModifierTilingFeatures&vk.FormatFeatureBlitSrcBit == 0 {
				continue
			}
			if r.importable(physical, f.format, m.DrmFormatModifier) {
				sup.Formats = append(sup.Formats, ports.DMABufFormat{Format: f.fourcc, Modifier: m.DrmFormatModifier})
			}
		}
	}
	return sup
}

// importable asks whether an image of that format and modifier can be
// imported from a dmabuf.
func (r *Renderer) importable(physical vk.PhysicalDevice, format vk.Format, modifier uint64) bool {
	mod := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{SType: vk.StructureTypePhysicalDeviceImageDRMFormatModifierInfoEXT, DrmFormatModifier: modifier, SharingMode: vk.SharingModeExclusive}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, Next: unsafe.Pointer(&mod), HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	info := vk.PhysicalDeviceImageFormatInfo2{SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, Next: unsafe.Pointer(&ext), Format: format, Type: vk.ImageType2d, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: vk.ImageUsageTransferSrcBit}
	extOut := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	out := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, Next: unsafe.Pointer(&extOut)}
	if r.id.GetPhysicalDeviceImageFormatProperties2(physical, &info, &out) != vk.Success {
		return false
	}
	return extOut.ExternalMemoryProperties.ExternalMemoryFeatures&vk.ExternalMemoryFeatureImportableBit != 0
}

// DMABuf reports the formats this renderer imports.
func (r *Renderer) DMABuf() ports.DMABufSupport { return r.dmabuf }

// imported is a client dmabuf bound to a VkImage.
type imported struct {
	image  vk.Image
	memory vk.DeviceMemory
	fd     int // our duplicate of plane 0, for implicit sync
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
	if !ok || len(b.Planes) != 1 || b.Width <= 0 || b.Height <= 0 {
		return nil, fmt.Errorf("dmabuf format %#x with %d planes unsupported", b.Format, len(b.Planes))
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
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: format, Extent: vk.Extent3D{Width: uint32(b.Width), Height: uint32(b.Height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDRMFormatModifierEXT, Usage: vk.ImageUsageTransferSrcBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
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
	if im.image != 0 {
		r.dd.DestroyImage(r.device, im.image, nil)
	}
	if im.memory != 0 {
		r.dd.FreeMemory(r.device, im.memory, nil)
	}
	if im.fd >= 0 {
		unix.Close(im.fd)
	}
}

// dropUnused frees imports not drawn for importTTL frames: the client moved
// on to other buffers or went away. Called after a frame completes.
func (r *Renderer) dropUnused() {
	r.frame++
	for id, im := range r.imports {
		if r.frame-im.last > importTTL {
			r.release(im)
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

// readFence returns a semaphore signalled when the client's pending writes
// to the buffer finish, or 0 when the kernel cannot export them (older than
// 5.20): the buffer is then read as is.
func (r *Renderer) readFence(im *imported) vk.Semaphore {
	arg := dmaBufSync{flags: dmaBufSyncRead, fd: -1}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(im.fd), ioctlExportSyncFile, uintptr(unsafe.Pointer(&arg))); errno != 0 || arg.fd < 0 {
		return 0
	}
	var sem vk.Semaphore
	si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
	if r.dd.CreateSemaphore(r.device, &si, nil, &sem) != vk.Success {
		unix.Close(int(arg.fd))
		return 0
	}
	imp := vk.ImportSemaphoreFdInfoKHR{SType: vk.StructureTypeImportSemaphoreFDInfoKHR, Semaphore: sem, Flags: vk.SemaphoreImportTemporaryBit, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit, Fd: arg.fd}
	if r.dd.ImportSemaphoreFdKHR(r.device, &imp) != vk.Success {
		unix.Close(int(arg.fd))
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
