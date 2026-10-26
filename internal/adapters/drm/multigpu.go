package drm

import "github.com/bnema/neferwl/internal/ports"

// MultiGPU selects how outputs on a different GPU are composed.
type MultiGPU int

const (
	// MultiGPUAuto tries direct import, then GPU copy.
	MultiGPUAuto MultiGPU = iota
	// MultiGPUDirect permits direct import only.
	MultiGPUDirect
	// MultiGPUOff disables cross-GPU outputs.
	MultiGPUOff
)

// CardDevices supplies the render device and an optional lazy local copy device.
type CardDevices struct {
	Render ports.RenderDevice
	Local  func() (ports.RenderDevice, error)
	Mode   MultiGPU
}
