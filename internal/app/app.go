// Package app coordinates the application lifecycle.
package app

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/adapters/xwayland"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

type Options struct {
	SessionID     string
	ReadyFD       int
	Backend       domain.Backend
	Profile       domain.Profile
	PlanOverrides core.PlanOverrides
	Config        domain.Config
	ConfigPath    string
	Timeout       time.Duration
	NoTerminal    bool
	// NoXwayland skips the X11 display even when the config enables it.
	NoXwayland bool
	// HeadlessHDR enables a virtual HDR output for protocol testing only.
	HeadlessHDR bool
	// Session exports the displays to D-Bus and systemd user services
	// before any client starts, and withdraws them on exit.
	Session bool
	// DBus serves the session D-Bus services (idle inhibition, screen
	// casting and screenshots) of a headless run, which otherwise leaves the
	// session bus alone. For tests under dbus-run-session.
	DBus bool
	// Sizes are headless outputs (width, height), left to right; empty
	// means one 1920x1080 output.
	Sizes      [][2]int
	testScenes chan<- []domain.Scene
	// captureAllowPath replaces the fixed allowlist path (tests).
	captureAllowPath string
	// captureAllowOwner is the uid that must own that file (tests use their
	// own; root, 0, otherwise).
	captureAllowOwner uint32
	// castInventoryApplied (tests) is signalled after each screencast
	// inventory the D-Bus service applied.
	castInventoryApplied chan<- struct{}
	// noLibinput (tests) keeps a libinput backend from opening real devices:
	// the inject channel drives input instead. A grab would steal the
	// developer's keyboard.
	noLibinput bool
}

func Run(ctx context.Context, opts Options) error { return run(ctx, opts, nil) }

func run(ctx context.Context, opts Options, inject func(chan<- ports.InputEvent)) error {
	log := logging.For(ctx, "app")
	log.Info().Str("backend", string(opts.Backend)).Msg("starting neferwl")
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		ctx, cancel = context.WithTimeoutCause(ctx, opts.Timeout, errTimeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	s, err := assembleSession(ctx, opts)
	if err != nil {
		return err
	}
	return s.run(ctx, inject)
}

// busRetry is the first delay before a bus service (the screensaver, the
// logind key lock) tries its bus again: without it, only surface inhibitors
// count and the power keys keep logind's behaviour, and the session goes on
// either way.
const busRetry = 2 * time.Second

// relayConfig forwards reloads to core. A layout change builds a new keymap, hands it
// to the input goroutine and sends it to clients; a repeat-only change just updates
// clients. A keymap that fails to build keeps the previous layout. A touchpad
// or mouse change goes to the input goroutine as one InputDevicesConfig; only
// the newest one waits there. The cursor idle delay goes straight to the
// cursor router.
func relayConfig(ctx context.Context, cur domain.Config, in <-chan domain.ConfigChanged, out chan<- domain.ConfigChanged, keymaps chan *xkb.Keymap, deviceConfigs chan domain.InputDevicesConfig, curs *cursors.Router, commands chan<- ports.ClientCommand, log zerowrap.Logger, controlKeymaps ...chan *xkb.Keymap) {
	kb := cur.Keyboard
	devCfg := cur.InputDevicesConfig
	for {
		var ev domain.ConfigChanged
		select {
		case <-ctx.Done():
			return
		case ev = <-in:
		}
		baseline := cur
		baseline.Keyboard = kb
		plan := core.DiffConfigReload(baseline, ev.Config)
		if len(plan.RestartOnly) != 0 {
			log.Warn().Msg("drm.devices, drm.ignore, render.device and render.multi-gpu apply after restart")
		}
		if plan.HintRestartOnly {
			log.Info().Bool("running", cur.Hint.Enable).Msg("hint.sources, hint.enable, hint.detector, hint.model and hint.threshold apply after restart")
		}
		ev.Config = plan.Sanitized
		next := ev.Config.Keyboard
		layoutChanged, repeatChanged := plan.KeyboardLayoutChanged, plan.RepeatChanged
		cmd := ports.SetKeymap{RepeatRate: next.RepeatRate, RepeatDelay: next.RepeatDelay}
		if layoutChanged {
			km, err := xkb.New(xkb.RMLVO{Layout: next.Layout, Variant: next.Variant, Options: next.Options})
			if err != nil {
				log.Warn().Err(err).Str("layout", next.Layout).Str("variant", next.Variant).Msg("keymap rejected; keeping previous layout")
				ev.Config.Keyboard.Layout, ev.Config.Keyboard.Variant, ev.Config.Keyboard.Options = kb.Layout, kb.Variant, kb.Options
				layoutChanged = false
			} else {
				cmd.Keymap = km.String()
				// Every producer needs its own mutable XKB state. Build the
				// control map separately: sharing km races physical input and
				// would merge modifiers across the two synthetic/real seats.
				for _, target := range controlKeymaps {
					if target == nil {
						continue
					}
					nextMap, err := xkb.New(xkb.RMLVO{Layout: next.Layout, Variant: next.Variant, Options: next.Options})
					if err != nil {
						log.Warn().Err(err).Msg("control keymap reload rejected")
						continue
					}
					select {
					case old := <-target:
						old.Close()
					default:
					}
					select {
					case target <- nextMap:
					case <-ctx.Done():
						nextMap.Close()
						km.Close()
						return
					}
				}
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
		curs.SetHideAfter(ev.Config.Cursor.HideAfter)
		if ev.Config.InputDevicesConfig != devCfg {
			devCfg = ev.Config.InputDevicesConfig
			routing.Latest(deviceConfigs, devCfg) // this goroutine is the only sender
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

func consumeScenes(ctx context.Context, scenes <-chan []domain.Scene, configErrors <-chan error, binds <-chan domain.InputTrace, placements <-chan domain.PlacementTrace, tap chan<- []domain.Scene, renderScenes chan []domain.Scene, observers ...func([]domain.Scene)) {
	log := logging.For(ctx, "core")
	configLog := logging.For(ctx, "config")
	for {
		select {
		case <-ctx.Done():
			return
		case set := <-scenes:
			for _, observe := range observers {
				observe(set)
			}
			if tap != nil {
				select {
				case tap <- set:
				default:
				}
			}
			routing.Latest(renderScenes, set) // this goroutine is the only sender
			logScenes(log, set)
		case t := <-placements:
			logPlacementTrace(log, t)
		case t := <-binds:
			logInputTrace(log, t)
		case err := <-configErrors:
			configLog.Warn().Err(err).Msg("config rejected")
		}
	}
}

// logInputTrace logs a bind dispatch or a seat input reset at debug level.
// It carries the bind combo only, never typed text.
func logInputTrace(log zerowrap.Logger, t domain.InputTrace) {
	ev := log.Debug()
	if !ev.Enabled() {
		return
	}
	switch v := t.(type) {
	case domain.BindDispatched:
		ev.Str("combo", v.Combo).Str("action", v.Action).Str("output", v.Output).Str("output_after", v.OutputAfter).
			Int("workspace_before", v.WorkspaceBefore).Int("workspace_after", v.WorkspaceAfter).Str("source", string(v.Source)).Msg("bind dispatched")
	case domain.SeatInputReset:
		ev.Int("keys", v.Keys).Int("mods", v.Mods).Int("buttons", v.Buttons).Msg("seat input reset")
	}
}

// logScenes logs each scene at debug level. The rect strings are built only
// when debug is on: at the default level a frame costs no allocation here.
func logScenes(log zerowrap.Logger, set []domain.Scene) {
	for _, s := range set {
		ev := log.Debug()
		if !ev.Enabled() {
			continue
		}
		ev = ev.Str("output", s.Output).Uint64("seq", s.Seq).Int("out_w", s.OutputWidth).Int("out_h", s.OutputHeight).Float64("scale", s.Scale)
		rects := make([]string, 0, len(s.Windows))
		for _, w := range s.Windows {
			rects = append(rects, fmt.Sprintf("%d:%d,%d %dx%d", w.ID, w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H))
		}
		ev.Strs("windows", rects).Msg("scene")
	}
}

// openXwayland reserves an X11 display for the selected backend, or returns
// nil when X11 is off or unavailable: the session runs without it.
func openXwayland(ctx context.Context, opts Options, env []string, log zerowrap.Logger, attach func(int), attachWM func(context.Context, int) func()) *xwayland.Display {
	cfg := opts.Config.Xwayland
	bin := cfg.Binary
	if cfg.Backend == domain.XwaylandOff || opts.NoXwayland {
		return nil
	}
	if cfg.Backend == domain.XwaylandBuiltin {
		if _, err := exec.LookPath(bin); err != nil {
			log.Warn().Err(err).Str("binary", bin).Msg("Xwayland not found: X11 apps disabled")
			return nil
		}
	} else if !xwayland.Supported(ctx, bin) {
		log.Warn().Str("binary", bin).Msg("xwayland-satellite 0.7 or later not found: X11 apps disabled")
		return nil
	}
	d, err := xwayland.Open(xwayland.Options{Backend: cfg.Backend, Attach: attach, AttachWM: attachWM, Binary: bin, Env: env, Dir: "/tmp/.X11-unix", TmpDir: "/tmp", Abstract: true, Log: log})
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

func logPlacementTrace(log zerowrap.Logger, t domain.PlacementTrace) {
	log.Info().Uint64("window", uint64(t.Window)).Str("path", t.Path).Str("output", t.Output).Uint64("workspace", t.Workspace).Uint64("launcher", uint64(t.Launcher)).Uint64("cover", uint64(t.Cover)).Bool("workspace_focus_changed", t.WorkspaceFocusChanged).Bool("seat_focus_changed", t.SeatFocusChanged).Msg("window placed")
}
