// Package app coordinates the application lifecycle.
package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/adapters/xwayland"
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
