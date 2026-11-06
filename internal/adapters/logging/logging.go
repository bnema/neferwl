// Package logging configures component-scoped zerowrap logging.
package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	runtimedebug "runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"bnema.dev/zerowrap"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/diode"
	"golang.org/x/term"
)

var components = map[string]bool{
	"core": true, "wayland": true, "input": true, "drm": true, "seat": true,
	"render": true, "sync": true, "config": true, "app": true, "control": true,
	"screensaver": true, "powerkey": true, "sched": true, "screencast": true, "hints": true,
}

// categories are debug switches narrower than a component, only on when
// named: "all" leaves them off. input-motion logs every pointer motion,
// input-keys every key (typed text), drm-flip every frame and commit
// completion with its timing, wayland-damage every fresh client commit with
// its damage rects, render-damage every rendered frame with its redrawn
// area. Naming one turns on debug for its component.
var categories = map[string]string{
	"input-motion": "input", "input-keys": "input", "drm-flip": "drm",
	"wayland-damage": "wayland", "render-damage": "render",
}

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
// The caller must invoke close after all logging is complete; crashed
// keeps the crash file for a panic about to be re-raised.
func Open(ctx context.Context, backend, level, debug string) (context.Context, func(crashed bool) error, error) {
	state, err := stateDir()
	if err != nil {
		return nil, nil, err
	}
	return openDir(ctx, filepath.Join(state, "neferwl", "runs", backend), level, debug, nil)
}

// OpenSession is Open for a headless session: its runs go to
// sessions/<id>/ under the state directory. The session directory is
// locked while the log is open, and session directories beyond the newest
// keepRuns (by modification time) are removed unless their lock is held.
func OpenSession(ctx context.Context, id, level, debug string) (context.Context, func(crashed bool) error, error) {
	if _, err := ParseDebug(debug); err != nil {
		return nil, nil, err
	}
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return nil, nil, fmt.Errorf("invalid session id %q", id)
	}
	state, err := stateDir()
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Join(state, "neferwl", "sessions")
	lifecycle, err := lockSessionRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer lifecycle.Close()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("session log %s is in use: %w", dir, err)
	}
	pruneSessions(root, id)
	return openDir(ctx, dir, level, debug, lock)
}

func stateDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return state, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// openDir starts a run in dir. lock, if any, is closed with the log.
func openDir(ctx context.Context, dir, level, debug string, lock *os.File) (_ context.Context, _ func(crashed bool) error, err error) {
	if lock != nil {
		defer func() {
			if err != nil {
				_ = lock.Close()
			}
		}()
	}
	selected, err := ParseDebug(debug)
	if err != nil {
		return nil, nil, err
	}
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
	// Queued entries are lost on a fatal crash: the runtime's own report
	// (panic and every stack) goes to its own file, besides stderr. A clean
	// Close removes it while still empty. Created before pruneRuns so
	// crash files rotate like logs.
	crashPath := filepath.Join(dir, strings.TrimSuffix(name, ".log")+".crash")
	// A run's crash file must include stacks even when the inherited
	// GOTRACEBACK setting suppresses them. This covers worker goroutines too.
	runtimedebug.SetTraceback("all")
	var crashErr error
	if crash, err := os.OpenFile(crashPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		crashErr = runtimedebug.SetCrashOutput(crash, runtimedebug.CrashOptions{})
		_ = crash.Close() // SetCrashOutput keeps its own duplicate
	} else {
		crashErr = err
		crashPath = ""
	}
	pruneRuns(dir)
	// Hide Close from the diode: asyncWriter closes the file once.
	var sink io.Writer = struct{ io.Writer }{file}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		sink = &consoleAndFile{file: file}
	}
	w := newAsyncWriter(sink, file, crashPath)
	ctx = zerowrap.WithCtx(ctx, zerowrap.New(zerowrap.Config{Level: level, Format: "json", Output: w}))
	ctx = context.WithValue(ctx, debugKey{}, debugSet(selected))
	ctx = context.WithValue(ctx, levelKey{}, level)
	ctx = context.WithValue(ctx, dirKey{}, dir)
	if crashErr != nil {
		log := For(ctx, "app")
		log.Error().Err(crashErr).Msg("crash output unavailable; runtime reports remain on stderr")
	}
	return ctx, func(crashed bool) error {
		if !crashed {
			w.Clean()
		}
		err := w.Close()
		if lock != nil {
			_ = lock.Close()
		}
		return err
	}, nil
}

// pruneSessions keeps the newest keepRuns session log directories of root
// by modification time; current and directories whose lock is held by a
// live session are kept. The caller must hold the root lifecycle lock.
func pruneSessions(root, current string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type session struct {
		path string
		mod  time.Time
	}
	var old []session
	for _, e := range entries {
		if !e.IsDir() || e.Name() == current {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		old = append(old, session{filepath.Join(root, e.Name()), info.ModTime()})
	}
	// The current session counts toward keepRuns.
	if len(old) < keepRuns {
		return
	}
	sort.Slice(old, func(i, j int) bool { return old[i].mod.After(old[j].mod) })
	for _, s := range old[keepRuns-1:] {
		removeSession(s.path)
	}
}

// lockSessionRoot serializes directory creation and pruning. The root lock file
// is never removed: every opener must lock the same inode before touching a
// session directory, then acquire its session lock before releasing this lock.
func lockSessionRoot(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// removeSession holds the session lock through removal. On any lock error,
// conservatively keep the directory. The caller must hold the root lock.
func removeSession(dir string) {
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return
	}
	_ = os.RemoveAll(dir)
}

// logQueue is how many entries may wait for the disk. Past it, entries are
// dropped (and counted on stderr) rather than blocking the logging
// goroutine: input, outputs and the display must never wait on a slow disk.
const logQueue = 8192

// newAsyncWriter writes entries to w from its own goroutine. Close drains
// what is queued, then closes c.
func newAsyncWriter(w io.Writer, c io.Closer, crashPath string) *asyncWriter {
	d := diode.NewWriter(w, logQueue, 0, func(missed int) {
		fmt.Fprintf(os.Stderr, "neferwl: %d log entries dropped: disk too slow\n", missed)
	})
	return &asyncWriter{Writer: d, closer: c, crashPath: crashPath}
}

type asyncWriter struct {
	diode.Writer
	closer    io.Closer
	crashPath string
	once      sync.Once
	err       error
}

// Close drains the queue and closes the file once.
func (a *asyncWriter) Close() error {
	a.once.Do(func() {
		_ = a.Writer.Close() // drains the queue
		a.err = a.closer.Close()
	})
	return a.err
}

// Clean ends a run that did not crash: the crash output is released and
// the crash file removed while still empty.
func (a *asyncWriter) Clean() {
	if a.crashPath == "" {
		return
	}
	_ = runtimedebug.SetCrashOutput(nil, runtimedebug.CrashOptions{})
	if fi, err := os.Stat(a.crashPath); err == nil && fi.Size() == 0 {
		_ = os.Remove(a.crashPath)
	}
}

func pruneRuns(dir string) {
	// Watchdog stall dumps (stall-<time>-<loop>.txt) rotate like runs.
	for _, pattern := range []string{"2*.log", "2*.crash", "stall-*.txt"} {
		runs, _ := filepath.Glob(filepath.Join(dir, pattern))
		sort.Strings(runs)
		for len(runs) > keepRuns {
			_ = os.Remove(runs[0])
			runs = runs[1:]
		}
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
