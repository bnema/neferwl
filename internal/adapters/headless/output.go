package headless

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
)

type Renderer interface {
	Render(ports.Scene, map[ports.WindowID]ports.SurfaceContent) error
	Pixels() *image.RGBA
	Close()
}

type Options struct {
	Width, Height int
	ScreenshotDir string
	Log           zerowrap.Logger
	NewRenderer   func(w, h int) (Renderer, error)
}

func Run(ctx context.Context, opts Options, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent) error {
	r, err := opts.NewRenderer(opts.Width, opts.Height)
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer r.Close()
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene := false
	frame := 0
	update := func(c ports.SurfaceContent) {
		if c.Pixels == nil {
			delete(surfaces, c.ID)
		} else {
			surfaces[c.ID] = c
		}
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-scenes:
			if !ok {
				scenes = nil
				continue
			}
			scene, haveScene = s, true
		case c, ok := <-contents:
			if !ok {
				contents = nil
				continue
			}
			update(c)
		}
		// Drain all queued updates before presenting one frame.
	drain:
		for {
			select {
			case s, ok := <-scenes:
				if !ok {
					scenes = nil
				} else {
					scene, haveScene = s, true
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
		if !haveScene {
			continue
		}
		start := time.Now()
		if err := r.Render(scene, surfaces); err != nil {
			return fmt.Errorf("render frame: %w", err)
		}
		frame++
		if opts.ScreenshotDir != "" {
			if err := writePNG(filepath.Join(opts.ScreenshotDir, fmt.Sprintf("frame-%06d.png", frame)), r.Pixels()); err != nil {
				return fmt.Errorf("screenshot: %w", err)
			}
			if err := writePNG(filepath.Join(opts.ScreenshotDir, "latest.png"), r.Pixels()); err != nil {
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
