package headless

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	"github.com/bnema/neferwl/internal/adapters/outputkit/corewake"
	"github.com/bnema/neferwl/internal/adapters/outputkit/presented"
	"github.com/bnema/neferwl/internal/adapters/outputkit/surfaces"
	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// ModeRequest is a live mode transaction. Result must be buffered by its sender.
// A non-nil HDR replaces the output's HDR settings in place (same output,
// same screen): an enable or SDR white change rebuilds the renderer, a peak
// change only republishes the caps. Mode is then the current mode.
type ModeRequest struct {
	Mode   domain.OutputMode
	HDR    *domain.OutputHDRSettings
	Result chan<- error
}

type Options struct {
	// Security is the defensive gate. SecurityChanges wakes an idle owner;
	// the snapshot, not the queued notification payload, is authoritative.
	Security        ports.SessionSecurity
	SecurityChanges <-chan domain.SecurityState
	Instance        domain.OutputInstance
	SecurityEvents  chan<- ports.SecurityBackendEvent
	// Cursor, when set, supplies cursor-requesting captures and debug screenshots
	// with the image from LoadCursor at the scene scale and transform. It is in
	// target space: the image comes rotated and Move takes target pixels.
	Cursor        *Cursor
	LoadCursor    func(c domain.CursorChange, scale float64, t domain.BufferTransform, limit int) (domain.CursorImage, error)
	Width, Height int
	ScreenshotDir string
	// HDR is the virtual output's HDR request (protocol testing only);
	// HDR.Enabled tries a PQ target and falls back to SDR when unavailable.
	HDR domain.OutputHDRSettings
	// HDRLuminance is the virtual display's luminance facts reported while
	// HDR is confirmed; zero uses DefaultVirtualHDRLuminance.
	HDRLuminance domain.OutputHDR
	// RawHDR also writes latest-pq.png, the raw PQ codes of an HDR output
	// (16-bit), next to the screenshots. Test-only; needs HDR and ScreenshotDir.
	RawHDR      bool
	Formats     chan<- domain.OutputFormats // confirmed color state; nil disables reporting
	Log         zerowrap.Logger
	NewRenderer func(w, h int) (ports.Renderer, error)
	// NewCaptureRenderer makes the child renderer of a hidden workspace
	// capture session (Scene.CaptureScene). Nil: such captures fail closed.
	NewCaptureRenderer func(w, h int) (ports.Renderer, error)
	// Name and Presented report what the output has read after each
	// frame, so wayland can release client buffers (nil: no reports).
	Name      string
	Presented ports.PresentationSink
	// Frames reports new presented sequences and wakes core for animations.
	// Sends never block; new sequences are retried (nil: nobody is told).
	Frames   chan<- domain.OutputFrame
	Events   chan<- ports.OutputEvent
	Captured chan<- domain.CaptureDone
	// RefreshMilli limits presentation to this refresh in mHz; zero renders immediately.
	RefreshMilli int
	// Modes applies live modes on the owner goroutine; nil keeps the initial mode.
	Modes <-chan domain.OutputMode
	// ModeRequests acknowledges owner-side application before inventory publication.
	ModeRequests <-chan ModeRequest
	// Clock paces presentation and renderer trim (nil: system clock).
	Clock ports.Clock
}

// trimEvery is how often an output lets its renderer free the buffers of
// windows it no longer draws.
const trimEvery = 10 * time.Second

// SetMode sets the initial mode. Live updates are sent through Modes; RunOutput
// applies them to its private Options copy on the renderer owner goroutine.
func (opts *Options) SetMode(w, h, refresh int) {
	opts.Width, opts.Height, opts.RefreshMilli = w, h, refresh
}

func refreshPeriod(refresh int) time.Duration {
	if refresh <= 0 {
		return 0
	}
	period := time.Second * 1000 / time.Duration(refresh)
	if time.Second*1000%time.Duration(refresh) != 0 {
		period++ // Round up so fractional nanoseconds cannot exceed the refresh.
	}
	return period
}

// DefaultVirtualHDRLuminance is the luminance a virtual HDR output reports
// when Options.HDRLuminance is unset.
var DefaultVirtualHDRLuminance = domain.OutputHDR{MaxLuminance: 1000, MaxFrameAverage: 400, MinLuminance: .005}

// enableHDR switches r to a PQ target at the settings' SDR white and probes
// one exported target; the probe's files are closed. On error the caller
// falls back or discards r.
func enableHDR(r ports.Renderer, settings domain.OutputHDRSettings) error {
	r.SetHDR(float64(settings.Normalized().SDRBrightness))
	bufs, err := r.ExportTargets(1, nil, false)
	if err != nil {
		return err
	}
	for _, b := range bufs {
		for _, p := range b.Planes {
			_ = p.File.Close()
		}
	}
	return nil
}

// setupHDR applies settings to a fresh renderer and returns the confirmed
// HDR luminance, nil for SDR (also when HDR was asked but is unavailable).
func setupHDR(r ports.Renderer, opts Options, settings domain.OutputHDRSettings) *domain.OutputHDR {
	if !settings.Enabled {
		return nil
	}
	if err := enableHDR(r, settings); err != nil {
		opts.Log.Warn().Err(err).Str("output", opts.Name).Msg("virtual HDR unavailable; falling back to SDR")
		r.SetHDR(0)
		_, _ = r.ExportTargets(0, nil, false)
		return nil
	}
	lum := opts.HDRLuminance
	if lum == (domain.OutputHDR{}) {
		lum = DefaultVirtualHDRLuminance
	}
	opts.Log.Info().Str("output", opts.Name).Msg("virtual HDR enabled")
	return &lum
}

// reportFormats sends the confirmed color state; false when ctx ended.
func reportFormats(ctx context.Context, opts Options, confirmed *domain.OutputHDR) bool {
	if opts.Formats == nil {
		return true
	}
	select {
	case opts.Formats <- domain.OutputFormats{Output: opts.Name, HDR: confirmed}:
		return true
	case <-ctx.Done():
		return false
	}
}

// reportCaps sends the HDR caps of settings and the confirmed state; false
// when ctx ended.
func reportCaps(ctx context.Context, opts Options, settings domain.OutputHDRSettings, confirmed *domain.OutputHDR) bool {
	if opts.Events == nil {
		return true
	}
	facts := domain.HDRCapsInput{Reason: "virtual HDR inactive"}
	if confirmed != nil {
		facts.Capable = true
		facts.MaxLuminance, facts.MaxFrameAverage, facts.MinLuminance = confirmed.MaxLuminance, confirmed.MaxFrameAverage, confirmed.MinLuminance
	}
	event := domain.NewOutputHDRCaps(opts.Name, settings, facts, confirmed != nil)
	select {
	case opts.Events <- ports.OutputHDRCaps{OutputHDRCaps: event}:
		return true
	case <-ctx.Done():
		return false
	}
}

func RunOutput(ctx context.Context, opts Options, scenes <-chan domain.Scene, contents <-chan *domain.ContentBatch, returns ports.ContentReturn, cursor <-chan domain.CursorChange, incoming <-chan domain.CaptureRequest) error {
	r, err := opts.NewRenderer(opts.Width, opts.Height)
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer func() { r.Close() }()
	hdrSettings := opts.HDR.Normalized()
	confirmed := setupHDR(r, opts, hdrSettings)
	// formats: whether the confirmed color state was reported (it is once
	// HDR was requested, so a later SDR state is reported too).
	formats := false
	if hdrSettings.Enabled {
		if !reportFormats(ctx, opts, confirmed) {
			return nil
		}
		formats = true
	}
	if !reportCaps(ctx, opts, hdrSettings, confirmed) {
		return nil
	}
	table := surfaces.New()
	drawn := table.Map()
	var scene domain.Scene
	haveScene, dirty := false, false
	frame := 0
	var index *os.File
	var indexWriter *bufio.Writer
	var lastShot time.Time
	var lastShotName string
	defer func() {
		if index != nil {
			// A following file makes concat honor the final held interval.
			if !lastShot.IsZero() {
				_, _ = fmt.Fprintf(indexWriter, "duration %.6f\nfile '%s'\n", time.Since(lastShot).Seconds(), lastShotName)
				_ = indexWriter.Flush()
			}
			_ = index.Close()
		}
	}()
	var requests capture.Requests
	ctx, cancelCaptures := context.WithCancel(ctx)
	requests.Init(ctx, opts.Captured)
	pipeline := capture.NewPipeline(ctx, opts.Captured)
	var captureCursor domain.CaptureCursor
	pipeline.Security = opts.Security
	if opts.NewCaptureRenderer != nil {
		pipeline.EnableOffscreen(opts.NewCaptureRenderer)
	}
	defer func() {
		// An error path out of a gated frame: what it held is failed.
		pipeline.EndGate(capture.ErrFrameNotPresented)
		cancelCaptures()
		pipeline.Close(r)
		requests.Stop(incoming)
	}()
	var want domain.CursorChange
	cursorScale := -1.0 // not loaded yet
	var cursorTransform domain.BufferTransform
	seen := map[domain.WindowID]uint64{}
	// reports queues what the output read; every report is pushed to the
	// sink at once (Push never blocks), so it holds one at most.
	var reports presented.Queue
	var shows flipShows
	var security domain.SecurityState
	securityKnown := false
	failRequests := func(reason error) {
		// Whatever this frame had handed to a worker is failed with it.
		pipeline.EndGate(capture.GateVerdict(reason))
		requests.FailPending(reason)
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
			black := domain.Scene{Security: security, Output: opts.Name, OutputWidth: opts.Width, OutputHeight: opts.Height, Scale: 1, Background: "#000000"}
			black.Off = inactive
			scene = black
			kind := domain.ProtectionInactiveOutput
			if !inactive {
				kind = domain.ProtectionProtectedFrame
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
				proof := ports.SecurityOutputProof{Proof: domain.OutputProtection{Generation: security.Generation, Instance: opts.Instance, Kind: kind}}
				select {
				case opts.SecurityEvents <- proof:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			// Capture reply backpressure must not postpone the first black
			// completion/proof. Capture drainage is a separate parent barrier.
			failRequests(capture.ErrSessionProtected)
		}
	}
	acceptScene := func(s domain.Scene) {
		if opts.Security != nil && (s.Security != security || s.Security != opts.Security.Snapshot()) {
			return
		}
		// The core keeps the Seq of a scene that draws the same: nothing to do.
		if haveScene && s.Seq != 0 && s.Seq == scene.Seq && s.Security == scene.Security {
			return
		}
		scene, haveScene, dirty = s, true, true
		// A closed window's last content goes once no scene draws it.
		table.Prune(scene)
	}
	update := func(b *domain.ContentBatch) {
		batchDirty, _ := table.Apply(b, scene, seen, returns)
		dirty = dirty || batchDirty
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.System{}
	}
	// Anchor the injected clock to the presentation protocol's monotonic epoch.
	clockStart := clk.Now()
	var monoStart unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &monoStart)
	period := refreshPeriod(opts.RefreshMilli)
	var presentedSeq, reportedSeq uint64
	var retry ports.Timer
	var retryDue <-chan time.Time
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	reportFrame := func(animating bool) {
		if opts.Frames == nil {
			return
		}
		if (animating || presentedSeq > reportedSeq) && corewake.Send(opts.Frames, opts.Name, presentedSeq) {
			reportedSeq = presentedSeq
		}
		if presentedSeq > reportedSeq {
			delay := period
			if delay <= 0 {
				delay = time.Second / 60
			}
			if retry == nil {
				retry = clk.NewTimer(delay)
			} else {
				retry.Reset(delay)
			}
			retryDue = retry.C()
		} else {
			if retry != nil {
				retry.Stop()
			}
			retryDue = nil
		}
	}
	var pace ports.Timer
	var paceDue <-chan time.Time
	defer func() {
		if pace != nil {
			pace.Stop()
		}
	}()
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
		// Requests wait for a scene that shows their indicator, at most
		// capture.HoldFor (see capture.Hold).
		var holdDue <-chan time.Time
		if requests.Len() > 0 {
			var wait time.Duration
			requests.List, wait = pipeline.Expire(scene, requests.List, time.Now())
			if wait > 0 {
				holdTimer.Reset(wait)
				holdDue = holdTimer.C
			}
		}
		if holdDue == nil {
			holdTimer.Stop()
		}
		// applyHDR replaces the HDR settings in place. Enable and SDR white
		// rebuild the renderer (as DRM restarts its output) on the same
		// output: no screen is removed. The peak only changes the caps.
		applyHDR := func(want domain.OutputHDRSettings) error {
			want = want.Normalized()
			if want == hdrSettings {
				return nil
			}
			if want.Enabled == hdrSettings.Enabled && want.SDRBrightness == hdrSettings.SDRBrightness {
				hdrSettings = want
				if !reportCaps(ctx, opts, hdrSettings, confirmed) {
					return ctx.Err()
				}
				return nil
			}
			next, err := opts.NewRenderer(opts.Width, opts.Height)
			if err != nil {
				return fmt.Errorf("recreate renderer: %w", err)
			}
			nextConfirmed := setupHDR(next, opts, want)
			pipeline.EndGate(capture.ErrFrameNotPresented)
			pipeline.Close(r)
			r.Close()
			r = next
			pipeline = capture.NewPipeline(ctx, opts.Captured)
			pipeline.Security = opts.Security
			if opts.NewCaptureRenderer != nil {
				pipeline.EnableOffscreen(opts.NewCaptureRenderer)
			}
			requests.FailPending(capture.ErrFrameNotPresented)
			hdrSettings, confirmed = want, nextConfirmed
			if want.Enabled || formats {
				if !reportFormats(ctx, opts, confirmed) {
					return ctx.Err()
				}
				formats = true
			}
			if !reportCaps(ctx, opts, hdrSettings, confirmed) {
				return ctx.Err()
			}
			// Redraw the current scene on the new renderer.
			dirty, securityKnown = haveScene, false
			cursorScale = -1
			reports.Reset()
			return nil
		}
		applyMode := func(mode domain.OutputMode) error {
			if mode.Width <= 0 || mode.Height <= 0 || mode.RefreshMilli < 0 {
				return fmt.Errorf("invalid output mode: %dx%d@%d", mode.Width, mode.Height, mode.RefreshMilli)
			}
			if mode.Width == opts.Width && mode.Height == opts.Height && mode.RefreshMilli == opts.RefreshMilli {
				return nil
			}
			if mode.Width != opts.Width || mode.Height != opts.Height {
				next, err := opts.NewRenderer(mode.Width, mode.Height)
				if err != nil {
					return fmt.Errorf("recreate renderer: %w", err)
				}
				if confirmed != nil {
					if err := enableHDR(next, hdrSettings); err != nil {
						next.Close()
						return fmt.Errorf("recreate HDR targets: %w", err)
					}
				}
				// Join workers and return every lease to its original renderer
				// before releasing any of that renderer's storage.
				pipeline.EndGate(capture.ErrFrameNotPresented)
				pipeline.Close(r)
				r.Close()
				r = next
				pipeline = capture.NewPipeline(ctx, opts.Captured)
				pipeline.Security = opts.Security
				if opts.NewCaptureRenderer != nil {
					pipeline.EnableOffscreen(opts.NewCaptureRenderer)
				}
			}
			requests.FailPending(capture.ErrFrameNotPresented)
			opts.SetMode(mode.Width, mode.Height, mode.RefreshMilli)
			period = refreshPeriod(opts.RefreshMilli)
			if pace != nil {
				pace.Stop()
			}
			paceDue = nil
			// Wait for a scene laid out for the new mode, even if its Seq
			// matches the old one. A protected output needs a new black proof.
			haveScene = haveScene && scene.OutputWidth == mode.Width && scene.OutputHeight == mode.Height
			dirty, securityKnown = haveScene, false
			cursorScale = -1
			reports.Reset()
			return nil
		}
		ticked := false
		var tickTime time.Time
		select {
		case req, ok := <-opts.ModeRequests:
			if !ok {
				opts.ModeRequests = nil
				continue
			}
			var err error
			if req.HDR != nil {
				err = applyHDR(*req.HDR)
			} else {
				err = applyMode(req.Mode)
			}
			req.Result <- err
			if err != nil {
				return err
			}
		case mode, ok := <-opts.Modes:
			if !ok {
				opts.Modes = nil
				continue
			}
			if err := applyMode(mode); err != nil {
				return err
			}
		case <-retryDue:
			reportFrame(false)
			continue
		case <-paceDue:
			paceDue = nil
			ticked = true
			tickTime = clk.Now()
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
				capture.Fail(ctx, q, capture.ErrOutputStopped, opts.Captured)
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			var refuse error
			if opts.protected() {
				refuse = capture.ErrSessionProtected
			} else if scene.Off {
				refuse = capture.ErrOutputOff
			}
			dirty = requests.Admit(q, refuse, scene, haveScene) || dirty
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
			opts.Log.Info().Str("output", opts.Name).Int("frames", frame).Msg("stats")
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
		case b, ok := <-contents:
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
			update(b)
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
			// Cursor images are sampled by captures and debug screenshots.
			// Motion alone does not redraw the display.
			want, cursorScale, cursorTransform = c, -1, 0
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
			case b, ok := <-contents:
				if !ok {
					contents = nil
				} else {
					if err := checkSecurity(); err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}
					update(b)
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
			failRequests(capture.ErrSessionProtected)
		}
		if scene.Off && requests.Len() > 0 {
			requests.FailPending(capture.ErrOutputOff)
		}
		if !haveScene || !dirty || scene.Off {
			// Contents not drawn are still read: report them. An output
			// turned off draws nothing.
			opts.report(&reports, nil, seen)
			continue
		}
		if period > 0 && !ticked {
			if paceDue == nil {
				if pace == nil {
					pace = clk.NewTimer(period)
				} else {
					pace.Reset(period)
				}
				paceDue = pace.C()
			}
			continue
		}
		dirty = false
		if !opts.protected() && opts.Cursor != nil && opts.LoadCursor != nil && (scene.Scale != cursorScale || scene.Transform != cursorTransform) {
			cursorScale, cursorTransform = scene.Scale, scene.Transform
			if img, err := opts.LoadCursor(want, scene.Scale, scene.Transform, 256); err == nil {
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
		pipeline.Cursor = nil
		ready, _ := pipeline.Hold(scene, requests.List, time.Now())
		if len(scene.CaptureIndicators) > 0 {
			// The captures of this frame are delivered only once its frame,
			// with the indicator, is presented (capture.Pipeline.EndGate).
			pipeline.BeginGate()
		}
		normal, excluded, hidden := pipeline.Split(scene, ready)
		if !opts.protected() {
			pipeline.Cursor = opts.Cursor.captureCursor(&captureCursor, normal, excluded)
		}
		pipeline.Retire(scene)
		if len(hidden) > 0 {
			// A hidden workspace: a child renderer draws it. The display's
			// fences do not cover the child, so wait for it before reporting.
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests(capture.ErrEpochChanged)
				continue
			}
			pipeline.SubmitHidden(scene, drawn, hidden)
			clear(hidden)
			// A slow child may outlive Wayland's stale-report timeout even
			// though this owner waits. Publish its non-expiring holds first.
			if opts.Presented != nil {
				_, reads, _ := pipeline.CapHiddenSeen(seen)
				if len(reads) > 0 {
					reports.Push(ports.OutputPresented{Output: opts.Name, Seen: seen, ChildReads: reads})
					reports.Flush(opts.Presented)
				}
			}
			if err := pipeline.WaitHidden(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("workspace frame fence: %w", err)
			}
		}
		// Captures never show the capture indicator or the position hint, and
		// show hint labels only to clients with hint.capture: their frame goes
		// first.
		took, pdone, perr := pipeline.SubmitPlain(r, scene, drawn, normal)
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
			// It has no indicator or position hint; it keeps hint labels with
			// hint.capture (Split refuses internal requests there).
			if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				failRequests(capture.ErrEpochChanged)
				continue
			}
			xdone, renderErr := r.Render(pipeline.ExcludedScene(scene), drawn)
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
				failRequests(capture.ErrEpochChanged)
				continue
			}
			pipeline.SubmitScoped(scene.Security, r, excluded)
			clear(excluded)
		}
		if opts.Security != nil && opts.Security.Snapshot() != scene.Security {
			failRequests(capture.ErrEpochChanged)
			continue
		}
		done, err := r.Render(scene, drawn)
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
			failRequests(capture.ErrEpochChanged)
			continue
		}
		pipeline.SubmitScoped(scene.Security, r, normal)
		// The indicated frame is presented: release what it gated.
		pipeline.EndGate(nil)
		requests.List = capture.Waiting(requests.List, scene)
		frame++
		flip := shows.flipInfo(scene, drawn, table.Kept)
		if period > 0 {
			flip.When = time.Duration(monoStart.Nano()) + tickTime.Sub(clockStart)
			flip.Refresh = period
		}
		opts.report(&reports, flip, seen)
		presentedSeq = max(presentedSeq, scene.Seq)
		reportFrame(scene.Animating)
		if opts.ScreenshotDir != "" && !opts.protected() {
			shot := r.Pixels()
			if opts.protected() || opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				continue
			}
			if shot == nil {
				opts.Log.Debug().Str("output", opts.Name).Int("frame", frame).Msg("screenshot readback unavailable; frame skipped")
				continue
			}
			if opts.Cursor != nil {
				opts.Cursor.drawSecure(shot, opts.Security)
			}
			if opts.protected() || opts.Security != nil && opts.Security.Snapshot() != scene.Security {
				continue
			}
			name := fmt.Sprintf("frame-%06d.png", frame)
			written, err := writePNGSecureResult(filepath.Join(opts.ScreenshotDir, name), shot, opts.Security, scene.Security)
			if err != nil {
				return fmt.Errorf("screenshot: %w", err)
			}
			if !written {
				continue
			}
			// latest.png is the frame just written, linked rather than encoded again.
			written, err = linkPNGSecure(filepath.Join(opts.ScreenshotDir, name), filepath.Join(opts.ScreenshotDir, "latest.png"), shot, opts.Security, scene.Security)
			if err != nil {
				return fmt.Errorf("latest screenshot: %w", err)
			}
			if !written {
				continue
			}
			now := time.Now()
			if index == nil {
				index, err = os.OpenFile(filepath.Join(opts.ScreenshotDir, "frames.ffconcat"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					return fmt.Errorf("frame index: %w", err)
				}
				indexWriter = bufio.NewWriter(index)
				_, _ = fmt.Fprintln(indexWriter, "ffconcat version 1.0")
			}
			// Complete the previous frame's duration before starting this one.
			// An idle scene leaves that frame displayed and adds no entries.
			if !lastShot.IsZero() {
				_, _ = fmt.Fprintf(indexWriter, "duration %.6f\n", now.Sub(lastShot).Seconds())
			}
			_, _ = fmt.Fprintf(indexWriter, "file '%s'\n", name)
			if err := indexWriter.Flush(); err != nil {
				return fmt.Errorf("frame index: %w", err)
			}
			lastShot, lastShotName = now, name
			if opts.RawHDR && opts.HDR.Enabled {
				// No cursor on the raw image. Only a confirmed HDR output
				// has a readable PQ target; else HDRPixels is nil.
				if opts.protected() || opts.Security != nil && opts.Security.Snapshot() != scene.Security {
					continue
				}
				raw := r.HDRPixels()
				if raw == nil {
					opts.Log.Debug().Str("output", opts.Name).Int("frame", frame).Msg("raw HDR readback unavailable; frame skipped")
					continue
				}
				if err := writePNGSecure(filepath.Join(opts.ScreenshotDir, "latest-pq.png"), raw, opts.Security, scene.Security); err != nil {
					return fmt.Errorf("raw HDR screenshot: %w", err)
				}
			}
		}
		if ev := opts.Log.Debug(); ev.Enabled() {
			ev.Int("frame", frame).Uint64("seq", scene.Seq).Int("windows", len(scene.Windows)).Dur("ms", time.Since(start)).Int("redrawn", r.TakeRedrawn()).Msg("frame")
		}
	}
}

func (opts Options) protected() bool {
	return opts.Security != nil && opts.Security.Snapshot().Protected
}

func writePNGSecure(path string, img image.Image, security ports.SessionSecurity, state domain.SecurityState) error {
	_, err := writePNGSecureResult(path, img, security, state)
	return err
}

// screenshotPNG encodes the PNGs written by the headless output (frames, the
// latest.png fallback, raw HDR latest-pq.png): encoding is by far the largest
// cost of a headless frame, so it trades file size for speed. Its buffers are
// reused between files.
var screenshotPNG = png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: new(pngBuffers)}

// pngBuffers is a png.EncoderBufferPool.
type pngBuffers struct{ pool sync.Pool }

func (p *pngBuffers) Get() *png.EncoderBuffer {
	b, _ := p.pool.Get().(*png.EncoderBuffer)
	return b
}

func (p *pngBuffers) Put(b *png.EncoderBuffer) { p.pool.Put(b) }

// gated reports whether a screenshot must not be published: the session is
// protected, or its security state is no longer the one the frame was drawn in.
func gated(security ports.SessionSecurity, state domain.SecurityState) bool {
	return security != nil && (state.Protected || security.Snapshot() != state)
}

// writePNGSecureResult distinguishes a security skip from a published PNG.
func writePNGSecureResult(path string, img image.Image, security ports.SessionSecurity, state domain.SecurityState) (bool, error) {
	if gated(security, state) {
		return false, nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".frame-*.png")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if err := screenshotPNG.Encode(f, img); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if gated(security, state) {
		return false, nil
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

// linkPNGSecure publishes the PNG already written at src as dst without
// encoding it again: a hard link under a temporary name, renamed over dst, so
// readers see the old or the new frame, never a partial one. Frame files are
// never modified after they are published, which makes the shared file safe.
// It gates on security like writePNGSecureResult, and falls back to encoding
// img where the file system cannot link.
func linkPNGSecure(src, dst string, img image.Image, security ports.SessionSecurity, state domain.SecurityState) (bool, error) {
	if gated(security, state) {
		return false, nil
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Link(src, tmp); err != nil {
		return writePNGSecureResult(dst, img, security, state)
	}
	defer os.Remove(tmp)
	if gated(security, state) {
		return false, nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		return false, err
	}
	return true, nil
}

// report queues what the output read, with flip for a drawn frame, and
// pushes it to the sink.
func (opts Options) report(q *presented.Queue, flip *domain.FlipInfo, seen map[domain.WindowID]uint64) {
	if opts.Presented == nil {
		return
	}
	q.Push(ports.OutputPresented{Output: opts.Name, Flip: flip, Seen: seen})
	q.Flush(opts.Presented)
}

// flipShows builds the Shows of successive flips. Its owner goroutine is the
// only user. The Shows snapshot is shared with earlier flips of the same
// content (see surfaces.ShowsSnapshot): never modify it.
type flipShows struct {
	snapshot surfaces.ShowsSnapshot
}

// of returns the content Seq of the windows scene showed to their client.
func (f *flipShows) of(scene domain.Scene, contents map[domain.WindowID]domain.SurfaceContent, kept func(domain.WindowID) bool) map[domain.WindowID]uint64 {
	// A closed window drawn from its kept content is not shown to its
	// client.
	return f.snapshot.FromContents(scene, contents, kept)
}

// flipInfo describes a drawn frame as a flip at the current CLOCK_MONOTONIC
// time (software clock, refresh unknown), so presentation feedback and
// frame callbacks follow headless frames. The FlipInfo itself is new per
// flip (the sink counts merges on it); only its Shows is shared.
func (f *flipShows) flipInfo(scene domain.Scene, contents map[domain.WindowID]domain.SurfaceContent, kept func(domain.WindowID) bool) *domain.FlipInfo {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return &domain.FlipInfo{When: time.Duration(ts.Nano()), Shows: f.of(scene, contents, kept)}
}
