package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sessiondir"
	"github.com/bnema/neferwl/internal/app"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/term"
)

// runFlags are the root command flags.
type runFlags struct {
	sessionID                                                  string
	readyFD                                                    int
	overrides                                                  core.PlanOverrides
	deprecatedServer                                           bool
	backend, profile, inputDevices, size, debug, config, pprof string
	noTerminal, noXwayland, headlessHDR, session, dbus         bool
	timeout                                                    time.Duration
}

func (f *runFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.backend, "backend", "drm", "drm or headless (server is deprecated)")
	fs.StringVar(&f.sessionID, "session-id", "", "headless session ID")
	fs.IntVar(&f.readyFD, "ready-fd", 0, "headless readiness file descriptor")
	fs.StringVar(&f.profile, "profile", "ephemeral", "headless profile: ephemeral or persistent")
	fs.StringVar(&f.inputDevices, "input-devices", "", "headless input devices: none or grab")
	fs.StringVar(&f.size, "size", "1920x1080", "headless output sizes WxH, comma-separated for several outputs")
	fs.BoolVar(&f.noTerminal, "no-terminal", false, "skip initial terminal")
	fs.BoolVar(&f.noXwayland, "no-xwayland", false, "no X11 display for X11 apps")
	fs.BoolVar(&f.headlessHDR, "headless-hdr", false, "test-only: report HDR on virtual outputs; fall back if Vulkan HDR unavailable")
	fs.DurationVar(&f.timeout, "timeout", 0, "duration before exit (0 disables timeout)")
	fs.StringVar(&f.debug, "debug", "", "debug components (comma-separated or all; all leaves out drm-flip, input-motion, input-keys, wayland-damage and render-damage, which log every flip, pointer motion, key, client commit or frame)")
	fs.StringVar(&f.config, "config", "", "config path (empty uses XDG default)")
	fs.BoolVar(&f.session, "session", false, "run as the login session: share WAYLAND_DISPLAY and DISPLAY with systemd and D-Bus user services; notify systemd when ready")
	fs.BoolVar(&f.dbus, "dbus", false, "headless: serve the session D-Bus services; for tests under dbus-run-session")
	fs.StringVar(&f.pprof, "pprof", "", "serve Go runtime profiles (heap, CPU, trace) on this address, e.g. localhost:6060")
}

// validate checks the flag combinations and parses --size. set holds the
// flags given on the command line.
func (f *runFlags) validate(args []string, set map[string]bool) ([][2]int, error) {
	if err := f.normalizeBackendAlias(set); err != nil {
		return nil, err
	}
	headless := f.backend == "headless"
	if (set["session-id"] || set["ready-fd"]) && !headless {
		return nil, usagef("--session-id and --ready-fd require headless")
	}
	if f.sessionID != "" && !sessiondir.ValidID(f.sessionID) {
		return nil, usagef("invalid session id")
	}
	if set["ready-fd"] && f.readyFD < 3 {
		return nil, usagef("ready-fd must be at least 3")
	}
	if !headless && set["profile"] {
		return nil, usagef("--profile requires --backend=headless")
	}
	if _, err := domain.ParseProfile(f.profile); err != nil {
		return nil, usagef("%v", err)
	}
	if err := f.parseOverrides(set, headless); err != nil {
		return nil, err
	}
	switch {
	case f.backend != "drm" && !headless:
		return nil, usagef("invalid backend %q", f.backend)
	case f.dbus && !headless:
		return nil, usagef("--dbus requires --backend=headless")
	case f.headlessHDR && !headless:
		return nil, usagef("--headless-hdr requires --backend=headless")
	case f.timeout < 0:
		return nil, usagef("negative timeout")
	case len(args) != 0:
		return nil, usagef("unexpected arguments: %v", args)
	}
	// The config file checks its own log.debug names.
	if _, err := logging.ParseDebug(f.debug); err != nil {
		return nil, usageError{fmt.Errorf("--debug: %w", err)}
	}
	if headless && f.profile == "persistent" && !set["size"] {
		return nil, nil
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
	return func(ctx context.Context, env Env, args []string) error {
		set := map[string]bool{}
		fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
		if err := f.normalizeBackendAlias(set); err != nil {
			return err
		}
		// Warn before validation, so a rejected alias run is warned too.
		if f.deprecatedServer {
			fmt.Fprintln(env.Stderr, "deprecated: use --backend=headless --profile=persistent")
		}
		sizes, err := f.validate(args, set)
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
	path, cfg, warnings, err := loadConfig(f.config)
	if err != nil {
		return err
	}
	tuneGC()
	if f.backend == "headless" && f.sessionID == "" {
		f.sessionID = sessiondir.NewID()
	}
	var closeLog func(bool) error
	if f.sessionID != "" {
		ctx, closeLog, err = logging.OpenSession(ctx, f.sessionID, cfg.Log.Level, mergeDebug(cfg.Log.Debug, f.debug))
	} else {
		ctx, closeLog, err = logging.Open(ctx, f.backend, cfg.Log.Level, mergeDebug(cfg.Log.Debug, f.debug))
	}
	if err != nil {
		return err
	}
	defer closeLog(false) // no-op after the panic path closed it
	log := logging.For(ctx, "app")
	defer func() {
		if p := recover(); p != nil {
			log.Error().Interface("panic", p).Str("stack", string(debug.Stack())).Msg("exit")
			_ = closeLog(true) // keep the crash file for the re-panic
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
		Backend:   domain.Backend(f.backend),
		SessionID: f.sessionID, ReadyFD: f.readyFD,
		Profile:       mustProfile(f.profile),
		PlanOverrides: f.overrides,
		Config:        cfg,
		ConfigPath:    path,
		Timeout:       f.timeout,
		NoXwayland:    f.noXwayland,
		HeadlessHDR:   f.headlessHDR,
		Session:       f.session,
		DBus:          f.dbus,
		Sizes:         sizes,
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
	// On a terminal the run log already went to stderr (logging.Open).
	if err != nil && term.IsTerminal(int(os.Stderr.Fd())) {
		return loggedError{err}
	}
	return err
}

// loggedError is an error the run log already printed on stderr; exitCode
// does not print it again.
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

func mustProfile(s string) domain.Profile { p, _ := domain.ParseProfile(s); return p }

// normalizeBackendAlias maps the deprecated --backend=server to headless with
// the persistent profile. An explicit --profile contradicts the alias.
// Idempotent: validate calls it again.
func (f *runFlags) normalizeBackendAlias(set map[string]bool) error {
	if f.backend == "server" {
		if set["profile"] {
			return usagef("--backend=server implies --profile=persistent; use --backend=headless --profile=%s", f.profile)
		}
		f.backend = "headless"
		f.profile = "persistent"
		f.deprecatedServer = true
	}
	return nil
}

func (f *runFlags) parseOverrides(set map[string]bool, headless bool) error {
	if set["input-devices"] {
		if !headless {
			return usagef("--input-devices requires --backend=headless")
		}
		input := core.InputNone
		switch f.inputDevices {
		case "none":
		case "grab":
			input = core.InputLibinputDirect
		default:
			return usagef("--input-devices must be none or grab")
		}
		f.overrides.Input = &input
	}
	if set["dbus"] {
		value := f.dbus
		f.overrides.SessionDBus = &value
	}
	if set["no-terminal"] {
		value := !f.noTerminal
		f.overrides.AutoTerminal = &value
	}
	return nil
}
