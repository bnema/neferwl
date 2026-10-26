package vulkan

import (
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// GPU owns a logical device and the queues shared by its renderers.
type GPU struct {
	inst          *Instance
	physical      vk.PhysicalDevice
	device        vk.Device
	dd            *vk.DeviceDispatch
	family        uint32
	memory        vk.PhysicalDeviceMemoryProperties
	maxRange      int
	queuePriority string
	deviceInfo    DeviceInfo
	dmabuf        ports.DMABufSupport
	// Export modifier lists are computed before OpenGPU returns and immutable afterwards.
	renderMods, hdrMods []uint64
	hdrReadbackMods     []uint64 // Immutable transfer-source export list, computed at open.
	modPlanes           map[uint64]uint32
	importMods          map[importKey][]uint64
	poolMu              sync.Mutex
	syncFD              bool
	resources           uint32
	indexing            bool
	queues              []*queue
	log                 zerowrap.Logger
}

func (g *GPU) initDevice(physical vk.PhysicalDevice, family, n uint32, start time.Time) (err error) {
	g.inst.id.GetPhysicalDeviceMemoryProperties(physical, &g.memory)
	var properties vk.PhysicalDeviceProperties
	g.inst.id.GetPhysicalDeviceProperties(physical, &properties)
	g.deviceInfo.Name = cstring(properties.DeviceName[:])
	drmProperties := vk.PhysicalDeviceDrmPropertiesEXT{SType: vk.StructureTypePhysicalDeviceDRMPropertiesEXT}
	deviceProperties := vk.PhysicalDeviceProperties2{SType: vk.StructureTypePhysicalDeviceProperties2, Next: unsafe.Pointer(&drmProperties)}
	g.inst.id.GetPhysicalDeviceProperties2(physical, &deviceProperties)
	if drmProperties.HasPrimary != 0 {
		g.deviceInfo.Primary = fmt.Sprintf("%d:%d", drmProperties.PrimaryMajor, drmProperties.PrimaryMinor)
	}
	if drmProperties.HasRender != 0 {
		g.deviceInfo.Render = fmt.Sprintf("%d:%d", drmProperties.RenderMajor, drmProperties.RenderMinor)
	}
	g.maxRange = int(properties.Limits.MaxStorageBufferRange)
	g.family = family
	priorities := make([]float32, n)
	for i := range priorities {
		priorities[i] = 1
	}
	qi := vk.DeviceQueueCreateInfo{SType: vk.StructureTypeDeviceQueueCreateInfo, QueueFamilyIndex: family, QueueCount: n, QueuePriorities: &priorities[0]}
	dynamicRendering := vk.PhysicalDeviceDynamicRenderingFeatures{SType: vk.StructureTypePhysicalDeviceDynamicRenderingFeatures, DynamicRendering: 1}
	indexing := descriptorIndexingFeatures{SType: descriptorIndexingFeaturesType}
	features := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, Next: unsafe.Pointer(&indexing)}
	g.inst.id.GetPhysicalDeviceFeatures2(physical, &features)
	g.resources = 1
	coreFeatures := vk.PhysicalDeviceFeatures{}
	if features.Features.ShaderSampledImageArrayDynamicIndexing != 0 && features.Features.ShaderStorageBufferArrayDynamicIndexing != 0 && indexing.ShaderSampledImageArrayNonUniformIndexing != 0 && indexing.ShaderStorageBufferArrayNonUniformIndexing != 0 {
		limit := composeResourceLimit(properties.Limits)
		if limit > 1 {
			g.resources, g.indexing = limit, true
			coreFeatures.ShaderSampledImageArrayDynamicIndexing = 1
			coreFeatures.ShaderStorageBufferArrayDynamicIndexing = 1
			// Enable only the array indexing features used by these shaders.
			indexing = descriptorIndexingFeatures{SType: descriptorIndexingFeaturesType, ShaderSampledImageArrayNonUniformIndexing: 1, ShaderStorageBufferArrayNonUniformIndexing: 1}
			dynamicRendering.Next = unsafe.Pointer(&indexing)
		}
	}
	di := vk.DeviceCreateInfo{SType: vk.StructureTypeDeviceCreateInfo, Next: unsafe.Pointer(&dynamicRendering), QueueCreateInfoCount: 1, QueueCreateInfos: &qi, EnabledFeatures: &coreFeatures}
	// dmabuf import needs every extension; without them clients use wl_shm.
	var extNames [][]byte
	var extPtrs []*byte
	have := extensions(g.inst.id, physical)
	dmabufSupported := true
	for _, e := range deviceExtensions {
		if !have[e] {
			dmabufSupported = false
		}
	}
	if dmabufSupported {
		for _, e := range deviceExtensions {
			extNames = append(extNames, append([]byte(e), 0))
			extPtrs = append(extPtrs, &extNames[len(extNames)-1][0])
		}
		di.EnabledExtensionCount = uint32(len(extPtrs))
		di.PpEnabledExtensionNames = &extPtrs[0]
	}
	globalExt := ""
	if have[vk.KHRGlobalPriorityExtensionName] {
		globalExt = vk.KHRGlobalPriorityExtensionName
	} else if have[vk.EXTGlobalPriorityExtensionName] {
		globalExt = vk.EXTGlobalPriorityExtensionName
	}
	var global vk.DeviceQueueGlobalPriorityCreateInfo
	chosen := "default"
	for {
		for _, attempt := range priorityAttempts(globalExt != "") {
			qi.Next = nil
			count := len(extPtrs)
			if attempt != 0 {
				global = vk.DeviceQueueGlobalPriorityCreateInfo{SType: vk.StructureTypeDeviceQueueGlobalPriorityCreateInfo, GlobalPriority: attempt}
				qi.Next = unsafe.Pointer(&global)
				extNames = append(extNames, append([]byte(globalExt), 0))
				extPtrs = append(extPtrs, &extNames[len(extNames)-1][0])
				chosen = "realtime"
				if attempt == vk.QueueGlobalPriorityHigh {
					chosen = "high"
				}
			} else {
				chosen = "default"
			}
			di.EnabledExtensionCount = uint32(len(extPtrs))
			if len(extPtrs) > 0 {
				di.PpEnabledExtensionNames = &extPtrs[0]
			} else {
				di.PpEnabledExtensionNames = nil
			}
			result := g.inst.id.CreateDevice(physical, &di, nil, &g.device)
			runtime.KeepAlive(global)
			runtime.KeepAlive(coreFeatures)
			runtime.KeepAlive(indexing)
			runtime.KeepAlive(dynamicRendering)
			runtime.KeepAlive(qi)
			runtime.KeepAlive(extNames)
			runtime.KeepAlive(extPtrs)
			extPtrs = extPtrs[:count]
			extNames = extNames[:count]
			if result == vk.Success {
				err = nil
				break
			}
			err = checked("vkCreateDevice", result)

		}
		if err == nil {
			break
		}
		if n == 1 {
			return
		}
		n = 1
		qi.QueueCount = n
	}
	g.queuePriority = chosen
	g.dd, err = vk.LoadDeviceDispatch(g.inst.id, g.device)
	if err != nil {
		name := append([]byte("vkDestroyDevice"), 0)
		if addr := g.inst.id.GetDeviceProcAddr(g.device, &name[0]); addr != 0 {
			purego.SyscallN(uintptr(addr), uintptr(g.device), 0)
		}
		g.device = 0
		err = fmt.Errorf("LoadDeviceDispatch: %w", err)
		return
	}
	runtime.KeepAlive(extNames)
	runtime.KeepAlive(extPtrs)
	if dmabufSupported {
		g.dmabuf = g.probeDMABuf(physical)
		if g.dmabuf.Device != 0 {
			g.probeRenderModifiers()
		}
		g.syncFD = g.dd.HasGetSemaphoreFdKHR()
	}
	g.queues = make([]*queue, n)
	for i := range g.queues {
		q := &queue{dd: g.dd}
		g.dd.GetDeviceQueue(g.device, family, uint32(i), &q.q)
		g.queues[i] = q
	}
	g.deviceInfo.CreateMS = time.Since(start).Milliseconds()
	g.log.Info().Str("queue_priority", g.queuePriority).Str("vk_device", g.deviceInfo.Name).Str("primary", g.deviceInfo.Primary).Str("render", g.deviceInfo.Render).Int64("create_ms", g.deviceInfo.CreateMS).Msg("vulkan queue")
	return
}

// DMABuf returns the formats probed once for this device.
func (g *GPU) DMABuf() ports.DMABufSupport { return g.dmabuf }

// Node returns the primary and render device numbers, or zero when absent.
func (g *GPU) Node() (primary, render uint64) {
	p := vk.PhysicalDeviceDrmPropertiesEXT{SType: vk.StructureTypePhysicalDeviceDRMPropertiesEXT}
	props := vk.PhysicalDeviceProperties2{SType: vk.StructureTypePhysicalDeviceProperties2, Next: unsafe.Pointer(&p)}
	g.inst.id.GetPhysicalDeviceProperties2(g.physical, &props)
	if p.HasPrimary != 0 {
		primary = unix.Mkdev(uint32(p.PrimaryMajor), uint32(p.PrimaryMinor))
	}
	if p.HasRender != 0 {
		render = unix.Mkdev(uint32(p.RenderMajor), uint32(p.RenderMinor))
	}
	return
}

// Close requires all users to have released their queues first.
func (g *GPU) Close() {
	if g == nil || g.dd == nil || g.device == 0 {
		return
	}
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	for _, q := range g.queues {
		if q.users != 0 {
			g.log.Error().Int("users", q.users).Msg("closing GPU with active renderers")
			break
		}
	}
	queues := g.queues
	// Queue order is stable; hold every submission lock through device destruction.
	for _, q := range g.queues {
		q.mu.Lock()
	}
	defer func() {
		for i := len(queues) - 1; i >= 0; i-- {
			queues[i].mu.Unlock()
		}
	}()
	if err := checked("vkDeviceWaitIdle", g.dd.DeviceWaitIdle(g.device)); err != nil {
		g.log.Error().Err(err).Msg("wait for GPU idle")
	}
	g.dd.DestroyDevice(g.device, nil)
	g.device = 0
	g.queues = nil
}

type queue struct {
	q     vk.Queue
	mu    sync.Mutex
	users int
	dd    *vk.DeviceDispatch
}

func (q *queue) submit(count uint32, submits *vk.SubmitInfo, fence vk.Fence) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return checked("vkQueueSubmit", q.dd.QueueSubmit(q.q, count, submits, fence))
}

// queuePool accounting is protected by GPU.poolMu, not by the submission lock.
type queuePool []*queue

func (p queuePool) acquire() *queue {
	for _, q := range p {
		if q.users == 0 {
			q.users++
			return q
		}
	}
	if len(p) == 0 {
		return nil
	}
	p[0].users++
	return p[0]
}

func (p queuePool) release(q *queue) {
	for _, candidate := range p {
		if candidate == q {
			if q.users > 0 {
				q.users--
			}
			return
		}
	}
}

func (g *GPU) acquireQueue() *queue {
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	return queuePool(g.queues).acquire()
}

func (g *GPU) releaseQueue(q *queue) {
	g.poolMu.Lock()
	defer g.poolMu.Unlock()
	queuePool(g.queues).release(q)
}

// modifierPlanes reads plane metadata published at GPU open.
func (g *GPU) modifierPlanes(mod uint64) (uint32, bool) {
	n, ok := g.modPlanes[mod]
	return n, ok
}

var _ ports.RenderDevice = (*GPU)(nil)
