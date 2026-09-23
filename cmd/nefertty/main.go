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
		var err error
		if len(os.Args) == 3 {
			path = os.Args[2]
			_, err = config.Load(path)
		} else {
			_, err = config.LoadDefault()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
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
	if path == "" {
		path = config.DefaultPath()
		cfg, cfgErr = config.LoadDefault()
	} else {
		cfg, cfgErr = config.Load(path)
	}
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, cfgErr)
		return cfgErr
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
	configLog := logging.For(ctx, "config")
	configLog.Info().Str("path", path).Msg("loaded config")
	if err := app.Run(ctx, app.Options{Backend: *backend, Config: cfg, Timeout: *timeout, NoTerminal: *noTerminal, ScreenshotDir: *screenshot}); err != nil {
		// SIGINT and SIGTERM cancel the context and are clean exits.
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		log.Error().Err(err).Msg("application failed")
		return err
	}
	return nil
}
