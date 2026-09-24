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
	"github.com/bnema/nefertty/internal/adapters/headless"
	"github.com/bnema/nefertty/internal/adapters/headlessinput"
	"github.com/bnema/nefertty/internal/adapters/launcher"
	"github.com/bnema/nefertty/internal/adapters/libinput"
	"github.com/bnema/nefertty/internal/adapters/vulkan"
	"github.com/bnema/nefertty/internal/adapters/wayland"
	"github.com/bnema/nefertty/internal/adapters/xkb"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
)

type Options struct {
	Backend       string
	Config        ports.Config
	ConfigPath    string
	Timeout       time.Duration
	NoTerminal    bool
	ScreenshotDir string
	// Width and Height size the headless output; 0 means 1920x1080.
	Width, Height int
	// Run closes Script on shutdown; the reader goroutine exits after Close.
	Script     io.ReadCloser
	testScenes chan<- ports.Scene
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
	width, height := opts.Width, opts.Height
	if width <= 0 || height <= 0 {
		width, height = 1920, 1080
	}
	var outInfo wayland.OutputInfo
	var hw *drmBackend
	if opts.Backend == "drm" {
		var err error
		if hw, err = openDRM(ctx, opts.Config.Outputs); err != nil {
			return err
		}
		defer hw.close()
		width, height = hw.out.Width(), hw.out.Height()
		outInfo = hw.outputInfo()
		hw.inputActive, hw.outputActive = hw.seat.Subscribe(), hw.seat.Subscribe()
	}
	client := make(chan ports.ClientEvent, 32)
	input := make(chan ports.InputEvent, 32)
	output := make(chan ports.OutputEvent, 32)
	configChanges := make(chan ports.ConfigChanged, 8)
	commands := make(chan ports.ClientCommand, 32)
	spawn := make(chan ports.SpawnRequest, 32)
	scenes := make(chan ports.Scene, 1)
	configErrors := make(chan error, 8)
	renderScenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 64)
	ch := core.Channels{Client: client, Input: input, Output: output, Config: configChanges, Commands: commands, Spawn: spawn, Scenes: scenes, ConfigErrors: configErrors}
	c, err := core.New(opts.Config, ch)
	if err != nil {
		return err
	}
	output <- ports.OutputMode{Width: width, Height: height}
	km, err := xkb.New(xkb.RMLVO{Layout: opts.Config.Keyboard.Layout, Variant: opts.Config.Keyboard.Variant, Options: opts.Config.Keyboard.Options})
	if err != nil {
		return err
	}
	keymap := km.String()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	server, err := wayland.New(wayland.Options{RuntimeDir: runtimeDir, OutputWidth: width, OutputHeight: height, Output: outInfo, Keymap: keymap, RepeatRate: opts.Config.Keyboard.RepeatRate, RepeatDelay: opts.Config.Keyboard.RepeatDelay}, wayland.Channels{Events: client, Commands: commands, Contents: contents}, logging.For(ctx, "wayland"))
	if err != nil {
		km.Close()
		return err
	}
	log.Info().Str("WAYLAND_DISPLAY", server.SocketName()).Msg("listening")
	child := launcher.New(launcher.ChildEnv(os.Environ(), server.SocketName(), runtimeDir), logging.For(ctx, "launcher"))
	if !opts.NoTerminal {
		spawn <- ports.SpawnRequest{Argv: append([]string(nil), opts.Config.Terminal.Command...)}
	}
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
	go func() {
		defer workers.Done()
		done <- config.Watch(ctx, path, configChanges, logging.For(ctx, "config"))
	}()
	script := make(chan string)
	go func() {
		defer workers.Done()
		if hw != nil {
			done <- safe("input", func() error {
				return libinput.Run(ctx, libinput.Options{Seat: hw.seat, SeatName: hw.seat.Name(), Keymap: km, Width: width, Height: height, Active: hw.inputActive, Log: logging.For(ctx, "input")}, input)
			})
			return
		}
		_ = headlessinput.Run(ctx, km, script, input, logging.For(ctx, "input"))
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
	go func() { defer workers.Done(); consumeScenes(ctx, scenes, configErrors, opts.testScenes, renderScenes) }()

	go func() {
		defer workers.Done()
		if hw != nil {
			done <- safe("output", func() error {
				return hw.out.Run(ctx, func(w, h int) (drm.Renderer, error) {
					r, err := vulkan.New(w, h)
					if err != nil {
						return nil, err
					}
					return r, nil
				}, hw.outputActive, renderScenes, contents)
			})
			return
		}
		done <- headless.Run(ctx, headless.Options{Width: width, Height: height, ScreenshotDir: opts.ScreenshotDir, Log: logging.For(ctx, "render"), NewRenderer: func(w, h int) (headless.Renderer, error) {
			r, err := vulkan.New(w, h)
			if err != nil {
				return nil, err
			}
			return r, nil
		}}, renderScenes, contents)
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

func consumeScenes(ctx context.Context, scenes <-chan ports.Scene, configErrors <-chan error, tap chan<- ports.Scene, renderScenes chan ports.Scene) {
	log := logging.For(ctx, "core")
	configLog := logging.For(ctx, "config")
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-scenes:
			if tap != nil {
				select {
				case tap <- s:
				default:
				}
			}
			select {
			case renderScenes <- s:
			default:
				select {
				case <-renderScenes:
				default:
				}
				select {
				case renderScenes <- s:
				default:
				}
			}
			ev := log.Debug().Uint64("seq", s.Seq).Int("out_w", s.OutputWidth).Int("out_h", s.OutputHeight)
			rects := make([]string, 0, len(s.Windows))
			for _, w := range s.Windows {
				rects = append(rects, fmt.Sprintf("%d:%d,%d %dx%d", w.ID, w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H))
			}
			ev.Strs("windows", rects).Msg("scene")
		case err := <-configErrors:
			configLog.Warn().Err(err).Msg("config rejected")
		}
	}
}
