package core

import (
	"context"
	"strconv"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// seatChanged handles the seat going inactive or active (a VT switch). The
// input adapter has already reset its keymap and ended its gestures. An
// inactive seat drops everything derived from the key and button stream, so
// no key, modifier, repeat, drag or grab survives into the resumed session.
// The focused output and the pointer output are left alone: the session
// returns where it was. An active seat needs nothing: the first motion
// re-hits the pointer as usual.
func (c *Core) seatChanged(ctx context.Context, v ports.SeatChanged) error {
	if v.Active {
		return nil
	}
	c.traceInput(domain.SeatInputReset{Keys: c.heldKeyCount(), Mods: modCount(c.mods), Buttons: len(c.buttons)})
	if c.security.Protected {
		// Under a lock the desktop must not be published or its windows
		// touched: only the key and button state goes.
		c.stopRepeat()
		c.pressed, c.inputKeys = map[string]bool{}, map[string]bool{}
		c.buttons, c.swallow, c.grab = map[uint32]bool{}, map[uint32]bool{}, 0
		c.mods, c.keyState = 0, v.State
		// The lock client loses the pointer too, so a button held across
		// the switch does not stay pressed in it.
		c.pointer, c.pointerAt = 0, [2]float64{}
		if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
			return err
		}
		return c.command(ctx, ports.KeyboardReset{State: v.State})
	}
	c.stopRepeat()
	if c.switching() {
		c.cancelSwitcher()
	}
	if err := c.gestureAbort(ctx, 0); err != nil {
		return err
	}
	c.dropSwipe()
	c.swipe = nil
	if c.pgrab != nil {
		c.pgrab.cancel()
		if err := c.abortPointerGrab(ctx); err != nil {
			return err
		}
	}
	if c.drag != nil {
		c.cancelDrag()
	}
	if c.dataDrag != nil {
		c.abortDataDrag()
		if err := c.command(ctx, ports.DataDragCancel{}); err != nil {
			return err
		}
	}
	c.osdCancel()
	c.hintCancel()
	c.pressed, c.inputKeys = map[string]bool{}, map[string]bool{}
	c.buttons, c.swallow, c.grab = map[uint32]bool{}, map[uint32]bool{}, 0
	c.mods, c.keyState = 0, v.State
	c.pointer, c.pointerAt = 0, [2]float64{}
	if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
		return err
	}
	if err := c.command(ctx, ports.KeyboardReset{State: v.State}); err != nil {
		return err
	}
	return c.publish(ctx)
}

// heldKeyCount counts the distinct keys tracked, by identity: pressed and
// inputKeys name the same keys in secure mode.
func (c *Core) heldKeyCount() int {
	n := len(c.pressed)
	for k := range c.inputKeys {
		if _, ok := c.pressed[k]; !ok {
			n++
		}
	}
	return n
}

func modCount(m domain.Mods) int {
	n := 0
	for ; m != 0; m &= m - 1 {
		n++
	}
	return n
}

// comboOf names the key combination of a bound key press for the bind trace.
func (c *Core) comboOf(key ports.KeyEvent) string {
	var b strings.Builder
	for _, m := range [...]struct {
		flag domain.Mods
		name string
	}{{domain.ModSuper, "super"}, {domain.ModCtrl, "ctrl"}, {domain.ModAlt, "alt"}, {domain.ModShift, "shift"}} {
		if key.Mods&m.flag != 0 {
			b.WriteString(m.name + "+")
		}
	}
	b.WriteString(keyName(key.Keysym))
	return b.String()
}

// bindMark is the focused output and its workspace before an action.
type bindMark struct {
	sc  *screen
	ws  int
	out string
}

func (c *Core) markBind() bindMark {
	if c.ch.Binds == nil || c.focusScreen >= len(c.screens) {
		return bindMark{}
	}
	sc := c.cur()
	return bindMark{sc: sc, ws: sc.mon.Active + 1, out: sc.name()}
}

// emitBind sends the trace of an action started at m.
func (c *Core) emitBind(m bindMark, combo, action string, source domain.BindSource) {
	if m.sc == nil {
		return
	}
	after := ""
	if c.focusScreen < len(c.screens) {
		after = c.cur().name()
	}
	c.traceInput(domain.BindDispatched{Combo: combo, Action: action, Output: m.out, OutputAfter: after,
		WorkspaceBefore: m.ws, WorkspaceAfter: m.sc.mon.Active + 1, Source: source})
}

// traceBind returns the func to defer once the action ran.
func (c *Core) traceBind(action, combo string, source domain.BindSource) func() {
	m := c.markBind()
	return func() { c.emitBind(m, combo, action, source) }
}

// traceInput hands a record to the debug log without waiting.
func (c *Core) traceInput(t domain.InputTrace) {
	if c.ch.Binds == nil {
		return
	}
	select {
	case c.ch.Binds <- t:
	default:
	}
}

func swipeCombo(fingers int) string { return "swipe" + strconv.Itoa(fingers) }
