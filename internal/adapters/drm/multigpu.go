package drm

import (
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
)

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
	Render     ports.RenderDevice
	RenderCard string
	Local      func() (ports.RenderDevice, error)
	Mode       MultiGPU
}

type outputTransport string

const (
	transportDirect outputTransport = "direct"
	transportCopy   outputTransport = "gpu-copy"
)

// localDeviceError intentionally does not unwrap: local GPU loss must not
// trigger recovery of the session's shared render GPU.
type localDeviceError struct{ cause error }

func (e localDeviceError) Error() string { return fmt.Sprintf("local copy device: %v", e.cause) }

func (o *Output) crossGPU() bool { return o.card != nil && o.card.dev.Local != nil }

func (o *Output) crossImages(r ports.Renderer, cause error, skip bool) (imageKind, error) {
	var err error = cause
	if !skip {
		mods := o.primaryModifiers(o.imageFormat())
		linear := false
		for _, mod := range mods {
			if mod == 0 {
				linear = true
				break
			}
		}
		if !linear {
			err = fmt.Errorf("primary plane has no LINEAR modifier")
		} else if o.card.alloc == nil {
			err = fmt.Errorf("GBM unavailable")
		} else {
			err = o.gbmImages(r, []uint64{0}, true)
		}
		if err == nil {
			if o.transport != transportDirect {
				o.log.Info().Str("card", o.card.info.Path).Str("render_card", o.card.dev.RenderCard).Str("multi_gpu", "direct").Msg("multi-GPU output")
			}
			o.transport = transportDirect
			return imagesLinear, nil
		}
	}
	if o.card.dev.Mode == MultiGPUDirect {
		o.log.Error().Err(err).Str("card", o.card.info.Path).Str("render_card", o.card.dev.RenderCard).Msg("cross-GPU direct import failed")
		return imagesLinear, fmt.Errorf("cross-GPU direct import: %w", err)
	}
	return imagesLinear, fmt.Errorf("gpu-copy not implemented: %v", err)
}
