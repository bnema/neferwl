// Package clock is the wall clock behind ports.Clock.
package clock

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// System is the real clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

func (System) NewTimer(d time.Duration) ports.Timer { return timer{time.NewTimer(d)} }

func (System) NewTicker(d time.Duration) ports.Ticker { return ticker{time.NewTicker(d)} }

func (System) AfterFunc(d time.Duration, f func()) ports.Timer {
	return timer{time.AfterFunc(d, f)}
}

type timer struct{ t *time.Timer }

func (t timer) C() <-chan time.Time        { return t.t.C }
func (t timer) Stop() bool                 { return t.t.Stop() }
func (t timer) Reset(d time.Duration) bool { return t.t.Reset(d) }

type ticker struct{ t *time.Ticker }

func (t ticker) C() <-chan time.Time { return t.t.C }
func (t ticker) Stop()               { t.t.Stop() }
