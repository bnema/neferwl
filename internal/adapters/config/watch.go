package config

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/bnema/kvconf"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// reloadDebounce is the quiet time after a change before the file is read:
// every write restarts it, so the file is never read half written.
const reloadDebounce = 100 * time.Millisecond

// loadRaw returns the effective keys of the file at startup; missing means none.
func loadRaw(path string) map[string]string {
	_, raw, _, err := loadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return raw
}

// Watch reloads the config when the file at path changes and sends each
// changed config on out. kvconf watches the parent directory, so atomic file
// replacements are seen, follows symlinks (dotfiles) down to their target, and
// waits for a missing directory to appear.
func Watch(ctx context.Context, path string, out chan<- ports.ConfigChanged, log zerowrap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A pending signal already means "changed": one slot is enough.
	signals := make(chan struct{}, 1)
	last := loadRaw(path)
	watchErr := make(chan error, 1)
	go func() {
		watchErr <- kvconf.Watch(ctx, path, kvconf.WatchOptions{Debounce: reloadDebounce}, signals)
	}()
	for {
		select {
		case <-ctx.Done():
			return <-watchErr
		case err := <-watchErr:
			return err
		case <-signals:
		}
		cfg, raw, warnings, err := loadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			log.Warn().Msg("config file removed; keeping current config")
			continue
		case err != nil:
			log.Warn().Err(err).Msg("config reload failed")
			continue
		}
		for _, w := range warnings {
			log.Warn().Int("line", w.Line).Msg(w.Msg)
		}
		changed := kvconf.Changed(last, raw)
		last = raw
		if len(changed) == 0 {
			log.Debug().Msg("config unchanged")
			continue
		}
		select {
		case <-ctx.Done():
			return <-watchErr
		case out <- ports.ConfigChanged{Config: cfg}:
		}
		log.Info().Strs("changed", changed).Msg("config reloaded")
	}
}
