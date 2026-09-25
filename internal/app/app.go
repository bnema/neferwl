// Package app coordinates the application lifecycle.
package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/adapters/drm"
	"github.com/bnema/nefertty/internal/adapters/headlessinput"
	"github.com/bnema/nefertty/internal/adapters/launcher"
	"github.com/bnema/nefertty/internal/adapters/libinput"
	"github.com/bnema/nefertty/internal/adapters/statefile"
	"github.com/bnema/nefertty/internal/adapters/vulkan"
	"github.com/bnema/nefertty/internal/adapters/wayland"
	"github.com/bnema/nefertty/internal/adapters/xkb"
	"github.com/bnema/nefertty/internal/adapters/xwayland"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
)

type Options struct {
	Backend    string
	Config     ports.Config
	ConfigPath string
	Timeout    time.Duration
	NoTerminal bool
	// NoXwayland skips the X11 display even when the config enables it.
	NoXwayland    bool
	ScreenshotDir string
	// Sizes are the headless outputs (width, height), left to right; empty
	// means one 1920x1080 output. With several outputs, screenshots go to
	// one subdirectory per output (HEADLESS-1, HEADLESS-2, ...).
	Sizes [][2]int
	// Run closes Script on shutdown; the reader goroutine exits after Close.
	Script     io.ReadCloser
	testScenes chan<- []ports.Scene
}

func Run(ctx context.Context, opts Options) error { return run(ctx, opts, nil) }

func run(ctx context.Context, opts Options, inject func(chan<- ports.InputEvent)) error {
	log := logging.For(ctx, "app")
	log.Info().Str("backend", opts.Backend).Msg("starting nefertty")
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
		if hw, err = openDRM(ctx, opts.Config.Outputs); err != nil {
			return err
		}
		defer hw.close()
	}
	client := make(chan ports.ClientEvent, 32)
	input := make(chan ports.InputEvent, 32)
	output := make(chan ports.OutputEvent, 32)
	configChanges := make(chan ports.ConfigChanged, 8)
	commands := make(chan ports.ClientCommand, 32)
	spawn := make(chan ports.SpawnRequest, 32)
	scenes := make(chan []ports.Scene, 1)
	layouts := make(chan ports.Layout, 1)
	constraints := make(chan ports.PointerConstraint, 1)
	states := make(chan ports.State, 1)
	configErrors := make(chan error, 8)
	renderScenes := make(chan []ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 64)
	cursorChanges := make(chan ports.CursorChange, 1)
	ch := core.Channels{Client: client, Input: input, Output: output, Config: configChanges, Commands: commands, Spawn: spawn, Scenes: scenes, Layouts: layouts, Constraints: constraints, State: states, ConfigErrors: configErrors, Terminal: !opts.NoTerminal}
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
	server, err := wayland.New(wayland.Options{RuntimeDir: runtimeDir, DMABuf: dmabuf, Keymap: keymap, RepeatRate: opts.Config.Keyboard.RepeatRate, RepeatDelay: opts.Config.Keyboard.RepeatDelay}, wayland.Channels{Events: client, Commands: commands, Contents: contents, Cursors: cursorChanges}, logging.For(ctx, "wayland"))
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
	child := launcher.New(childEnv, logging.For(ctx, "launcher"))
	if inject != nil {
		inject(input)
	}
	var workers sync.WaitGroup
	workers.Add(7)
	done := make(chan error, 8)
	path := opts.ConfigPath
	if path == "" {
		path = config.DefaultPath()
	}
	watched := make(chan ports.ConfigChanged, 8)
	keymaps := make(chan *xkb.Keymap, 1)
	go func() {
		defer workers.Done()
		done <- config.Watch(ctx, path, watched, logging.For(ctx, "config"))
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		relayConfig(ctx, opts.Config, watched, configChanges, keymaps, commands, logging.For(ctx, "config"))
	}()
	script := make(chan string)
	curs := newCursors()
	go func() {
		defer workers.Done()
		if hw != nil {
			// Core sends the first layout once DRM reports the outputs.
			var layout ports.Layout
			select {
			case layout = <-layouts:
			case <-ctx.Done():
				km.Close()
				return
			}
			done <- safe("input", func() error {
				return libinput.Run(ctx, libinput.Options{Seat: hw.seat, SeatName: hw.seat.Name(), Keymap: km, Keymaps: keymaps, Layout: layout, Layouts: layouts, Constraints: constraints, Active: hw.seat.Subscribe(), MoveCursor: curs.move, Log: logging.For(ctx, "input")}, input)
			})
			return
		}
		_ = headlessinput.Run(ctx, km, keymaps, script, input, layouts, curs.move, logging.For(ctx, "input"))
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
	go func() { defer workers.Done(); done <- child.Run(ctx, spawn) }()
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
			if err := statefile.Run(ctx, statePath, states, logging.For(ctx, "statefile")); err != nil {
				// Scripts lose their state; the session goes on.
				log.Warn().Err(err).Msg("state file disabled")
			}
		}()
	}
	go func() { defer workers.Done(); consumeScenes(ctx, scenes, configErrors, opts.testScenes, renderScenes) }()

	go func() {
		defer workers.Done()
		newRenderer := func(w, h int) (ports.Renderer, error) {
			r, err := vulkan.New(w, h)
			if err != nil {
				return nil, err
			}
			return r, nil
		}
		if hw != nil {
			done <- safe("output", func() error {
				want := func() drm.Want { return wantFromConfig(opts.Config.Outputs) }
				return hw.runOutputs(ctx, want, output, renderScenes, contents, cursorChanges, curs, newRenderer, logging.For(ctx, "drm"))
			})
			return
		}
		done <- runHeadless(ctx, sizes, opts.ScreenshotDir, output, renderScenes, contents, cursorChanges, curs, newRenderer, logging.For(ctx, "render"))
	}()

	if hw != nil {
		workers.Add(1)
		go func() { defer workers.Done(); done <- safe("seat", func() error { return hw.seat.Run(ctx) }) }()
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
	// A keymap the input goroutine never took is still ours to free.
	select {
	case km := <-keymaps:
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

// relayConfig forwards reloads to core. A layout change builds a new keymap, hands it
// to the input goroutine and sends it to clients; a repeat-only change just updates
// clients. A keymap that fails to build keeps the previous layout.
func relayConfig(ctx context.Context, cur ports.Config, in <-chan ports.ConfigChanged, out chan<- ports.ConfigChanged, keymaps chan *xkb.Keymap, commands chan<- ports.ClientCommand, log zerowrap.Logger) {
	kb := cur.Keyboard
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
			select {
			case renderScenes <- set:
			default:
				select {
				case <-renderScenes:
				default:
				}
				select {
				case renderScenes <- set:
				default:
				}
			}
			for _, s := range set {
				ev := log.Debug().Str("output", s.Output).Uint64("seq", s.Seq).Int("out_w", s.OutputWidth).Int("out_h", s.OutputHeight).Float64("scale", s.Scale)
				rects := make([]string, 0, len(s.Windows))
				for _, w := range s.Windows {
					rects = append(rects, fmt.Sprintf("%d:%d,%d %dx%d", w.ID, w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H))
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
