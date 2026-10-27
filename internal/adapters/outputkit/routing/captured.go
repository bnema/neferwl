package routing

import (
	"context"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// RouteCaptured demultiplexes capture replies from the outputs: replies whose
// ID carries ports.CaptureIDInternal go to internal, all others to wayland.
// Both sends give way to ctx. A nil internal means no internal consumer is
// wired: internal replies are dropped (logged at debug) instead of blocking
// forever, since nothing would ever receive them. It returns when ctx is done
// or in is closed.
func RouteCaptured(ctx context.Context, in <-chan domain.CaptureDone, wayland, internal chan<- domain.CaptureDone) {
	log := logging.For(ctx, "app")
	for {
		select {
		case <-ctx.Done():
			return
		case done, ok := <-in:
			if !ok {
				return
			}
			dst := wayland
			if done.ID&ports.CaptureIDInternal != 0 {
				if internal == nil {
					log.Debug().Uint64("id", done.ID).Msg("internal capture reply dropped: no consumer")
					continue
				}
				dst = internal
			}
			select {
			case dst <- done:
			case <-ctx.Done():
				return
			}
		}
	}
}
