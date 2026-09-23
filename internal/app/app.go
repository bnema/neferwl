// Package app coordinates the application lifecycle.
package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
)

type Options struct {
	Backend string
	Config  ports.Config
	Timeout time.Duration
}

func Run(ctx context.Context, opts Options) error { return run(ctx, opts, nil) }

func run(ctx context.Context, opts Options, inject func(chan<- ports.InputEvent)) error {
	log := logging.For(ctx, "app")
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
	ch := core.Channels{Client: client, Input: input, Output: output, Config: config, Commands: commands, Spawn: spawn, Scenes: scenes, ConfigErrors: configErrors}
	c, err := core.New(opts.Config, ch)
	if err != nil {
		return err
	}
	if opts.Backend == "headless" {
		output <- ports.OutputMode{Width: 1920, Height: 1080}
	}
	if inject != nil {
		inject(input)
	}
	var workers sync.WaitGroup
	workers.Add(2)
	done := make(chan error, 1)
	go func() { defer workers.Done(); done <- c.Run(ctx) }()
	go func() {
		defer workers.Done()
		log := logging.For(ctx, "core")
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-scenes:
				log.Debug().Uint64("seq", s.Seq).Int("windows", len(s.Windows)).Msg("scene")
			case v := <-commands:
				log.Debug().Interface("command", v).Msg("command")
			case v := <-spawn:
				log.Debug().Interface("spawn", v).Msg("spawn")
			case e := <-configErrors:
				log.Debug().Err(e).Msg("config rejected")
			}
		}
	}()
	var result error
	select {
	case <-ctx.Done():
	case result = <-done:
	}
	cancel()
	workers.Wait()
	if errors.Is(result, core.ErrQuit) {
		return nil
	}
	return result
}
