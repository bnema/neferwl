package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/app"
	"github.com/bnema/nefertty/internal/logging"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "validate-config" {
		if len(os.Args) > 3 {
			err := fmt.Errorf("usage: validate-config [path]")
			fmt.Fprintln(os.Stderr, err)
			return err
		}
		path := config.DefaultPath()
		if len(os.Args) == 3 {
			path = os.Args[2]
		}
		if _, err := config.Load(path); err != nil {
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
	timeout := flags.Duration("timeout", 0, "duration before exit (0 disables timeout)")
	debugFlag := flags.String("debug", "", "debug components (comma-separated or all)")
	configFlag := flags.String("config", "", "config path (empty uses XDG default)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, closeLog, err := logging.Open(ctx, *debugFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	defer closeLog()
	log := logging.For(ctx, "app")
	if *backend != "drm" && *backend != "headless" {
		err := fmt.Errorf("invalid backend %q", *backend)
		log.Error().Err(err).Msg("invalid options")
		return err
	}
	if *timeout < 0 {
		err := fmt.Errorf("negative timeout")
		log.Error().Err(err).Msg("invalid options")
		return err
	}
	if flags.NArg() != 0 {
		err := fmt.Errorf("unexpected arguments: %v", flags.Args())
		log.Error().Err(err).Msg("invalid options")
		return err
	}
	path := *configFlag
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	configLog := logging.For(ctx, "config")
	configLog.Info().Str("path", path).Msg("loaded config")
	if err := app.Run(ctx, app.Options{Backend: *backend, Config: cfg, Timeout: *timeout}); err != nil {
		log.Error().Err(err).Msg("application failed")
		return err
	}
	return nil
}
