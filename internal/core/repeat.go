package core

import (
	"context"
	"time"

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
	mods ports.Mods
	// action is the bind it repeats; key the overview key, when action is
	// empty.
	action Action
	key    ports.KeyEvent
	timer  ports.Timer
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

// repeatTick runs one repeat of the held key and arms the next. It stops
// when the key can no longer run what it ran: the overview closed or a
// launcher took the keyboard, or a window inhibits the shortcuts.
func (c *Core) repeatTick(ctx context.Context) error {
	r := c.repeat
	if r.timer == nil {
		return nil
	}
	c.inputActive = true
	defer func() { c.inputActive = false }()
	if r.action == "" {
		mon := c.cur().mon
		if !mon.ov.open || c.overviewKeyboardTaken() {
			c.stopRepeat()
			return nil
		}
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
	if c.keyboard.inhibited(c.keyboardFocus()) {
		c.stopRepeat()
		return nil
	}
	c.repeat.timer.Reset(repeatInterval)
	return c.runBind(ctx, r.action)
}
