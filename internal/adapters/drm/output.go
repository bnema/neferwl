package drm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Output is one KMS connector flipping between two images. One goroutine
// runs it (Run) and closes it.
type Output struct {
	fd      int
	flipped <-chan int // page flips of this CRTC, from Card.ReadEvents
	crtc    uint32
	conn    connector
	mode    modeInfo
	saved   modeCrtc
	// fbs are the two images frames alternate between, back the one the
	// next frame draws into. They are renderer images exported as dmabufs
	// (ADR 014: zero copy), or dumb buffers filled by the CPU when the GPU
	// cannot export a scanout-capable image (logged fallback).
	fbs       [2]uint32
	bufs      [2]*dumbBuffer // fallback only
	back      int
	pending   bool
	flipStart time.Time
	flips     int
	monitor   Monitor
	cursor    *Cursor
	log       zerowrap.Logger
	// Direct scanout: client framebuffers by DMABuf ID, GEM handle counts,
	// and the buffer on screen and queued (0: the composed image).
	card          *Card
	scanout       bool
	clientFBs     map[uint64]*clientFB
	shown, queued uint64
	reason        string // why the last frame was composed ("" = scanout)
	unsent        ports.OutputPresented
	// Tearing (ADR 006): async flips of scanned-out buffers that ask for
	// them, while the card accepts them. vrrProp is the CRTC's VRR_ENABLED
	// property (0: no VRR), turned on while a buffer is scanned out.
	tearing      bool
	vrrProp      uint32
	vrrOn, async bool
	// kind is how the images were made; validated once a modeset took them.
	kind      imageKind
	validated bool
}

// CursorLoader returns the image of a cursor at an output scale, at most
// limit pixels on a side; an empty image hides the cursor.
type CursorLoader func(c ports.CursorChange, scale float64, limit int) (ports.CursorImage, error)

// newOutput prepares the cursor and saves the CRTC for a connector on crtc.
func newOutput(card *Card, c connector, mode modeInfo, crtc uint32) (*Output, error) {
	log := card.log
	o := &Output{fd: card.fd, flipped: card.flips[crtc], crtc: crtc, conn: c, mode: mode, log: log, monitor: readMonitor(card.path, c.name), card: card, scanout: !card.want.NoScanout, clientFBs: map[uint64]*clientFB{}, reason: "start"}
	var err error
	if o.saved, err = getCrtc(card.fd, crtc); err != nil {
		log.Warn().Err(err).Uint32("crtc", crtc).Msg("save crtc; it will not be restored on exit")
	}
	o.tearing = card.async && !card.want.NoTearing
	if !card.want.NoVRR {
		o.vrrProp = vrrProperty(card.fd, c.id, crtc)
	}
	if o.cursor, err = newCursor(card.fd, crtc, slices.Index(card.crtcs, crtc)); err != nil {
		log.Warn().Err(err).Str("connector", c.name).Msg("no hardware cursor")
	}
	log.Info().Str("card", card.path).Str("connector", c.name).Str("mode", mode.String()).Str("make", o.monitor.Make).Str("model", o.monitor.Model).Uint32("crtc", crtc).Bool("tearing", o.tearing).Bool("vrr", o.vrrProp != 0).Msg("output")
	return o, nil
}

// Cursor is the hardware cursor, or nil when the driver has none.
func (o *Output) Cursor() *Cursor { return o.cursor }

// Info describes the output for core and clients. Unknown EDID fields are empty.
func (o *Output) Info() ports.OutputInfo {
	known := func(v string) string {
		if v == "Unknown" {
			return ""
		}
		return v
	}
	return ports.OutputInfo{Name: o.conn.name, Make: known(o.monitor.Make), Model: known(o.monitor.Model), Serial: o.monitor.Serial, Width: o.Width(), Height: o.Height(), RefreshMilli: o.mode.refreshMilli(), PhysicalW: o.conn.mmW, PhysicalH: o.conn.mmH}
}

func (o *Output) Width() int  { return int(o.mode.HDisplay) }
func (o *Output) Height() int { return int(o.mode.VDisplay) }

// modeset shows the front buffer; needed at start and after every VT resume.
func (o *Output) modeset() error {
	o.pending = false
	o.shown, o.queued = 0, 0
	if err := setCrtc(o.fd, o.crtc, o.conn.id, o.fbs[1-o.back], &o.mode); err != nil {
		return fmt.Errorf("set crtc: %w", err)
	}
	o.log.Info().Str("connector", o.conn.name).Msg("modeset")
	// Another master may have left VRR on: start from off.
	o.vrrOn = true
	o.setVRR(false)
	return nil
}

// present queues a page flip to the frame just drawn into the back image
// (copied into it by the CPU on the dumb-buffer fallback).
func (o *Output) present(r ports.Renderer) error {
	if b := o.bufs[o.back]; b != nil {
		r.CopyBGRX(b.mem, int(b.pitch))
	}
	o.setVRR(false)
	o.setAsync(false)
	if err := flip(o.fd, o.crtc, o.fbs[o.back], false); err != nil {
		return err
	}
	o.pending, o.flipStart = true, time.Now()
	o.queued = 0
	o.back = 1 - o.back
	return nil
}

// tryScanout flips a fullscreen client buffer directly. It reports false,
// with the reason logged on change, when the frame must be composed.
func (o *Output) tryScanout(scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) (bool, error) {
	reason := "disabled"
	var c ports.SurfaceContent
	if o.scanout {
		c, reason = scanoutCandidate(scene, surfaces, o.Width(), o.Height())
	}
	var fb uint32
	if reason == "" {
		fb, reason = o.scanoutFB(c.DMABuf, time.Now())
	}
	if reason != o.reason {
		o.log.Info().Bool("direct_scanout", reason == "").Str("reason", reason).Str("connector", o.conn.name).Msg("scanout")
		o.reason = reason
	}
	if reason != "" {
		return false, nil
	}
	if o.shown == c.DMABuf.ID && o.queued == 0 {
		// Already on screen: nothing to flip.
		return true, nil
	}
	o.setVRR(true)
	// Drivers refuse async flips that change the format or modifier: the
	// flip from the composed image into scanout waits for vblank.
	async := c.Async && o.tearing && o.shown != 0
	o.setAsync(async)
	err := flip(o.fd, o.crtc, fb, async)
	if err != nil && async && errors.Is(err, unix.EINVAL) {
		// Refused anyway (e.g. the client changed buffer layout): flip
		// at vblank this time.
		o.log.Debug().Err(err).Str("connector", o.conn.name).Msg("async flip refused")
		err = flip(o.fd, o.crtc, fb, false)
	}
	if err != nil {
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ERANGE) {
			// KMS refuses this buffer on the plane: compose it instead.
			o.log.Info().Err(err).Uint32("format", c.DMABuf.Format).Uint64("modifier", c.DMABuf.Modifier).Msg("scanout flip refused")
			o.clientFBs[c.DMABuf.ID].failed = "flip_refused"
			o.reason = "flip_refused"
			o.log.Info().Bool("direct_scanout", false).Str("reason", o.reason).Str("connector", o.conn.name).Msg("scanout")
			return false, nil
		}
		return true, err
	}
	o.pending, o.flipStart = true, time.Now()
	o.queued = c.DMABuf.ID
	return true, nil
}

// setVRR turns variable refresh on or off, when the output has it.
func (o *Output) setVRR(on bool) {
	if o.vrrProp == 0 || on == o.vrrOn {
		return
	}
	v := uint64(0)
	if on {
		v = 1
	}
	if err := setProp(o.fd, o.crtc, objCrtc, o.vrrProp, v); err != nil {
		o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("vrr; disabled")
		o.vrrProp = 0
		return
	}
	o.vrrOn = on
	o.log.Info().Bool("vrr", on).Str("connector", o.conn.name).Msg("vrr")
}

// setAsync logs when the output starts or stops tearing.
func (o *Output) setAsync(on bool) {
	if on != o.async {
		o.async = on
		o.log.Info().Bool("tearing", on).Str("connector", o.conn.name).Msg("tearing")
	}
}

// Close restores the CRTC state found at startup, when it had a mode, and
// frees buffers.
func (o *Output) Close() {
	if o.cursor != nil {
		o.cursor.close()
	}
	o.setVRR(false)
	if o.saved.crtcID != 0 {
		ids := []uint32{o.conn.id}
		s := o.saved
		if s.modeValid != 0 {
			_ = setCrtc(o.fd, s.crtcID, ids[0], s.fbID, &s.mode)
		}
	}
	o.freeImages()
	o.shown, o.queued = 0, 0
	o.dropClientFBs(time.Now(), true)
}

// Run renders scenes and flips until ctx ends. active reports seat enable/disable.
// Each completed page flip is reported on presented (dropped when full).
func (o *Output) Run(ctx context.Context, newRenderer func(w, h int) (ports.Renderer, error), loadCursor CursorLoader, active <-chan bool, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, presented chan<- ports.OutputPresented) error {
	r, err := newRenderer(o.Width(), o.Height())
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer r.Close()
	// The seat sends its state first; while switched away (e.g. a monitor
	// plugged in on another VT) the modeset waits for the enable.
	enabled := true
	select {
	case enabled = <-active:
	default:
	}
	// Output images, from the best to the fallback (ADR 014). While
	// switched away they are validated by the first modeset on enable.
	if enabled {
		err = o.showImages(r, imagesDriver, nil)
	} else {
		o.kind, err = o.setupImages(r, imagesDriver, nil)
	}
	if err != nil {
		return err
	}
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty := false, false
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	frame := 0
	stats := time.NewTicker(10 * time.Second)
	defer stats.Stop()
	// seen is the latest content Seq per window, reported to wayland with
	// what the output scans out, after each frame decision.
	seen := map[ports.WindowID]uint64{}
	reportDirty := false
	flipped := false
	for {
		if flipped || reportDirty {
			o.report(presented, flipped, seen)
			flipped, reportDirty = false, false
		} else {
			o.flushReport(presented)
		}
		// An unsent report is retried soon, not only on the next event.
		var retry <-chan time.Time
		if o.unsent.Output != "" {
			retry = time.After(time.Millisecond)
		}
		select {
		case <-retry:
			continue
		case <-ctx.Done():
			return nil
		case on := <-active:
			// Modeset on every enable: a fast disable+enable can coalesce to one true.
			if on {
				err := o.modeset()
				if lostMaster(err) {
					// The seat took DRM master back: wait for the next enable.
					o.log.Warn().Err(err).Msg("resume")
					continue
				}
				if refused(err) && !o.validated && o.kind < imagesDumb {
					o.log.Warn().Err(err).Str("connector", o.conn.name).Int("kind", int(o.kind)).Msg("modeset refused the output images")
					o.freeImages()
					err = o.showImages(r, o.kind+1, err)
				}
				if err != nil {
					o.log.Error().Err(err).Msg("resume")
					return err
				}
				o.validated = true
				dirty = haveScene
				if o.cursor != nil {
					if err := o.cursor.Reapply(); err != nil {
						o.log.Warn().Err(err).Msg("cursor")
					}
				}
			}
			enabled = on
			o.log.Info().Bool("enabled", on).Msg("output")
		case n := <-o.flipped:
			if !o.pending {
				continue // stale event from before a modeset
			}
			o.pending = false
			o.flips += n
			o.shown = o.queued
			o.queued = 0
			flipped = true
			if d := time.Since(o.flipStart); d > 20*time.Millisecond {
				o.log.Info().Dur("flip_ms", d).Msg("slow flip")
			}
		case s := <-scenes:
			if o.cursor != nil && loadCursor != nil && s.Scale != cursorScale {
				cursorScale = s.Scale
				o.setCursor(loadCursor, want, s.Scale)
			}
			scene, haveScene, dirty = s, true, true
		case c := <-cursor:
			want = c
			// Before the first scene the scale is unknown: loaded then.
			if o.cursor != nil && loadCursor != nil && cursorScale > 0 {
				o.setCursor(loadCursor, want, cursorScale)
			}
			continue
		case c := <-contents:
			if c.Empty() {
				delete(surfaces, c.ID)
			} else {
				surfaces[c.ID] = c
			}
			if c.Seq > seen[c.ID] {
				seen[c.ID] = c.Seq
				reportDirty = true
			}
			// Windows on other outputs or workspaces do not need a frame.
			dirty = dirty || scene.Shows(c.ID)
		case <-stats.C:
			o.dropClientFBs(time.Now(), false)
			ev := o.log.Info().Int("frames", frame).Int("flips", o.flips)
			if o.cursor != nil {
				cs := o.cursor.TakeStats()
				ev = ev.Int("cursor_moves", cs.Moves).Int("cursor_ioctls", cs.Ioctls).Dur("cursor_max_ioctl_ms", cs.MaxIoctl)
			}
			ev.Msg("stats")
		}
		if !dirty || !haveScene || !enabled || o.pending {
			continue
		}
		start := time.Now()
		direct, err := o.tryScanout(scene, surfaces)
		if !direct {
			r.UseTarget(o.back)
			if err := r.Render(scene, surfaces); err != nil {
				return fmt.Errorf("render frame: %w", err)
			}
			err = o.present(r)
		}
		copyStart := time.Now()
		if err != nil {
			if errors.Is(err, unix.EBUSY) {
				// A flip is still in flight; retry after its event.
				o.pending, o.flipStart = true, time.Now()
				continue // dirty stays true
			}
			if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
				// Lost DRM master mid-switch; the seat disable follows.
				o.log.Warn().Err(err).Msg("flip refused")
				enabled = false
				continue
			}
			o.log.Error().Err(err).Msg("page flip")
			return fmt.Errorf("page flip: %w", err)
		}
		dirty = false
		reportDirty = true
		frame++
		o.log.Debug().Int("frame", frame).Uint64("seq", scene.Seq).Int("windows", len(scene.Windows)).Dur("render", time.Since(start)).Dur("copy", time.Since(copyStart)).Msg("frame")
	}
}

// setCursor loads a cursor for scale onto the cursor plane.
func (o *Output) setCursor(load CursorLoader, c ports.CursorChange, scale float64) {
	img, err := load(c, scale, o.cursor.Limit())
	if err == nil {
		err = o.cursor.SetImage(img.Pixels, img.W, img.H, img.HotX, img.HotY)
	}
	if err != nil {
		o.log.Warn().Err(err).Msg("cursor")
		return
	}
	o.log.Debug().Float64("scale", scale).Str("shape", c.Shape).Bool("client", c.Image != nil).Int("w", img.W).Int("h", img.H).Msg("cursor image")
}

// report tells wayland what the output shows and has read, so replaced
// client buffers can be released. It must follow the frame decision: a
// buffer is safe once a later content is seen and it is neither shown nor
// queued. A report the channel cannot take waits in unsent, replaced by
// newer ones (Flip kept), and is retried on the next loop.
func (o *Output) report(presented chan<- ports.OutputPresented, flip bool, seen map[ports.WindowID]uint64) {
	r := ports.OutputPresented{Output: o.conn.name, Flip: flip || o.unsent.Flip, Shown: o.shown, Queued: o.queued, Seen: maps.Clone(seen)}
	o.unsent = r
	o.flushReport(presented)
}

// flushReport sends the unsent report, if any, without blocking.
func (o *Output) flushReport(presented chan<- ports.OutputPresented) {
	if o.unsent.Output == "" {
		return
	}
	select {
	case presented <- o.unsent:
		o.unsent = ports.OutputPresented{}
	default:
	}
}

// imageKind is how output images are made, best first.
type imageKind int

const (
	imagesDriver imageKind = iota // exported, modifier chosen by the driver
	imagesLinear                  // exported, linear
	imagesDumb                    // dumb buffers filled by CPU copies
)

// showImages sets up images from kind on and modesets them. KMS may refuse
// an image only at modeset: then the next kind is tried. cause is why the
// previous kind failed, for the log.
func (o *Output) showImages(r ports.Renderer, kind imageKind, cause error) error {
	for {
		got, err := o.setupImages(r, kind, cause)
		if err != nil {
			return err
		}
		o.kind = got
		if err = o.modeset(); err == nil {
			o.validated = true
			return nil
		}
		if !refused(err) {
			return err
		}
		o.log.Warn().Err(err).Str("connector", o.conn.name).Int("kind", int(got)).Msg("modeset refused the output images")
		o.freeImages()
		if got == imagesDumb {
			return err
		}
		kind, cause = got+1, err
	}
}

// refused reports a modeset error meaning KMS rejects the images.
func refused(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ERANGE)
}

// lostMaster reports a modeset error meaning the seat is switched away.
func lostMaster(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)
}

// setupImages gives the output the two images it flips between: renderer
// targets exported as dmabufs, or dumb buffers as a logged fallback. A
// kind that fails falls through to the next one.
func (o *Output) setupImages(r ports.Renderer, kind imageKind, cause error) (imageKind, error) {
	err := cause
	for ; kind < imagesDumb; kind++ {
		mods := []uint64(nil)
		if kind == imagesLinear {
			mods = []uint64{0}
		}
		if err = o.exportImages(r, mods); err == nil {
			return kind, nil
		}
		o.log.Info().Err(err).Str("connector", o.conn.name).Int("kind", int(kind)).Msg("output image export")
	}
	o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("output falls back to CPU copies (ADR 014)")
	// The renderer draws into its own image again, read back by CopyBGRX.
	_, _ = r.ExportTargets(0, nil)
	for i := range o.bufs {
		if o.bufs[i], err = newDumb(o.fd, o.Width(), o.Height()); err != nil {
			o.freeImages()
			return imagesDumb, err
		}
		o.fbs[i] = o.bufs[i].fbID
	}
	return imagesDumb, nil
}

// exportImages makes the renderer's exported targets the output images.
func (o *Output) exportImages(r ports.Renderer, mods []uint64) error {
	bufs, err := r.ExportTargets(len(o.fbs), mods)
	if err != nil {
		return err
	}
	for i := range bufs {
		if err == nil {
			o.fbs[i], err = o.card.addFB(&bufs[i])
		}
		bufs[i].Planes[0].File.Close()
	}
	// GPU memory starts undefined (old VRAM contents): clear both images
	// before the modeset shows one.
	for i := range o.fbs {
		if err != nil {
			break
		}
		r.UseTarget(i)
		err = r.Render(ports.Scene{Background: "#000000"}, nil)
	}
	if err != nil {
		o.freeImages()
		_, _ = r.ExportTargets(0, nil)
		return err
	}
	o.log.Info().Str("connector", o.conn.name).Uint64("modifier", bufs[0].Modifier).Msg("zero-copy output")
	return nil
}

// freeImages removes the output images; dumb buffers own their fb.
func (o *Output) freeImages() {
	for i, fb := range o.fbs {
		if b := o.bufs[i]; b != nil {
			b.destroy(o.fd)
			o.bufs[i] = nil
		} else if fb != 0 {
			v := fb
			_ = ioctl(o.fd, ioctlRmFB, unsafe.Pointer(&v))
		}
		o.fbs[i] = 0
	}
}
