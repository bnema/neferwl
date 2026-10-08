package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Touchpad gestures the compositor does not use go to the client under the
// pointer (zwp_pointer_gestures_v1): swipes of any finger count but three
// and four, every pinch and every hold. Like scrolling, they follow the
// pointer focus. Three- and four-finger swipes stay with the compositor
// (gesture.go).

// clientGesture is the gesture being forwarded to a client. Input streams
// one gesture at a time. A gesture that begins with no pointer focus, or
// whose focus moved, still takes its stream (on stays set) so the rest of
// it never reaches the compositor or another window.
type clientGesture struct {
	on   bool
	kind ports.GestureKind
	// id is the window that got the begin; 0 once cancelled or when none had
	// the pointer.
	id WindowID
	// at is the device time of the latest event, for a cancellation.
	at time.Duration
}

// forwardsSwipe reports whether the compositor leaves a swipe of that many
// fingers to the client.
func forwardsSwipe(fingers int) bool { return fingers != 3 && fingers != 4 }

// gestureBegin starts forwarding a gesture to the window with the pointer
// focus. One still running (its end was lost) ends cancelled first.
func (c *Core) gestureBegin(ctx context.Context, kind ports.GestureKind, fingers int, at time.Duration) error {
	if err := c.gestureAbort(ctx, at); err != nil {
		return err
	}
	c.gesture = clientGesture{on: true, kind: kind, at: at}
	if c.pointer == 0 || c.drag != nil {
		return nil
	}
	c.gesture.id = c.pointer
	return c.command(ctx, ports.GestureBeginTo{ID: c.pointer, Kind: kind, Fingers: fingers, Time: at})
}

// gestureUpdate moves the forwarded gesture of that kind. A window that
// lost the pointer meanwhile gets its gesture cancelled.
func (c *Core) gestureUpdate(ctx context.Context, u ports.GestureUpdateTo) error {
	g := &c.gesture
	if !g.on || g.kind != u.Kind {
		return nil
	}
	g.at = u.Time
	if g.id == 0 {
		return nil
	}
	if g.id != c.pointer {
		return c.gestureCancel(ctx)
	}
	u.ID = g.id
	return c.command(ctx, u)
}

// gestureEnd ends the forwarded gesture of that kind.
func (c *Core) gestureEnd(ctx context.Context, kind ports.GestureKind, cancelled bool, at time.Duration) error {
	g := c.gesture
	if !g.on || g.kind != kind {
		return nil
	}
	c.gesture = clientGesture{}
	if g.id == 0 {
		return nil
	}
	if at == 0 {
		// A removed device's end has no time: the client's clock must
		// not run backwards.
		at = g.at
	}
	return c.command(ctx, ports.GestureEndTo{ID: g.id, Kind: kind, Cancelled: cancelled || g.id != c.pointer, Time: at})
}

// gestureAbort ends whatever gesture is forwarded, cancelled.
func (c *Core) gestureAbort(ctx context.Context, at time.Duration) error {
	if !c.gesture.on {
		return nil
	}
	return c.gestureEnd(ctx, c.gesture.kind, true, at)
}

// gestureCancel ends the gesture of the window that lost the pointer, as
// cancelled. The rest of the device's gesture is swallowed.
func (c *Core) gestureCancel(ctx context.Context) error {
	g := &c.gesture
	if !g.on || g.id == 0 {
		return nil
	}
	id := g.id
	g.id = 0
	return c.command(ctx, ports.GestureEndTo{ID: id, Kind: g.kind, Cancelled: true, Time: g.at})
}

// pointerFocus sends a pointer focus change; the window leaving the
// pointer first gets its gesture cancelled, so its client sees the end
// before the leave.
func (c *Core) pointerFocus(ctx context.Context, f ports.PointerFocus) error {
	if c.gesture.id != 0 && c.gesture.id != f.ID {
		if err := c.gestureCancel(ctx); err != nil {
			return err
		}
	}
	return c.command(ctx, f)
}
