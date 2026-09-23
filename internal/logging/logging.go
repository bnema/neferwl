// Package logging configures component-scoped zerowrap logging.
package logging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"
	"golang.org/x/term"
)

var components = map[string]bool{
	"core": true, "wayland": true, "input": true, "drm": true,
	"render": true, "sync": true, "config": true, "app": true,
}

type debugKey struct{}
type levelKey struct{}
type debugSet map[string]bool

func ParseDebug(value string) (map[string]bool, error) {
	selected := make(debugSet)
	if value == "" {
		return selected, nil
	}
	for _, name := range strings.Split(value, ",") {
		if name == "all" {
			selected[name] = true
			continue
		}
		if !components[name] {
			return nil, fmt.Errorf("invalid debug component %q", name)
		}
		selected[name] = true
	}
	return selected, nil
}

// Open rotates the previous run's log, opens a fresh JSON log and attaches the logger to ctx.
// The caller must invoke close after all logging is complete.
func Open(ctx context.Context, level, debug string) (context.Context, func() error, error) {
	selected, err := ParseDebug(debug)
	if err != nil {
		return nil, nil, err
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		state = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(state, "nefertty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "nefertty.log")
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var output = zerowrap.Config{Level: level, Format: "json", Output: file}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		output.Output = &consoleAndFile{file: file}
	}
	ctx = zerowrap.WithCtx(ctx, zerowrap.New(output))
	ctx = context.WithValue(ctx, debugKey{}, debugSet(selected))
	ctx = context.WithValue(ctx, levelKey{}, level)
	return ctx, file.Close, nil
}

type consoleAndFile struct{ file *os.File }

func (w *consoleAndFile) Write(p []byte) (int, error) {
	// JSON remains the canonical output; terminal users also see each entry.
	_, _ = os.Stderr.Write(p)
	return w.file.Write(p)
}

// For returns a component logger with debug enabled only for selected components.
func For(ctx context.Context, component string) zerowrap.Logger {
	log := zerowrap.FromCtxWithField(ctx, zerowrap.FieldComponent, component)
	selected, _ := ctx.Value(debugKey{}).(debugSet)
	level := zerolog.InfoLevel
	switch ctx.Value(levelKey{}) {
	case "debug":
		level = zerolog.DebugLevel
	case "warn":
		level = zerolog.WarnLevel
	case "error":
		level = zerolog.ErrorLevel
	}
	if selected["all"] || selected[component] {
		level = zerolog.DebugLevel
	}
	log = zerowrap.Logger{Logger: log.Level(level)}
	return log
}
