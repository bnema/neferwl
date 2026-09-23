// Package app coordinates the application lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/adapters/headless"
	"github.com/bnema/nefertty/internal/adapters/launcher"
	"github.com/bnema/nefertty/internal/adapters/vulkan"
	"github.com/bnema/nefertty/internal/adapters/wayland"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
)

type Options struct {
	Backend       string
	Config        ports.Config
	Timeout       time.Duration
	NoTerminal    bool
	ScreenshotDir string
	testScenes    chan<- ports.Scene
}

func Run(ctx context.Context, opts Options) error { return run(ctx, opts, nil) }

func run(ctx context.Context, opts Options, inject func(chan<- ports.InputEvent)) error {
	log := logging.For(ctx, "app")
	if opts.Backend != "headless" {
		return fmt.Errorf("drm backend not implemented yet")
	}
	log.Info().Str("backend", opts.Backend).Msg("starting nefertty")
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	client := make(chan ports.ClientEvent, 32)
	input := make(chan ports.InputEvent, 32)
	output := make(chan ports.OutputEvent, 32)
	config := make(chan ports.ConfigChanged, 8)
	commands := make(chan ports.ClientCommand, 32)
	spawn := make(chan ports.SpawnRequest, 32)
	scenes := make(chan ports.Scene, 1)
	configErrors := make(chan error, 8)
	renderScenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 64)
	ch := core.Channels{Client: client, Input: input, Output: output, Config: config, Commands: commands, Spawn: spawn, Scenes: scenes, ConfigErrors: configErrors}
	c, err := core.New(opts.Config, ch)
	if err != nil {
		return err
	}
	output <- ports.OutputMode{Width: 1920, Height: 1080}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	server, err := wayland.New(wayland.Options{RuntimeDir: runtimeDir, OutputWidth: 1920, OutputHeight: 1080}, wayland.Channels{Events: client, Commands: commands, Contents: contents}, logging.For(ctx, "wayland"))
	if err != nil {
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
	workers.Add(5)
	done := make(chan error, 4)
	go func() { defer workers.Done(); done <- server.Run(ctx) }()
	go func() { defer workers.Done(); done <- child.Run(ctx, spawn) }()
	go func() { defer workers.Done(); done <- c.Run(ctx) }()
	go func() { defer workers.Done(); consumeScenes(ctx, scenes, configErrors, opts.testScenes, renderScenes) }()

	go func() {
		defer workers.Done()
		done <- headless.Run(ctx, headless.Options{Width: 1920, Height: 1080, ScreenshotDir: opts.ScreenshotDir, Log: logging.For(ctx, "render"), NewRenderer: func(w, h int) (headless.Renderer, error) {
			r, err := vulkan.New(w, h)
			if err != nil {
				return nil, err
			}
			return r, nil
		}}, renderScenes, contents)
	}()

	var result error
	select {
	case <-ctx.Done():
	case result = <-done:
	}
	cancel()
	workers.Wait()
	for len(done) > 0 {
		err := <-done
		if result == nil && err != nil {
			result = err
		}
	}
	if errors.Is(result, core.ErrQuit) {
		return nil
	}
	return result
}

func consumeScenes(ctx context.Context, scenes <-chan ports.Scene, configErrors <-chan error, tap chan<- ports.Scene, renderScenes chan ports.Scene) {
	log := logging.For(ctx, "core")
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
			log.Debug().Uint64("seq", s.Seq).Int("windows", len(s.Windows)).Msg("scene")
		case err := <-configErrors:
			log.Debug().Err(err).Msg("config rejected")
		}
	}
}
