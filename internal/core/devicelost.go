package core

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

type ReopenLimiter struct {
	Clock ports.Clock
	times []time.Time
}

// Allow admits three attempts in an inclusive 60-second window. An attempt
// exactly 60 seconds old still counts; older attempts expire.
func (l *ReopenLimiter) Allow() bool {
	now := l.Clock.Now()
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

func LostFormats(advertised, current []domain.DMABufFormat) int {
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
