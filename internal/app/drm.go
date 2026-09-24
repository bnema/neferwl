package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/bnema/nefertty/internal/adapters/drm"
	"github.com/bnema/nefertty/internal/adapters/seat"
	"github.com/bnema/nefertty/internal/logging"
)

var errTimeout = errors.New("timeout")

type drmBackend struct {
	seat *seat.Seat
	out  *drm.Output
	fd   int
}

// openDRM opens the seat and the first card with a connected display.
func openDRM(ctx context.Context) (*drmBackend, error) {
	log := logging.For(ctx, "drm")
	s, err := seat.Open(ctx, logging.For(ctx, "seat"))
	if err != nil {
		log.Error().Err(err).Msg("open seat")
		return nil, fmt.Errorf("open seat: %w", err)
	}
	cards, _ := filepath.Glob("/dev/dri/card[0-9]*")
	sort.Strings(cards)
	var errs []error
	for _, card := range cards {
		fd, err := s.OpenDevice(card)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out, err := drm.Open(fd, card, log)
		if err != nil {
			log.Warn().Err(err).Str("card", card).Msg("card unusable")
			errs = append(errs, err)
			s.CloseDevice(fd)
			continue
		}
		return &drmBackend{seat: s, out: out, fd: fd}, nil
	}
	s.Close()
	err = fmt.Errorf("no usable DRM card in %v: %w", cards, errors.Join(errs...))
	log.Error().Err(err).Msg("open drm")
	return nil, err
}

// close restores the CRTC, then releases the card and the seat so the TTY comes back.
func (b *drmBackend) close() {
	b.out.Close()
	b.seat.CloseDevice(b.fd)
	b.seat.Close()
}
