package vulkan

import (
	"fmt"
	"runtime"
	"unsafe"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Bound descriptor-table work and memory even on devices with huge limits.
const maxComposeResources = 64

// instanceDraw matches Draw in compose.vert (std430, 128-byte stride).
type instanceDraw struct {
	pc       drawParameters
	resource [4]uint32
}

// resourceKey retains the Go identity as well as the Vulkan handles: a
// destroyed object's recycled handle must not look like a cached descriptor.
type resourceKey struct {
	im  *imported
	shm *shmCopy
}

// composeBatch belongs to a frame slot. Neither its mapped memory nor its
// descriptors are changed until the slot's previous submission completed.
type composeBatch struct {
	gpu        gpuBuffer
	pool       vk.DescriptorPool
	set        vk.DescriptorSet
	count      uint32
	last       uint64 // frame that last prepared this batch
	instances  []instanceDraw
	keys       [maxComposeResources]resourceKey
	cached     [maxComposeResources]resourceKey
	used       int
	valid      bool
	images     [maxComposeResources]vk.DescriptorImageInfo
	chroma     [maxComposeResources]vk.DescriptorImageInfo
	buffers    [maxComposeResources]vk.DescriptorBufferInfo
	drawBuffer vk.DescriptorBufferInfo
	writes     [4]vk.WriteDescriptorSet
}

// add preserves paint order. Solids need no resource and can share either
// neighbouring resource's batch, including on devices without indexing.
func (b *composeBatch) add(dr draw, resources, maxDraws int) bool {
	if len(b.instances) == maxDraws {
		return false
	}
	key := resourceKey{im: dr.im, shm: dr.shm}
	index := 0
	if key != (resourceKey{}) {
		for index < b.used && b.keys[index] != key {
			index++
		}
		if index == b.used {
			if b.used == resources {
				return false
			}
			b.keys[index] = key
			b.used++
		}
	}
	b.instances = append(b.instances, instanceDraw{pc: dr.pc, resource: [4]uint32{uint32(index)}})
	return true
}

func (r *Renderer) prepareCompose(slot *frameSlot, ds []draw) ([]*composeBatch, error) {
	resources := int(r.compose.resources)
	maxDraws := r.maxRange / int(unsafe.Sizeof(instanceDraw{}))
	if maxDraws < 1 {
		return nil, fmt.Errorf("storage buffer range too small for composition")
	}
	used := 0
	for len(ds) > 0 {
		if used == len(slot.batches) {
			slot.batches = append(slot.batches, &composeBatch{})
		}
		b := slot.batches[used]
		clear(b.keys[:])
		b.used = 0
		b.instances = b.instances[:0]
		for len(ds) > 0 && b.add(ds[0], resources, maxDraws) {
			ds = ds[1:]
		}
		if err := r.uploadCompose(b); err != nil {
			return nil, err
		}
		b.last = r.frame
		used++
	}
	// The slot is idle. Drop trailing batches after the same frame TTL
	// as client resources, rather than retaining a peak scene forever.
	for len(slot.batches) > used {
		last := len(slot.batches) - 1
		b := slot.batches[last]
		if r.frame-b.last <= importTTL {
			break
		}
		r.freeComposeBatch(b)
		slot.batches[last] = nil
		slot.batches = slot.batches[:last]
	}
	return slot.batches[:used], nil
}

func (r *Renderer) uploadCompose(b *composeBatch) error {
	count := len(b.instances)
	size := count * int(unsafe.Sizeof(instanceDraw{}))
	if b.gpu.size < size {
		// This slot is idle, so its previous buffer can be replaced now.
		capacity := min(r.maxRange, max(size, b.gpu.size*2, 64*int(unsafe.Sizeof(instanceDraw{}))))
		b.valid = false
		r.freeGPUBuffer(&b.gpu)
		if err := r.newGPUBuffer(&b.gpu, capacity); err != nil {
			return err
		}
	}
	copy(unsafe.Slice((*instanceDraw)(b.gpu.mapped), count), b.instances)
	b.count = uint32(count)
	if b.set == 0 {
		if err := r.createComposeSet(b); err != nil {
			return err
		}
	}
	if b.valid && b.keys == b.cached {
		return nil
	}
	n := int(r.compose.resources)
	for i, key := range b.keys[:n] {
		view, chroma, buffer := r.compose.dummyView, r.compose.dummyView, r.compose.dummyBuffer.buffer
		if key.im != nil {
			view = key.im.view
			chroma = view
			if key.im.chromaView != 0 {
				chroma = key.im.chromaView
			}
		}
		if key.shm != nil {
			buffer = key.shm.gpu.buffer
		}
		b.images[i] = vk.DescriptorImageInfo{Sampler: r.compose.sampler, ImageView: view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
		b.chroma[i] = vk.DescriptorImageInfo{Sampler: r.compose.sampler, ImageView: chroma, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
		b.buffers[i] = vk.DescriptorBufferInfo{Buffer: buffer, Range: wholeSize}
	}
	b.drawBuffer = vk.DescriptorBufferInfo{Buffer: b.gpu.buffer, Range: vk.DeviceSize(b.gpu.size)}
	b.writes = [4]vk.WriteDescriptorSet{
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: b.set, DstBinding: 0, DescriptorCount: uint32(n), DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &b.images[0]},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: b.set, DstBinding: 1, DescriptorCount: uint32(n), DescriptorType: vk.DescriptorTypeStorageBuffer, BufferInfo: &b.buffers[0]},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: b.set, DstBinding: 2, DescriptorCount: uint32(n), DescriptorType: vk.DescriptorTypeCombinedImageSampler, ImageInfo: &b.chroma[0]},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: b.set, DstBinding: 3, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, BufferInfo: &b.drawBuffer},
	}
	r.dd.UpdateDescriptorSets(r.device, uint32(len(b.writes)), &b.writes[0], 0, nil)
	runtime.KeepAlive(b)
	b.cached, b.valid = b.keys, true
	return nil
}

func (r *Renderer) createComposeSet(b *composeBatch) error {
	n := r.compose.resources
	sizes := [2]vk.DescriptorPoolSize{
		{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 2 * n},
		{Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: n + 1},
	}
	info := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: uint32(len(sizes)), PoolSizes: &sizes[0]}
	if err := checked("vkCreateDescriptorPool(compose)", r.dd.CreateDescriptorPool(r.device, &info, nil, &b.pool)); err != nil {
		return err
	}
	alloc := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: b.pool, DescriptorSetCount: 1, SetLayouts: &r.compose.setLayout}
	if err := checked("vkAllocateDescriptorSets(compose)", r.dd.AllocateDescriptorSets(r.device, &alloc, &b.set)); err != nil {
		r.dd.DestroyDescriptorPool(r.device, b.pool, nil)
		b.pool = 0
		return err
	}
	return nil
}

// Released resources may leave unbound descriptors naming dead handles.
// Invalidate their cached identities before destruction: the next bind
// rewrites the entire table after its slot fence. No GPU-visible mutation
// happens here, even if another batch in the slot is still in flight.
func (r *Renderer) invalidateCompose(key resourceKey) {
	for i := range r.slots {
		for _, b := range r.slots[i].batches {
			for j := range b.cached {
				if b.cached[j] == key {
					b.cached[j], b.keys[j] = resourceKey{}, resourceKey{}
					b.valid = false
				}
			}
		}
	}
}

// trimCompose runs only after poll, and never frees an in-flight slot.
func (r *Renderer) trimCompose(cutoff uint64) {
	for i := range r.slots {
		s := &r.slots[i]
		if s.busy {
			continue
		}
		for len(s.batches) > 0 {
			last := len(s.batches) - 1
			b := s.batches[last]
			if b.last > cutoff || b.last >= r.frame {
				break
			}
			r.freeComposeBatch(b)
			s.batches[last] = nil
			s.batches = s.batches[:last]
		}
	}
}

func (r *Renderer) freeComposeBatch(b *composeBatch) {
	if b.pool != 0 {
		r.dd.DestroyDescriptorPool(r.device, b.pool, nil)
	}
	r.freeGPUBuffer(&b.gpu)
	*b = composeBatch{}
}
