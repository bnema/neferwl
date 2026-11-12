package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/control"
	"github.com/bnema/neferwl/internal/adapters/directseat"
	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/drm/supervisor"
	"github.com/bnema/neferwl/internal/adapters/hints"
	"github.com/bnema/neferwl/internal/adapters/launcher"
	"github.com/bnema/neferwl/internal/adapters/libinput"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/headlessbackend"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/powerkey"
	"github.com/bnema/neferwl/internal/adapters/sched"
	"github.com/bnema/neferwl/internal/adapters/screencast"
	"github.com/bnema/neferwl/internal/adapters/screensaver"
	"github.com/bnema/neferwl/internal/adapters/sessiondir"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/adapters/watchdog"
	"github.com/bnema/neferwl/internal/adapters/wayland"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/adapters/xwayland"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// session holds the dependencies assembleSession acquired and wired; run
// starts them. The session owns hw, curs and xdisplay (released by close)
// until run ends; the keymap and the wayland server own themselves.
type session struct {
	controlDone chan domain.CaptureDone
	registry    *sessiondir.Dir
	opts        Options
	plan        core.BackendPlan
	log         zerowrap.Logger

	clock    ports.Clock
	security *sessionsecurity.Gate
	p        *pipes
	sizes    [][2]int
	path     string

	hw           *supervisor.Backend
	km           *xkb.Keymap
	core         *core.Core
	inst         *vulkan.Instance
	gpu          ports.RenderDevice
	dmabuf       domain.DMABufSupport
	hintOpts     hints.Options
	wsIDs        *workspaceid.IDs
	captureAllow *captureallow.Store
	cast         *screencast.Service // nil without the D-Bus services
	control      *control.Server
	server       *wayland.Server
	childEnv     []string
	statePath    string
	xdisplay     *xwayland.Display
	child        *launcher.Launcher
	curs         *cursors.Router

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
// (control.Listen, statefile.Path, openXwayland, launcher.New, newCursors) degrades with a
// warning or nil instead of failing. A failure releases the DRM backend,
// keymap and Vulkan resources (abort).
func assembleSession(ctx context.Context, opts Options) (s *session, err error) {
	overrides := opts.PlanOverrides
	overrides.SizedHeads = len(opts.Sizes) > 0
	s = &session{opts: opts, plan: core.PlanBackend(opts.Backend, opts.Profile, overrides), log: logging.For(ctx, "app"), clock: clock.System{}, security: &sessionsecurity.Gate{}}
	defer func() {
		if err != nil {
			s.abort()
			s = nil
		}
	}()
	if s.plan.Control {
		id := opts.SessionID
		if id == "" {
			id = sessiondir.NewID()
		}
		s.registry, err = sessiondir.Create(os.Getenv("XDG_RUNTIME_DIR"), id)
		if err != nil {
			return s, err
		}
		if err = os.MkdirAll(filepath.Join(s.registry.Path(), "shots"), 0700); err != nil {
			return s, err
		}
	}
	log := s.log
	s.sizes = opts.Sizes
	if len(s.sizes) == 0 && s.plan.FixedHeads == core.HeadsSized {
		s.sizes = [][2]int{{1920, 1080}}
	}
	// Every output renders on the same GPU: its formats are the clients'.
	s.inst, err = vulkan.NewInstance(logging.For(ctx, "render"))
	if err != nil {
		return s, err
	}
	if s.plan.KMS {
		if s.hw, err = supervisor.Open(ctx, opts.Config, s.inst); err != nil {
			return s, err
		}
		s.gpu = s.hw.RenderDevice()
	} else {
		cands, listErr := drm.ListGPUs("/sys", "/dev")
		s.gpu, err = openHeadlessRenderPolicy(s.inst, cands, listErr, opts.Config, os.Getenv("NEFERWL_RENDER_DEVICE"), s.plan.RequireRenderNode, logging.For(ctx, "drm"))
		if err != nil {
			return s, err
		}
	}
	s.dmabuf = s.gpu.DMABuf()
	log.Info().Int("formats", len(s.dmabuf.Formats)).Msg("dmabuf")
	s.p = newPipes(s.plan.PersistScales, opts.Config.Hint.Enable)
	if s.plan.Control {
		s.p.control = make(chan ports.ControlRequest, 32)
		s.p.runtimeOutputs = make(chan ports.OutputRuntimeRequest, 32)
		s.p.continuous = make(chan ports.ContinuousCaptureRequest, 8)
	}
	// core() carries channels only; the gate, clock and terminal flag are
	// explicit core.Options.
	if s.core, err = core.New(opts.Config, s.p.core(), core.Options{Security: s.security, Clock: s.clock, Tokens: launcher.Tokens{}, Terminal: s.plan.AutoTerminal, HintDisabled: hintDisabledNote(ctx), PlaceVirtuals: !s.plan.IsolateVirtuals}); err != nil {
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
	if s.plan.SessionDBus == core.SessionDBusAlways {
		s.cast = screencast.New(s.captureAllow.Policy, s.p.castCommands, logging.For(ctx, "screencast"))
		s.cast.SetCastBuffers(screencast.CurrentBuffers(func() ports.CastBufferAllocator {
			if s.hw != nil {
				return s.hw.RenderDevice()
			}
			return s.gpu
		}))
		casts = s.cast
		if opts.castInventoryApplied != nil {
			s.cast.NotifyInventoryApplied(opts.castInventoryApplied)
		}
	}
	if s.server, err = wayland.New(wayland.Options{
		// Assembly opens the Display below, before any server/client runs.
		X11Clients:    lateX11{&s.xdisplay},
		CaptureAllow:  s.captureAllow,
		Casts:         casts,
		WorkspaceIDs:  s.wsIDs,
		Security:      s.security,
		RuntimeDir:    runtimeDir,
		DMABuf:        s.dmabuf,
		CaptureDMABuf: s.gpu.CaptureFormats(),
		SyncobjNode:   renderNodeForDevice(s.gpu),
		Keymap:        s.km.String(),
		RepeatRate:    opts.Config.Keyboard.RepeatRate,
		RepeatDelay:   opts.Config.Keyboard.RepeatDelay,
		LogDamage:     logging.Enabled(ctx, "wayland-damage"),
		Xwayland:      xwaylandSettings(opts.Config.Xwayland),
	}, s.p.wayland(), logging.For(ctx, "wayland")); err != nil {
		return s, err
	}
	s.childEnv = launcher.ChildEnv(os.Environ(), s.server.SocketName(), runtimeDir, xcursor.CursorSize())
	controlPath, err := s.openControl()
	if err != nil {
		return s, err
	}

	statePath, serr := statefile.Path(runtimeDir, s.server.SocketName())
	if s.registry != nil {
		statePath = filepath.Join(s.registry.Path(), "state.json")
		serr = nil
	}
	if serr != nil {
		log.Warn().Err(serr).Msg("state file disabled")
	} else {
		s.statePath = statePath
		s.childEnv = append(s.childEnv, statefile.Env+"="+statePath)
	}
	if s.xdisplay = openXwayland(ctx, opts, s.childEnv, logging.For(ctx, "xwayland"), s.server.AttachXwayland, s.server.AttachXWM); s.xdisplay != nil {
		s.childEnv = append(s.childEnv, "DISPLAY="+s.xdisplay.Name())
	}
	s.announceReady(opts, controlPath)
	s.child = launcher.New(s.childEnv, s.security, logging.For(ctx, "launcher"))
	s.curs = cursors.New(s.clock, opts.Config.Cursor.HideAfter)
	s.path = opts.ConfigPath
	if s.path == "" {
		s.path = config.DefaultPath()
	}
	{
		traceDamage := logging.Enabled(ctx, "render-damage")
		s.newRenderer = func(w, h int) (ports.Renderer, error) {
			device := s.gpu
			if s.hw != nil {
				device = s.hw.RenderDevice()
			}
			return device.NewRenderer(w, h, domain.RendererOptions{Virtual: true, Readback: true, TraceDamage: traceDamage})
		}
	}
	return s, nil
}

// abort releases what assembly acquired before ownership passed on: the DRM
// backend and the keymap. It runs only when assembly fails, before
// anything else is owned.
func (s *session) abort() {
	if s.registry != nil {
		defer func() {
			if err := s.registry.Close(); err != nil {
				s.log.Warn().Err(err).Msg("close session registry")
			}
		}()
	}
	if s.control != nil {
		_ = s.control.Close()
	}
	s.releaseRender()
	if s.km != nil {
		s.km.Close()
	}
}

// close releases what the session owns after its workers have joined. The
// keymap and the wayland server are not here: the input goroutine and
// Server.Run own them.
func (s *session) close() {
	if s.registry != nil {
		defer func() {
			if err := s.registry.Close(); err != nil {
				s.log.Warn().Err(err).Msg("close session registry")
			}
		}()
	}
	s.curs.Stop()
	if s.control != nil {
		_ = s.control.Close()
	}
	if s.xdisplay != nil {
		s.xdisplay.Close()
	}
	s.releaseRender()
}

// releaseRender releases the backend/device before its Vulkan instance.
func (s *session) releaseRender() {
	if s.hw != nil {
		s.hw.Close()
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
	opts, plan, log, p, hw, km, security := s.opts, s.plan, s.log, s.p, s.hw, s.km, s.security
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
	done := make(chan error, 9)
	if s.control != nil {
		workers.Add(1)
		go func() { defer workers.Done(); done <- s.control.Run(ctx, p.control) }()
	}
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
		// Only a live control socket needs its own keymap on reload.
		var controlKeymaps chan *xkb.Keymap
		if s.control != nil {
			controlKeymaps = p.controlKeymaps
		}
		relayConfig(ctx, opts.Config, p.watched, p.filtered, p.keymaps, p.deviceConfigs, curs, p.commands, logging.For(ctx, "config"), controlKeymaps)
	}()
	go func() {
		defer workers.Done()
		var (
			seat     ports.Seat
			seatName string
			active   <-chan bool
		)
		switch {
		case plan.Input == core.InputLibinputLibseat:
			seat, seatName, active = hw.Seat(), hw.Seat().Name(), hw.Seat().Subscribe()
		case plan.Input == core.InputLibinputDirect && !opts.noLibinput:
			direct := directseat.New(logging.For(ctx, "seat"))
			seat, seatName, active = direct, direct.Name(), direct.Active()
		}
		if seat != nil {
			// Core sends the first layout once the backend reports the outputs.
			var layout domain.Layout
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
					Seat:          seat,
					SeatName:      seatName,
					Keymap:        km,
					Keymaps:       p.keymaps,
					Layout:        layout,
					Layouts:       p.layouts,
					Constraints:   p.constraints,
					DeviceConfig:  opts.Config.InputDevicesConfig,
					DeviceConfigs: p.deviceConfigs,
					Active:        active,
					MoveCursor:    curs.Move,
					Log:           logging.For(ctx, "input"),
					LogMotion:     logging.Enabled(ctx, "input-motion"),
					LogKeys:       logging.Enabled(ctx, "input-keys"),
				}, p.input)
			})
			return
		}
		// No physical input producer: control owns synthetic input. Continue
		// consuming input-only state updates so the wiring cannot back up.
		defer func() { km.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case next := <-p.keymaps:
				km.Close()
				km = next
			case <-p.layouts:
			case <-p.constraints:
			case <-p.deviceConfigs:
			}
		}
	}()

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
					probes = append(probes, hw.WatchdogProbes()...)
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
	// Without hints or control the outputs reply on the wayland channel
	// itself; any split needs the router or wayland replies are lost.
	if p.routesCaptured() {
		workers.Add(1)
		go func() {
			defer workers.Done()
			routing.RouteCaptured(ctx, p.capturedAll, p.captured, p.internalCaptured(), s.controlDone)
		}()
	}
	if p.hintScans != nil {
		workers.Add(1)
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
		consumeScenes(ctx, p.scenes, p.configErrors, p.binds, p.placements, opts.testScenes, p.renderScenes)
	}()

	go func() {
		defer workers.Done()
		presented := server.Presentation()
		if opts.presentedTap != nil {
			presented = opts.presentedTap(presented)
		}
		outputIO := p.outputs(security, presented, server)
		apply := core.NewOutputApply(core.NewOutputOverrides(opts.Config, hw == nil))
		if hw != nil {
			done <- safe("output", func() error {
				return hw.RunOutputs(ctx, opts.Config, apply, outputIO, curs, s.newRenderer, logging.For(ctx, "drm"))
			})
			return
		}
		done <- headlessbackend.Run(ctx, headlessbackend.BackendOptions{Sizes: sizes, HDR: opts.HeadlessHDR}, apply, outputIO, curs, s.newRenderer, logging.For(ctx, "render"))
	}()

	if hw != nil {
		workers.Add(1)
		go func() { defer workers.Done(); done <- safe("seat", func() error { return hw.Seat().Run(ctx) }) }()
	}
	// Only a real session serves the session bus: headless runs would take
	// the names from the desktop they run in, unless asked (--dbus).
	if plan.SessionDBus == core.SessionDBusAlways {
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
	if plan.LogindPowerKeys {
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
	if errors.Is(result, core.ErrQuit) && s.control != nil {
		flushCtx, flushCancel := context.WithTimeout(ctx, time.Second)
		s.control.WaitQuitReplies(flushCtx)
		flushCancel()
	}
	cancel()
	workers.Wait()
	// The wayland server has stopped: no request can be queued any more.
	routing.DrainCaptures(ctx, p.captures, nil)
	// A keymap the input goroutine never took is still ours to free.
	for _, maps := range []chan *xkb.Keymap{p.keymaps, p.controlKeymaps} {
		select {
		case km := <-maps:
			km.Close()
		default:
		}
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

// openControl opens the session's control socket when the plan serves one.
// A socket that cannot listen only disables control; the path is empty then.
func (s *session) openControl() (string, error) {
	if !s.plan.Control {
		return "", nil
	}
	path := filepath.Join(s.registry.Path(), "control.sock")
	kb := s.opts.Config.Keyboard
	controlMap, mapErr := xkb.New(xkb.RMLVO{Layout: kb.Layout, Variant: kb.Variant, Options: kb.Options})
	if mapErr != nil {
		return "", mapErr
	}
	s.controlDone = make(chan domain.CaptureDone, ports.MaxCaptureInflight)
	if listener, err := control.Listen(path, s.wsIDs, control.Options{FramesDir: filepath.Join(s.registry.Path(), "shots", "frames"), Shooter: control.NewShooter(s.p.captures, s.controlDone, filepath.Join(s.registry.Path(), "shots")), Keymap: controlMap, Keymaps: s.p.controlKeymaps, Security: s.security}); err != nil {
		controlMap.Close()
		s.controlDone = nil
		s.log.Warn().Err(err).Msg("control socket disabled")
	} else {
		s.p.routeControlCaptures()
		s.control = listener
		s.childEnv = append(s.childEnv, "NEFERWL_CONTROL="+path)
	}
	if s.control == nil {
		return "", nil
	}
	return path, nil
}

func renderNodeForDevice(dev ports.RenderDevice) string {
	_, render := dev.Node()
	return renderNode(render)
}

func (s *session) announceReady(opts Options, controlPath string) {
	id := ""
	display := ""
	if s.xdisplay != nil {
		display = s.xdisplay.Name()
	}
	if s.registry != nil {
		id = s.registry.Meta().ID
		s.childEnv = append(s.childEnv, "NEFERWL_SESSION="+id)
		if err := s.registry.WriteMeta(sessiondir.Meta{ID: id, Wayland: s.server.SocketName(), Display: display, Control: controlPath, State: s.statePath, Profile: opts.Profile.String(), PID: os.Getpid(), Created: time.Now()}); err != nil {
			s.log.Warn().Err(err).Msg("session metadata failed")
		}
	}
	s.log.Info().Str("session", id).Str("WAYLAND_DISPLAY", s.server.SocketName()).Str("control", controlPath).Msg("listening")
	if opts.ReadyFD >= 3 {
		f := os.NewFile(uintptr(opts.ReadyFD), "session-ready")
		if f != nil {
			_ = json.NewEncoder(f).Encode(map[string]string{"id": id, "wayland": s.server.SocketName(), "control": controlPath, "display": display})
			_ = f.Close()
		}
	}
}
