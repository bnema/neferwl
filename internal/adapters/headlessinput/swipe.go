package headlessinput

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// A scripted swipe is a quick flick, as libinput reports one: swipeSteps
// updates of swipeStep touchpad units, one every swipeEvery.
const (
	swipeSteps = 10
	swipeStep  = 40.0
	swipeEvery = 8 * time.Millisecond
)

// swipeEvents is a three-finger swipe toward (dx, dy), a unit direction,
// starting at the device time start.
func swipeEvents(dx, dy float64, start time.Duration) []ports.InputEvent {
	evs := []ports.InputEvent{ports.SwipeBegin{Time: start}}
	at := start
	for range swipeSteps {
		at += swipeEvery
		evs = append(evs, ports.SwipeUpdate{DX: dx * swipeStep, DY: dy * swipeStep, Time: at})
	}
	return append(evs, ports.SwipeEnd{Time: at})
}

// monotonic is CLOCK_MONOTONIC, the clock of libinput's timestamps.
func monotonic() time.Duration {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return time.Duration(ts.Nano())
}

func msec(d time.Duration) uint32 { return uint32(d / time.Millisecond) }
