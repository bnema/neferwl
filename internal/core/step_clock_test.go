package core_test

import (
	"sync"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// stepClock is a clock the test moves: springs sample it, so a test lands
// them by advancing it and sending page flips, never by sleeping. Its
// timers never fire: frames come from the test.
type stepClock struct {
	mu    sync.Mutex
	now   time.Time
	clock ports.Clock
}

func newStepClock(t *testing.T) *stepClock {
	t.Helper()
	s := &stepClock{now: time.Unix(100, 0)}
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.now
	}).Maybe()
	// One timer built before core runs: a mock created by core while the
	// test ends would race its own cleanup check.
	timer := portsmocks.NewMockTimer(t)
	timer.EXPECT().C().Return(make(chan time.Time)).Maybe()
	timer.EXPECT().Stop().Return(true).Maybe()
	clock.EXPECT().NewTimer(mock.Anything).Return(timer).Maybe()
	s.clock = clock
	return s
}

func (s *stepClock) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

// flip moves the clock by d and sends a page flip for each output, then
// returns the scene set core published for them (ok false: none, so no
// spring ran). Core publishes before it takes its next event, so a flip that
// names no output is a barrier: once its send returns, every earlier event
// is handled and the scene channel holds the result. One is sent first too,
// and a scene left over from earlier events is dropped, so a scene found
// afterwards is the flips' own.
func (s *stepClock) flip(t *testing.T, frames chan ports.OutputFrame, scenes chan []ports.Scene, d time.Duration, outputs ...string) ([]ports.Scene, bool) {
	t.Helper()
	send(frames, ports.OutputFrame{Output: "sync"})
	select {
	case <-scenes:
	default:
	}
	s.advance(d)
	for _, o := range outputs {
		send(frames, ports.OutputFrame{Output: o})
	}
	send(frames, ports.OutputFrame{Output: "sync"})
	select {
	case set := <-scenes:
		return set, true
	default:
		return nil, false
	}
}

// settle is flip past every spring.
func (s *stepClock) settle(t *testing.T, frames chan ports.OutputFrame, scenes chan []ports.Scene, outputs ...string) ([]ports.Scene, bool) {
	t.Helper()
	return s.flip(t, frames, scenes, 5*time.Second, outputs...)
}

// landRig is a multiRig whose settle lands every running spring: with
// animations on it runs on a clock the test moves (swipeRig) and sends the
// page flips; with animations off it is the plain rig and land is the
// identity. Each test body runs once per mode, so the off variant keeps
// its meaning and the on variant asserts the same settled facts.
type landRig struct {
	*multiRig
	land func([]ports.Scene) []ports.Scene
	// lands counts the settles that found a spring running.
	lands *int
}

// startLanding runs core on the given outputs (gaps and terminal policy as
// in startMulti) with animations on or off.
func startLanding(t *testing.T, animated bool, edit func(*ports.Config), outs ...ports.OutputInfo) *landRig {
	t.Helper()
	if !animated {
		r := startMulti(t, func(c *ports.Config) {
			if edit != nil {
				edit(c)
			}
			c.Animations.On = false
		}, outs...)
		return &landRig{multiRig: r, land: func(set []ports.Scene) []ports.Scene { return set }, lands: new(int)}
	}
	defaults := altCmdDefaults()
	r := startSwipeOn(t, func(c *ports.Config) {
		c.Layout.Gaps, c.Terminal.AutoOpen = defaults.Layout.Gaps, defaults.Terminal.AutoOpen
		if edit != nil {
			edit(c)
		}
		c.Animations.On = true
	}, outs...)
	lands := new(int)
	// An on variant that never saw a spring would be the off variant again.
	t.Cleanup(func() {
		if *lands == 0 {
			t.Error("no spring ever ran: the animations-on variant checked nothing")
		}
	})
	return &landRig{multiRig: r.multiRig, lands: lands, land: func(set []ports.Scene) []ports.Scene {
		landed, ok := r.settleAll(t, r.outs...)
		if !ok {
			return set
		}
		*lands++
		return landed
	}}
}

// both runs body with animations off, then on. A rig of the on run fails the
// test when it never settled a running spring (startLanding,
// startAnimatedDragRig).
func both(t *testing.T, body func(t *testing.T, animated bool)) {
	t.Helper()
	t.Run("off", func(t *testing.T) { body(t, false) })
	t.Run("on", func(t *testing.T) { body(t, true) })
}

// keyLanded presses a key and returns the settled scene set.
func (r *landRig) keyLanded(t *testing.T, sym string, mods ports.Mods) []ports.Scene {
	t.Helper()
	return r.land(r.key(t, sym, mods))
}

// mapLanded maps a window and returns the settled scene set.
func (r *landRig) mapLanded(t *testing.T, id ports.WindowID) []ports.Scene {
	t.Helper()
	return r.land(r.mapWindow(t, id))
}
