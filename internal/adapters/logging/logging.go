// Package logging configures component-scoped zerowrap logging.
package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/diode"
	"golang.org/x/term"
)

var components = map[string]bool{
	"core": true, "wayland": true, "input": true, "drm": true, "seat": true,
	"render": true, "sync": true, "config": true, "app": true,
	"screensaver": true, "powerkey": true, "sched": true,
}

// categories are debug switches narrower than a component, only on when
// named: "all" leaves them off. input-motion logs every pointer motion,
// input-keys every key (typed text), drm-flip every frame and commit
// completion with its timing. Naming one turns on debug for its component.
var categories = map[string]string{"input-motion": "input", "input-keys": "input", "drm-flip": "drm"}

type debugKey struct{}
type levelKey struct{}
type dirKey struct{}
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
		if _, ok := categories[name]; !components[name] && !ok {
			return nil, fmt.Errorf("invalid debug component %q", name)
		}
		selected[name] = true
	}
	return selected, nil
}

// keepRuns is how many per-run log files Open retains.
const keepRuns = 20

// Open creates runs/<backend>/<timestamp>.log, points latest.log in that
// directory at it, prunes old runs of that backend and attaches the logger
// to ctx. Each backend keeps its own runs: headless test runs never rotate
// a DRM session's log away.
// The caller must invoke close after all logging is complete.
func Open(ctx context.Context, backend, level, debug string) (context.Context, func() error, error) {
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
	dir := filepath.Join(state, "neferwl", "runs", backend)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	name := time.Now().Format("20060102-150405.000") + ".log"
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	latest := filepath.Join(dir, "latest.log")
	_ = os.Remove(latest)
	if err := os.Symlink(name, latest); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	pruneRuns(dir)
	// Hide Close from the diode: asyncWriter closes the file once.
	var sink io.Writer = struct{ io.Writer }{file}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		sink = &consoleAndFile{file: file}
	}
	w := newAsyncWriter(sink, file)
	ctx = zerowrap.WithCtx(ctx, zerowrap.New(zerowrap.Config{Level: level, Format: "json", Output: w}))
	ctx = context.WithValue(ctx, debugKey{}, debugSet(selected))
	ctx = context.WithValue(ctx, levelKey{}, level)
	ctx = context.WithValue(ctx, dirKey{}, dir)
	return ctx, w.Close, nil
}

// logQueue is how many entries may wait for the disk. Past it, entries are
// dropped (and counted on stderr) rather than blocking the logging
// goroutine: input, outputs and the display must never wait on a slow disk.
const logQueue = 8192

// newAsyncWriter writes entries to w from its own goroutine. Close drains
// what is queued, then closes c.
func newAsyncWriter(w io.Writer, c io.Closer) io.WriteCloser {
	d := diode.NewWriter(w, logQueue, 0, func(missed int) {
		fmt.Fprintf(os.Stderr, "neferwl: %d log entries dropped: disk too slow\n", missed)
	})
	return asyncWriter{Writer: d, closer: c}
}

type asyncWriter struct {
	diode.Writer
	closer io.Closer
}

func (a asyncWriter) Close() error {
	_ = a.Writer.Close() // drains the queue
	return a.closer.Close()
}

func pruneRuns(dir string) {
	runs, _ := filepath.Glob(filepath.Join(dir, "2*.log"))
	sort.Strings(runs)
	for len(runs) > keepRuns {
		_ = os.Remove(runs[0])
		runs = runs[1:]
	}
}

type consoleAndFile struct{ file *os.File }

func (w *consoleAndFile) Write(p []byte) (int, error) {
	// JSON remains the canonical output; terminal users also see each entry.
	_, _ = os.Stderr.Write(p)
	return w.file.Write(p)
}

// Dir is the directory of the run log Open created, or "" without one.
func Dir(ctx context.Context) string {
	dir, _ := ctx.Value(dirKey{}).(string)
	return dir
}

// Enabled reports whether a debug category was named in --debug.
func Enabled(ctx context.Context, category string) bool {
	selected, _ := ctx.Value(debugKey{}).(debugSet)
	return selected[category]
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
	for name, owner := range categories {
		if selected[name] && owner == component {
			level = zerolog.DebugLevel
		}
	}
	log = zerowrap.Logger{Logger: log.Level(level)}
	return log
}
