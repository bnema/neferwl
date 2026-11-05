package routing

import (
	"context"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// RouteCaptured demultiplexes replies by their reserved ID bits: control
// captures go to control, hint captures to internal, and all others to wayland.
// A nil control or internal sink drops that class rather than blocking.
// Security admission remains in the output capture pipeline; failure replies
// are routed just like successes so consumers can retire their requests.
// Sends give way to ctx. It returns when ctx is done or in is closed.
func RouteCaptured(ctx context.Context, in <-chan domain.CaptureDone, wayland, internal, control chan<- domain.CaptureDone) {
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
			if done.ID&ports.CaptureIDControl != 0 {
				if control == nil {
					log.Debug().Uint64("id", done.ID).Msg("control capture reply dropped: no consumer")
					continue
				}
				dst = control
			} else if done.ID&ports.CaptureIDInternal != 0 {
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
