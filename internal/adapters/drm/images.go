package drm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Output images: the renderer targets frames alternate between.

// imageKind is how output images are made, best first. Each kind is tried
// with GBM allocation first, then with images the renderer allocates and
// exports.
type imageKind int

const (
	imagesDriver      imageKind = iota // modifier chosen by the driver (DCC planes allowed)
	imagesSinglePlane                  // driver modifier with one memory plane
	imagesLinear                       // linear
)

// Who allocated the output images (the "alloc" log field).
const (
	allocGBM    = "gbm"
	allocVulkan = "vulkan"
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
		o.dropImages(r)
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
	// skipGBM makes the next setup use renderer-allocated images for kind:
	// GBM images of this kind were refused by the modeset test.
	skipGBM := false
	for {
		got, err := o.setupImages(r, kind, cause, skipGBM)
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
		if !refused(err) {
			o.dropImages(r)
			return err
		}
		if len(o.images) > 0 {
			// GBM images (the renderer imported them).
			// The renderer's own images of this kind may still pass.
			o.log.Warn().Err(err).Str("connector", o.conn.name).Int("kind", int(got)).Str("alloc", allocGBM).Msg("modeset refused the output images")
			o.dropImages(r)
			kind, cause, skipGBM = got, err, true
			continue
		}
		if got == imagesLinear {
			o.dropImages(r)
			return err
		}
		if o.imageMod == 0 {
			// The refused images were already linear: the later kinds
			// would export the same ones again.
			o.dropImages(r)
			return err
		}
		o.log.Warn().Err(err).Str("connector", o.conn.name).Int("kind", int(got)).Str("alloc", allocVulkan).Msg("modeset refused the output images")
		o.dropImages(r)
		kind, cause, skipGBM = got+1, err, false
		if kind == imagesSinglePlane && o.planes == 1 {
			// The refused images already had one plane: single-plane
			// images would be the same ones.
			kind = imagesLinear
		}
	}
}

// setupImages gives the output the two renderer images it flips between,
// as dmabufs: allocated with GBM and imported by the renderer, else
// allocated and exported by the renderer, for each kind in turn. skipGBM
// leaves GBM out for the first kind. There is no CPU fallback (ADR 014): a
// GPU that cannot export a scanout-capable image cannot drive the output.
func (o *Output) setupImages(r ports.Renderer, kind imageKind, cause error, skipGBM bool) (imageKind, error) {
	if o.crossGPU() {
		return o.crossImages(r, cause, skipGBM)
	}
	err := cause
	start := kind
	for ; kind <= imagesLinear; kind++ {
		noGBM := skipGBM && kind == start
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
		// two refused driver attempts. AMD DCC retile modifiers are never
		// exported, whatever the plane lists: they cost an extra
		// full-surface GPU pass per frame (vulkan retilesDCC). On a Radeon
		// 680M the plane-accepted pick is therefore single-plane tiled
		// (0x200000010401b03). Only a plane listing no modifier at all for
		// the format (SDR) leaves the choice to the renderer.
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
		if !noGBM && o.card != nil && o.card.alloc != nil {
			gerr := o.gbmImages(r, mods, kind == imagesSinglePlane)
			if gerr == nil {
				return kind, nil
			}
			o.log.Info().Err(gerr).Str("connector", o.conn.name).Int("kind", int(kind)).Str("alloc", allocGBM).Msg("output image allocation")
		}
		// Deprecated fallback (neferwl#23): renderer-exported images, kept
		// while GBM is validated on few setups. Remove once no report shows
		// "alloc":"vulkan" for a few releases.
		if err = o.exportImages(r, mods, kind == imagesSinglePlane); err == nil {
			return kind, nil
		}
		o.log.Info().Err(err).Str("connector", o.conn.name).Int("kind", int(kind)).Str("alloc", allocVulkan).Msg("output image export")
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

// closeBufs closes every plane file of bufs. The callers own them: the
// renderer and the framebuffers keep their own references to the memory.
func closeBufs(bufs []domain.DMABuf) {
	for _, b := range bufs {
		for _, p := range b.Planes {
			_ = p.File.Close()
		}
	}
}

// exportImages makes the renderer's exported targets the output images.
//
// Deprecated fallback for gbmImages, to remove with neferwl#23. Exported
// Vulkan images corrupt scanout on nvidia-drm.
func (o *Output) exportImages(r ports.Renderer, mods []uint64, singlePlane bool) error {
	o.dropImages(r)
	bufs, err := r.ExportTargets(len(o.fbs), mods, singlePlane)
	if err != nil {
		return err
	}
	defer closeBufs(bufs)
	if err = o.showBufs(r, bufs, allocVulkan); err != nil {
		o.dropImages(r)
		_, _ = r.ExportTargets(0, nil, false)
		return err
	}
	return nil
}

// showBufs makes framebuffers of the output images bufs (already the
// renderer's targets), clears them and records their layout. The caller
// closes the plane files and drops the images on error.
func (o *Output) showBufs(r ports.Renderer, bufs []domain.DMABuf, alloc string) error {
	o.planes, o.imageMod = 0, 0
	for i := range bufs {
		if n := len(bufs[i].Planes); n == 0 || n > maxFBPlanes {
			return fmt.Errorf("output image has %d planes", n)
		}
		fb, err := o.k.addFB(&bufs[i], o.imageFormat())
		if err != nil {
			return err
		}
		o.fbs[i] = fb
	}
	if err := o.clearImages(r); err != nil {
		return err
	}
	o.planes, o.imageMod = len(bufs[0].Planes), bufs[0].Modifier
	ev := o.log.Info()
	if alloc == allocVulkan {
		// The deprecated fallback is in use: make it visible in reports.
		ev = o.log.Warn()
	}
	ev.Str("connector", o.conn.name).Uint64("modifier", bufs[0].Modifier).Int("planes", o.planes).Str("alloc", alloc).Msg("zero-copy output")
	return nil
}

// clearImages draws black into both output images and waits for the GPU.
// GPU memory starts undefined (old VRAM contents): both are cleared before
// the modeset shows one.
func (o *Output) clearImages(r ports.Renderer) error {
	var err error
	for i := range o.fbs {
		r.UseTarget(i)
		var done *os.File
		done, err = r.Render(domain.Scene{Background: "#000000"}, nil)
		if err == nil {
			done, err = o.copyFence(i, done)
		}
		if done != nil {
			// The modeset is a blocking commit: wait for the clear.
			waitCtx := o.runContext
			if waitCtx == nil {
				waitCtx = context.Background()
			}
			err = errors.Join(err, syncfile.Wait(waitCtx, done))
			done.Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// gbmImages allocates the output images with GBM, makes them the
// renderer's targets and framebuffers, and clears them. The modifiers are
// those the plane lists (mods, as for exportImages) that the renderer can
// render into and import. On failure nothing is left allocated.
func (o *Output) gbmImages(r ports.Renderer, mods []uint64, singlePlane bool) (err error) {
	o.dropImages(r)
	usable := r.TargetModifiers(mods, singlePlane)
	if len(usable) == 0 {
		return errors.New("no modifier both the plane and the renderer accept for GBM images")
	}
	var bufs []domain.DMABuf
	defer func() {
		closeBufs(bufs)
		if err != nil {
			o.dropImages(r)
		}
	}()
	for range o.fbs {
		img, cerr := o.card.allocate(o.Width(), o.Height(), o.imageFormat(), usable)
		if cerr != nil {
			return cerr
		}
		o.images = append(o.images, img)
		b, derr := o.card.imageDMABuf(img)
		if derr != nil {
			return derr
		}
		bufs = append(bufs, b)
	}
	if err = r.ImportTargets(bufs); err != nil {
		return err
	}
	return o.showBufs(r, bufs, allocGBM)
}

// dropImages frees the output images: their framebuffers, the GBM buffers
// and the renderer targets built over them (they would keep the memory
// alive after the buffers are gone). It is a no-op when there are none.
func (o *Output) dropImages(r ports.Renderer) {
	copied := o.transport == transportCopy
	o.closeCopier()
	if copied {
		_, _ = r.ExportTargets(0, nil, false)
	} else if len(o.images) > 0 {
		_ = r.ImportTargets(nil)
	}
	o.freeImages()
}

// freeImages removes the output images' framebuffers and frees the images
// GBM allocated. Callers that still have a renderer use dropImages: this
// alone leaves the renderer's targets over the freed images (the renderer
// drops them when it closes, before Close frees the output's images).
func (o *Output) freeImages() {
	o.closeCopier()
	o.front.forget()
	o.forgetOverlayProbes()
	for i, fb := range o.fbs {
		if fb != 0 {
			_ = o.k.rmFB(fb)
		}
		o.fbs[i] = 0
	}
	for _, img := range o.images {
		o.card.freeImage(img)
	}
	o.images = nil
}
