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
	// Security is the defensive gate. SecurityChanges wakes an idle owner;
	// the snapshot, not the queued notification payload, is authoritative.
	Security        ports.SessionSecurity
	SecurityChanges <-chan ports.SecurityState
	Instance        ports.OutputInstance
	SecurityEvents  chan<- ports.SecurityBackendEvent
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
	pipeline.Security = opts.Security
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
			case q, ok := <-incoming:
				if !ok {
					return
				}
				capture.Fail(ctx, q, fmt.Errorf("output stopped"), opts.Captured)
			default:
				return
			}
		}
	}()
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	seen := map[ports.WindowID]uint64{}
	var holds holdSnapshots
	// pending is a report the channel could not take, retried soon.
	var pending *ports.OutputPresented
	var security ports.SecurityState
	securityKnown := false
	failRequests := func(reason string) {
		for _, q := range requests {
			if !capture.Handed(q) {
				capture.Fail(ctx, q, fmt.Errorf("%s", reason), opts.Captured)
			}
		}
		clear(requests)
		requests = requestStorage[:0]
	}
	// A transition is its own frame, never a locker-buffer-dependent frame.
	// Keep its epoch through the fence wait and report only actual completion.
	checkSecurity := func() error {
		if opts.Security == nil {
			return nil
		}
		for {
			next := opts.Security.Snapshot()
			if securityKnown && next == security {
				return nil
			}
			// No hardware scanout exists: an already applied Off scene is
			// confirmed software inactivity, independent of its old epoch.
			inactive := haveScene && scene.Off
			security, securityKnown = next, true
			pending = nil
			haveScene, dirty = false, false
			if !security.Protected {
				continue
			}
			black := ports.Scene{Security: security, Output: opts.Name, OutputWidth: opts.Width, OutputHeight: opts.Height, Scale: 1, Background: "#000000"}
			black.Off = inactive
			scene = black
			kind := ports.ProtectionInactiveOutput
			if !inactive {
				kind = ports.ProtectionProtectedFrame
				done, err := r.Render(black, nil)
				if err != nil {
					return fmt.Errorf("render protection: %w", err)
				}
				if done != nil {
					err = syncfile.Wait(ctx, done)
					_ = done.Close()
					if err != nil {
						return fmt.Errorf("protection fence: %w", err)
					}
				}
			}
			if opts.Security.Snapshot() != security {
				continue
			}
			if opts.SecurityEvents != nil {
				proof := ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: security.Generation, Instance: opts.Instance, Kind: kind}}
				select {
				case opts.SecurityEvents <- proof:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			// Capture reply backpressure must not postpone the first black
			// completion/proof. Capture drainage is a separate parent barrier.
			failRequests("session protected")
		}
	}
	acceptScene := func(s ports.Scene) {
		if opts.Security != nil && (s.Security != security || s.Security != opts.Security.Snapshot()) {
			return
		}
		scene, haveScene, dirty = s, true, true
	}
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
		if err := checkSecurity(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var retry <-chan time.Time
		if pending != nil {
			retry = time.After(time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-opts.SecurityChanges:
			if !ok {
				opts.SecurityChanges = nil
			}
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			continue
		case b := <-pipeline.Completed():
			// The completed token has left the pipeline's queue. Return its
			// lease before any transition path can error or block.
			pipeline.Recycle(b, r)
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			continue
		case b := <-pipeline.HiddenCompleted():
			pipeline.RecycleHidden(b)
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			pipeline.Retire(scene)
			continue
		case q, ok := <-incoming:
			if !ok {
				incoming = nil
				continue
			}
			if err := checkSecurity(); err != nil {
				// q has left incoming but is not yet owned by requests/workers.
				capture.Fail(ctx, q, fmt.Errorf("output stopped"), opts.Captured)
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if opts.protected() {
				capture.Fail(ctx, q, fmt.Errorf("session protected"), opts.Captured)
			} else if scene.Off {
				capture.Fail(ctx, q, fmt.Errorf("output off"), opts.Captured)
			} else if len(requests) == cap(requests) {
				capture.Fail(ctx, q, fmt.Errorf("output capture batch full"), opts.Captured)
			} else {
				requests = append(requests, q)
				dirty = haveScene
			}
		case <-retry:
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if pending != nil {
				pending = opts.send(pending)
			}
			continue
		case <-trim.C():
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
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
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			acceptScene(s)
		case c, ok := <-contents:
			if !ok {
				contents = nil
				continue
			}
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			update(c)
		case c, ok := <-cursor:
			if !ok {
				cursor = nil
				continue
			}
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
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
					if err := checkSecurity(); err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}
					acceptScene(s)
				}
			case c, ok := <-contents:
				if !ok {
					contents = nil
				} else {
					if err := checkSecurity(); err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}
					update(c)
				}
			default:
				break drain
			}
		}
		if err := checkSecurity(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if opts.protected() {
			failRequests("session protected")
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
		if !opts.protected() && opts.Cursor != nil && opts.LoadCursor != nil && scene.Scale != cursorScale {
			cursorScale = scene.Scale
			if img, err := opts.LoadCursor(want, scene.Scale, 256); err == nil {
				if !opts.protected() {
					opts.Cursor.set(img)
				}
			} else {
				opts.Log.Warn().Err(err).Msg("cursor")
			}
		}
		if err := checkSecurity(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !haveScene {
			continue
		}
		start := time.Now()
		// Requests the scene cannot serve (exclusion of another session, a
		// workspace that moved) are failed here; see capture.Pipeline.Split.
		normal, clean, hidden := pipeline.Split(scene, requests)
		pipeline.Retire(scene)
		if len(hidden) > 0 {
			// A hidden workspace: a child renderer draws it. The display's
			// fences do not cover the child, so wait for it before reporting.
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests("security epoch changed")
				continue
			}
			pipeline.SubmitHidden(scene, surfaces, hidden)
			clear(hidden)
			// A slow child may outlive Wayland's stale-report timeout even
			// though this owner waits. Publish its non-expiring holds first.
			if opts.Presented != nil {
				_, reads, _ := pipeline.CapHiddenSeen(seen)
				if len(reads) > 0 {
					r := holds.report(opts.Name, seen, reads)
					select {
					case opts.Presented <- r:
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
		}
		if len(clean) > 0 {
			// Exclude requests: the frame without the excluded surfaces is
			// drawn first (same renderer, queue order), then the displayed one.
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests("security epoch changed")
				continue
			}
			cdone, renderErr := r.Render(pipeline.CleanScene(scene), surfaces)
			if cdone != nil {
				// Clean composition reads client buffers even when the displayed
				// frame is skipped by a gate/off transition. Own and wait its
				// fence before any Seen report or protection check can proceed.
				waitErr := syncfile.Wait(ctx, cdone)
				_ = cdone.Close()
				if waitErr != nil {
					if ctx.Err() != nil {
						return nil
					}
					return fmt.Errorf("clean frame fence: %w", waitErr)
				}
			}
			if renderErr != nil {
				return fmt.Errorf("render clean frame: %w", renderErr)
			}
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests("security epoch changed")
				continue
			}
			pipeline.SubmitScoped(scene.Security, r, clean)
			clear(clean)
		}
		if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
			failRequests("security epoch changed")
			continue
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
		// Do not capture or report a frame that crossed a transition during
		// Render/fence wait. The next loop establishes the new black proof.
		if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
			failRequests("security epoch changed")
			continue
		}
		pipeline.SubmitScoped(scene.Security, r, normal)
		clear(requests)
		requests = requestStorage[:0]
		frame++
		pending = opts.flipped(pending, seen, scene, surfaces)
		if opts.ScreenshotDir != "" && !opts.protected() {
			shot := r.Pixels()
			if opts.protected() || opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				continue
			}
			if shot == nil {
				opts.Log.Debug().Str("component", "headless").Str("output", opts.Name).Int("frame", frame).Msg("screenshot readback unavailable; frame skipped")
				continue
			}
			if opts.Cursor != nil {
				opts.Cursor.drawSecure(shot, opts.Security)
			}
			if opts.protected() || opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				continue
			}
			if err := writePNGSecure(filepath.Join(opts.ScreenshotDir, fmt.Sprintf("frame-%06d.png", frame)), shot, opts.Security, scene.Security); err != nil {
				return fmt.Errorf("screenshot: %w", err)
			}
			if err := writePNGSecure(filepath.Join(opts.ScreenshotDir, "latest.png"), shot, opts.Security, scene.Security); err != nil {
				return fmt.Errorf("latest screenshot: %w", err)
			}
		}
		opts.Log.Debug().Str("component", "render").Int("frame", frame).Uint64("seq", scene.Seq).Int("windows", len(scene.Windows)).Dur("ms", time.Since(start)).Msg("frame")
	}
}

// holdSnapshots caches the immutable maps of the child-hold reports. The
// receiver may still read an earlier report on another goroutine, so a cached
// map is never mutated: it is replaced by a clone when the content changes,
// and an unchanged publication allocates nothing.
type holdSnapshots struct {
	seen, reads map[ports.WindowID]uint64
}

func (h *holdSnapshots) report(output string, seen, reads map[ports.WindowID]uint64) ports.OutputPresented {
	if !maps.Equal(h.seen, seen) {
		h.seen = maps.Clone(seen)
	}
	if !maps.Equal(h.reads, reads) {
		h.reads = maps.Clone(reads)
	}
	return ports.OutputPresented{Output: output, Seen: h.seen, ChildReads: h.reads}
}

func (opts Options) protected() bool {
	return opts.Security != nil && opts.Security.Snapshot().Protected
}

func writePNGSecure(path string, img *image.RGBA, security ports.SessionSecurity, state ports.SecurityState) error {
	if security != nil && (state.Protected || security.Snapshot() != state) {
		return nil
	}
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
	if security != nil && security.Snapshot() != state {
		return nil
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
