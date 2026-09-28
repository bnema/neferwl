package ports

import "time"

// Clock tells the time and makes timers. Owner goroutines select on timer
// channels in their own loop, so tests control time by firing them.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
	NewTicker(d time.Duration) Ticker
	// AfterFunc calls f in its own goroutine after d, unless stopped.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a time.Timer. A stopped or reset timer never delivers a stale
// value (Go 1.23+ timer semantics).
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Ticker is a time.Ticker.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}
