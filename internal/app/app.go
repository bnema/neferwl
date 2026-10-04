// Package app coordinates the application lifecycle.
package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/headlessinput"
	"github.com/bnema/neferwl/internal/adapters/launcher"
	"github.com/bnema/neferwl/internal/adapters/libinput"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sched"
	"github.com/bnema/neferwl/internal/adapters/screensaver"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/adapters/wayland"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/adapters/xwayland"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

type Options struct {
	Backend    string
	Config     ports.Config
	ConfigPath string
	Timeout    time.Duration
	NoTerminal bool
	// NoXwayland skips the X11 display even when the config enables it.
	NoXwayland bool
	// HeadlessHDR enables a virtual HDR output for protocol testing only.
	HeadlessHDR bool
	// Session exports the displays to D-Bus and systemd user services
	// before any client starts, and withdraws them on exit.
	Session       bool
	ScreenshotDir string
	// ScreenshotRaw also writes latest-pq.png, the raw PQ codes of a
	// virtual HDR output (test-only; needs ScreenshotDir and HeadlessHDR).
	ScreenshotRaw bool
	// Sizes are the headless outputs (width, height), left to right; empty
	// means one 1920x1080 output. With several outputs, screenshots go to
	// one subdirectory per output (HEADLESS-1, HEADLESS-2, ...).
	Sizes [][2]int
	// Run closes Script on shutdown; the reader goroutine exits after Close.
	Script     io.ReadCloser
	testScenes chan<- []ports.Scene
	// captureAllowPath replaces the fixed allowlist path (tests).
	captureAllowPath string
	// captureAllowOwner is the uid that must own that file (tests use their
	// own; root, 0, otherwise).
	captureAllowOwner uint32
}

func Run(ctx context.Context, opts Options) error { return run(ctx, opts, nil) }

func run(ctx context.Context, opts Options, inject func(chan<- ports.InputEvent)) error {
	log := logging.For(ctx, "app")
	log.Info().Str("backend", opts.Backend).Msg("starting neferwl")
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		ctx, cancel = context.WithTimeoutCause(ctx, opts.Timeout, errTimeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	sizes := opts.Sizes
	if len(sizes) == 0 {
		sizes = [][2]int{{1920, 1080}}
	}
	var hw *drmBackend
	if opts.Backend == "drm" {
		var err error
		if hw, err = openDRM(ctx, opts.Config); err != nil {
			return err
		}
		defer hw.close()
	}
	security := &sessionsecurity.Gate{}
	p := newPipes(hw != nil)
	ch := p.core()
	ch.Security = security
	ch.Terminal = !opts.NoTerminal
	ch.Clock = clock.System{}
	c, err := core.New(opts.Config, ch)
	if err != nil {
		return err
	}
	km, err := xkb.New(xkb.RMLVO{Layout: opts.Config.Keyboard.Layout, Variant: opts.Config.Keyboard.Variant, Options: opts.Config.Keyboard.Options})
	if err != nil {
		return err
	}
	keymap := km.String()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	// Every output renders on the same GPU: its formats are the clients'.
	dmabuf := vulkan.Probe()
	log.Info().Int("formats", len(dmabuf.Formats)).Msg("dmabuf")
	// One launch prefix for ext-workspace ids and the state file.
	wsIDs := workspaceid.New()
	// The path is fixed: a config key would let a program of the same user
	// widen who may capture.
	// Production: the fixed path, a root-owned file. Only tests set a path,
	// and an owner for it.
	captureAllowLog := logging.For(ctx, "captureallow")
	var captureAllow *captureallow.Store
	if opts.captureAllowPath != "" {
		captureAllow = captureallow.NewStoreOwnedBy(opts.captureAllowPath, opts.captureAllowOwner, captureAllowLog)
	} else {
		captureAllow = captureallow.NewStore(captureallow.DefaultPath, captureAllowLog)
	}
	server, err := wayland.New(wayland.Options{
		CaptureAllow: captureAllow,
		WorkspaceIDs: wsIDs,
		Security:     security,
		RuntimeDir:   runtimeDir,
		DMABuf:       dmabuf,
		SyncobjNode:  renderNode(dmabuf.Device),
		Keymap:       keymap,
		RepeatRate:   opts.Config.Keyboard.RepeatRate,
		RepeatDelay:  opts.Config.Keyboard.RepeatDelay,
	}, p.wayland(), logging.For(ctx, "wayland"))
	if err != nil {
		km.Close()
		return err
	}
	log.Info().Str("WAYLAND_DISPLAY", server.SocketName()).Msg("listening")
	childEnv := launcher.ChildEnv(os.Environ(), server.SocketName(), runtimeDir, cursorSize())
	statePath, err := statefile.Path(runtimeDir, server.SocketName())
	if err != nil {
		log.Warn().Err(err).Msg("state file disabled")
	} else {
		childEnv = append(childEnv, statefile.Env+"="+statePath)
	}
	xdisplay := openXwayland(ctx, opts, childEnv, logging.For(ctx, "xwayland"))
	if xdisplay != nil {
		defer xdisplay.Close()
		childEnv = append(childEnv, "DISPLAY="+xdisplay.Name())
	}
	if opts.Session {
		launcher.ExportSession(ctx, childEnv, logging.For(ctx, "launcher"))
		launcher.NotifyReady(os.Getenv("NOTIFY_SOCKET"), logging.For(ctx, "launcher"))
		defer launcher.UnexportSession(childEnv, logging.For(ctx, "launcher"))
	}
	child := launcher.New(childEnv, logging.For(ctx, "launcher"))
	child.Security = security
	if inject != nil {
		inject(p.input)
	}
	var workers sync.WaitGroup
	workers.Add(8)
	done := make(chan error, 8)
	path := opts.ConfigPath
	if path == "" {
		path = config.DefaultPath()
	}
	go func() {
		defer workers.Done()
		done <- config.Watch(ctx, path, p.watched, logging.For(ctx, "config"))
	}()
	if p.scales != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			core.PersistScales(ctx, p.scales, config.ScaleStore{Path: path}, launcher.NewNotifier(ctx, childEnv, logging.For(ctx, "launcher")), clock.System{}, time.Second)
		}()
	}
	curs := newCursors(clock.System{}, opts.Config.Cursor.HideAfter)
	defer curs.stop()
	workers.Add(1)
	go func() {
		defer workers.Done()
		relayConfig(ctx, opts.Config, p.watched, p.filtered, p.keymaps, p.deviceConfigs, curs, p.commands, logging.For(ctx, "config"))
	}()
	script := make(chan string)
	go func() {
		defer workers.Done()
		if hw != nil {
			// Core sends the first layout once DRM reports the outputs.
			var layout ports.Layout
			select {
			case layout = <-p.layouts:
			case <-ctx.Done():
				km.Close()
				return
			}
			done <- safe("input", func() error {
				if opts.Config.Performance.Realtime {
					runtime.LockOSThread()
					// Let this dedicated thread exit with its scheduling policy.
					schedLog := logging.For(ctx, "sched")
					if err := sched.Realtime(schedLog); err != nil {
						schedLog.Warn().Err(err).Msg("input scheduling")
					}
				}
				return libinput.Run(ctx, libinput.Options{
					Security:      security,
					Seat:          hw.seat,
					SeatName:      hw.seat.Name(),
					Keymap:        km,
					Keymaps:       p.keymaps,
					Layout:        layout,
					Layouts:       p.layouts,
					Constraints:   p.constraints,
					DeviceConfig:  opts.Config.InputDevicesConfig,
					DeviceConfigs: p.deviceConfigs,
					Active:        hw.seat.Subscribe(),
					MoveCursor:    curs.move,
					Log:           logging.For(ctx, "input"),
					LogMotion:     logging.Enabled(ctx, "input-motion"),
					LogKeys:       logging.Enabled(ctx, "input-keys"),
				}, p.input)
			})
			return
		}
		if err := headlessinput.RunSecure(ctx, km, p.keymaps, script, p.input, p.layouts, func(o string, x, y float64) { curs.move(o, x, y, true) }, logging.For(ctx, "input"), security); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case done <- err:
			case <-ctx.Done():
			}
		}
	}()
	if opts.Script != nil {
		go func() { <-ctx.Done(); _ = opts.Script.Close() }()
		go func() {
			defer close(script)
			scanner := bufio.NewScanner(opts.Script)
			for scanner.Scan() {
				select {
				case <-ctx.Done():
					return
				case script <- scanner.Text():
				}
			}
			if err := scanner.Err(); err != nil {
				log.Warn().Err(err).Msg("reading input script")
			}
		}()
	} else {
		close(script)
	}
	go func() { defer workers.Done(); done <- server.Run(ctx) }()
	go func() {
		defer workers.Done()
		// Without a watch the allowlist stays as loaded: restart to apply.
		if err := captureAllow.Run(ctx); err != nil {
			log.Warn().Err(err).Msg("capture allowlist not watched, restart to apply changes")
		}
	}()
	go func() { defer workers.Done(); done <- child.Run(ctx, p.spawn) }()
	go func() { defer workers.Done(); done <- c.Run(ctx) }()
	if xdisplay != nil {
		workers.Add(1)
		// X11 failing leaves the Wayland session running; Run logs why.
		go func() { defer workers.Done(); _ = xdisplay.Run(ctx) }()
	}
	if statePath != "" {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := statefile.Run(ctx, statePath, p.states, wsIDs, logging.For(ctx, "statefile")); err != nil {
				// Scripts lose their state; the session goes on.
				log.Warn().Err(err).Msg("state file disabled")
			}
		}()
	}
	go func() {
		defer workers.Done()
		consumeScenes(ctx, p.scenes, p.configErrors, opts.testScenes, p.renderScenes)
	}()
	workers.Add(1)
	go func() { defer workers.Done(); relayPresented(ctx, p.flips, p.presented, p.frames) }()

	go func() {
		defer workers.Done()
		outputIO := p.outputs(security)
		apply := newOutputApply(newOutputOverrides(opts.Config, hw == nil), logging.For(ctx, "app"))
		renderLog := logging.For(ctx, "render")
		newRenderer := func(w, h int) (ports.Renderer, error) {
			r, err := vulkan.New(w, h)
			if err != nil {
				return nil, err
			}
			r.SetLogger(renderLog)
			renderLog.Info().Str("queue_priority", r.QueuePriority()).Msg("vulkan queue")
			return r, nil
		}
		if hw != nil {
			done <- safe("output", func() error {
				want := func(cfg ports.Config) drm.Want {
					w := wantFromConfig(cfg)
					w.Sampled = dmabuf.Formats
					w.TraceFlips = logging.Enabled(ctx, "drm-flip")
					return w
				}
				return hw.runOutputs(ctx, want, opts.Config, apply, outputIO, curs, newRenderer, logging.For(ctx, "drm"))
			})
			return
		}
		done <- runHeadless(ctx, headlessOptions{sizes: sizes, shots: opts.ScreenshotDir, hdr: opts.HeadlessHDR, raw: opts.ScreenshotRaw}, apply, outputIO, curs, newRenderer, logging.For(ctx, "render"))
	}()

	if hw != nil {
		workers.Add(1)
		go func() { defer workers.Done(); done <- safe("seat", func() error { return hw.seat.Run(ctx) }) }()
		// Only a real session serves the session bus: headless runs would take
		// the name from the desktop they run in.
		workers.Add(1)
		go func() {
			defer workers.Done()
			reports := screensaver.Reports{Held: p.idleInhibited, Activity: p.idleActivity}
			screensaver.Serve(ctx, "", reports, screensaverRetry, logging.For(ctx, "screensaver"))
		}()
	}
	var result error
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errTimeout) {
			log.Info().Str("reason", "timeout").Dur("after", opts.Timeout).Msg("stopping")
		}
	case result = <-done:
		log.Info().AnErr("cause", result).Msg("stopping")
	}
	cancel()
	workers.Wait()
	// The wayland server has stopped: no request can be queued any more.
	drainCaptures(ctx, p.captures, nil)
	// A keymap the input goroutine never took is still ours to free.
	select {
	case km := <-p.keymaps:
		km.Close()
	default:
	}
	for len(done) > 0 {
		err := <-done
		if result == nil && err != nil {
			result = err
		}
	}
	if errors.Is(result, core.ErrQuit) || errors.Is(result, libinput.ErrEmergencyQuit) {
		return nil
	}
	return result
}

// screensaverRetry is the first delay before the screensaver service tries
// the session bus again: without one, only surface inhibitors count, and
// the session goes on either way.
const screensaverRetry = 2 * time.Second

// relayConfig forwards reloads to core. A layout change builds a new keymap, hands it
// to the input goroutine and sends it to clients; a repeat-only change just updates
// clients. A keymap that fails to build keeps the previous layout. A touchpad
// or mouse change goes to the input goroutine as one InputDevicesConfig; only
// the newest one waits there. The cursor idle delay goes straight to the
// cursor router.
func relayConfig(ctx context.Context, cur ports.Config, in <-chan ports.ConfigChanged, out chan<- ports.ConfigChanged, keymaps chan *xkb.Keymap, deviceConfigs chan ports.InputDevicesConfig, curs *cursors, commands chan<- ports.ClientCommand, log zerowrap.Logger) {
	kb := cur.Keyboard
	devCfg := cur.InputDevicesConfig
	for {
		var ev ports.ConfigChanged
		select {
		case <-ctx.Done():
			return
		case ev = <-in:
		}
		next := ev.Config.Keyboard
		layoutChanged := next.Layout != kb.Layout || next.Variant != kb.Variant || next.Options != kb.Options
		repeatChanged := next.RepeatRate != kb.RepeatRate || next.RepeatDelay != kb.RepeatDelay
		cmd := ports.SetKeymap{RepeatRate: next.RepeatRate, RepeatDelay: next.RepeatDelay}
		if layoutChanged {
			km, err := xkb.New(xkb.RMLVO{Layout: next.Layout, Variant: next.Variant, Options: next.Options})
			if err != nil {
				log.Warn().Err(err).Str("layout", next.Layout).Str("variant", next.Variant).Msg("keymap rejected; keeping previous layout")
				ev.Config.Keyboard.Layout, ev.Config.Keyboard.Variant, ev.Config.Keyboard.Options = kb.Layout, kb.Variant, kb.Options
				layoutChanged = false
			} else {
				cmd.Keymap = km.String()
				// Drop a keymap the input goroutine has not taken yet; the newest wins.
				select {
				case old := <-keymaps:
					old.Close()
				default:
				}
				select {
				case keymaps <- km:
				case <-ctx.Done():
					km.Close()
					return
				}
			}
		}
		curs.setHideAfter(ev.Config.Cursor.HideAfter)
		if ev.Config.InputDevicesConfig != devCfg {
			devCfg = ev.Config.InputDevicesConfig
			latest(deviceConfigs, devCfg) // this goroutine is the only sender
		}
		if layoutChanged || repeatChanged {
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
			if layoutChanged {
				kb.Layout, kb.Variant, kb.Options = next.Layout, next.Variant, next.Options
			}
			kb.RepeatRate, kb.RepeatDelay = next.RepeatRate, next.RepeatDelay
			log.Info().Str("layout", kb.Layout).Str("variant", kb.Variant).Str("options", kb.Options).Int("rate", kb.RepeatRate).Int("delay", kb.RepeatDelay).Bool("keymap", layoutChanged).Msg("keyboard reloaded")
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// relayPresented passes output reports to wayland in order. Each flip is
// also a frame for core; a frame core has not read yet is enough, so
// frames never wait.
func relayPresented(ctx context.Context, in <-chan ports.OutputPresented, out chan<- ports.OutputPresented, frames chan<- ports.OutputFrame) {
	for {
		var p ports.OutputPresented
		select {
		case <-ctx.Done():
			return
		case p = <-in:
		}
		if p.Flip != nil {
			select {
			case frames <- ports.OutputFrame{Output: p.Output}:
			default:
			}
		}
		select {
		case out <- p:
		case <-ctx.Done():
			return
		}
	}
}

func consumeScenes(ctx context.Context, scenes <-chan []ports.Scene, configErrors <-chan error, tap chan<- []ports.Scene, renderScenes chan []ports.Scene) {
	log := logging.For(ctx, "core")
	configLog := logging.For(ctx, "config")
	for {
		select {
		case <-ctx.Done():
			return
		case set := <-scenes:
			if tap != nil {
				select {
				case tap <- set:
				default:
				}
			}
			latest(renderScenes, set) // this goroutine is the only sender
			for _, s := range set {
				ev := log.Debug().Str("output", s.Output).Uint64("seq", s.Seq).Int("out_w", s.OutputWidth).Int("out_h", s.OutputHeight).Float64("scale", s.Scale)
				rects := make([]string, 0, len(s.Windows))
				for _, w := range s.Windows {
					rects = append(rects, fmt.Sprintf("%d:%d,%d %dx%d", w.ID, w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H))
					if w.FocusEffect > 0 {
						// The focus indicator frame by frame: window and effect.
						ev = ev.Uint64("focus_effect_window", uint64(w.ID)).Float64("focus_effect", w.FocusEffect)
					}
				}
				ev.Strs("windows", rects).Msg("scene")
			}
		case err := <-configErrors:
			configLog.Warn().Err(err).Msg("config rejected")
		}
	}
}

// openXwayland reserves an X11 display for xwayland-satellite, or returns
// nil when X11 is off or unavailable: the session runs without it.
func openXwayland(ctx context.Context, opts Options, env []string, log zerowrap.Logger) *xwayland.Display {
	bin := opts.Config.Xwayland
	if bin == "" || opts.NoXwayland {
		return nil
	}
	if !xwayland.Supported(ctx, bin) {
		log.Warn().Str("binary", bin).Msg("xwayland-satellite 0.7 or later not found: X11 apps disabled")
		return nil
	}
	d, err := xwayland.Open(xwayland.Options{Binary: bin, Env: env, Dir: "/tmp/.X11-unix", TmpDir: "/tmp", Abstract: true, Log: log})
	if err != nil {
		log.Warn().Err(err).Msg("X11 display disabled")
		return nil
	}
	log.Info().Str("DISPLAY", d.Name()).Msg("listening on X11 display")
	return d
}

// renderNode is the /dev/dri path of a render node dev_t, "" when none.
func renderNode(dev uint64) string {
	if dev == 0 {
		return ""
	}
	return fmt.Sprintf("/dev/dri/renderD%d", unix.Minor(dev))
}

// latest sends v without blocking and replaces a value the consumer has not
// read yet: the newest wins. Only one goroutine may send on ch; consumers only
// receive, so the retry after draining cannot find the buffer full again.
func latest[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
}
