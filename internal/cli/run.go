package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/app"
	"github.com/bnema/neferwl/internal/logging"
)

// runFlags are the root command flags.
type runFlags struct {
	backend, screenshot, size, input, debug, config, pprof      string
	noTerminal, noXwayland, headlessHDR, screenshotRaw, session bool
	timeout                                                     time.Duration
}

func (f *runFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.backend, "backend", "drm", "drm or headless")
	fs.StringVar(&f.screenshot, "screenshot", "", "write PNG frames to directory")
	fs.StringVar(&f.size, "size", "1920x1080", "headless output sizes WxH, comma-separated for several outputs")
	fs.StringVar(&f.input, "input", "", "headless input script path (- for stdin)")
	fs.BoolVar(&f.noTerminal, "no-terminal", false, "skip initial terminal")
	fs.BoolVar(&f.noXwayland, "no-xwayland", false, "no X11 display for X11 apps")
	fs.BoolVar(&f.headlessHDR, "headless-hdr", false, "test-only: report HDR on virtual outputs; fall back if Vulkan HDR unavailable")
	fs.BoolVar(&f.screenshotRaw, "screenshot-raw", false, "test-only: also write latest-pq.png, the raw PQ codes of an HDR output")
	fs.DurationVar(&f.timeout, "timeout", 0, "duration before exit (0 disables timeout)")
	fs.StringVar(&f.debug, "debug", "", "debug components (comma-separated or all; all leaves out drm-flip, input-motion and input-keys, which log every flip, pointer motion or key)")
	fs.StringVar(&f.config, "config", "", "config path (empty uses XDG default)")
	fs.BoolVar(&f.session, "session", false, "run as the login session: share WAYLAND_DISPLAY and DISPLAY with systemd and D-Bus user services; notify systemd when ready")
	fs.StringVar(&f.pprof, "pprof", "", "serve Go runtime profiles (heap, CPU, trace) on this address, e.g. localhost:6060")
}

// validate checks the flag combinations and parses --size.
func (f *runFlags) validate(args []string) ([][2]int, error) {
	headless := f.backend == "headless"
	switch {
	case f.backend != "drm" && !headless:
		return nil, usagef("invalid backend %q", f.backend)
	case f.headlessHDR && !headless:
		return nil, usagef("--headless-hdr requires --backend=headless")
	case f.screenshot != "" && !headless:
		return nil, usagef("--screenshot requires --backend=headless")
	case f.screenshotRaw && (f.screenshot == "" || !f.headlessHDR):
		return nil, usagef("--screenshot-raw requires --screenshot and --headless-hdr")
	case f.input != "" && !headless:
		return nil, usagef("--input requires --backend=headless")
	case f.timeout < 0:
		return nil, usagef("negative timeout")
	case len(args) != 0:
		return nil, usagef("unexpected arguments: %v", args)
	}
	// The config file checks its own log.debug names.
	if _, err := logging.ParseDebug(f.debug); err != nil {
		return nil, usageError{fmt.Errorf("--debug: %w", err)}
	}
	return parseSizes(f.size)
}

func parseSizes(value string) ([][2]int, error) {
	sizes := make([][2]int, 0, strings.Count(value, ",")+1)
	for s := range strings.SplitSeq(value, ",") {
		w, h, _, err := config.ParseMode(strings.TrimSpace(s))
		if err != nil {
			return nil, usagef("--size: %w", err)
		}
		if w == 0 {
			return nil, usagef("--size: need WxH, got %q", s)
		}
		sizes = append(sizes, [2]int{w, h})
	}
	return sizes, nil
}

func setupRun(fs *flag.FlagSet) Runner {
	var f runFlags
	f.register(fs)
	return func(ctx context.Context, _ Env, args []string) error {
		sizes, err := f.validate(args)
		if err != nil {
			return err
		}
		return f.run(ctx, sizes)
	}
}

// run starts the compositor once the flags are valid. SIGINT and SIGTERM
// stop it; other commands keep the default signal behaviour.
func (f *runFlags) run(ctx context.Context, sizes [][2]int) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if f.screenshot != "" {
		if err := os.MkdirAll(f.screenshot, 0o755); err != nil {
			return err
		}
	}
	path, cfg, warnings, err := loadConfig(f.config)
	if err != nil {
		return err
	}
	script, closeScript, err := openScript(f.input)
	if err != nil {
		return err
	}
	// app.Run closes the script on shutdown; this covers the returns before it.
	defer closeScript()
	tuneGC()
	ctx, closeLog, err := logging.Open(ctx, f.backend, cfg.Log.Level, mergeDebug(cfg.Log.Debug, f.debug))
	if err != nil {
		return err
	}
	defer closeLog()
	log := logging.For(ctx, "app")
	defer func() {
		if p := recover(); p != nil {
			log.Error().Interface("panic", p).Str("stack", string(debug.Stack())).Msg("exit")
			_ = closeLog()
			panic(p)
		}
	}()
	if f.pprof != "" {
		if err := servePprof(ctx, f.pprof); err != nil {
			return err
		}
		log.Info().Str("addr", f.pprof).Msg("pprof")
	}
	start := time.Now()
	log.Info().Strs("args", os.Args[1:]).Str("backend", f.backend).Str("tty", os.Getenv("XDG_VTNR")).Str("session_type", os.Getenv("XDG_SESSION_TYPE")).Msg("run")
	configLog := logging.For(ctx, "config")
	configLog.Info().Str("path", path).Msg("loaded config")
	for _, w := range warnings {
		configLog.Warn().Int("line", w.Line).Msg(w.Msg)
	}
	err = app.Run(ctx, app.Options{
		Backend:       f.backend,
		Config:        cfg,
		ConfigPath:    path,
		Timeout:       f.timeout,
		NoTerminal:    f.noTerminal,
		NoXwayland:    f.noXwayland,
		HeadlessHDR:   f.headlessHDR,
		Session:       f.session,
		ScreenshotDir: f.screenshot,
		ScreenshotRaw: f.screenshotRaw,
		Sizes:         sizes,
		Script:        script,
	})
	// SIGINT and SIGTERM cancel the context and are clean exits.
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		err = nil
	}
	reason := "clean"
	switch {
	case err != nil:
		reason = "error"
	case ctx.Err() != nil:
		reason = "signal"
	case f.timeout > 0 && time.Since(start) >= f.timeout:
		reason = "timeout"
	}
	log.Info().Str("reason", reason).AnErr("error", err).Dur("uptime", time.Since(start)).Msg("exit")
	if err != nil {
		return loggedError{err}
	}
	return nil
}

// loggedError is an error already written to the run log, which also goes
// to stderr on a terminal; exitCode does not print it again.
type loggedError struct{ error }

func (e loggedError) Unwrap() error { return e.error }

// mergeDebug joins the configured debug categories and the --debug value
// in at most one allocation.
func mergeDebug(configured []string, cli string) string {
	switch {
	case len(configured) == 0:
		return cli
	case len(configured) == 1 && cli == "":
		return configured[0]
	}
	n := len(cli) + len(configured)
	for _, c := range configured {
		n += len(c)
	}
	var b strings.Builder
	b.Grow(n)
	for i, c := range configured {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(c)
	}
	if cli != "" {
		b.WriteByte(',')
		b.WriteString(cli)
	}
	return b.String()
}

// openScript opens the --input script. It returns a nil interface, not a
// typed-nil *os.File, when no script was requested: app.Run tests the
// interface against nil to decide whether to read a script. closeScript
// closes what openScript opened; it does nothing for stdin and for no script.
func openScript(path string) (script io.ReadCloser, closeScript func(), err error) {
	switch path {
	case "":
		return nil, func() {}, nil
	case "-":
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}

// tuneGC bounds the heap of a process that runs for days. The Go live heap
// is small (tens of MB; buffers live in GPU and shared memory), so the
// default GOGC=100 is kept: on the tiled playback test, GOGC=50 ran 13
// collections for 91 ms of GC CPU where GOGC=100 ran 2 for 3 ms, for 2 MB
// more heap. A soft limit makes the collector work harder before the
// process gets large. GOGC and GOMEMLIMIT set in the environment win.
func tuneGC() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(256 << 20)
	}
}

// servePprof exposes the runtime profiles under /debug/pprof/ until ctx is
// done. It binds before returning so a bad address fails at startup.
func servePprof(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("--pprof: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return nil
}
