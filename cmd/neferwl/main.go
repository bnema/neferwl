package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/app"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
)

type usageError struct{ error }

// version is set with -ldflags "-X main.version=..." by packaged builds.
var version string

func main() {
	tuneGC()
	os.Exit(runCode())
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

func runCode() int {
	err := run()
	if err == nil || errors.Is(err, flag.ErrHelp) || errors.Is(err, context.Canceled) {
		return 0
	}
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func mergeDebug(configured []string, cli string) string {
	if cli == "" {
		return strings.Join(configured, ",")
	}
	if len(configured) == 0 {
		return cli
	}
	return strings.Join(configured, ",") + "," + cli
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "validate-config" {
		if len(os.Args) > 3 {
			err := usageError{fmt.Errorf("usage: validate-config [path]")}
			fmt.Fprintln(os.Stderr, err)
			return err
		}
		path := config.DefaultPath()
		load := config.LoadDefault
		if len(os.Args) == 3 {
			path = os.Args[2]
			load = func() (ports.Config, []config.Warning, error) { return config.Load(path) }
		}
		_, warnings, err := load()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "%s:%s\n", path, w)
		}
		if len(warnings) > 0 {
			return fmt.Errorf("%d warning(s)", len(warnings))
		}
		fmt.Println("ok: " + path)
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "state" {
		return runState(os.Args[2:])
	}
	if len(os.Args) == 2 && os.Args[1] == "version" {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			fmt.Println("unknown")
			return nil
		}
		v := info.Main.Version
		if version != "" {
			v = version // set by packaged builds (PKGBUILD -X main.version)
		} else if v == "" {
			v = "(devel)"
		}
		fmt.Println(v)
		return nil
	}
	flags := flag.NewFlagSet("neferwl", flag.ContinueOnError)
	backend := flags.String("backend", "drm", "drm or headless")
	screenshot := flags.String("screenshot", "", "write PNG frames to directory")
	size := flags.String("size", "1920x1080", "headless output sizes WxH, comma-separated for several outputs")
	inputPath := flags.String("input", "", "headless input script path (- for stdin)")
	noTerminal := flags.Bool("no-terminal", false, "skip initial terminal")
	noXwayland := flags.Bool("no-xwayland", false, "no X11 display for X11 apps")
	headlessHDR := flags.Bool("headless-hdr", false, "test-only: report HDR on virtual outputs; fall back if Vulkan HDR unavailable")
	timeout := flags.Duration("timeout", 0, "duration before exit (0 disables timeout)")
	debugFlag := flags.String("debug", "", "debug components (comma-separated or all; input-motion logs every pointer motion)")
	configFlag := flags.String("config", "", "config path (empty uses XDG default)")
	session := flags.Bool("session", false, "run as the login session: share WAYLAND_DISPLAY and DISPLAY with systemd and D-Bus user services; notify systemd when ready")
	pprofAddr := flags.String("pprof", "", "serve Go runtime profiles (heap, CPU, trace) on this address, e.g. localhost:6060")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	if *backend != "drm" && *backend != "headless" {
		err := usageError{fmt.Errorf("invalid backend %q", *backend)}
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	if *headlessHDR && *backend != "headless" {
		return usageError{fmt.Errorf("--headless-hdr requires --backend=headless")}
	}
	if *screenshot != "" && *backend != "headless" {
		err := usageError{fmt.Errorf("--screenshot requires --backend=headless")}
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	var sizes [][2]int
	for _, s := range strings.Split(*size, ",") {
		w, h, _, sizeErr := config.ParseMode(strings.TrimSpace(s))
		if sizeErr != nil || w == 0 {
			err := usageError{fmt.Errorf("--size: %w", sizeErr)}
			if sizeErr == nil {
				err = usageError{fmt.Errorf("--size: need WxH, got %q", s)}
			}
			fmt.Fprintln(os.Stderr, err)
			return err
		}
		sizes = append(sizes, [2]int{w, h})
	}
	if *inputPath != "" && *backend != "headless" {
		err := usageError{fmt.Errorf("--input requires --backend=headless")}
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	if *timeout < 0 {
		err := usageError{fmt.Errorf("negative timeout")}
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	if flags.NArg() != 0 {
		err := usageError{fmt.Errorf("unexpected arguments: %v", flags.Args())}
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	if *screenshot != "" {
		if err := os.MkdirAll(*screenshot, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
		}
	}
	path := *configFlag
	var cfgErr error
	var cfg = config.Defaults()
	var warnings []config.Warning
	if path == "" {
		path = config.DefaultPath()
		cfg, warnings, cfgErr = config.LoadDefault()
	} else {
		cfg, warnings, cfgErr = config.Load(path)
	}
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, cfgErr)
		return cfgErr
	}
	var script *os.File
	if *inputPath != "" {
		if *inputPath == "-" {
			script = os.Stdin
		} else {
			var err error
			script, err = os.Open(*inputPath)
			if err != nil {
				return err
			}
			defer script.Close()
		}
	}
	selected := mergeDebug(cfg.Log.Debug, *debugFlag)
	if _, err := logging.ParseDebug(selected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return usageError{err}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, closeLog, err := logging.Open(ctx, cfg.Log.Level, selected)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	if *pprofAddr != "" {
		if err := servePprof(ctx, *pprofAddr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
		}
		log.Info().Str("addr", *pprofAddr).Msg("pprof")
	}
	start := time.Now()
	log.Info().Strs("args", os.Args[1:]).Str("backend", *backend).Str("tty", os.Getenv("XDG_VTNR")).Str("session_type", os.Getenv("XDG_SESSION_TYPE")).Msg("run")
	configLog := logging.For(ctx, "config")
	configLog.Info().Str("path", path).Msg("loaded config")
	for _, w := range warnings {
		configLog.Warn().Int("line", w.Line).Msg(w.Msg)
	}
	err = app.Run(ctx, app.Options{Backend: *backend, Config: cfg, ConfigPath: path, Timeout: *timeout, NoTerminal: *noTerminal, NoXwayland: *noXwayland, HeadlessHDR: *headlessHDR, Session: *session, ScreenshotDir: *screenshot, Sizes: sizes, Script: script})
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
	case *timeout > 0 && time.Since(start) >= *timeout:
		reason = "timeout"
	}
	log.Info().Str("reason", reason).AnErr("error", err).Dur("uptime", time.Since(start)).Msg("exit")
	return err
}

// runState prints the session state for scripts:
//
//	neferwl state                 the whole state
//	neferwl state output-of <pid> the output showing that process's window
//
// The file is $NEFERWL_STATE, else the one of $WAYLAND_DISPLAY.
func runState(args []string) error {
	usage := usageError{fmt.Errorf("usage: state [output-of <pid>]")}
	pid := 0
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "output-of":
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			fmt.Fprintln(os.Stderr, usage)
			return usage
		}
		pid = n
	default:
		fmt.Fprintln(os.Stderr, usage)
		return usage
	}
	path := os.Getenv(statefile.Env)
	if path == "" {
		var err error
		if path, err = statefile.Path(os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("WAYLAND_DISPLAY")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
		}
	}
	st, err := statefile.Read(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	var out any = st
	if pid != 0 {
		if out, err = statefile.OutputOf(st, pid); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
