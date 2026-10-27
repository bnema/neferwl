package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// A held key repeats the moves it runs in the compositor: a navigation bind
// (focus, move, resize) or an overview key (h/j/k/l, arrows). Clients repeat
// their own keys; these never reach a client. The pace is the compositor's,
// slower than typing so each step can be followed: the first repeat after
// repeatDelay, then one every repeatInterval. With animations on, each step
// retargets the running motion, so a held key slides on smoothly.
const (
	repeatDelay    = 250 * time.Millisecond
	repeatInterval = 125 * time.Millisecond
)

// keyRepeat is the held key that repeats, if any.
type keyRepeat struct {
	// held is the physical key (heldKey); mods the modifiers it was
	// pressed with: a change stops the repeat.
	held string
	mods domain.Mods
	// action is the bind it repeats; key the overview key, when action is
	// empty.
	action Action
	key    ports.KeyEvent
	// overview is whether the overview was open at the press: opening or
	// closing it stops the repeat.
	overview bool
	timer    ports.Timer
}

// repeats reports whether a held key repeats bind a: moves only, never an
// action that toggles, opens, closes or spawns.
func repeats(a Action) bool {
	if _, _, ok := ResizeArg(a); ok {
		return true
	}
	switch a {
	case ActionFocusColumnLeft, ActionFocusColumnRight,
		ActionFocusWindowUp, ActionFocusWindowDown,
		ActionMoveColumnLeft, ActionMoveColumnRight,
		ActionMoveWindowUp, ActionMoveWindowDown,
		ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight,
		ActionFocusWorkspacePrev, ActionFocusWorkspaceNext,
		ActionMoveWorkspacePrev, ActionMoveWorkspaceNext,
		ActionMoveColumnToWorkspacePrev, ActionMoveColumnToWorkspaceNext,
		ActionMoveWindowToWorkspacePrev, ActionMoveWindowToWorkspaceNext:
		return true
	}
	return false
}

// overviewRepeats reports whether a held overview key repeats: the moves,
// not Return or Escape.
func overviewRepeats(key ports.KeyEvent) bool {
	switch keyName(key.Keysym) {
	case "h", "j", "k", "l", "Left", "Right", "Up", "Down":
		return true
	}
	return false
}

// startRepeat repeats action, or the overview key when action is empty,
// while key stays held with its modifiers.
func (c *Core) startRepeat(key ports.KeyEvent, action Action) {
	c.stopRepeat()
	r := &c.repeat
	r.held, r.mods, r.action, r.key = heldKey(key), key.Mods, action, key
	r.overview = c.cur().mon.ov.open
	r.timer = newPortTimer(c.opts.Clock, repeatDelay)
	c.repeatC = r.timer.C()
}

// stopRepeat ends the repeat, if any.
func (c *Core) stopRepeat() {
	if c.repeat.timer != nil {
		c.repeat.timer.Stop()
	}
	c.repeat, c.repeatC = keyRepeat{}, nil
}

// repeatKey stops the repeat on a key event that ends it: another key
// pressed, the held key released, or the modifiers changed.
func (c *Core) repeatKey(key ports.KeyEvent) {
	r := &c.repeat
	if r.timer == nil {
		return
	}
	if key.Pressed || heldKey(key) == r.held || key.Mods != r.mods {
		c.stopRepeat()
	}
}

// repeatLive reports whether the held key still runs what it ran, and stops
// the repeat when not: the overview opened or closed, or a launcher or a
// menu took the keyboard. A bind also stops when a window inhibits the
// shortcuts; overview keys run before inhibitors, as on a press.
func (c *Core) repeatLive() bool {
	r := c.repeat
	if r.timer == nil {
		return false
	}
	if c.cur().mon.ov.open != r.overview || c.overviewKeyboardTaken() ||
		r.action != "" && c.keyboard.inhibited(c.keyboardFocus()) {
		c.stopRepeat()
		return false
	}
	return true
}

// repeatTick runs one repeat of the held key, if still live, and arms the
// next.
func (c *Core) repeatTick(ctx context.Context) error {
	if !c.repeatLive() {
		return nil
	}
	r, mon := c.repeat, c.cur().mon
	c.inputActive = true
	defer func() { c.inputActive = false }()
	if r.action == "" {
		now := c.now()
		shots := c.snapshot(now)
		mon.overviewKey(r.key)
		c.transition(shots, now)
		if err := c.workspaceVisible(ctx, true); err != nil {
			return err
		}
		c.repeat.timer.Reset(repeatInterval)
		return c.publish(ctx)
	}
	c.repeat.timer.Reset(repeatInterval)
	return c.runBind(ctx, r.action)
}
