package app

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bnema/neferwl/internal/adapters/atspi"
	"github.com/bnema/neferwl/internal/adapters/hints"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// hintOptions builds the hint worker options from the configuration (read at
// startup) and, when NEFERWL_HINT_DEBUG is "1", the dump directory of scanned
// frames, $XDG_STATE_HOME/neferwl/hints. The detector process only starts at
// the first scan.
func hintOptions(ctx context.Context, cfg ports.Config) hints.Options {
	opts := hintDump(ctx)
	opts.Sources = append([]ports.HintSource(nil), cfg.Hint.Sources...)
	for _, kind := range opts.Sources {
		if kind == ports.HintSourceATSPI {
			opts.ATSPI = atspi.New()
		}
	}
	opts.Command, opts.ModelDir, opts.Threshold = cfg.Hint.Detector, cfg.Hint.Model, cfg.Hint.Threshold
	return opts
}

// hintDump reads NEFERWL_HINT_DEBUG once.
func hintDump(ctx context.Context) hints.Options {
	if os.Getenv("NEFERWL_HINT_DEBUG") != "1" {
		return hints.Options{}
	}
	log := logging.For(ctx, "hints")
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Warn().Err(err).Msg("hint debug dumps disabled")
			return hints.Options{}
		}
		state = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(state, "neferwl", "hints")
	log.Warn().Str("dir", dir).Msg("hint debug dumps enabled")
	return hints.Options{DumpDir: dir}
}

// hintDisabledNote returns the once-only notice for a hint bind pressed while
// hint mode is off. The logger is made now; nothing else of hint mode exists.
func hintDisabledNote(ctx context.Context) func() {
	log := logging.For(ctx, "core")
	return func() { log.Info().Msg("hint mode is disabled; set hint.enable = on and restart") }
}
