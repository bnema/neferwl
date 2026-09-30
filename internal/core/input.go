package core

import (
	"context"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

func (c *Core) handleInput(ctx context.Context, ev ports.InputEvent) error {
	c.inputActive = true
	defer func() { c.inputActive = false }()
	if c.security.Protected {
		if err := c.protectedInput(ctx, ev); err != nil {
			return err
		}
		return nil
	}
	switch v := ev.(type) {
	case ports.PointerMotion:
		c.motionMsec = v.TimeMsec
		// A locked pointer stays still; relative motion still flows.
		if c.constraint.Mode != ports.ConstraintLock {
			c.cursorX, c.cursorY = c.constraint.Clamp(c.layout().Clamp(c.cursorX, c.cursorY, v.X, v.Y))
		}
		// The focused screen follows the pointer, so new windows
		// and launchers open where the user is.
		// Only a pointer entering another output switches: keyboard
		// moves to another screen stick until then. Not mid-drag.
		if o, ok := c.layout().At(c.cursorX, c.cursorY); ok && o.Info.Name != c.pointerOutput && c.grab == 0 {
			c.pointerOutput = o.Info.Name
			if o.Info.Name != c.cur().name() {
				c.focusScreen = c.screenIndex(o.Info.Name)
				if err := c.publish(ctx); err != nil {
					return err
				}
			}
		}
		id, x, y := c.hit(c.cursorX, c.cursorY)
		// Layout changes are intentionally re-hit-tested only on motion.
		if id != c.pointer {
			c.pointer = id
			if err := c.command(ctx, ports.PointerFocus{ID: id, X: x, Y: y}); err != nil {
				return err
			}
		}
		c.pointerAt = [2]float64{x, y}
		if id != 0 {
			if err := c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, DX: v.DX, DY: v.DY, UnaccelDX: v.UnaccelDX, UnaccelDY: v.UnaccelDY, TimeMsec: v.TimeMsec, TimeUsec: v.TimeUsec}); err != nil {
				return err
			}
		}
		return nil
	case ports.PointerButton:
		if v.Pressed && c.pointer == 0 && c.grab == 0 && len(c.buttons) == 0 {
			if picked, err := c.overviewClick(ctx); err != nil {
				return err
			} else if picked {
				return nil
			}
		}
		id := c.pointer
		if c.grab != 0 {
			id = c.grab
		}
		if v.Pressed && len(c.buttons) == 0 {
			// A click outside an open menu closes it.
			if err := c.dismissGrabs(ctx, id); err != nil {
				return err
			}
		}
		if v.Pressed {
			if len(c.buttons) == 0 {
				c.grab = id
			}
			c.buttons[v.Button] = true
		} else {
			delete(c.buttons, v.Button)
		}
		if id != 0 {
			if err := c.command(ctx, ports.PointerButtonTo{ID: id, Button: v.Button, Pressed: v.Pressed, TimeMsec: v.TimeMsec}); err != nil {
				return err
			}
			// A click on an on-demand layer gives it the keyboard.
			if v.Pressed && c.onDemand(id) && c.keyboard.clickLayer(id, c.windowFocus()) {
				if err := c.publish(ctx); err != nil {
					return err
				}
			} else if l := c.keyboard.layer; v.Pressed && l != 0 && id != l && c.popupRoot(id) != l {
				// A click anywhere else takes the keyboard back.
				c.keyboard.takeBack()
				if err := c.publish(ctx); err != nil {
					return err
				}
			}
			// A click focuses the window and its output.
			s, w := c.screenOf(id)
			if v.Pressed && s != nil && w == s.mon.Current() && (c.keyboard.sent != id || s != c.cur()) {
				w.Click(id)
				c.focusScreen = c.screenIndex(s.name())
				if err := c.publish(ctx); err != nil {
					return err
				}
			}
		}
		if len(c.buttons) == 0 {
			c.grab = 0
		}
		return nil
	case ports.PointerAxis:
		// In the overview, scrolling moves the selection.
		if c.cur().mon.ov.open && !c.overviewKeyboardTaken() {
			if c.cur().mon.overviewScroll(v) {
				if err := c.workspaceVisible(ctx, true); err != nil {
					return err
				}
				if err := c.publish(ctx); err != nil {
					return err
				}
			}
			return nil
		}
		// Scroll goes to the window under the pointer, which has the
		// pointer focus even mid-drag.
		if c.pointer != 0 {
			if err := c.command(ctx, ports.PointerAxisTo{ID: c.pointer, Axis: v}); err != nil {
				return err
			}
		}
		return nil
	case ports.SwipeBegin:
		if c.swipeBegin(v) {
			return c.slid(ctx, false)
		}
		return nil
	case ports.SwipeUpdate:
		if c.swipeUpdate(v) {
			return c.slid(ctx, false)
		}
		return nil
	case ports.SwipeEnd:
		return c.slid(ctx, c.swipeEnd(v))
	}
	key, ok := ev.(ports.KeyEvent)
	if !ok {
		return nil
	}
	// The overview takes its keys before any window; others still
	// run binds, and are not forwarded.
	// A launcher or a menu holding the keyboard gets them first.
	if mon := c.cur().mon; mon.ov.open && !c.overviewKeyboardTaken() {
		if key.Pressed && mon.overviewKey(key) {
			c.pressed[heldKey(key)] = true
			if err := c.workspaceVisible(ctx, true); err != nil {
				return err
			}
			if err := c.publish(ctx); err != nil {
				return err
			}
			return nil
		}
	}
	// A focused window inhibiting shortcuts gets every key:
	// no bind runs (emergency quit and VT switch are handled by
	// input before core).
	if c.keyboard.inhibited(c.keyboardFocus()) {
		if held := heldKey(key); !key.Pressed && c.pressed[held] {
			// Its press ran a bind before inhibiting began: the
			// window never saw it.
			delete(c.pressed, held)
			return nil
		}
		if err := c.command(ctx, ports.ForwardKey{ID: c.keyboard.inhibiting, Key: key}); err != nil {
			return err
		}
		return nil
	}
	name := keyName(key.Keysym)
	// A bind on the keysym wins; then the unshifted keysym (Cmd+Shift+1
	// prints exclam on US); then the physical key (code:N).
	action, bound := c.binds[binding{key: name, mods: key.Mods}]
	if !bound && key.Base != "" {
		action, bound = c.binds[binding{key: keyName(key.Base), mods: key.Mods}]
	}
	if !bound && key.Keycode != 0 {
		action, bound = c.binds[binding{key: "code:" + strconv.FormatUint(uint64(key.Keycode), 10), mods: key.Mods}]
	}
	// Track presses by physical key: Shift may be released before the key.
	held := heldKey(key)
	if !key.Pressed {
		consumed := c.pressed[held]
		delete(c.pressed, held)
		if consumed {
			return nil
		}
	}
	if key.Pressed {
		if bound {
			c.pressed[held] = true
			if action == ActionScaleUp || action == ActionScaleDown {
				dir := 1
				if action == ActionScaleDown {
					dir = -1
				}
				sc := c.cur()
				prev := sc.scale
				sc.setScale(StepScale(sc.info.Width, sc.info.Height, sc.scale, dir))
				c.order()
				if c.ch.Scales != nil && sc.scale != prev && sc.name() != "" {
					// Never wait on persistence: a stalled save drops the change.
					select {
					case c.ch.Scales <- ports.ScaleChanged{Output: sc.name(), Scale: sc.scale}:
					default:
					}
				}
				if err := c.publish(ctx); err != nil {
					return err
				}
				return nil
			}
			before := c.cur().mon.Current()
			swiped := c.swipedWorkspace()
			c.keyboard.takeBack() // a bind acts on the windows
			effect := c.applyAction(action)
			if effect.Quit {
				return ErrQuit
			}
			if effect.Spawn {
				argv := effect.Argv
				if argv == nil {
					argv = append([]string(nil), c.cfg.Terminal.Command...)
				}
				// Explicit actions are suppressed on a full launcher queue;
				// never keep the input owner waiting across an epoch change.
				c.trySpawn(ctx, ports.SpawnRequest{Argv: argv})
				if c.inputEpochChanged() {
					return errSecurityChanged
				}
			}
			if c.swipedWorkspace() != swiped {
				c.dropSwipe()
			}
			if err := c.workspaceVisible(ctx, c.cur().mon.Current() != before); err != nil {
				return err
			}
			if effect.Close != 0 {
				if err := c.command(ctx, ports.CloseWindow{ID: effect.Close}); err != nil {
					return err
				}
			}
			if err := c.publish(ctx); err != nil {
				return err
			}
			return nil
		}
		c.pressed[held] = false
	}
	if id := c.keyboardFocus(); id != 0 && (!c.cur().mon.ov.open || c.overviewKeyboardTaken()) {
		if err := c.command(ctx, ports.ForwardKey{ID: id, Key: key}); err != nil {
			return err
		}
	}
	return nil
}
