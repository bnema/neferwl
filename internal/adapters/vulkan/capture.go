package vulkan

import (
	_ "embed"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

//go:generate glslc -O --target-env=vulkan1.3 shaders/capture_sdr.frag -o shaders/capture_sdr.frag.spv

//go:embed shaders/capture_sdr.frag.spv
var captureFrag []byte

// Asynchronous capture (screencopy). BeginCapture records a copy of the
// last frame into one of captureSlots host-visible buffers and submits it
// on the frame queue right after the frame, signalling a fence and a
// semaphore exported as a sync file. A worker goroutine waits on the sync
// file and reads the mapped buffer; the owner never waits for the GPU.
// A slot is leased from BeginCapture to EndCapture; a returned slot is
// reused only once its fence polls signalled, so an early return (worker
// cancelled) never lets a running copy be recorded over.
//
// HDR frames are converted on the GPU by the capture pass (capture_sdr
// shader) into a reusable B8G8R8A8 image before the copy.

// captureSlots is how many captures may be leased at once.
const captureSlots = 2

// captureMaxBytes caps the host memory of one renderer's capture slots
// (captureSlots × width × height × 4): 128 MiB holds two 5K slots;
// larger outputs refuse captures.
const captureMaxBytes = 128 << 20

// captureBytesPerPixel is the size of one sRGB8 BGRA capture pixel.
const captureBytesPerPixel = 4

// captureCurve is the tone map of the capture pass on the largest
// component, mirrored from capture_sdr.frag: identity to the knee, then
// a rational shoulder (scale captureScale) that never reaches 1.
const (
	captureKnee  = 0.9
	captureScale = 0.5
)

func captureCurve(m float64) float64 {
	if m <= captureKnee {
		return m
	}
	t := (m - captureKnee) / captureScale
	return captureKnee + (1-captureKnee)*t/(1+t)
}

// captureSDR maps one linear BT.709 pixel in SDR-white units to the sRGB8
// bytes the capture pass produces (tests compare against it).
func captureSDR(c [3]float64) [3]uint8 {
	for i, v := range c {
		if math.IsNaN(v) {
			c[i] = 0
		}
		c[i] = math.Max(-65504, math.Min(65504, c[i]))
	}
	y := math.Max(0.2126*c[0]+0.7152*c[1]+0.0722*c[2], 0)
	lo := math.Min(math.Min(c[0], c[1]), c[2])
	if lo < 0 {
		t := math.Min(1, -lo/math.Max(y-lo, 1e-6))
		for i := range c {
			c[i] += t * (y - c[i])
		}
	}
	m := 0.0
	for i := range c {
		c[i] = math.Max(c[i], 0)
		m = math.Max(m, c[i])
	}
	if m > captureKnee {
		s := captureCurve(m) / m
		for i := range c {
			c[i] *= s
		}
	}
	var out [3]uint8
	for i, v := range c {
		out[i] = encodeSRGB8(float32(v))
	}
	return out
}

// encodeSRGB8 clips a linear-light value to [0, 1] and encodes it as sRGB.
func encodeSRGB8(f float32) byte {
	v := float64(f)
	if !(v > 0) { // also maps NaN to black
		return 0
	}
	v = math.Min(v, 1)
	if v <= .0031308 {
		v *= 12.92
	} else {
		v = 1.055*math.Pow(v, 1/2.4) - .055
	}
	return byte(math.Round(v * 255))
}

// captureSync is the fence and semaphore surface of a capture slot: the
// non-blocking poll, the sync file export and the semaphore replacement
// (a seam for tests; the device implements it).
type captureSync interface {
	// fenceStatus reports Success (signalled), NotReady/Timeout (still
	// running) or an error, without waiting.
	fenceStatus(f vk.Fence) vk.Result
	resetFence(f vk.Fence) vk.Result
	// exportSyncFD exports the semaphore's pending signal as a sync file
	// (-1: already signalled).
	exportSyncFD(s vk.Semaphore) (int32, vk.Result)
	newSemaphore() (vk.Semaphore, error)
	destroySemaphore(s vk.Semaphore)
}

// deviceSync is the captureSync of the renderer's device.
type deviceSync struct{ r *Renderer }

func (d deviceSync) fenceStatus(f vk.Fence) vk.Result {
	// A zero timeout only reports the fence state (the bindings lack
	// vkGetFenceStatus): Timeout while the copy runs.
	return d.r.dd.WaitForFences(d.r.device, 1, &f, 1, 0)
}

func (d deviceSync) resetFence(f vk.Fence) vk.Result {
	return d.r.dd.ResetFences(d.r.device, 1, &f)
}

func (d deviceSync) exportSyncFD(s vk.Semaphore) (int32, vk.Result) {
	var fd int32
	get := vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFDInfoKHR, Semaphore: s, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit}
	res := d.r.dd.GetSemaphoreFdKHR(d.r.device, &get, &fd)
	return fd, res
}

func (d deviceSync) newSemaphore() (vk.Semaphore, error) {
	export := vk.ExportSemaphoreCreateInfo{SType: vk.StructureTypeExportSemaphoreCreateInfo, HandleTypes: vk.ExternalSemaphoreHandleTypeSyncFDBit}
	si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo, Next: unsafe.Pointer(&export)}
	var s vk.Semaphore
	err := checked("vkCreateSemaphore(capture)", d.r.dd.CreateSemaphore(d.r.device, &si, nil, &s))
	return s, err
}

func (d deviceSync) destroySemaphore(s vk.Semaphore) { d.r.dd.DestroySemaphore(d.r.device, s, nil) }

// captureSlot is one readback buffer with its copy command and sync.
type captureSlot struct {
	buffer vk.Buffer
	memory vk.DeviceMemory
	mapped unsafe.Pointer
	cmd    vk.CommandBuffer
	fence  vk.Fence     // signalled by the copy; polled before reuse
	done   vk.Semaphore // signalled by the copy; exported as the sync file
	// leased: between BeginCapture and EndCapture. pending: submitted,
	// fence not yet seen signalled (set with leased, cleared by a poll).
	leased, pending bool
	// replaceDone: the last export failed, so done may stay signalled;
	// it is replaced once the fence polls signalled, before reuse.
	replaceDone bool
	// frame is the slot's lease object, reused for every lease: a
	// handle kept after EndCapture is invalid by contract (its next
	// state belongs to another lease).
	frame captureFrame
}

// captureFrame is the lease handed to the worker (ports.CaptureFrame).
// Its fields are written by the owner before the handoff and read by the
// worker; the owner touches them again only after EndCapture.
type captureFrame struct {
	r      *Renderer
	slot   *captureSlot
	done   *os.File
	pixels []byte // the slot's mapping, width*height*4
	w, h   int
	valid  bool // false after EndCapture
}

func (f *captureFrame) Done() *os.File { return f.done }

// Read copies region from the mapped slot as opaque BGRA rows.
func (f *captureFrame) Read(region image.Rectangle, dst []byte, stride int) error {
	if !f.valid {
		return errors.New("capture frame ended")
	}
	w, h := region.Dx(), region.Dy()
	need := w * captureBytesPerPixel
	if region.Empty() || !region.In(image.Rect(0, 0, f.w, f.h)) || stride < need || len(dst) < need {
		return errors.New("invalid capture region or destination")
	}
	// The last row must fit: (h-1)*stride + need <= len(dst), checked by
	// division so a huge stride cannot overflow the product.
	if h > 1 && stride > (len(dst)-need)/(h-1) {
		return errors.New("capture destination too small for stride")
	}
	src := f.pixels
	for y := range h {
		start := ((region.Min.Y+y)*f.w + region.Min.X) * captureBytesPerPixel
		row := dst[y*stride : y*stride+w*captureBytesPerPixel]
		copy(row, src[start:start+w*captureBytesPerPixel])
		// The output is opaque: x-format client buffers leave alpha undefined.
		for x := 3; x < len(row); x += captureBytesPerPixel {
			row[x] = 255
		}
	}
	return nil
}

// captureSize is the bytes of one slot, or an error over the cap.
func (r *Renderer) captureSize() (vk.DeviceSize, error) {
	size := uint64(r.width) * uint64(r.height) * captureBytesPerPixel
	if size == 0 || size > math.MaxInt || size*captureSlots > captureMaxBytes {
		return 0, fmt.Errorf("capture of %dx%d exceeds %d bytes", r.width, r.height, captureMaxBytes)
	}
	return vk.DeviceSize(size), nil
}

// createCaptureSlots allocates every slot on the first accepted capture.
func (r *Renderer) createCaptureSlots() (err error) {
	if r.captures != nil {
		return nil
	}
	size, err := r.captureSize()
	if err != nil {
		return err
	}
	d := r.dd
	cmds := make([]vk.CommandBuffer, captureSlots)
	ai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: r.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: captureSlots}
	if err := checked("vkAllocateCommandBuffers(capture)", d.AllocateCommandBuffers(r.device, &ai, &cmds[0])); err != nil {
		return err
	}
	// Every command buffer belongs to a slot before anything can fail,
	// so a failure frees them all.
	slots := make([]*captureSlot, captureSlots)
	for i := range slots {
		slots[i] = &captureSlot{cmd: cmds[i]}
	}
	defer func() {
		if err != nil {
			for _, s := range slots {
				r.freeCaptureSlot(s)
			}
		}
	}()
	for _, s := range slots {
		bi := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: size, Usage: vk.BufferUsageTransferDstBit, SharingMode: vk.SharingModeExclusive}
		if err := checked("vkCreateBuffer(capture)", d.CreateBuffer(r.device, &bi, nil, &s.buffer)); err != nil {
			return err
		}
		var req vk.MemoryRequirements
		d.GetBufferMemoryRequirements(r.device, s.buffer, &req)
		// The worker reads the whole buffer: prefer cached host memory.
		kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit|vk.MemoryPropertyHostCachedBit)
		if err != nil {
			kind, err = r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
		}
		if err != nil {
			return err
		}
		alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
		if err := checked("vkAllocateMemory(capture)", d.AllocateMemory(r.device, &alloc, nil, &s.memory)); err != nil {
			return err
		}
		if err := checked("vkBindBufferMemory(capture)", d.BindBufferMemory(r.device, s.buffer, s.memory, 0)); err != nil {
			return err
		}
		if err := checked("vkMapMemory(capture)", d.MapMemory(r.device, s.memory, 0, size, 0, &s.mapped)); err != nil {
			return err
		}
		fi := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
		if err := checked("vkCreateFence(capture)", d.CreateFence(r.device, &fi, nil, &s.fence)); err != nil {
			return err
		}
		if s.done, err = r.captureSync().newSemaphore(); err != nil {
			return err
		}
		s.frame = captureFrame{r: r, slot: s, w: r.width, h: r.height, pixels: unsafe.Slice((*byte)(s.mapped), int(size))}
	}
	r.captures = slots
	return nil
}

// captureSync is the slot sync seam: the device unless a test replaced it.
func (r *Renderer) captureSync() captureSync {
	if r.sync == nil {
		r.sync = deviceSync{r}
	}
	return r.sync
}

// freeCaptureSlot destroys a slot; the device must be idle for it.
func (r *Renderer) freeCaptureSlot(s *captureSlot) {
	d := r.dd
	s.frame.valid = false
	if s.frame.done != nil {
		s.frame.done.Close()
		s.frame.done = nil
	}
	if s.done != 0 {
		r.captureSync().destroySemaphore(s.done)
	}
	if s.fence != 0 {
		d.DestroyFence(r.device, s.fence, nil)
	}
	if s.mapped != nil {
		d.UnmapMemory(r.device, s.memory)
	}
	if s.buffer != 0 {
		d.DestroyBuffer(r.device, s.buffer, nil)
	}
	if s.memory != 0 {
		d.FreeMemory(r.device, s.memory, nil)
	}
	if s.cmd != 0 {
		d.FreeCommandBuffers(r.device, r.pool, 1, &s.cmd)
	}
	*s = captureSlot{}
}

// destroyCaptures frees the slots and the HDR conversion pass. Every
// lease is invalid afterwards; the device is idle (Close).
func (r *Renderer) destroyCaptures() {
	for _, s := range r.captures {
		r.freeCaptureSlot(s)
	}
	r.captures = nil
	r.destroyCapturePass()
	r.freeTarget(&r.captureSDR)
}

// freeCapture picks a slot: unleased and, if a copy was submitted, its
// fence signalled (polled, never waited). Nil when none is free; an
// error when the device fails to report or reset a fence.
func (r *Renderer) freeCapture() (*captureSlot, error) {
	sync := r.captureSync()
	for _, s := range r.captures {
		if s.leased {
			continue
		}
		if s.pending {
			switch res := sync.fenceStatus(s.fence); res {
			case vk.Success:
			case vk.Timeout, vk.NotReady:
				continue
			default:
				return nil, checked("vkWaitForFences(capture poll)", res)
			}
			if err := checked("vkResetFences(capture)", sync.resetFence(s.fence)); err != nil {
				return nil, err
			}
			s.pending = false
		}
		if s.replaceDone {
			// The copy is done. Retry a failed replacement before reuse,
			// even if its fence was already reset on the previous attempt.
			if s.done != 0 {
				sync.destroySemaphore(s.done)
				s.done = 0
			}
			done, err := sync.newSemaphore()
			if err != nil {
				return nil, err
			}
			s.done, s.replaceDone = done, false
		}
		return s, nil
	}
	return nil, nil
}

// CaptureSupported reports whether BeginCapture can work on this device: it
// needs sync-file export, which software devices such as lavapipe lack.
func (r *Renderer) CaptureSupported() bool {
	return r.dd != nil && r.syncFD && r.dd.HasGetSemaphoreFdKHR()
}

// BeginCapture submits a GPU copy of the last frame and leases its slot.
// The lease is valid until EndCapture and no longer: the same object
// serves the slot's next lease.
func (r *Renderer) BeginCapture() (ports.CaptureFrame, error) {
	f, err := r.beginCapture(false)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// beginCapture is BeginCapture; debug (Pixels) skips the sync file, the
// caller waits on the slot's fence instead.
func (r *Renderer) beginCapture(debug bool) (*captureFrame, error) {
	if r.device == 0 || r.dd == nil {
		return nil, errors.New("renderer closed")
	}
	if r.last == nil || r.last.image == 0 {
		return nil, errors.New("no rendered frame")
	}
	if !debug && (!r.syncFD || !r.dd.HasGetSemaphoreFdKHR()) {
		return nil, errors.New("capture needs sync file export")
	}
	if r.submitted != r.frame {
		return nil, errors.New("last frame not submitted")
	}
	if err := r.createCaptureSlots(); err != nil {
		return nil, err
	}
	hdr := r.last == &r.hdrOwn
	if hdr {
		if err := r.createCapturePass(); err != nil {
			return nil, err
		}
	}
	s, err := r.freeCapture()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ports.ErrCaptureBusy
	}
	d := r.dd
	cmd := s.cmd
	if err := checked("vkResetCommandBuffer(capture)", d.ResetCommandBuffer(cmd, 0)); err != nil {
		return nil, err
	}
	begin := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageOneTimeSubmitBit}
	if err := checked("vkBeginCommandBuffer(capture)", d.BeginCommandBuffer(cmd, &begin)); err != nil {
		return nil, err
	}
	if hdr {
		r.recordCaptureHDR(cmd, s)
	} else {
		r.recordCaptureCopy(cmd, r.last, r.last.layout, s)
	}
	if err := checked("vkEndCommandBuffer(capture)", d.EndCommandBuffer(cmd)); err != nil {
		return nil, err
	}
	// After the frame on the same queue: the copy reads what it drew, and
	// the next frame's first barrier (transfer source stage) orders its
	// writes after this read.
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &cmd}
	if !debug {
		// The export below takes the signal; a debug capture waits on
		// the fence only, so the semaphore must not be signalled (a
		// binary semaphore is signalled once, then waited or exported).
		submit.SignalSemaphoreCount, submit.SignalSemaphores = 1, &s.done
	}
	if err := checked("vkQueueSubmit(capture)", d.QueueSubmit(r.queue, 1, &submit, s.fence)); err != nil {
		return nil, err
	}
	s.pending = true
	f := &s.frame
	f.done = nil
	if !debug {
		fd, res := r.captureSync().exportSyncFD(s.done)
		if res != vk.Success {
			// The copy runs untracked by any sync file: the slot stays
			// pending until its fence polls signalled, and only then is
			// the semaphore (still to be signalled) replaced.
			s.replaceDone = true
			return nil, checked("vkGetSemaphoreFdKHR(capture)", res)
		}
		if fd >= 0 {
			// -1: already signalled at export, nothing to wait for.
			f.done = os.NewFile(uintptr(fd), "neferwl-capture")
			if f.done == nil {
				return nil, fmt.Errorf("invalid capture sync file %d", fd)
			}
		}
	}
	s.leased, f.valid = true, true
	return f, nil
}

// EndCapture returns a lease. The slot's copy may still run: it stays
// pending until a later BeginCapture polls its fence.
func (r *Renderer) EndCapture(cf ports.CaptureFrame) {
	f, ok := cf.(*captureFrame)
	if !ok || f == nil || f.r != r || !f.valid {
		return
	}
	f.valid = false
	if f.done != nil {
		f.done.Close()
		f.done = nil
	}
	f.slot.leased = false
}

// debugCapture leases a capture and waits for it on the CPU (slot fence):
// Pixels only, never the protocol path. It does not take leased slots.
func (r *Renderer) debugCapture() (*captureFrame, error) {
	f, err := r.beginCapture(true)
	if err != nil {
		return nil, err
	}
	if err := checked("vkWaitForFences(capture)", r.dd.WaitForFences(r.device, 1, &f.slot.fence, 1, math.MaxUint64)); err != nil {
		r.EndCapture(f)
		return nil, err
	}
	return f, nil
}

// readHDRTarget copies the exported PQ target last rendered (XRGB2101010
// words, one per pixel) through a free capture slot and waits for the GPU.
// Exported targets are transfer sources only when hdrReadback is set; it
// errors when there is no HDR frame to read, every slot is leased or the
// copy fails. read gets the words in the slot's mapped memory, valid only
// during the call: copy what must outlive it.
func (r *Renderer) readHDRTarget(read func(words []uint32)) error {
	switch {
	case r.device == 0 || r.dd == nil:
		return errors.New("renderer closed")
	case r.hdrNits == 0 || !r.hdrReadback:
		return errors.New("HDR readback not enabled")
	case r.lastHDRTarget < 0 || r.lastHDRTarget >= len(r.targets):
		return errors.New("no exported HDR target")
	case r.last != &r.hdrOwn:
		return errors.New("no HDR frame rendered")
	case r.submitted != r.frame:
		return errors.New("last frame not submitted")
	}
	if err := r.waitFrame(r.submitted); err != nil {
		return err
	}
	if err := r.createCaptureSlots(); err != nil {
		return err
	}
	slot, err := r.freeCapture()
	if err != nil {
		return err
	}
	if slot == nil {
		return ports.ErrCaptureBusy
	}
	target := r.targets[r.lastHDRTarget]
	err = r.oneShot(func(cmd vk.CommandBuffer) {
		d := r.dd
		// Acquire from the display, copy, release back.
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: vk.ImageLayoutGeneral, NewLayout: vk.ImageLayoutTransferSrcOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.family, Image: target.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTopOfPipeBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
		region := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
		d.CmdCopyImageToBuffer(cmd, target.image, vk.ImageLayoutTransferSrcOptimal, slot.buffer, 1, &region)
		hb := vk.BufferMemoryBarrier{SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: vk.AccessTransferWriteBit, DstAccessMask: vk.AccessHostReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Buffer: slot.buffer, Size: wholeSize}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageHostBit, 0, 0, nil, 1, &hb, 0, nil)
		b.SrcAccessMask, b.DstAccessMask = vk.AccessTransferReadBit, 0
		b.OldLayout, b.NewLayout = vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
	})
	if err != nil {
		return err
	}
	read(unsafe.Slice((*uint32)(slot.mapped), r.width*r.height))
	return nil
}

// recordCaptureCopy copies image t (in layout) into the slot's buffer.
// Exported targets are borrowed from the display without a layout
// change: the display may be reading them at the same time.
func (r *Renderer) recordCaptureCopy(cmd vk.CommandBuffer, t *target, layout vk.ImageLayout, s *captureSlot) {
	d := r.dd
	if t.exported {
		// Acquire from the display. The frame released the image at
		// its color attachment stage: this acquire waits for that.
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: layout, NewLayout: layout, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.family, Image: t.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageAllCommandsBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
	}
	region := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
	d.CmdCopyImageToBuffer(cmd, t.image, layout, s.buffer, 1, &region)
	hb := vk.BufferMemoryBarrier{SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: vk.AccessTransferWriteBit, DstAccessMask: vk.AccessHostReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Buffer: s.buffer, Size: wholeSize}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageHostBit, 0, 0, nil, 1, &hb, 0, nil)
	if t.exported {
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessTransferReadBit, OldLayout: layout, NewLayout: layout, SrcQueueFamilyIndex: r.family, DstQueueFamilyIndex: vk.QueueFamilyForeignEXT, Image: t.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
	}
}

// recordCaptureHDR converts hdrOwn (TRANSFER_SRC after an HDR frame) into
// the sRGB8 capture image with the capture pass, then copies that. hdrOwn
// is put back in TRANSFER_SRC, as the next frame expects.
func (r *Renderer) recordCaptureHDR(cmd vk.CommandBuffer, s *captureSlot) {
	d, p, out := r.dd, &r.capturePass, &r.captureSDR
	in := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessTransferReadBit, DstAccessMask: vk.AccessShaderReadBit, OldLayout: vk.ImageLayoutTransferSrcOptimal, NewLayout: vk.ImageLayoutShaderReadOnlyOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: r.hdrOwn.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageFragmentShaderBit, 0, 0, nil, 0, nil, 1, &in)
	// The previous capture read the image as a transfer source.
	b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessTransferReadBit, DstAccessMask: vk.AccessColorAttachmentWriteBit, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutColorAttachmentOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: out.image, SubresourceRange: colorRange}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageColorAttachmentOutputBit, 0, 0, nil, 0, nil, 1, &b)
	attachment := vk.RenderingAttachmentInfo{SType: vk.StructureTypeRenderingAttachmentInfo, ImageView: out.view, ImageLayout: vk.ImageLayoutColorAttachmentOptimal, LoadOp: vk.AttachmentLoadOpDontCare, StoreOp: vk.AttachmentStoreOpStore}
	info := vk.RenderingInfo{SType: vk.StructureTypeRenderingInfo, RenderArea: vk.Rect2D{Extent: vk.Extent2D{Width: uint32(r.width), Height: uint32(r.height)}}, LayerCount: 1, ColorAttachmentCount: 1, ColorAttachments: &attachment}
	d.CmdBeginRendering(cmd, &info)
	d.CmdBindPipeline(cmd, vk.PipelineBindPointGraphics, p.pipeline)
	d.CmdBindDescriptorSets(cmd, vk.PipelineBindPointGraphics, p.layout, 0, 1, &p.set, 0, nil)
	d.CmdDraw(cmd, 3, 1, 0, 0)
	d.CmdEndRendering(cmd)
	in.SrcAccessMask, in.DstAccessMask = vk.AccessShaderReadBit, vk.AccessTransferReadBit
	in.OldLayout, in.NewLayout = vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(cmd, vk.PipelineStageFragmentShaderBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &in)
	b.SrcAccessMask, b.DstAccessMask = vk.AccessColorAttachmentWriteBit, vk.AccessTransferReadBit
	b.OldLayout, b.NewLayout = vk.ImageLayoutColorAttachmentOptimal, vk.ImageLayoutTransferSrcOptimal
	d.CmdPipelineBarrier(cmd, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
	r.recordCaptureCopy(cmd, out, vk.ImageLayoutTransferSrcOptimal, s)
}

// createCapturePass makes the sRGB8 image and the conversion pipeline
// once, on the first HDR capture.
func (r *Renderer) createCapturePass() (err error) {
	if r.capturePass.pipeline != 0 {
		return nil
	}
	if r.hdrOwn.view == 0 {
		return errors.New("no HDR composition image")
	}
	defer func() {
		if err != nil {
			r.destroyCapturePass()
			r.freeTarget(&r.captureSDR)
		}
	}()
	d, p, t := r.dd, &r.capturePass, &r.captureSDR
	if t.image == 0 {
		ii := vk.ImageCreateInfo{SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vk.FormatB8g8r8a8Unorm, Extent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}, MipLevels: 1, ArrayLayers: 1, Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal, Usage: targetUsage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined}
		if err := checked("vkCreateImage(capture)", d.CreateImage(r.device, &ii, nil, &t.image)); err != nil {
			return err
		}
		var req vk.MemoryRequirements
		d.GetImageMemoryRequirements(r.device, t.image, &req)
		kind, err := r.findMemoryType(req.MemoryTypeBits, vk.MemoryPropertyDeviceLocalBit)
		if err != nil {
			return err
		}
		alloc := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: kind}
		if err := checked("vkAllocateMemory(capture)", d.AllocateMemory(r.device, &alloc, nil, &t.memory)); err != nil {
			return err
		}
		if err := checked("vkBindImageMemory(capture)", d.BindImageMemory(r.device, t.image, t.memory, 0)); err != nil {
			return err
		}
		if err := r.createView(t); err != nil {
			return err
		}
	}
	binding := vk.DescriptorSetLayoutBinding{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFragmentBit}
	sli := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 1, Bindings: &binding}
	if err := checked("vkCreateDescriptorSetLayout(capture)", d.CreateDescriptorSetLayout(r.device, &sli, nil, &p.setLayout)); err != nil {
		return err
	}
	pli := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, SetLayouts: &p.setLayout}
	if err := checked("vkCreatePipelineLayout(capture)", d.CreatePipelineLayout(r.device, &pli, nil, &p.layout)); err != nil {
		return err
	}
	si := vk.SamplerCreateInfo{SType: vk.StructureTypeSamplerCreateInfo, MagFilter: vk.FilterNearest, MinFilter: vk.FilterNearest, AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge}
	if err := checked("vkCreateSampler(capture)", d.CreateSampler(r.device, &si, nil, &p.sampler)); err != nil {
		return err
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1}
	dpi := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 1, PoolSizes: &size}
	if err := checked("vkCreateDescriptorPool(capture)", d.CreateDescriptorPool(r.device, &dpi, nil, &p.pool)); err != nil {
		return err
	}
	dai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: p.pool, DescriptorSetCount: 1, SetLayouts: &p.setLayout}
	if err := checked("vkAllocateDescriptorSets(capture)", d.AllocateDescriptorSets(r.device, &dai, &p.set)); err != nil {
		return err
	}
	img := vk.DescriptorImageInfo{Sampler: p.sampler, ImageView: r.hdrOwn.view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	write := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: p.set, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &img}
	d.UpdateDescriptorSets(r.device, 1, &write, 0, nil)
	// The pipeline is last: it marks the pass complete.
	return r.createGraphicsPipeline(hdrVert, captureFrag, vk.FormatB8g8r8a8Unorm, p.layout, false, &p.pipeline)
}

// destroyCapturePass frees the conversion pipeline (device idle).
func (r *Renderer) destroyCapturePass() {
	if r.dd == nil {
		return
	}
	d, p := r.dd, &r.capturePass
	if p.pool != 0 {
		d.DestroyDescriptorPool(r.device, p.pool, nil)
	}
	if p.sampler != 0 {
		d.DestroySampler(r.device, p.sampler, nil)
	}
	if p.pipeline != 0 {
		d.DestroyPipeline(r.device, p.pipeline, nil)
	}
	if p.layout != 0 {
		d.DestroyPipelineLayout(r.device, p.layout, nil)
	}
	if p.setLayout != 0 {
		d.DestroyDescriptorSetLayout(r.device, p.setLayout, nil)
	}
	*p = hdrPass{}
}
