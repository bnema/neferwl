package core

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// PersistScales saves the scales set by scale binds once they settle for
// debounce, so a burst of Cmd+= presses writes the config once, and tells
// the user through notes. It runs until ctx ends; pending scales are then
// dropped.
func PersistScales(ctx context.Context, changes <-chan ports.ScaleChanged, store ports.OutputScaleStore, notes ports.Notifier, clock ports.Clock, debounce time.Duration) {
	pending := map[string]float64{}
	timer := clock.NewTimer(debounce)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case ev := <-changes:
			pending[ev.Output] = ev.Scale
			timer.Reset(debounce)
		case <-timer.C():
			// Sorted: several outputs save and notify in a stable order.
			for _, name := range slices.Sorted(maps.Keys(pending)) {
				scale := pending[name]
				if err := store.SaveOutputScale(name, scale); err != nil {
					notes.Notify(name+" scale not saved", err.Error())
					continue
				}
				notes.Notify(name+" scale saved", fmt.Sprintf("Scale %.4g is kept after restart", scale))
			}
			clear(pending)
		}
	}
}
