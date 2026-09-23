package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/bnema/nefertty/internal/app"
	"github.com/bnema/nefertty/internal/logging"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
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
	config := flags.String("config", "", "config path (empty uses XDG default later)")
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
	if err := app.Run(ctx, app.Options{Backend: *backend, Config: *config, Timeout: *timeout}); err != nil {
		log.Error().Err(err).Msg("application failed")
		return err
	}
	return nil
}
