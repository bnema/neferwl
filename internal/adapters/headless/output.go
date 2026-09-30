package headless

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"image"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

type Options struct {
	// Cursor, when set, is drawn into screenshots with the image from
	// LoadCursor at the scene scale.
	Cursor        *Cursor
	LoadCursor    func(c ports.CursorChange, scale float64, limit int) (ports.CursorImage, error)
	Width, Height int
	ScreenshotDir string
	HDR           bool                       // virtual HDR output for protocol testing only
	Formats       chan<- ports.OutputFormats // confirmed color state; nil disables reporting
	Log           zerowrap.Logger
	NewRenderer   func(w, h int) (ports.Renderer, error)
	// NewCaptureRenderer makes the child renderer of a hidden workspace
	// capture session (Scene.CaptureScene). Nil: such captures fail closed.
	NewCaptureRenderer func(w, h int) (ports.Renderer, error)
	// Name and Presented report what the output has read after each
	// frame, so wayland can release client buffers (nil: no reports).
	Name      string
	Presented chan<- ports.OutputPresented
	Captured  chan<- ports.CaptureDone
	// Clock paces the renderer trim (nil: system clock).
	Clock ports.Clock
}

// trimEvery is how often an output lets its renderer free the buffers of
// windows it no longer draws.
const trimEvery = 10 * time.Second

func Run(ctx context.Context, opts Options, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, incoming <-chan ports.CaptureRequest) error {
	r, err := opts.NewRenderer(opts.Width, opts.Height)
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer r.Close()
	var confirmed *ports.OutputHDR
	if opts.HDR {
		r.SetHDR(203)
		if bufs, err := r.ExportTargets(1, nil); err != nil {
			opts.Log.Warn().Err(err).Str("output", opts.Name).Msg("virtual HDR unavailable; falling back to SDR")
			r.SetHDR(0)
			_, _ = r.ExportTargets(0, nil)
		} else {
			for _, b := range bufs {
				for _, p := range b.Planes {
					_ = p.File.Close()
				}
			}
			confirmed = &ports.OutputHDR{MaxLuminance: 1000, MaxFrameAverage: 400, MinLuminance: .005}
			opts.Log.Info().Str("output", opts.Name).Msg("virtual HDR enabled")
		}
		if opts.Formats != nil {
			select {
			case opts.Formats <- ports.OutputFormats{Output: opts.Name, HDR: confirmed}:
			case <-ctx.Done():
				return nil
			}
		}
	}
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty := false, false
	frame := 0
	var requestStorage [capture.MaxRequests]ports.CaptureRequest
	requests := requestStorage[:0]
	ctx, cancelCaptures := context.WithCancel(ctx)
	pipeline := capture.NewPipeline(ctx, opts.Captured)
	if opts.NewCaptureRenderer != nil {
		pipeline.EnableOffscreen(opts.NewCaptureRenderer)
	}
	defer func() {
		cancelCaptures()
		pipeline.Close(r)
		for _, q := range requests {
			if capture.Handed(q) {
				continue // already answered, or owned by a worker
			}
			capture.Fail(ctx, q, fmt.Errorf("output stopped"), opts.Captured)
		}
		for {
			select {
			case q := <-incoming:
				capture.Fail(ctx, q, fmt.Errorf("output stopped"), opts.Captured)
			default:
				return
			}
		}
	}()
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	seen := map[ports.WindowID]uint64{}
	// pending is a report the channel could not take, retried soon.
	var pending *ports.OutputPresented
	update := func(c ports.SurfaceContent) {
		seen[c.ID] = max(seen[c.ID], c.Seq)
		dirty = dirty || capture.Shows(scene, c.ID)
		if c.Empty() {
			delete(surfaces, c.ID)
		} else {
			surfaces[c.ID] = c
		}
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.System{}
	}
	trim := clk.NewTicker(trimEvery)
	defer trim.Stop()
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
		case b := <-pipeline.Completed():
			pipeline.Recycle(b, r)
			continue
		case b := <-pipeline.HiddenCompleted():
			pipeline.RecycleHidden(b)
			pipeline.Retire(scene)
			continue
		case q := <-incoming:
			if scene.Off {
				capture.Fail(ctx, q, fmt.Errorf("output off"), opts.Captured)
			} else if len(requests) == cap(requests) {
				capture.Fail(ctx, q, fmt.Errorf("output capture batch full"), opts.Captured)
			} else {
				requests = append(requests, q)
				dirty = haveScene
			}
		case <-retry:
			pending = opts.send(pending)
			continue
		case <-trim.C():
			// An idle output renders nothing: free what windows that left
			// it held, and the child of an ended session.
			pipeline.Retire(scene)
			if err := r.Trim(clk.Now()); err != nil {
				return fmt.Errorf("trim renderer: %w", err)
			}
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
		if scene.Off && len(requests) > 0 {
			for _, q := range requests {
				capture.Fail(ctx, q, fmt.Errorf("output off"), opts.Captured)
			}
			clear(requests)
			requests = requestStorage[:0]
		}
		if !haveScene || !dirty || scene.Off {
			// Contents not drawn are still read: report them. An output
			// turned off draws nothing.
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
		// Clean requests of a session the scene does not carry are failed
		// here. The others draw the clean frame first (same renderer, queue
		// order), then the displayed one; see capture.Pipeline.Split.
		normal, clean := pipeline.Split(scene, requests)
		pipeline.Retire(scene)
		if len(clean) > 0 && scene.CaptureScene != nil {
			// A hidden workspace: a child renderer draws it. The display's
			// fences do not cover the child, so wait for it before reporting.
			pipeline.SubmitHidden(scene, surfaces, clean)
			clear(clean)
			// A slow child may outlive Wayland's stale-report timeout even
			// though this owner waits. Publish its non-expiring holds first.
			if opts.Presented != nil {
				_, reads, _ := pipeline.CapHiddenSeen(seen)
				if len(reads) > 0 {
					r := &ports.OutputPresented{Output: opts.Name, Seen: maps.Clone(seen), ChildReads: maps.Clone(reads)}
					select {
					case opts.Presented <- *r:
					case <-ctx.Done():
						return nil
					}
				}
			}
			if err := pipeline.WaitHidden(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("workspace frame fence: %w", err)
			}
		} else if len(clean) > 0 {
			cdone, err := r.Render(pipeline.CleanScene(scene), surfaces)
			if err != nil {
				return fmt.Errorf("render clean frame: %w", err)
			}
			if cdone != nil {
				cdone.Close()
			}
			pipeline.Submit(r, clean)
			clear(clean)
		}
		done, err := r.Render(scene, surfaces)
		if err != nil {
			return fmt.Errorf("render frame: %w", err)
		}
		if done != nil {
			// No screen paces headless frames: wait for the GPU before
			// reporting that its buffers were read.
			err = syncfile.Wait(ctx, done)
			done.Close()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("frame fence: %w", err)
			}
		}
		if len(normal) > 0 {
			opts.Log.Debug().Str("output", opts.Name).Int("captures", len(normal)).Msg("capture frame composed")
		}
		pipeline.Submit(r, normal)
		clear(requests)
		requests = requestStorage[:0]
		frame++
		pending = opts.flipped(pending, seen, scene, surfaces)
		if opts.ScreenshotDir != "" {
			shot := r.Pixels()
			if shot == nil {
				opts.Log.Debug().Str("component", "headless").Str("output", opts.Name).Int("frame", frame).Msg("screenshot readback unavailable; frame skipped")
				continue
			}
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

// report sends what the output read, or returns it to retry when the
// channel is full. Frames that were not drawn carry no flip.
func (opts Options) report(_ *ports.OutputPresented, seen map[ports.WindowID]uint64) *ports.OutputPresented {
	if opts.Presented == nil {
		return nil
	}
	return opts.send(&ports.OutputPresented{Output: opts.Name, Seen: maps.Clone(seen)})
}

// flipped reports a drawn frame as a flip at the current CLOCK_MONOTONIC
// time (software clock, refresh unknown), so presentation feedback and
// frame callbacks follow headless frames. A report still waiting is
// sent first.
func (opts Options) flipped(pending *ports.OutputPresented, seen map[ports.WindowID]uint64, scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) *ports.OutputPresented {
	if opts.Presented == nil {
		return nil
	}
	var merged int
	if pending != nil && opts.send(pending) != nil {
		// The reader is behind: this frame's report replaces the waiting
		// one, and its flip counts as merged (its feedback is discarded).
		if pending.Flip != nil {
			merged = 1 + pending.Flip.Merged
		}
	}
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	shows := map[ports.WindowID]uint64{}
	for id, c := range surfaces {
		if scene.Shows(id) {
			shows[id] = c.Seq
		}
	}
	return opts.send(&ports.OutputPresented{Output: opts.Name, Seen: maps.Clone(seen), Flip: &ports.FlipInfo{When: time.Duration(ts.Nano()), Shows: shows, Merged: merged}})
}

func (opts Options) send(r *ports.OutputPresented) *ports.OutputPresented {
	select {
	case opts.Presented <- *r:
		return nil
	default:
		return r
	}
}
