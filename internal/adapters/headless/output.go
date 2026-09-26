package headless

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

type Options struct {
	// Cursor, when set, is drawn into screenshots with the image from
	// LoadCursor at the scene scale.
	Cursor        *Cursor
	LoadCursor    func(c ports.CursorChange, scale float64, limit int) (ports.CursorImage, error)
	Width, Height int
	ScreenshotDir string
	Log           zerowrap.Logger
	NewRenderer   func(w, h int) (ports.Renderer, error)
	// Name and Presented report what the output has read after each
	// frame, so wayland can release client buffers (nil: no reports).
	Name      string
	Presented chan<- ports.OutputPresented
}

func Run(ctx context.Context, opts Options, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange) error {
	r, err := opts.NewRenderer(opts.Width, opts.Height)
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer r.Close()
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty := false, false
	frame := 0
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	seen := map[ports.WindowID]uint64{}
	// pending is a report the channel could not take, retried soon.
	var pending *ports.OutputPresented
	update := func(c ports.SurfaceContent) {
		seen[c.ID] = max(seen[c.ID], c.Seq)
		dirty = dirty || scene.Shows(c.ID)
		if c.Empty() {
			delete(surfaces, c.ID)
		} else {
			surfaces[c.ID] = c
		}
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		var retry <-chan time.Time
		if pending != nil {
			retry = time.After(time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-retry:
			pending = opts.send(pending)
			continue
		case s, ok := <-scenes:
			if !ok {
				scenes = nil
				continue
			}
			scene, haveScene, dirty = s, true, true
		case c, ok := <-contents:
			if !ok {
				contents = nil
				continue
			}
			update(c)
		case c := <-cursor:
			// The cursor is only drawn into screenshots: no new frame.
			want, cursorScale = c, -1
		}
		// Drain all queued updates before presenting one frame.
	drain:
		for {
			select {
			case s, ok := <-scenes:
				if !ok {
					scenes = nil
				} else {
					scene, haveScene, dirty = s, true, true
				}
			case c, ok := <-contents:
				if !ok {
					contents = nil
				} else {
					update(c)
				}
			default:
				break drain
			}
		}
		if !haveScene || !dirty {
			// Contents not drawn are still read: report them.
			pending = opts.report(pending, seen)
			continue
		}
		dirty = false
		if opts.Cursor != nil && opts.LoadCursor != nil && scene.Scale != cursorScale {
			cursorScale = scene.Scale
			if img, err := opts.LoadCursor(want, scene.Scale, 256); err == nil {
				opts.Cursor.set(img)
			} else {
				opts.Log.Warn().Err(err).Msg("cursor")
			}
		}
		start := time.Now()
		done, err := r.Render(scene, surfaces)
		if err != nil {
			return fmt.Errorf("render frame: %w", err)
		}
		if done != nil {
			// No screen paces headless frames: wait for the GPU before
			// reporting that its buffers were read.
			err = waitFence(done)
			done.Close()
			if err != nil {
				return fmt.Errorf("frame fence: %w", err)
			}
		}
		frame++
		pending = opts.report(pending, seen)
		if opts.ScreenshotDir != "" {
			shot := r.Pixels()
			if opts.Cursor != nil {
				opts.Cursor.draw(shot)
			}
			if err := writePNG(filepath.Join(opts.ScreenshotDir, fmt.Sprintf("frame-%06d.png", frame)), shot); err != nil {
				return fmt.Errorf("screenshot: %w", err)
			}
			if err := writePNG(filepath.Join(opts.ScreenshotDir, "latest.png"), shot); err != nil {
				return fmt.Errorf("latest screenshot: %w", err)
			}
		}
		opts.Log.Debug().Str("component", "render").Int("frame", frame).Uint64("seq", scene.Seq).Int("windows", len(scene.Windows)).Dur("ms", time.Since(start)).Msg("frame")
	}
}

func writePNG(path string, img *image.RGBA) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".frame-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// report sends what the output read; a full channel drops it.
// report sends what the output read, or returns it to retry when the
// channel is full. Headless frames are not paced by a screen: Flip stays
// false and frame callbacks follow the refresh timer.
func (opts Options) report(_ *ports.OutputPresented, seen map[ports.WindowID]uint64) *ports.OutputPresented {
	if opts.Presented == nil {
		return nil
	}
	return opts.send(&ports.OutputPresented{Output: opts.Name, Seen: maps.Clone(seen)})
}

func (opts Options) send(r *ports.OutputPresented) *ports.OutputPresented {
	select {
	case opts.Presented <- *r:
		return nil
	default:
		return r
	}
}

// waitFence blocks until a sync file signals.
func waitFence(f *os.File) error {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, -1)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
