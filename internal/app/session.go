package app

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/headlessinput"
	"github.com/bnema/neferwl/internal/adapters/hints"
	"github.com/bnema/neferwl/internal/adapters/launcher"
	"github.com/bnema/neferwl/internal/adapters/libinput"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/powerkey"
	"github.com/bnema/neferwl/internal/adapters/sched"
	"github.com/bnema/neferwl/internal/adapters/screencast"
	"github.com/bnema/neferwl/internal/adapters/screensaver"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/adapters/watchdog"
	"github.com/bnema/neferwl/internal/adapters/wayland"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/adapters/xwayland"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// session holds the dependencies assembleSession acquired and wired; run
// starts them. The session owns hw, curs and xdisplay (released by close)
// until run ends; the keymap and the wayland server own themselves.
type session struct {
	opts Options
	log  zerowrap.Logger

	clock    ports.Clock
	security *sessionsecurity.Gate
	p        *pipes
	sizes    [][2]int
	path     string

	hw           *drmBackend
	km           *xkb.Keymap
	core         *core.Core
	inst         *vulkan.Instance
	gpu          ports.RenderDevice
	dmabuf       ports.DMABufSupport
	hintOpts     hints.Options
	wsIDs        *workspaceid.IDs
	captureAllow *captureallow.Store
	cast         *screencast.Service // nil without the D-Bus services
	server       *wayland.Server
	childEnv     []string
	statePath    string
	xdisplay     *xwayland.Display
	child        *launcher.Launcher
	curs         *cursors

	newRenderer func(w, h int) (ports.Renderer, error)
}

// The watchdog only writes a stack dump for a loop that stopped answering; it
// recovers nothing. Each probe round wakes the process, so rounds are sparse.
// Stalls are only checked at rounds: a probe still unanswered at the next round
// (watchdogEvery later, over watchdogTimeout) is dumped. A stall that begins
// just after a round is thus dumped about 2*watchdogEvery (10 s) after it began.
const (
	watchdogEvery   = 5 * time.Second
	watchdogTimeout = 3 * time.Second
)

// assembleSession acquires and wires every dependency in startup order and
// starts no long-lived worker.
//
// Invariant: wayland.New is the last fallible step. *wayland.Server has no
// Close: its sockets are released only by Server.Run. Every later step
// (statefile.Path, openXwayland, launcher.New, newCursors) degrades with a
// warning or nil instead of failing. A failure releases the DRM backend, keymap and Vulkan resources (abort).
func assembleSession(ctx context.Context, opts Options) (s *session, err error) {
	s = &session{opts: opts, log: logging.For(ctx, "app"), clock: clock.System{}, security: &sessionsecurity.Gate{}}
	defer func() {
		if err != nil {
			s.abort()
			s = nil
		}
	}()
	log := s.log
	s.sizes = opts.Sizes
	if len(s.sizes) == 0 {
		s.sizes = [][2]int{{1920, 1080}}
	}
	// Every output renders on the same GPU: its formats are the clients'.
	s.inst, err = vulkan.NewInstance(logging.For(ctx, "render"))
	if err != nil {
		return s, err
	}
	if opts.Backend == "drm" {
		if s.hw, err = openDRM(ctx, opts.Config, s.inst); err != nil {
			return s, err
		}
		s.gpu = s.hw.render
	} else {
		cands, listErr := drm.ListGPUs("/sys", "/dev")
		s.gpu, err = openHeadlessRender(s.inst, cands, listErr, opts.Config, os.Getenv("NEFERWL_RENDER_DEVICE"), logging.For(ctx, "drm"))
		if err != nil {
			return s, err
		}
	}
	s.dmabuf = s.gpu.DMABuf()
	log.Info().Int("formats", len(s.dmabuf.Formats)).Msg("dmabuf")
	s.p = newPipes(s.hw != nil, opts.Config.Hint.Enable)
	// core() carries channels only; the gate, clock and terminal flag are
	// explicit core.Options.
	if s.core, err = core.New(opts.Config, s.p.core(), core.Options{Security: s.security, Clock: s.clock, Terminal: !opts.NoTerminal, HintDisabled: hintDisabledNote(ctx)}); err != nil {
		return s, err
	}
	if s.km, err = xkb.New(xkb.RMLVO{Layout: opts.Config.Keyboard.Layout, Variant: opts.Config.Keyboard.Variant, Options: opts.Config.Keyboard.Options}); err != nil {
		return s, err
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	// Hint mode off: no detector, no debug env, nothing.
	if opts.Config.Hint.Enable {
		s.hintOpts = hintOptions(ctx, opts.Config)
	}
	// One launch prefix for ext-workspace ids and the state file.
	s.wsIDs = workspaceid.New()
	// The path is fixed: a config key would let a program of the same user
	// widen who may capture.
	// Production: the fixed path, a root-owned file. Only tests set a path,
	// and an owner for it.
	captureAllowLog := logging.For(ctx, "captureallow")
	if opts.captureAllowPath != "" {
		s.captureAllow = captureallow.NewStoreOwnedBy(opts.captureAllowPath, opts.captureAllowOwner, captureAllowLog)
	} else {
		s.captureAllow = captureallow.NewStore(captureallow.DefaultPath, captureAllowLog)
	}
	// Only a real session (or a test that asks) serves the session bus:
	// headless runs would take the names from the desktop they run in.
	var casts ports.CastSink
	if s.hw != nil || opts.DBus {
		s.cast = screencast.New(s.captureAllow.Policy, s.p.castCommands, logging.For(ctx, "screencast"))
		casts = s.cast
		if opts.castInventoryApplied != nil {
			s.cast.NotifyInventoryApplied(opts.castInventoryApplied)
		}
	}
	if s.server, err = wayland.New(wayland.Options{
		CaptureAllow: s.captureAllow,
		Casts:        casts,
		WorkspaceIDs: s.wsIDs,
		Security:     s.security,
		RuntimeDir:   runtimeDir,
		DMABuf:       s.dmabuf,
		SyncobjNode:  renderNodeForDevice(s.gpu),
		Keymap:       s.km.String(),
		RepeatRate:   opts.Config.Keyboard.RepeatRate,
		RepeatDelay:  opts.Config.Keyboard.RepeatDelay,
		LogDamage:    logging.Enabled(ctx, "wayland-damage"),
	}, s.p.wayland(), logging.For(ctx, "wayland")); err != nil {
		return s, err
	}
	log.Info().Str("WAYLAND_DISPLAY", s.server.SocketName()).Msg("listening")
	s.childEnv = launcher.ChildEnv(os.Environ(), s.server.SocketName(), runtimeDir, cursorSize())
	statePath, serr := statefile.Path(runtimeDir, s.server.SocketName())
	if serr != nil {
		log.Warn().Err(serr).Msg("state file disabled")
	} else {
		s.statePath = statePath
		s.childEnv = append(s.childEnv, statefile.Env+"="+statePath)
	}
	if s.xdisplay = openXwayland(ctx, opts, s.childEnv, logging.For(ctx, "xwayland")); s.xdisplay != nil {
		s.childEnv = append(s.childEnv, "DISPLAY="+s.xdisplay.Name())
	}
	s.child = launcher.New(s.childEnv, s.security, logging.For(ctx, "launcher"))
	s.curs = newCursors(s.clock, opts.Config.Cursor.HideAfter)
	s.path = opts.ConfigPath
	if s.path == "" {
		s.path = config.DefaultPath()
	}
	if s.hw == nil {
		traceDamage := logging.Enabled(ctx, "render-damage")
		s.newRenderer = func(w, h int) (ports.Renderer, error) {
			return s.gpu.NewRenderer(w, h, ports.RendererOptions{Virtual: true, Readback: opts.ScreenshotRaw, TraceDamage: traceDamage})
		}
	}
	return s, nil
}

// abort releases what assembly acquired before ownership passed on: the DRM
// backend and the keymap. It runs only when assembly fails, before
// anything else is owned.
func (s *session) abort() {
	if s.hw != nil {
		s.hw.close()
	}
	if s.hw == nil && s.gpu != nil {
		s.gpu.Close()
	}
	if s.inst != nil {
		s.inst.Close()
	}
	if s.km != nil {
		s.km.Close()
	}
}

// close releases what the session owns after its workers have joined. The
// keymap and the wayland server are not here: the input goroutine and
// Server.Run own them.
func (s *session) close() {
	s.curs.stop()
	if s.xdisplay != nil {
		s.xdisplay.Close()
	}
	if s.hw != nil {
		s.hw.close()
	}
	if s.hw == nil && s.gpu != nil {
		s.gpu.Close()
	}
	if s.inst != nil {
		s.inst.Close()
	}
}

// run starts the workers and waits for the session to end; it releases the
// session's resources after every worker has joined.
func (s *session) run(ctx context.Context, inject func(chan<- ports.InputEvent)) error {
	defer s.close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts, log, p, hw, km, security := s.opts, s.log, s.p, s.hw, s.km, s.security
	childEnv, curs, xdisplay, statePath := s.childEnv, s.curs, s.xdisplay, s.statePath
	server, captureAllow, c, child, wsIDs, sizes, path := s.server, s.captureAllow, s.core, s.child, s.wsIDs, s.sizes, s.path
	if opts.Session {
		launcher.ExportSession(ctx, childEnv, logging.For(ctx, "launcher"))
		launcher.NotifyReady(os.Getenv("NOTIFY_SOCKET"), logging.For(ctx, "launcher"))
		defer launcher.UnexportSession(childEnv, logging.For(ctx, "launcher"))
	}
	if inject != nil {
		inject(p.input)
	}
	var workers sync.WaitGroup
	workers.Add(8)
	done := make(chan error, 8)
	go func() {
		defer workers.Done()
		done <- config.Watch(ctx, path, p.watched, logging.For(ctx, "config"))
	}()
	if p.scales != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			core.PersistScales(ctx, p.scales, config.ScaleStore{Path: path}, launcher.NewNotifier(ctx, childEnv, logging.For(ctx, "launcher")), s.clock, time.Second)
		}()
	}
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
	if dir := logging.Dir(ctx); dir != "" {
		workers.Add(1)
		go func() {
			defer workers.Done()
			watchdog.Run(ctx, func() []watchdog.Probe {
				probes := []watchdog.Probe{
					{Name: "wayland", Ping: func() { server.Ping(ctx) }},
					{Name: "core", Ping: func() { c.Ping(ctx) }},
				}
				if hw != nil {
					probes = append(probes, hw.watchdogProbes()...)
				}
				return probes
			}, watchdog.Options{Dir: dir, Every: watchdogEvery, Timeout: watchdogTimeout, Log: logging.For(ctx, "app")})
		}()
	}
	go func() {
		defer workers.Done()
		// Without a watch the allowlist stays as loaded: restart to apply.
		if err := captureAllow.Run(ctx); err != nil {
			log.Warn().Err(err).Msg("capture allowlist not watched, restart to apply changes")
		}
	}()
	if p.hintScans != nil {
		workers.Add(2)
		go func() { defer workers.Done(); routeCaptured(ctx, p.capturedAll, p.captured, p.internalCaptured()) }()
		go func() {
			defer workers.Done()
			// Run only returns when ctx is done: nothing to report.
			_ = hints.Run(ctx, p.hintScans, p.captures, p.hintCaptured, p.hintResults, s.hintOpts)
		}()
	}
	go func() { defer workers.Done(); done <- child.Run(ctx, p.spawn) }()
	go func() { defer workers.Done(); done <- c.Run(ctx) }()
	workers.Add(1)
	go func() { defer workers.Done(); logCommandStats(ctx, c, logging.For(ctx, "core")) }()
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

	go func() {
		defer workers.Done()
		outputIO := p.outputs(security, server.Presentation(), server)
		apply := newOutputApply(core.NewOutputOverrides(opts.Config, hw == nil), logging.For(ctx, "app"))
		if hw != nil {
			done <- safe("output", func() error {
				want := func(cfg ports.Config) drm.Want {
					w := wantFromConfig(cfg)
					w.Sampled = hw.render.DMABuf().Formats
					w.TraceFlips = logging.Enabled(ctx, "drm-flip")
					return w
				}
				return hw.runOutputs(ctx, want, opts.Config, apply, outputIO, curs, logging.For(ctx, "drm"))
			})
			return
		}
		done <- runHeadless(ctx, headlessOptions{sizes: sizes, shots: opts.ScreenshotDir, hdr: opts.HeadlessHDR, raw: opts.ScreenshotRaw}, apply, outputIO, curs, s.newRenderer, logging.For(ctx, "render"))
	}()

	if hw != nil {
		workers.Add(1)
		go func() { defer workers.Done(); done <- safe("seat", func() error { return hw.seat.Run(ctx) }) }()
	}
	// Only a real session serves the session bus: headless runs would take
	// the names from the desktop they run in, unless a test asks (DBus).
	if hw != nil || opts.DBus {
		workers.Add(2)
		go func() {
			defer workers.Done()
			reports := screensaver.Reports{Held: p.idleInhibited, Activity: p.idleActivity}
			screensaver.Serve(ctx, "", reports, busRetry, logging.For(ctx, "screensaver"))
		}()
		go func() {
			defer workers.Done()
			// nefercap finds this compositor through the session environment.
			s.cast.SetEnv(childEnv)
			s.cast.Run(ctx, busRetry)
		}()
	}
	if hw != nil {
		// The same goes for the system bus: logind's locks keep the power
		// keys from acting before a bind sees them.
		workers.Add(1)
		go func() {
			defer workers.Done()
			powerkey.Serve(ctx, "", busRetry, logging.For(ctx, "powerkey"))
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

func renderNodeForDevice(dev ports.RenderDevice) string {
	_, render := dev.Node()
	return renderNode(render)
}
