package drm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Output is one KMS connector driven with two dumb buffers. One goroutine
// runs it (Run) and closes it.
type Output struct {
	fd        int
	flipped   <-chan int // page flips of this CRTC, from Card.ReadEvents
	crtc      uint32
	conn      connector
	mode      modeInfo
	saved     modeCrtc
	bufs      [2]*dumbBuffer
	back      int
	pending   bool
	flipStart time.Time
	flips     int
	monitor   Monitor
	cursor    *Cursor
	log       zerowrap.Logger
}

// CursorLoader returns the image of a cursor at an output scale, at most
// limit pixels on a side; an empty image hides the cursor.
type CursorLoader func(c ports.CursorChange, scale float64, limit int) (ports.CursorImage, error)

// newOutput allocates buffers and the cursor for a connector on crtc.
func newOutput(card *Card, c connector, mode modeInfo, crtc uint32) (*Output, error) {
	log := card.log
	o := &Output{fd: card.fd, flipped: card.flips[crtc], crtc: crtc, conn: c, mode: mode, log: log, monitor: readMonitor(card.path, c.name)}
	var err error
	if o.saved, err = getCrtc(card.fd, crtc); err != nil {
		log.Warn().Err(err).Uint32("crtc", crtc).Msg("save crtc; it will not be restored on exit")
	}
	for i := range o.bufs {
		if o.bufs[i], err = newDumb(card.fd, o.Width(), o.Height()); err != nil {
			o.Close()
			return nil, err
		}
	}
	if o.cursor, err = newCursor(card.fd, crtc, slices.Index(card.crtcs, crtc)); err != nil {
		log.Warn().Err(err).Str("connector", c.name).Msg("no hardware cursor")
	}
	log.Info().Str("card", card.path).Str("connector", c.name).Str("mode", mode.String()).Str("make", o.monitor.Make).Str("model", o.monitor.Model).Uint32("crtc", crtc).Msg("output")
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
	front := o.bufs[1-o.back]
	if err := setCrtc(o.fd, o.crtc, o.conn.id, front.fbID, &o.mode); err != nil {
		return fmt.Errorf("set crtc: %w", err)
	}
	o.log.Info().Str("connector", o.conn.name).Msg("modeset")
	return nil
}

// present copies img into the back buffer and queues a page flip.
func (o *Output) present(r ports.Renderer) error {
	b := o.bufs[o.back]
	r.CopyBGRX(b.mem, int(b.pitch))
	if err := flip(o.fd, o.crtc, b.fbID); err != nil {
		return err
	}
	o.pending, o.flipStart = true, time.Now()
	o.back = 1 - o.back
	return nil
}

// Close restores the CRTC state found at startup, when it had a mode, and
// frees buffers.
func (o *Output) Close() {
	if o.cursor != nil {
		o.cursor.close()
	}
	if o.saved.crtcID != 0 {
		ids := []uint32{o.conn.id}
		s := o.saved
		if s.modeValid != 0 {
			_ = setCrtc(o.fd, s.crtcID, ids[0], s.fbID, &s.mode)
		}
	}
	for _, b := range o.bufs {
		if b != nil {
			b.destroy(o.fd)
		}
	}
}

// Run renders scenes and flips until ctx ends. active reports seat enable/disable.
func (o *Output) Run(ctx context.Context, newRenderer func(w, h int) (ports.Renderer, error), loadCursor CursorLoader, active <-chan bool, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange) error {
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
	if enabled {
		if err := o.modeset(); err != nil {
			return err
		}
	}
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty := false, false
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	frame := 0
	stats := time.NewTicker(10 * time.Second)
	defer stats.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case on := <-active:
			// Modeset on every enable: a fast disable+enable can coalesce to one true.
			if on {
				if err := o.modeset(); err != nil {
					o.log.Error().Err(err).Msg("resume")
					return err
				}
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
			// Windows on other outputs or workspaces do not need a frame.
			dirty = dirty || scene.Shows(c.ID)
		case <-stats.C:
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
		if err := r.Render(scene, surfaces); err != nil {
			return fmt.Errorf("render frame: %w", err)
		}
		copyStart := time.Now()
		if err := o.present(r); err != nil {
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
