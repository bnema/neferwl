package vulkan

import (
	"fmt"
	"image"
	"math"
	"os"
	"runtime"
	"slices"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Frames are recorded into a ring of slots, one per frame in flight. The
// CPU never waits for a frame it just submitted: Render returns a sync
// file the display waits on (KMS IN_FENCE_FD). A slot's fence is waited
// only when the slot is reused, and objects a frame read (imports, GPU
// buffers, client semaphores) are freed only once that frame completed.

// frameSlots is how many frames may be in flight: one per output target.
const frameSlots = 2

// frameSlot is the command buffer and sync objects of one frame in flight.
type frameSlot struct {
	cmd   vk.CommandBuffer
	fence vk.Fence
	// done is signalled with the frame and exported as its sync file.
	done  vk.Semaphore
	frame uint64 // the frame recorded last, 0 before any
	busy  bool   // submitted, fence not yet seen signalled
}

// deferredFree runs once frame completed.
type deferredFree struct {
	frame uint64
	free  func()
}

func (r *Renderer) createSlots() error {
	d := r.dd
	cmds := make([]vk.CommandBuffer, frameSlots)
	ai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: r.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: frameSlots}
	if err := checked("vkAllocateCommandBuffers", d.AllocateCommandBuffers(r.device, &ai, &cmds[0])); err != nil {
		return err
	}
	for i := range r.slots {
		s := &r.slots[i]
		s.cmd = cmds[i]
		fi := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
		if err := checked("vkCreateFence", d.CreateFence(r.device, &fi, nil, &s.fence)); err != nil {
			return err
		}
		if err := r.newDone(s); err != nil {
			return err
		}
	}
	return nil
}

// newDone creates a slot's frame semaphore, exportable as a sync file when
// the device allows it; a device that refuses falls back to waiting for
// frames on the CPU (syncFD off).
func (r *Renderer) newDone(s *frameSlot) error {
	d := r.dd
	si := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
	export := vk.ExportSemaphoreCreateInfo{SType: vk.StructureTypeExportSemaphoreCreateInfo, HandleTypes: vk.ExternalSemaphoreHandleTypeSyncFDBit}
	if r.syncFD {
		si.Next = unsafe.Pointer(&export)
		if d.CreateSemaphore(r.device, &si, nil, &s.done) == vk.Success {
			return nil
		}
		r.syncFD = false
		si.Next = nil
	}
	return checked("vkCreateSemaphore", d.CreateSemaphore(r.device, &si, nil, &s.done))
}

func (r *Renderer) destroySlots() {
	d := r.dd
	for i := range r.slots {
		s := &r.slots[i]
		if s.fence != 0 {
			d.DestroyFence(r.device, s.fence, nil)
		}
		if s.done != 0 {
			d.DestroySemaphore(r.device, s.done, nil)
		}
		if s.cmd != 0 {
			d.FreeCommandBuffers(r.device, r.pool, 1, &s.cmd)
		}
		*s = frameSlot{}
	}
}

// waitFrame blocks until frame n completed. Frames not submitted yet and
// frames known done return at once. Every slot holding a frame up to n
// is waited and reset, so its fence is unsignalled before reuse.
func (r *Renderer) waitFrame(n uint64) error {
	n = min(n, r.submitted)
	if n <= r.completed {
		return nil
	}
	for i := range r.slots {
		s := &r.slots[i]
		if !s.busy || s.frame > n {
			continue
		}
		if err := checked("vkWaitForFences", r.dd.WaitForFences(r.device, 1, &s.fence, 1, math.MaxUint64)); err != nil {
			return err
		}
		if err := checked("vkResetFences", r.dd.ResetFences(r.device, 1, &s.fence)); err != nil {
			return err
		}
		s.busy = false
	}
	r.completed = n
	r.runDeferred()
	return nil
}

// retire frees an object once the frame that last used it completed.
func (r *Renderer) retire(frame uint64, free func()) {
	if frame <= r.completed {
		free()
		return
	}
	r.deferred = append(r.deferred, deferredFree{frame: frame, free: free})
}

func (r *Renderer) runDeferred() {
	r.deferred = slices.DeleteFunc(r.deferred, func(f deferredFree) bool {
		if f.frame <= r.completed {
			f.free()
			return true
		}
		return false
	})
}

// idle records that the device finished every submitted frame.
func (r *Renderer) idle() {
	for i := range r.slots {
		s := &r.slots[i]
		if s.busy {
			_ = r.dd.ResetFences(r.device, 1, &s.fence)
			s.busy = false
		}
	}
	// Frames recorded but never submitted have no GPU work either.
	r.completed = r.frame
	r.runDeferred()
}

// Render draws a frame into the current target. done is signalled when
// the GPU finished it; nil when the device cannot export a sync file,
// then the frame is finished on return.
func (r *Renderer) Render(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent) (done *os.File, err error) {
	// A frame that fails before its submit keeps its number: objects it
	// touched are freed once a later frame completes (never early).
	r.frame++
	slot := &r.slots[r.frame%frameSlots]
	// The slot's previous frame must be done before its command buffer
	// is recorded again.
	if slot.busy {
		if err := r.waitFrame(slot.frame); err != nil {
			return nil, err
		}
	}
	tg := r.target()
	hdr := r.hdrNits > 0 && len(r.targets) > 0
	if r.hdrNits > 0 && !hdr {
		return nil, fmt.Errorf("HDR composition requires an exported target")
	}
	if hdr {
		tg = &r.hdrOwn
	}
	dmg := r.frameDamage(tg, s, image.Rect(0, 0, r.width, r.height))
	ds := r.draws(s, contents, dmg)
	dmg.finish()
	r.dropPools()
	partial := !dmg.all()
	if partial {
		// The pass keeps the image: repaint the background under the
		// region, then everything clipped to it.
		// Keep the background first without allocating a second draw slice.
		ds = append(ds, draw{})
		copy(ds[1:], ds[:len(ds)-1])
		ds[0] = r.fillDraw(dmg.area, parseColor(s.Background))
		ds = dmg.clip(ds)
		r.scratchDraws = ds
		r.redrawn += dmg.area.Dx() * dmg.area.Dy()
	} else {
		r.redrawn += r.width * r.height
	}
	d := r.dd
	cmd := slot.cmd
	if err := checked("vkResetCommandBuffer", d.ResetCommandBuffer(cmd, 0)); err != nil {
		return nil, err
	}
	begin := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageOneTimeSubmitBit}
	if err := checked("vkBeginCommandBuffer", d.BeginCommandBuffer(cmd, &begin)); err != nil {
		return nil, err
	}
	// A full redraw clears the target: its old contents never matter
	// (UNDEFINED). A partial one keeps them.
	b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessColorAttachmentWriteBit, OldLayout: vk.ImageLayoutUndefined, NewLayout: vk.ImageLayoutColorAttachmentOptimal, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Image: tg.image, SubresourceRange: colorRange}
	if partial {
		b.OldLayout = tg.layout
		b.DstAccessMask |= vk.AccessColorAttachmentReadBit
	}
	if tg.exported {
		// Acquire from the display (foreign queue family).
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vk.QueueFamilyForeignEXT, r.family
	}
	d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit|vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageColorAttachmentOutputBit, 0, 0, nil, 0, nil, 1, &b)
	// Client buffers come from the foreign queue family (their driver)
	// and go back to it after the frame, so the next frame acquires them.
	dmas := r.frameDMAs[:0]
	if r.frameAcquires == nil {
		r.frameAcquires = make(map[*imported]*os.File)
	}
	clear(r.frameAcquires)
	acquires := r.frameAcquires
	for _, dr := range ds {
		if dr.im != nil && !slices.Contains(dmas, dr.im) {
			dmas = append(dmas, dr.im)
			dr.im.last = r.frame
		}
		if dr.acquire != nil {
			acquires[dr.im] = dr.acquire
		}
	}
	r.frameDMAs = dmas
	r.ownership(cmd, dmas, true)
	r.recordDraws(cmd, tg.view, parseColor(s.Background), ds, partial)
	r.ownership(cmd, dmas, false)
	if hdr {
		r.recordHDR(cmd, r.targets[r.current])
	}
	if !hdr {
		b.SrcAccessMask, b.OldLayout = vk.AccessColorAttachmentWriteBit, vk.ImageLayoutColorAttachmentOptimal
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = queueFamilyIgnored, queueFamilyIgnored
		if tg.exported {
			// Release to the display: KMS scans it out after the fence.
			b.NewLayout, b.DstAccessMask = vk.ImageLayoutGeneral, 0
			b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
			d.CmdPipelineBarrier(cmd, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
		} else {
			b.NewLayout, b.DstAccessMask = vk.ImageLayoutTransferSrcOptimal, vk.AccessTransferReadBit
			d.CmdPipelineBarrier(cmd, vk.PipelineStageColorAttachmentOutputBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
		}
	}
	if err := checked("vkEndCommandBuffer", d.EndCommandBuffer(cmd)); err != nil {
		return nil, err
	}
	// Wait for the client's GPU writes to each buffer: its explicit
	// acquire fence, else the buffer's implicit fences.
	waits := r.frameWaits[:0]
	for _, im := range dmas {
		if f := acquires[im]; f != nil {
			if sem := r.importFence(f); sem != 0 {
				waits = append(waits, sem)
			}
		} else {
			waits = r.appendReadFences(waits, im)
		}
	}
	r.frameWaits = waits
	frame := r.frame
	for _, sem := range waits {
		sem := sem
		r.retire(frame, func() { d.DestroySemaphore(r.device, sem, nil) })
	}
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &cmd}
	if cap(r.frameStages) < len(waits) {
		r.frameStages = make([]vk.PipelineStageFlags, len(waits))
	}
	stages := r.frameStages[:len(waits)]
	if len(waits) > 0 {
		for i := range stages {
			stages[i] = vk.PipelineStageFragmentShaderBit
		}
		submit.WaitSemaphoreCount, submit.WaitSemaphores, submit.WaitDstStageMask = uint32(len(waits)), &waits[0], &stages[0]
	}
	if r.syncFD {
		submit.SignalSemaphoreCount, submit.SignalSemaphores = 1, &slot.done
	}
	if err := checked("vkQueueSubmit", d.QueueSubmit(r.queue, 1, &submit, slot.fence)); err != nil {
		return nil, err
	}
	runtime.KeepAlive(stages)
	runtime.KeepAlive(waits)
	slot.frame, slot.busy, r.submitted = frame, true, frame
	if hdr {
		// recordHDR left the composed linear image in TRANSFER_SRC for capture.
		tg.layout = vk.ImageLayoutTransferSrcOptimal
		r.targets[r.current].layout = vk.ImageLayoutGeneral
	} else {
		tg.layout = b.NewLayout
	}
	oldWindows := tg.windows
	tg.hold(s, dmg)
	r.damageDrawn = oldWindows
	r.last = tg
	r.dropUnused()
	r.dropShm()
	if !r.syncFD {
		return nil, r.waitFrame(frame)
	}
	var fd int32
	get := vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFDInfoKHR, Semaphore: slot.done, HandleType: vk.ExternalSemaphoreHandleTypeSyncFDBit}
	if d.GetSemaphoreFdKHR(r.device, &get, &fd) != vk.Success {
		// The frame is drawn: finish it on the CPU. The semaphore stays
		// signalled: replace it, so the slot's next submit signals a
		// fresh one.
		if err := r.waitFrame(frame); err != nil {
			return nil, err
		}
		d.DestroySemaphore(r.device, slot.done, nil)
		slot.done = 0
		return nil, r.newDone(slot)
	}
	if fd < 0 {
		// Already signalled: the driver may return -1.
		return nil, r.waitFrame(frame)
	}
	f := os.NewFile(uintptr(fd), "neferwl-frame")
	if f == nil {
		return nil, fmt.Errorf("invalid frame sync file %d", fd)
	}
	return f, nil
}

// ownership moves client images between their driver (foreign queue
// family, GENERAL: their contents survive) and the fragment shader.
func (r *Renderer) ownership(cmd vk.CommandBuffer, dmas []*imported, acquire bool) {
	for _, im := range dmas {
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, Image: im.image, SubresourceRange: colorRange}
		if im.yuv {
			b.SubresourceRange.AspectMask = aspectPlane0 | aspectPlane1
		}
		if acquire {
			b.OldLayout, b.NewLayout = vk.ImageLayoutGeneral, vk.ImageLayoutShaderReadOnlyOptimal
			b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vk.QueueFamilyForeignEXT, r.family
			b.DstAccessMask = vk.AccessShaderReadBit
			// The frame waits on the client's fence at the fragment
			// stage: the transfer must come after that wait.
			r.dd.CmdPipelineBarrier(cmd, vk.PipelineStageFragmentShaderBit, vk.PipelineStageFragmentShaderBit, 0, 0, nil, 0, nil, 1, &b)
		} else {
			b.OldLayout, b.NewLayout = vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutGeneral
			b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
			b.SrcAccessMask = vk.AccessShaderReadBit
			r.dd.CmdPipelineBarrier(cmd, vk.PipelineStageFragmentShaderBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
		}
	}
}

// oneShot records and runs commands outside the frame ring and waits for
// them (setup and screenshots only, never per frame).
func (r *Renderer) oneShot(record func(vk.CommandBuffer)) error {
	d := r.dd
	var cmd vk.CommandBuffer
	ai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: r.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: 1}
	if err := checked("vkAllocateCommandBuffers", d.AllocateCommandBuffers(r.device, &ai, &cmd)); err != nil {
		return err
	}
	defer d.FreeCommandBuffers(r.device, r.pool, 1, &cmd)
	var fence vk.Fence
	fi := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
	if err := checked("vkCreateFence", d.CreateFence(r.device, &fi, nil, &fence)); err != nil {
		return err
	}
	defer d.DestroyFence(r.device, fence, nil)
	begin := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageOneTimeSubmitBit}
	if err := checked("vkBeginCommandBuffer", d.BeginCommandBuffer(cmd, &begin)); err != nil {
		return err
	}
	record(cmd)
	if err := checked("vkEndCommandBuffer", d.EndCommandBuffer(cmd)); err != nil {
		return err
	}
	submit := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, CommandBuffers: &cmd}
	if err := checked("vkQueueSubmit", d.QueueSubmit(r.queue, 1, &submit, fence)); err != nil {
		return err
	}
	return checked("vkWaitForFences", d.WaitForFences(r.device, 1, &fence, 1, math.MaxUint64))
}

// Clear draws a background-only frame and waits for it.
func (r *Renderer) Clear(rgb [3]uint8) error {
	done, err := r.Render(ports.Scene{Background: fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])}, nil)
	if done != nil {
		done.Close()
	}
	if err != nil {
		return err
	}
	return r.waitFrame(r.frame)
}
