package app

import (
	"fmt"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

type reopenLimiter struct {
	clock ports.Clock
	times []time.Time
}

// allow admits three attempts in an inclusive 60-second window. An attempt
// exactly 60 seconds old still counts; older attempts expire.
func (l *reopenLimiter) allow() bool {
	now := l.clock.Now()
	cutoff := now.Add(-60 * time.Second)
	kept := l.times[:0]
	for _, at := range l.times {
		if !at.Before(cutoff) {
			kept = append(kept, at)
		}
	}
	l.times = kept
	if len(l.times) >= 3 {
		return false
	}
	l.times = append(l.times, now)
	return true
}

// reopenRender closes the old device once and charges every open attempt.
func reopenRender(opener ports.RenderDeviceOpener, info ports.GPUInfo, old ports.RenderDevice, limiter *reopenLimiter) (ports.RenderDevice, error) {
	if old != nil {
		old.Close()
	}
	var lastErr error
	for limiter.allow() {
		dev, err := opener.Open(info)
		if err == nil {
			return dev, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("render device %s: %w: %w", info.Path, ports.ErrDeviceLost, lastErr)
	}
	return nil, fmt.Errorf("render device %s: %w", info.Path, ports.ErrDeviceLost)
}

func lostFormats(advertised, current []ports.DMABufFormat) int {
	lost := 0
	for _, a := range advertised {
		found := false
		for _, c := range current {
			if a == c {
				found = true
				break
			}
		}
		if !found {
			lost++
		}
	}
	return lost
}
