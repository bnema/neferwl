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

// settle moves the clock past every spring, flips each output and returns
// the set core published for them (ok false: no spring ran, so nothing
// moved). The last flip names no output: core takes it only after it
// handled the previous ones, so the scene channel then holds their result.
func (s *stepClock) settle(t *testing.T, frames chan ports.OutputFrame, scenes chan []ports.Scene, outputs ...string) ([]ports.Scene, bool) {
	t.Helper()
	s.advance(5 * time.Second)
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

// landRig is a multiRig whose settle lands every running spring: with
// animations on it runs on a clock the test moves (swipeRig) and sends the
// page flips; with animations off it is the plain rig and land is the
// identity. Each test body runs once per mode, so the off variant keeps
// its meaning and the on variant asserts the same settled facts.
type landRig struct {
	*multiRig
	land func([]ports.Scene) []ports.Scene
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
		return &landRig{multiRig: r, land: func(set []ports.Scene) []ports.Scene { return set }}
	}
	defaults := altCmdDefaults()
	r := startSwipeOn(t, func(c *ports.Config) {
		c.Layout.Gaps, c.Terminal.AutoOpen = defaults.Layout.Gaps, defaults.Terminal.AutoOpen
		if edit != nil {
			edit(c)
		}
		c.Animations.On = true
	}, outs...)
	return &landRig{multiRig: r.multiRig, land: func(set []ports.Scene) []ports.Scene { return r.landed(t, set) }}
}

// both runs body with animations off, then on.
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
