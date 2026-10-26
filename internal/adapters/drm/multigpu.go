package drm

import (
	"errors"
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
	"os"
	"slices"
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
	if o.transport == transportCopy && skip {
		return imagesLinear, fmt.Errorf("gpu-copy modeset refused: %v", err)
	}
	return imagesDriver, o.copyImages(r)
}

func (o *Output) closeCopier() {
	if o.copier != nil {
		o.copier.Close()
		o.copier = nil
	}
}

// copyFence leaves the caller's render file ownership unchanged at the port.
func (o *Output) copyFence(index int, fence *os.File) (*os.File, error) {
	if o.copier == nil {
		return fence, nil
	}
	done, err := o.copier.Copy(index, index, fence)
	if fence != nil {
		if err != nil {
			o.holdRead(fence)
		}
		fence.Close()
	}
	if err != nil {
		if done != nil {
			done.Close()
		}
		return nil, localDeviceError{err}
	}
	return done, nil
}

func (o *Output) copyImages(r ports.Renderer) (err error) {
	o.dropImages(r)
	local, err := o.card.dev.Local()
	if err != nil {
		return localDeviceError{err}
	}
	copier, err := local.NewCopier(o.Width(), o.Height())
	if err != nil {
		return localDeviceError{err}
	}
	o.copier = copier
	defer func() {
		if err != nil {
			o.closeCopier()
			o.freeImages()
			_, _ = r.ExportTargets(0, nil, false)
		}
	}()
	var candidates []ports.DMABufFormat
	for _, f := range local.DMABuf().Formats {
		if f.Format == o.imageFormat() {
			candidates = append(candidates, f)
		}
	}
	accepted := copier.SourceModifiers(candidates)
	var mods []uint64
	for _, f := range accepted {
		if f.Format == o.imageFormat() && !slices.Contains(mods, f.Modifier) {
			mods = append(mods, f.Modifier)
		}
	}
	if len(mods) == 0 {
		return errors.New("no GPU-copy source modifier")
	}
	// Local import capability supplies candidates; ExportTargets authoritatively
	// intersects render exportability. Prefer cross-vendor LINEAR before tiling.
	var sources []ports.DMABuf
	if slices.Contains(mods, uint64(0)) {
		sources, err = r.ExportTargets(len(o.fbs), []uint64{0}, false)
	}
	if len(sources) == 0 {
		sources, err = r.ExportTargets(len(o.fbs), mods, false)
	}
	if err != nil {
		return err
	}
	defer closeBufs(sources)
	o.log.Debug().Uint64("source_modifier", sources[0].Modifier).Msg("GPU-copy source modifier")
	if err = copier.ImportSources(sources); err != nil {
		return localDeviceError{err}
	}
	if o.card.alloc == nil {
		return errors.New("GPU-copy requires GBM scanout images")
	}
	var bufs []ports.DMABuf
	defer func() { closeBufs(bufs) }()
	scanoutMods := o.primaryModifiers(o.imageFormat())
	for range o.fbs {
		img, e := o.card.allocate(o.Width(), o.Height(), o.imageFormat(), scanoutMods)
		if e != nil {
			return e
		}
		o.images = append(o.images, img)
		b, e := o.card.imageDMABuf(img)
		if e != nil {
			return e
		}
		bufs = append(bufs, b)
	}
	if err = copier.ImportTargets(bufs); err != nil {
		return localDeviceError{err}
	}
	if err = o.showBufs(r, bufs, allocGBM); err != nil {
		return err
	}
	if o.transport != transportCopy {
		o.log.Info().Str("card", o.card.info.Path).Str("render_card", o.card.dev.RenderCard).Str("multi_gpu", "gpu-copy").Msg("multi-GPU output")
	}
	o.transport = transportCopy
	return nil
}
