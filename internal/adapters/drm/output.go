package drm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Renderer draws a scene into an RGBA image of the output size.
type Renderer interface {
	Render(ports.Scene, map[ports.WindowID]ports.SurfaceContent) error
	// CopyBGRX writes the last frame as XRGB8888 rows of the given pitch.
	CopyBGRX(dst []byte, pitch int)
	Close()
}

// Output is one KMS connector driven with two dumb buffers.
type Output struct {
	fd        int
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

// CursorLoader returns the cursor image for an output scale, at most limit
// pixels on a side.
type CursorLoader func(scale float64, limit int) (ports.CursorImage, error)

// Info describes the chosen output for clients (wl_output, xdg-output).
type Info struct {
	Name                 string
	Monitor              Monitor
	Width, Height        int
	RefreshMilli         int
	PhysicalW, PhysicalH int // millimetres
}

// Open picks a connector on the card fd according to want and allocates buffers.
func Open(fd int, card string, want Want, log zerowrap.Logger) (*Output, error) {
	crtcs, ids, err := resources(fd)
	if err != nil {
		return nil, err
	}
	var conns []connector
	for _, id := range ids {
		c, err := readConnector(fd, id)
		if err != nil {
			log.Warn().Err(err).Uint32("connector", id).Msg("read connector")
			continue
		}
		ev := log.Info().Str("connector", c.name).Bool("connected", c.connected)
		if c.connected {
			mon := readMonitor(card, c.name)
			modes := make([]string, 0, len(c.modes))
			for _, m := range c.modes {
				modes = append(modes, m.String())
			}
			ev = ev.Str("make", mon.Make).Str("model", mon.Model).Int("mm_w", c.mmW).Int("mm_h", c.mmH).Strs("modes", modes)
		}
		ev.Msg("connector")
		conns = append(conns, c)
	}
	c, mode, err := pickConnector(conns, want)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", card, err)
	}
	crtc, err := pickCrtc(fd, c, crtcs)
	if err != nil {
		return nil, err
	}
	o := &Output{fd: fd, crtc: crtc, conn: c, mode: mode, log: log, monitor: readMonitor(card, c.name)}
	if want.Name != "" && want.Name != c.name {
		log.Warn().Str("wanted", want.Name).Str("using", c.name).Msg("configured output not connected")
	}
	if o.saved, err = getCrtc(fd, crtc); err != nil {
		log.Warn().Err(err).Uint32("crtc", crtc).Msg("save crtc; it will not be restored on exit")
	}
	for i := range o.bufs {
		if o.bufs[i], err = newDumb(fd, o.Width(), o.Height()); err != nil {
			o.Close()
			return nil, err
		}
	}
	if o.cursor, err = newCursor(fd, crtc, slices.Index(crtcs, crtc)); err != nil {
		log.Warn().Err(err).Msg("no hardware cursor")
	}
	log.Info().Str("card", card).Str("connector", c.name).Str("mode", mode.String()).Str("make", o.monitor.Make).Str("model", o.monitor.Model).Uint32("crtc", crtc).Msg("output")
	return o, nil
}

// Cursor is the hardware cursor, or nil when the driver has none.
func (o *Output) Cursor() *Cursor { return o.cursor }

// Info returns the chosen connector, monitor and mode.
func (o *Output) Info() Info {
	return Info{Name: o.conn.name, Monitor: o.monitor, Width: o.Width(), Height: o.Height(), RefreshMilli: o.mode.refreshMilli(), PhysicalW: o.conn.mmW, PhysicalH: o.conn.mmH}
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
func (o *Output) present(r Renderer) error {
	b := o.bufs[o.back]
	r.CopyBGRX(b.mem, int(b.pitch))
	if err := flip(o.fd, o.crtc, b.fbID); err != nil {
		return err
	}
	o.pending, o.flipStart = true, time.Now()
	o.back = 1 - o.back
	return nil
}

// Close restores the CRTC state found at startup and frees buffers.
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
func (o *Output) Run(ctx context.Context, newRenderer func(w, h int) (Renderer, error), loadCursor CursorLoader, active <-chan bool, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent) error {
	r, err := newRenderer(o.Width(), o.Height())
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer r.Close()
	if err := o.modeset(); err != nil {
		return err
	}
	events := make(chan int, 4)
	evErr := make(chan error, 1)
	evCtx, stop := context.WithCancel(ctx)
	var reader sync.WaitGroup
	reader.Go(func() { o.readEvents(evCtx, events, evErr) })
	// Join the reader before the card fd can be closed by the caller.
	defer func() { stop(); reader.Wait() }()
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty, enabled := false, false, true
	cursorScale := -1.0 // not loaded yet
	frame := 0
	stats := time.NewTicker(10 * time.Second)
	defer stats.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-evErr:
			return err
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
		case n := <-events:
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
				o.setCursor(loadCursor, s.Scale)
			}
			scene, haveScene, dirty = s, true, true
		case c := <-contents:
			if c.Pixels == nil {
				delete(surfaces, c.ID)
			} else {
				surfaces[c.ID] = c
			}
			dirty = true
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

// setCursor loads the theme cursor for scale onto the cursor plane.
func (o *Output) setCursor(load CursorLoader, scale float64) {
	img, err := load(scale, o.cursor.Limit())
	if err == nil {
		err = o.cursor.SetImage(img.Pixels, img.W, img.H, img.HotX, img.HotY)
	}
	if err != nil {
		o.log.Warn().Err(err).Msg("cursor")
		return
	}
	o.log.Info().Float64("scale", scale).Int("w", img.W).Int("h", img.H).Msg("cursor image")
}

func (o *Output) readEvents(ctx context.Context, events chan<- int, errs chan<- error) {
	buf := make([]byte, 1024)
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(o.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			errs <- fmt.Errorf("drm poll: %w", err)
			return
		}
		if n <= 0 {
			continue
		}
		m, err := unix.Read(o.fd, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			errs <- fmt.Errorf("drm read: %w", err)
			return
		}
		if c := countFlipEvents(buf[:m]); c > 0 {
			select {
			case events <- c:
			case <-ctx.Done():
				return
			}
		}
	}
}
