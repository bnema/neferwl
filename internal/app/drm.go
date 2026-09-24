package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/adapters/drm"
	"github.com/bnema/nefertty/internal/adapters/seat"
	"github.com/bnema/nefertty/internal/adapters/wayland"
	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
)

var errTimeout = errors.New("timeout")

type drmBackend struct {
	seat *seat.Seat
	out  *drm.Output
	fd   int

	inputActive, outputActive <-chan bool
}

// safe turns a panic in a hardware goroutine into an error, so the TTY is still restored.
func safe(name string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s panic: %v\n%s", name, p, debug.Stack())
		}
	}()
	return fn()
}

// openDRM opens the seat and the first card with a connected display.
func openDRM(ctx context.Context, outputs []ports.OutputConfig) (*drmBackend, error) {
	log := logging.For(ctx, "drm")
	s, err := seat.Open(ctx, logging.For(ctx, "seat"))
	if err != nil {
		log.Error().Err(err).Msg("open seat")
		return nil, fmt.Errorf("open seat: %w", err)
	}
	want := wantFromConfig(outputs)
	cards, _ := filepath.Glob("/dev/dri/card[0-9]*")
	sort.Strings(cards)
	var errs []error
	// First pass: the configured output on any card; second: any usable output.
	passes := []drm.Want{want}
	if want.Name != "" {
		strict := want
		strict.Strict = true
		passes = []drm.Want{strict, want}
	}
	for _, w := range passes {
		for _, card := range cards {
			fd, err := s.OpenDevice(card)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out, err := drm.Open(fd, card, w, log)
			if err != nil {
				if !w.Strict {
					log.Warn().Err(err).Str("card", card).Msg("card unusable")
					errs = append(errs, err)
				}
				s.CloseDevice(fd)
				continue
			}
			return &drmBackend{seat: s, out: out, fd: fd}, nil
		}
	}
	s.Close()
	err = fmt.Errorf("no usable DRM card in %v: %w", cards, errors.Join(errs...))
	log.Error().Err(err).Msg("open drm")
	return nil, err
}

// wantFromConfig turns [[output]] entries into a connector choice: the first enabled entry wins.
func wantFromConfig(outputs []ports.OutputConfig) drm.Want {
	want := drm.Want{Disabled: map[string]bool{}}
	for _, o := range outputs {
		if o.Off {
			want.Disabled[o.Name] = true
			continue
		}
		if want.Name != "" {
			continue
		}
		want.Name = o.Name
		if o.Mode != "" {
			// Validated at config load.
			want.W, want.H, want.Hz, _ = config.ParseMode(o.Mode)
		}
	}
	return want
}

// outputInfo describes the chosen output for wl_output and xdg-output.
func (b *drmBackend) outputInfo() wayland.OutputInfo {
	i := b.out.Info()
	return wayland.OutputInfo{
		Name: i.Name, Make: i.Monitor.Make, Model: i.Monitor.Model,
		Description:  strings.TrimSpace(i.Monitor.Make + " " + i.Monitor.Model + " " + i.Monitor.Serial + " (" + i.Name + ")"),
		RefreshMilli: i.RefreshMilli, PhysicalW: i.PhysicalW, PhysicalH: i.PhysicalH,
	}
}

// close restores the CRTC, then releases the card and the seat so the TTY comes back.
func (b *drmBackend) close() {
	b.out.Close()
	b.seat.CloseDevice(b.fd)
	b.seat.Close()
}
