package drm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/ports"
)

// Output images: the renderer targets frames alternate between.

// imageKind is how output images are made, best first.
type imageKind int

const (
	imagesDriver      imageKind = iota // exported, modifier chosen by the driver (DCC planes allowed)
	imagesSinglePlane                  // exported, driver modifier with one memory plane
	imagesLinear                       // exported, linear
)

// maxFBPlanes is the number of planes a KMS framebuffer holds (fbCmd2).
const maxFBPlanes = 4

// showImages sets up images from kind on and modesets them. KMS may refuse
// an image only in a modeset: a TEST_ONLY modeset checks it first, and the
// next kind is tried on refusal. cause is why the
// previous kind failed, for the log.
func (o *Output) showImages(r ports.Renderer, kind imageKind, cause error) error {
	if o.hdr.wanted() {
		o.hdr.on = true
		// Reuse the live blob across VT resume. A changed EDID creates a
		// replacement, but the old one remains alive until KMS accepts it.
		sw, err := o.hdr.prepareBlob(o.k, hdrMetadata(o.monitor))
		if err == nil {
			r.SetHDR(float64(o.hdr.settings.SDRBrightness))
			err = o.showImageKind(r, imagesDriver, nil)
		}
		var pe protectedCommitError
		if errors.As(err, &pe) {
			// A protected KMS failure is not an HDR failure: images exist and
			// the owner retries the protected modeset. Never fall back to SDR
			// or latch hdr.failed for it.
			o.hdr.rollbackBlob(o.k, sw)
			return err
		}
		if err == nil {
			o.hdr.commitBlob(o.k, sw)
			o.log.Info().Str("connector", o.conn.name).Int("sdr_brightness", o.hdr.settings.SDRBrightness).Float64("max_luminance", o.hdr.cap.MaxLuminance).Float64("max_frame_average", o.hdr.cap.MaxFrameAverage).Msg("HDR10 on")
			return nil
		}
		if sw.old != 0 {
			// Failed replacement: the existing modeset may still use it.
			o.hdr.rollbackBlob(o.k, sw)
		}
		o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("HDR modeset unavailable; falling back to SDR")
		o.hdr.failed = true
		o.hdr.on = false
		o.freeImages()
		r.SetHDR(0)
		_, _ = r.ExportTargets(0, nil, false)
		// A failed test commit can leave the old HDR mode on screen;
		// retain its blob until the SDR modeset succeeds or Close restores it.
	} else {
		r.SetHDR(0)
	}
	err := o.showImageKind(r, kind, cause)
	if err == nil && o.hdr.failed && o.hdr.blob != 0 {
		// The SDR commit has completed; the old HDR metadata is no longer in use.
		o.hdr.releaseBlob(o.k)
	}
	return err
}

func (o *Output) showImageKind(r ports.Renderer, kind imageKind, cause error) error {
	for {
		got, err := o.setupImages(r, kind, cause)
		if err != nil {
			return err
		}
		o.kind = got
		if err = o.testModeset(); err == nil {
			err = o.modeset()
			if err != nil && o.protected && !errors.Is(err, errSecurityScene) && !errors.Is(err, errProtectionPending) && !errors.As(err, new(protectedCommitError)) {
				err = protectedCommitError{err}
			}
			// A protected failure is not an image refusal: never fall back to
			// another image kind or SDR for it. The caller hands it to the
			// bounded retry (commitFailed).
			return err
		}
		if o.protected && !refused(err) {
			// EBUSY, lost master or a blob failure of the test: the images
			// are fine; retry the protected modeset later.
			return protectedCommitError{err}
		}
		if !refused(err) || got == imagesLinear {
			o.freeImages()
			return err
		}
		if o.imageMod == 0 {
			// The refused images were already linear: the later kinds
			// would export the same ones again.
			o.freeImages()
			return err
		}
		o.log.Warn().Err(err).Str("connector", o.conn.name).Int("kind", int(got)).Msg("modeset refused the output images")
		o.freeImages()
		kind, cause = got+1, err
		if kind == imagesSinglePlane && o.planes == 1 {
			// The refused images already had one plane: single-plane
			// images would be the same ones.
			kind = imagesLinear
		}
	}
}

// setupImages gives the output the two renderer images it flips between,
// exported as dmabufs. There is no CPU fallback (ADR 014): a GPU that
// cannot export a scanout-capable image cannot drive the output.
func (o *Output) setupImages(r ports.Renderer, kind imageKind, cause error) (imageKind, error) {
	err := cause
	for ; kind <= imagesLinear; kind++ {
		if o.runContext != nil && o.runContext.Err() != nil {
			return kind, o.runContext.Err()
		}
		// The renderer is asked for the modifiers the primary plane lists
		// for the image format: DRM core refuses (ADDFB2: EINVAL) a
		// framebuffer whose format and modifier no plane advertises, and
		// the renderer's own preference may be such a one (RADV's
		// pipe-aligned DCC, which amdgpu does not list for scanout). A
		// plane without IN_FORMATS lists every format with modifier 0
		// (readPlanes), so it gets linear images straight away rather than
		// two refused driver attempts. Only a plane listing no modifier
		// at all for the format (SDR) leaves the choice to the renderer.
		mods := o.primaryModifiers(o.imageFormat())
		if o.hdr.on {
			if len(mods) == 0 {
				return kind, fmt.Errorf("primary plane has no XRGB2101010 modifiers")
			}
			if kind == imagesLinear {
				if !slices.Contains(mods, uint64(0)) {
					continue
				}
				mods = []uint64{0}
			}
		} else if kind == imagesLinear {
			mods = []uint64{0}
		}
		if kind == imagesSinglePlane && len(mods) == 1 && mods[0] == 0 {
			// The display only lists linear, already one plane: the driver
			// attempt was the single-plane one.
			continue
		}
		if err = o.exportImages(r, mods, kind == imagesSinglePlane); err == nil {
			return kind, nil
		}
		o.log.Info().Err(err).Str("connector", o.conn.name).Int("kind", int(kind)).Msg("output image export")
	}
	return imagesLinear, fmt.Errorf("%s: GPU cannot export scanout images (ADR 014): %w", o.conn.name, err)
}

// imageFormat is the framebuffer format of the output images.
func (o *Output) imageFormat() uint32 {
	if o.hdr.on {
		return fourccXR30
	}
	return fourccXRGB
}

// primaryModifiers lists the modifiers the primary plane accepts for
// format, in IN_FORMATS order.
func (o *Output) primaryModifiers(format uint32) []uint64 {
	var mods []uint64
	for _, f := range o.primary.formats {
		if f.Format == format {
			mods = append(mods, f.Modifier)
		}
	}
	return mods
}

// exportImages makes the renderer's exported targets the output images.
func (o *Output) exportImages(r ports.Renderer, mods []uint64, singlePlane bool) error {
	bufs, err := r.ExportTargets(len(o.fbs), mods, singlePlane)
	if err != nil {
		return err
	}
	o.planes, o.imageMod = 0, 0
	for i := range bufs {
		if len(bufs[i].Planes) == 0 || len(bufs[i].Planes) > maxFBPlanes {
			err = errors.Join(err, fmt.Errorf("output image has %d planes", len(bufs[i].Planes)))
		}
		if err == nil {
			o.fbs[i], err = o.k.addFB(&bufs[i], o.imageFormat())
		}
		// Every plane owns its file; the framebuffer keeps its own
		// reference to the buffer.
		for _, p := range bufs[i].Planes {
			p.File.Close()
		}
	}
	// GPU memory starts undefined (old VRAM contents): clear both images
	// before the modeset shows one.
	for i := range o.fbs {
		if err != nil {
			break
		}
		r.UseTarget(i)
		var done *os.File
		if done, err = r.Render(ports.Scene{Background: "#000000"}, nil); done != nil {
			// The modeset is a blocking commit: wait for the clear.
			waitCtx := o.runContext
			if waitCtx == nil {
				waitCtx = context.Background()
			}
			err = errors.Join(err, syncfile.Wait(waitCtx, done))
			done.Close()
		}
	}
	if err != nil {
		o.freeImages()
		_, _ = r.ExportTargets(0, nil, false)
		return err
	}
	o.planes, o.imageMod = len(bufs[0].Planes), bufs[0].Modifier
	o.log.Info().Str("connector", o.conn.name).Uint64("modifier", bufs[0].Modifier).Int("planes", len(bufs[0].Planes)).Msg("zero-copy output")
	return nil
}

// freeImages removes the output images' framebuffers.
func (o *Output) freeImages() {
	for i, fb := range o.fbs {
		if fb != 0 {
			_ = o.k.rmFB(fb)
		}
		o.fbs[i] = 0
	}
}
