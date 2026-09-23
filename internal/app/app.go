// Package app coordinates the application lifecycle.
package app

import (
	"context"
	"time"

	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
)

type Options struct {
	Backend string
	Config  ports.Config
	Timeout time.Duration
}

func Run(ctx context.Context, opts Options) error {
	log := logging.For(ctx, "app")
	log.Info().Str("backend", opts.Backend).Msg("starting nefertty")
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	<-ctx.Done()
	return nil
}
