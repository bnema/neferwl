package app

import (
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

const testHideAfter = 5 * time.Second

// idleRig is a cursor router on a clock the test drives. The first arm
// creates the timer through AfterFunc (exactly once); later arms Reset it.
type idleRig struct {
	c     *cursors
	timer *portsmocks.MockTimer
	cur   *mockcursor
	now   time.Time
	fire  func()
}

func newIdleRig(t *testing.T) *idleRig {
	t.Helper()
	r := &idleRig{now: time.Unix(1000, 0), timer: portsmocks.NewMockTimer(t)}
	clk := portsmocks.NewMockClock(t)
	clk.EXPECT().Now().RunAndReturn(func() time.Time { return r.now }).Maybe()
	clk.EXPECT().AfterFunc(testHideAfter, mock.Anything).RunAndReturn(func(_ time.Duration, f func()) ports.Timer {
		r.fire = f
		return r.timer
	}).Once()
	r.c = newCursors(clk, testHideAfter)
	r.cur = newMockcursor(t)
	r.cur.EXPECT().Hide().Once() // a cursor registered before the first move is hidden
	r.c.set("A", r.cur)
	r.cur.EXPECT().Move(1.0, 1.0).Once()
	r.c.move("A", 1, 1, true)
	return r
}

// idleHide lets the delay pass and expects the cursor to hide.
func (r *idleRig) idleHide() {
	r.now = r.now.Add(testHideAfter)
	r.cur.EXPECT().Hide().Once()
	r.fire()
}

func TestCursorHidesWhenIdleAndShowsOnMotion(t *testing.T) {
	r := newIdleRig(t)
	r.idleHide()
	r.cur.EXPECT().Move(2.0, 2.0).Once()
	r.timer.EXPECT().Reset(testHideAfter).Return(false).Once()
	r.c.move("A", 2, 2, true)
}

// A timer that fires while the pointer kept moving waits for the time left.
func TestCursorIdleTimerWaitsForTimeLeft(t *testing.T) {
	r := newIdleRig(t)
	r.now = r.now.Add(3 * time.Second)
	r.cur.EXPECT().Move(2.0, 2.0).Once()
	r.c.move("A", 2, 2, true)
	r.now = r.now.Add(2 * time.Second)
	r.timer.EXPECT().Reset(3 * time.Second).Return(false).Once()
	r.fire() // no Hide: only 2s since the last motion
}

// Placing the pointer again after a layout or constraint change is not
// motion: it moves a visible cursor without delaying its hide, and never
// shows a hidden one.
func TestCursorResyncIsNotMotion(t *testing.T) {
	r := newIdleRig(t)
	r.now = r.now.Add(3 * time.Second)
	r.cur.EXPECT().Move(2.0, 2.0).Once()
	r.c.move("A", 2, 2, false)
	r.idleHide() // 5s after the last motion, not 2s after the resync
	r.c.move("A", 3, 3, false)
}

// An output that restarts while the cursor is idle-hidden keeps it hidden.
func TestCursorStaysHiddenOnOutputRestart(t *testing.T) {
	r := newIdleRig(t)
	r.idleHide()
	next := newMockcursor(t)
	next.EXPECT().Hide().Once()
	r.c.set("A", next)
}

func TestCursorHideAfterReload(t *testing.T) {
	t.Run("visible", func(t *testing.T) {
		r := newIdleRig(t)
		r.timer.EXPECT().Stop().Return(true).Once()
		r.timer.EXPECT().Reset(10 * time.Second).Return(false).Once()
		r.c.setHideAfter(10 * time.Second)
	})
	t.Run("hidden", func(t *testing.T) {
		r := newIdleRig(t)
		r.idleHide()
		r.timer.EXPECT().Stop().Return(false).Once()
		r.c.setHideAfter(10 * time.Second) // no re-arm: nothing to hide
		next := newMockcursor(t)
		next.EXPECT().Hide().Once()
		r.c.set("A", next)
	})
	t.Run("off", func(t *testing.T) {
		r := newIdleRig(t)
		r.timer.EXPECT().Stop().Return(true).Once()
		r.c.setHideAfter(0)
		r.now = r.now.Add(time.Minute)
		r.fire() // a fire already under way hides nothing
		r.cur.EXPECT().Move(2.0, 2.0).Once()
		r.c.move("A", 2, 2, true) // and motion arms no timer
	})
}

func TestCursorStopCancelsPendingHide(t *testing.T) {
	r := newIdleRig(t)
	r.timer.EXPECT().Stop().Return(true).Once()
	r.c.stop()
	r.now = r.now.Add(testHideAfter)
	r.fire() // no Hide
}
