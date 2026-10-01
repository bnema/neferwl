package headless

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/presented"
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
		// An error path out of a gated frame: what it held is failed.
		pipeline.EndGate(capture.ErrFrameNotPresented)
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
	// reports holds what the channel could not take yet, retried soon.
	var reports presented.Queue
	var security ports.SecurityState
	securityKnown := false
	failRequests := func(reason string) {
		// Whatever this frame had handed to a worker is failed with it.
		pipeline.EndGate(capture.GateVerdict(errors.New(reason)))
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
			reports.Reset()
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
	// One timer for the wait of held requests, reset per pass (a time.After
	// per loop pass would leave a timer behind each).
	holdTimer := time.NewTimer(time.Hour)
	holdTimer.Stop()
	defer holdTimer.Stop()
	for {
		// A frame that left its gate open (any path out but its present) fails
		// what it held: nothing is delivered for a frame that did not present.
		pipeline.EndGate(capture.ErrFrameNotPresented)
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
		if reports.Len() > 0 {
			retry = time.After(time.Millisecond)
		}
		// Requests wait for a scene that shows their indicator, at most
		// capture.HoldFor (see capture.Hold).
		var holdDue <-chan time.Time
		if len(requests) > 0 {
			var wait time.Duration
			requests, wait = pipeline.Expire(scene, requests, time.Now())
			if wait > 0 {
				holdTimer.Reset(wait)
				holdDue = holdTimer.C
			}
		}
		if holdDue == nil {
			holdTimer.Stop()
		}
		select {
		case <-holdDue:
			continue
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
				q.Since = time.Now()
				requests = append(requests, q)
				// Its indicator may already be on screen; else it waits for the
				// scene that shows it.
				dirty = dirty || haveScene && capture.IndicatorShown(scene, q)
			}
		case <-retry:
			if err := checkSecurity(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			opts.flush(&reports)
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
			opts.report(&reports, nil, seen)
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
		// A request whose indicator the scene does not show yet stays held.
		ready, _ := pipeline.Hold(scene, requests, time.Now())
		if len(scene.CaptureIndicators) > 0 {
			// The captures of this frame are delivered only once its frame,
			// with the indicator, is presented (capture.Pipeline.EndGate).
			pipeline.BeginGate()
		}
		normal, excluded, hidden := pipeline.Split(scene, ready)
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
					reports.Push(ports.OutputPresented{Output: opts.Name, Seen: seen, ChildReads: reads})
					if reports.Drain(ctx, opts.Presented) != nil {
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
		// Captures never show the capture indicator: their frame goes first.
		took, pdone, perr := pipeline.SubmitPlain(r, scene, surfaces, normal)
		if pdone != nil {
			// The plain render reads client buffers even when its requests
			// are rejected by a gate transition: own and wait its fence
			// before any Seen report or protection check can proceed.
			waitErr := syncfile.Wait(ctx, pdone)
			_ = pdone.Close()
			if waitErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("plain frame fence: %w", waitErr)
			}
		}
		if perr != nil {
			return fmt.Errorf("render plain frame: %w", perr)
		}
		if took {
			normal = nil // answered from the frame without the indicator
		}
		if len(excluded) > 0 {
			// Exclude requests: the frame without the excluded surfaces is
			// drawn first (same renderer, queue order), then the displayed one.
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests("security epoch changed")
				continue
			}
			xdone, renderErr := r.Render(pipeline.ExcludedScene(scene), surfaces)
			if xdone != nil {
				// The excluded-frame composition reads client buffers even when the displayed
				// frame is skipped by a gate/off transition. Own and wait its
				// fence before any Seen report or protection check can proceed.
				waitErr := syncfile.Wait(ctx, xdone)
				_ = xdone.Close()
				if waitErr != nil {
					if ctx.Err() != nil {
						return nil
					}
					return fmt.Errorf("excluded frame fence: %w", waitErr)
				}
			}
			if renderErr != nil {
				return fmt.Errorf("render excluded frame: %w", renderErr)
			}
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests("security epoch changed")
				continue
			}
			pipeline.SubmitScoped(scene.Security, r, excluded)
			clear(excluded)
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
		// The indicated frame is presented: release what it gated.
		pipeline.EndGate(nil)
		requests = capture.Waiting(requests, scene)
		frame++
		opts.report(&reports, flipInfo(scene, surfaces), seen)
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

// report queues what the output read, with flip for a drawn frame, and
// sends what the channel takes now; the rest is retried soon.
func (opts Options) report(q *presented.Queue, flip *ports.FlipInfo, seen map[ports.WindowID]uint64) {
	if opts.Presented == nil {
		return
	}
	if q.Push(ports.OutputPresented{Output: opts.Name, Flip: flip, Seen: seen}) {
		opts.Log.Warn().Str("component", "render").Str("output", opts.Name).Msg("wayland is not reading output reports; flips merged")
	}
	q.Flush(opts.Presented)
}

// flush retries reports the channel could not take.
func (opts Options) flush(q *presented.Queue) {
	if opts.Presented != nil {
		q.Flush(opts.Presented)
	}
}

// flipInfo describes a drawn frame as a flip at the current CLOCK_MONOTONIC
// time (software clock, refresh unknown), so presentation feedback and
// frame callbacks follow headless frames.
func flipInfo(scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) *ports.FlipInfo {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	shows := map[ports.WindowID]uint64{}
	for id, c := range surfaces {
		if scene.Shows(id) {
			shows[id] = c.Seq
		}
	}
	return &ports.FlipInfo{When: time.Duration(ts.Nano()), Shows: shows}
}
