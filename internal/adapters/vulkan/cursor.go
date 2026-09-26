package vulkan

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Cursor images for the display's cursor plane: two linear B8G8R8A8
// images in host-visible memory, exported as dmabufs. The CPU writes a
// cursor image only when the cursor changes, never per frame.

type cursorImage struct {
	image  vk.Image
	memory vk.DeviceMemory
	mapped unsafe.Pointer
	pitch  int
	size   int
}

// fourccARGB is DRM_FORMAT_ARGB8888: B8G8R8A8 in memory.
const fourccARGB = 'A' | 'R'<<8 | '2'<<16 | '4'<<24

// CursorBuffers allocates the two cursor images, replacing earlier ones.
func (r *Renderer) CursorBuffers(size int) ([2]ports.DMABuf, error) {
	var out [2]ports.DMABuf
	r.dropCursors()
	if r.dd.GetMemoryFdKHR == nil || size <= 0 || size > 512 {
		return out, errors.New("device cannot export cursor images")
	}
	for i := range r.cursors {
		c, buf, err := r.cursorImage(size)
		if err != nil {
			r.dropCursors()
			for _, b := range out[:i] {
				b.Planes[0].File.Close()
			}
			return [2]ports.DMABuf{}, err
		}
		r.cursors[i], out[i] = c, buf
	}
	return out, nil
}

func (r *Renderer) cursorImage(size int) (*cursorImage, ports.DMABuf, error) {
	d := r.dd
	c := &cursorImage{size: size}
	ok := false
	defer func() {
		if !ok {
			r.freeCursor(c)
		}
	}()
	external := vk.ExternalMemoryImageCreateInfo{SType: vk.StructureTypeExternalMemoryImageCreateInfo, HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, Next: unsafe.Pointer(&external), ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: vk.Extent3D{Width: uint32(size), Height: uint32(size), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingLinear, Usage: vk.ImageUsageTransferSrcBit, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutPreinitialized}
	if err := checked("vkCreateImage(cursor)", d.CreateImage(r.device, &ii, nil, &c.image)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	var req vk.MemoryRequirements
	d.GetImageMemoryRequirements(r.device, c.image, &req)
	kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
	if err != nil {
		return nil, ports.DMABuf{}, err
	}
	dedicated := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: c.image}
	export := vk.ExportMemoryAllocateInfo{SType: vk.StructureTypeExportMemoryAllocateInfo, Next: unsafe.Pointer(&dedicated), HandleTypes: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, Next: unsafe.Pointer(&export), AllocationSize: req.Size, MemoryTypeIndex: kind}
	if err := checked("vkAllocateMemory(cursor)", d.AllocateMemory(r.device, &alloc, nil, &c.memory)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	if err := checked("vkBindImageMemory(cursor)", d.BindImageMemory(r.device, c.image, c.memory, 0)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	var layout vk.SubresourceLayout
	sub := vk.ImageSubresource{AspectMask: vk.ImageAspectColorBit}
	d.GetImageSubresourceLayout(r.device, c.image, &sub, &layout)
	c.pitch = int(layout.RowPitch)
	// Cursor planes scan out packed rows from the start of the buffer.
	if c.pitch != size*4 || layout.Offset != 0 {
		return nil, ports.DMABuf{}, fmt.Errorf("cursor image layout pitch %d offset %d, want pitch %d offset 0", c.pitch, layout.Offset, size*4)
	}
	if err := checked("vkMapMemory(cursor)", d.MapMemory(r.device, c.memory, 0, vk.DeviceSize(req.Size), 0, &c.mapped)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	c.mapped = unsafe.Add(c.mapped, layout.Offset)
	clear(unsafe.Slice((*byte)(c.mapped), c.pitch*size))
	var fd int32
	get := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFDInfoKHR, Memory: c.memory, HandleType: vk.ExternalMemoryHandleTypeDMABUFBitEXT}
	if err := checked("vkGetMemoryFdKHR(cursor)", d.GetMemoryFdKHR(r.device, &get, &fd)); err != nil {
		return nil, ports.DMABuf{}, err
	}
	f := os.NewFile(uintptr(fd), "nefertty-cursor")
	if f == nil {
		return nil, ports.DMABuf{}, fmt.Errorf("invalid exported fd %d", fd)
	}
	ok = true
	return c, ports.DMABuf{Width: size, Height: size, Format: fourccARGB, Modifier: 0, Planes: []ports.DMABufPlane{{File: f, Offset: uint32(layout.Offset), Stride: uint32(layout.RowPitch)}}}, nil
}

// WriteCursor copies premultiplied ARGB8888 pixels (w×h, w*4 per row)
// into cursor image i at its row pitch, cropped and cleared around.
func (r *Renderer) WriteCursor(i int, pixels []byte, w, h int) error {
	if i < 0 || i >= len(r.cursors) || r.cursors[i] == nil {
		return errors.New("no cursor image")
	}
	if w < 0 || h < 0 || len(pixels) < w*h*4 {
		return errors.New("short cursor image")
	}
	c := r.cursors[i]
	mem := unsafe.Slice((*byte)(c.mapped), c.pitch*c.size)
	clear(mem)
	cw := min(w, c.size) * 4
	for y := range min(h, c.size) {
		copy(mem[y*c.pitch:y*c.pitch+cw], pixels[y*w*4:y*w*4+cw])
	}
	return nil
}

func (r *Renderer) freeCursor(c *cursorImage) {
	if c.mapped != nil {
		r.dd.UnmapMemory(r.device, c.memory)
		c.mapped = nil
	}
	if c.image != 0 {
		r.dd.DestroyImage(r.device, c.image, nil)
		c.image = 0
	}
	if c.memory != 0 {
		r.dd.FreeMemory(r.device, c.memory, nil)
		c.memory = 0
	}
}

func (r *Renderer) dropCursors() {
	for i, c := range r.cursors {
		if c != nil {
			r.freeCursor(c)
			r.cursors[i] = nil
		}
	}
}
