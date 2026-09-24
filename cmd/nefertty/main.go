package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/app"
	"github.com/bnema/nefertty/internal/logging"
)

type usageError struct{ error }

func main() { os.Exit(runCode()) }
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
		if len(os.Args) == 3 {
			path = os.Args[2]
		}
		_, warnings, err := config.Load(path)
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
	if len(os.Args) == 2 && os.Args[1] == "version" {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			fmt.Println("unknown")
			return nil
		}
		version := info.Main.Version
		if version == "" {
			version = "(devel)"
		}
		fmt.Println(version)
		return nil
	}
	flags := flag.NewFlagSet("nefertty", flag.ContinueOnError)
	backend := flags.String("backend", "drm", "drm or headless")
	screenshot := flags.String("screenshot", "", "write PNG frames to directory")
	size := flags.String("size", "1920x1080", "headless output size WxH")
	inputPath := flags.String("input", "", "headless input script path (- for stdin)")
	noTerminal := flags.Bool("no-terminal", false, "skip initial terminal")
	timeout := flags.Duration("timeout", 0, "duration before exit (0 disables timeout)")
	debugFlag := flags.String("debug", "", "debug components (comma-separated or all)")
	configFlag := flags.String("config", "", "config path (empty uses XDG default)")
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
	if *screenshot != "" && *backend != "headless" {
		err := usageError{fmt.Errorf("--screenshot requires --backend=headless")}
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	outW, outH, _, sizeErr := config.ParseMode(*size)
	if sizeErr != nil {
		err := usageError{fmt.Errorf("--size: %w", sizeErr)}
		fmt.Fprintln(os.Stderr, err)
		return err
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
	start := time.Now()
	log.Info().Strs("args", os.Args[1:]).Str("backend", *backend).Str("tty", os.Getenv("XDG_VTNR")).Str("session_type", os.Getenv("XDG_SESSION_TYPE")).Msg("run")
	configLog := logging.For(ctx, "config")
	configLog.Info().Str("path", path).Msg("loaded config")
	for _, w := range warnings {
		configLog.Warn().Int("line", w.Line).Msg(w.Msg)
	}
	err = app.Run(ctx, app.Options{Backend: *backend, Config: cfg, ConfigPath: path, Timeout: *timeout, NoTerminal: *noTerminal, ScreenshotDir: *screenshot, Width: outW, Height: outH, Script: script})
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
