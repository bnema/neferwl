package app

import (
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
