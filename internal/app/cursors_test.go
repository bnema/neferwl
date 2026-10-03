package app

import (
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// idleCursors returns a router whose clock the test drives: now is the
// time, fire runs the idle timer and resets records each re-arm.
func idleCursors(t *testing.T, hideAfter time.Duration) (c *cursors, now *time.Time, fire func(), resets *[]time.Duration) {
	t.Helper()
	at := time.Unix(1000, 0)
	var f func()
	var rs []time.Duration
	clk := portsmocks.NewMockClock(t)
	clk.EXPECT().Now().RunAndReturn(func() time.Time { return at }).Maybe()
	timer := portsmocks.NewMockTimer(t)
	timer.EXPECT().Reset(mock.Anything).RunAndReturn(func(d time.Duration) bool { rs = append(rs, d); return true }).Maybe()
	timer.EXPECT().Stop().Return(true).Maybe()
	clk.EXPECT().AfterFunc(hideAfter, mock.Anything).RunAndReturn(func(_ time.Duration, fn func()) ports.Timer {
		f = fn
		return timer
	}).Maybe()
	return newCursors(clk, hideAfter), &at, func() { f() }, &rs
}

// The cursor hides after the idle delay without motion and shows on the next move.
func TestCursorHidesWhenIdle(t *testing.T) {
	c, now, fire, _ := idleCursors(t, 5*time.Second)
	cur := newMockcursor(t)
	cur.EXPECT().Hide().Once() // registered before the first move
	c.set("A", cur)
	cur.EXPECT().Move(1.0, 2.0).Once()
	c.move("A", 1, 2)

	*now = now.Add(5 * time.Second)
	cur.EXPECT().Hide().Once()
	fire()

	cur.EXPECT().Move(3.0, 4.0).Once()
	c.move("A", 3, 4)
	if !c.armed {
		t.Fatal("move after hide did not re-arm the idle timer")
	}
}

// A timer that fires while the pointer kept moving re-arms for the time left.
func TestCursorIdleTimerRearmsAfterMotion(t *testing.T) {
	c, now, fire, resets := idleCursors(t, 5*time.Second)
	cur := newMockcursor(t)
	cur.EXPECT().Hide().Once()
	c.set("A", cur)
	cur.EXPECT().Move(mock.Anything, mock.Anything).Times(2)
	c.move("A", 1, 1)
	*now = now.Add(3 * time.Second)
	c.move("A", 2, 2)
	*now = now.Add(2 * time.Second)
	fire() // no Hide expected: only 2s since the last move
	if len(*resets) != 1 || (*resets)[0] != 3*time.Second {
		t.Fatalf("re-arm %v, want [3s]", *resets)
	}
}

// Turning the delay off stops a pending hide; 0 never arms a timer.
func TestCursorHideAfterOff(t *testing.T) {
	c, now, fire, _ := idleCursors(t, 5*time.Second)
	cur := newMockcursor(t)
	cur.EXPECT().Hide().Once()
	c.set("A", cur)
	cur.EXPECT().Move(mock.Anything, mock.Anything)
	c.move("A", 1, 1)
	c.setHideAfter(0)
	*now = now.Add(time.Minute)
	fire() // no Hide expected
	c.move("A", 2, 2)
	if c.armed {
		t.Fatal("idle timer armed with hiding off")
	}
}
