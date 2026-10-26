package vulkan

import (
	"fmt"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Instance owns the process Vulkan instance. Close GPUs before closing it.
type Instance struct {
	instance vk.Instance
	id       *vk.InstanceDispatch
	log      zerowrap.Logger
}

func NewInstance(log zerowrap.Logger) (*Instance, error) {
	if err := vk.Init(); err != nil {
		return nil, fmt.Errorf("vk.Init: %w", err)
	}
	inst := &Instance{log: log}
	app := vk.ApplicationInfo{SType: vk.StructureTypeApplicationInfo, ApiVersion: vk.MakeVersion(1, 3, 0)}
	info := vk.InstanceCreateInfo{SType: vk.StructureTypeInstanceCreateInfo, ApplicationInfo: &app}
	if err := checked("vkCreateInstance", vk.Global().CreateInstance(&info, nil, &inst.instance)); err != nil {
		return nil, err
	}
	var err error
	inst.id, err = vk.LoadInstanceDispatch(inst.instance)
	if err != nil {
		name := append([]byte("vkDestroyInstance"), 0)
		if addr := vk.Global().GetInstanceProcAddr(inst.instance, &name[0]); addr != 0 {
			purego.SyscallN(uintptr(addr), uintptr(inst.instance), 0)
		}
		return nil, fmt.Errorf("LoadInstanceDispatch: %w", err)
	}
	return inst, nil
}

func (inst *Instance) Close() {
	if inst == nil || inst.instance == 0 || inst.id == nil {
		return
	}
	inst.id.DestroyInstance(inst.instance, nil)
	inst.instance = 0
}

var _ ports.RenderDeviceOpener = (*Instance)(nil)

// Open opens the selected GPU through the render-device port.
func (inst *Instance) Open(info ports.GPUInfo) (ports.RenderDevice, error) {
	g, err := inst.OpenGPU(info)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (inst *Instance) OpenGPU(info ports.GPUInfo) (*GPU, error) {
	var count uint32
	if err := checked("vkEnumeratePhysicalDevices(count)", inst.id.EnumeratePhysicalDevices(inst.instance, &count, nil)); err != nil {
		return nil, err
	}
	devices := make([]vk.PhysicalDevice, count)
	if count > 0 {
		if err := checked("vkEnumeratePhysicalDevices", inst.id.EnumeratePhysicalDevices(inst.instance, &count, &devices[0])); err != nil {
			return nil, err
		}
	}
	for _, physical := range devices[:count] {
		var props vk.PhysicalDeviceProperties
		inst.id.GetPhysicalDeviceProperties(physical, &props)
		if props.ApiVersion < vk.MakeVersion(1, 3, 0) {
			continue
		}
		if info != (ports.GPUInfo{}) {
			if !extensions(inst.id, physical)[vk.EXTPhysicalDeviceDRMExtensionName] {
				continue
			}
			drm := vk.PhysicalDeviceDrmPropertiesEXT{SType: vk.StructureTypePhysicalDeviceDRMPropertiesEXT}
			properties := vk.PhysicalDeviceProperties2{SType: vk.StructureTypePhysicalDeviceProperties2, Next: unsafe.Pointer(&drm)}
			inst.id.GetPhysicalDeviceProperties2(physical, &properties)
			if !matchDRM(drm, info.Primary, info.Render) {
				continue
			}
		}
		var n uint32
		inst.id.GetPhysicalDeviceQueueFamilyProperties(physical, &n, nil)
		if n == 0 {
			continue
		}
		families := make([]vk.QueueFamilyProperties, n)
		inst.id.GetPhysicalDeviceQueueFamilyProperties(physical, &n, &families[0])
		for family, f := range families[:n] {
			if f.QueueCount == 0 || f.QueueFlags&vk.QueueGraphicsBit == 0 {
				continue
			}
			g := &GPU{inst: inst, physical: physical, log: inst.log, importMods: make(map[importKey][]uint64)}
			if err := g.initDevice(physical, uint32(family), min(f.QueueCount, 8), time.Now()); err != nil {
				g.Close()
				return nil, err
			}
			return g, nil
		}
	}
	return nil, fmt.Errorf("no Vulkan device for %s", info.Path)
}

func matchDRM(p vk.PhysicalDeviceDrmPropertiesEXT, primary, render uint64) bool {
	return render != 0 && p.HasRender != 0 && unix.Mkdev(uint32(p.RenderMajor), uint32(p.RenderMinor)) == render ||
		primary != 0 && p.HasPrimary != 0 && unix.Mkdev(uint32(p.PrimaryMajor), uint32(p.PrimaryMinor)) == primary
}
