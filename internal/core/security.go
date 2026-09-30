package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// syncSecurity runs only on the owner. The gate, not event selection order,
// establishes the epoch; surface events can only fill in that exact epoch.
func (c *Core) syncSecurity() bool {
	if c.inputActive {
		return false
	}
	if c.ch.Security == nil {
		return false
	}
	state := c.ch.Security.Snapshot()
	if state == c.security {
		return false
	}
	c.security = state
	c.lockSurfaces, c.lockFocus = nil, 0
	c.pressed, c.buttons, c.inputKeys = map[string]bool{}, map[uint32]bool{}, map[string]bool{}
	c.pointer, c.grab, c.pointerOutput = 0, 0, ""
	c.pointerAt, c.motionMsec = [2]float64{}, 0
	c.keyboard = keyboard{}
	c.constrained, c.constraint = ports.PointerConstrained{}, ports.PointerConstraint{}
	if c.ch.Constraints != nil {
		latest(c.ch.Constraints, c.constraint)
	}
	c.dropSwipe()
	c.swipe = nil
	for _, sc := range c.screens {
		sc.mon.stopSwitch()
		for _, w := range sc.mon.all() {
			w.stopSlide()
		}
	}
	c.stopFrame()
	if state.Protected {
		c.dropCapture()
	}
	return true
}

// Check spawns without adopting a new epoch midway through processing an
// event: commands emitted by that event must retain its original owner epoch.
func (c *Core) inputEpochChanged() bool {
	return c.inputActive && c.ch.Security != nil && c.ch.Security.Snapshot() != c.security
}

func (c *Core) protectionRequested() bool {
	return c.security.Protected || c.ch.Security != nil && c.ch.Security.Snapshot() != c.security
}

func (c *Core) securityCheckpoint(ctx context.Context) error {
	if !c.syncSecurity() {
		return nil
	}
	// Clear display focus even if the new epoch has no mapped lock surfaces.
	if err := c.command(ctx, ports.PointerFocus{}); err != nil {
		return err
	}
	if err := c.command(ctx, ports.FocusWindow{}); err != nil {
		return err
	}
	return c.publish(ctx)
}

func blockedProtectedEvent(ev ports.ClientEvent) bool {
	switch ev.(type) {
	case ports.WindowActivate, ports.WorkspaceActivate, ports.PointerWarp, ports.PointerConstrained,
		ports.PopupRequest, ports.ShortcutsInhibit, ports.WindowFullscreenRequest,
		ports.CaptureSessionBegin, ports.CaptureSessionLayer, ports.CaptureSessionPing:
		return true
	}
	return false
}

func (c *Core) applyLockChanged(v ports.SessionLockChanged) bool {
	if c.ch.Security == nil || v.State != c.security || v.State != c.ch.Security.Snapshot() {
		return false
	}
	if !c.security.Protected {
		return true
	}
	c.lockSurfaces = slices.Clone(v.Surfaces)
	c.lockKeyboardFocus()
	// A destroyed or resized role must not retain a pointer grab.
	if !c.validLockID(c.pointer) {
		c.pointer = 0
	}
	if !c.validLockID(c.grab) {
		c.grab = 0
		clear(c.buttons)
	}
	return true
}

func (c *Core) lockSurface(sc *screen) (ports.LockSurfacePlacement, bool) {
	o := sc.mon.Output()
	for _, s := range c.lockSurfaces {
		if s.ID != 0 && sc.name() != "" && s.Output == sc.name() && s.Width == o.W && s.Height == o.H && o.W > 0 && o.H > 0 {
			return s, true
		}
	}
	return ports.LockSurfacePlacement{}, false
}

func (c *Core) validLockID(id WindowID) bool {
	if id == 0 {
		return false
	}
	for _, sc := range c.screens {
		if s, ok := c.lockSurface(sc); ok && s.ID == id {
			return true
		}
	}
	return false
}

func (c *Core) lockKeyboardFocus() WindowID {
	if c.validLockID(c.lockFocus) {
		return c.lockFocus
	}
	c.lockFocus = 0
	// Prefer the focused output, where the user is looking, then any other.
	if c.focusScreen >= 0 && c.focusScreen < len(c.screens) {
		if s, ok := c.lockSurface(c.screens[c.focusScreen]); ok {
			c.lockFocus = s.ID
			return c.lockFocus
		}
	}
	for _, sc := range c.screens {
		if s, ok := c.lockSurface(sc); ok {
			c.lockFocus = s.ID
			break
		}
	}
	return c.lockFocus
}

func (c *Core) lockHit(x, y float64) (WindowID, float64, float64) {
	o, ok := c.layout().At(x, y)
	if !ok {
		return 0, 0, 0
	}
	i := c.screenIndex(o.Info.Name)
	if i < 0 {
		return 0, 0, 0
	}
	if s, ok := c.lockSurface(c.screens[i]); ok {
		return s.ID, x - float64(o.X), y - float64(o.Y)
	}
	return 0, 0, 0
}

func (c *Core) publishProtected(ctx context.Context) error {
	v := ports.SetOutputs{Outputs: c.layout(), Focused: c.cur().name(), Off: c.offOutputs()}
	if !sameOutputs(v, c.sentOutputs) {
		if err := c.command(ctx, v); err != nil {
			return err
		}
		if c.ch.Layouts != nil {
			latest(c.ch.Layouts, v.Outputs)
		}
		c.sentOutputs = v
	}
	focus := c.lockKeyboardFocus()
	scenes := make([]ports.Scene, 0, len(c.screens))
	for _, sc := range c.screens {
		c.seq++
		o := sc.mon.Output()
		scene := ports.Scene{Security: c.security, Output: sc.name(), Seq: c.seq, OutputWidth: o.W, OutputHeight: o.H, Scale: sc.scale, Off: sc.off, Background: "#000000"}
		if s, ok := c.lockSurface(sc); ok {
			scene.Windows = []ports.SceneWindow{{ID: s.ID, Rect: Rect{W: o.W, H: o.H}, Fullscreen: true, Focused: s.ID == focus}}
		}
		scenes = append(scenes, scene)
	}
	if c.keyboard.sent != focus {
		if err := c.command(ctx, ports.FocusWindow{ID: focus}); err != nil {
			return err
		}
		c.keyboard.sent = focus
	}
	// Surface updates may invalidate the role under a stationary pointer.
	if !c.validLockID(c.pointer) {
		c.pointer = 0
	}
	if err := c.command(ctx, ports.PointerFocus{ID: c.pointer, X: c.pointerAt[0], Y: c.pointerAt[1]}); err != nil {
		return err
	}
	latest(c.ch.Scenes, scenes)
	c.publishState()
	c.publishWorkspaces()
	return nil
}

func (c *Core) admitInput(ev ports.InputEvent) (ports.InputEvent, bool) {
	if v, ok := ev.(ports.SecurityInput); ok {
		if v.State != c.security || v.Event == nil {
			return nil, false
		}
		ev = v.Event
	} else if c.ch.Security != nil || c.security != (ports.SecurityState{}) {
		return nil, false
	}
	// The producer resets native keymap state and quarantines physically held
	// keys on transitions. Core preserves that keymap-aware modifier state;
	// it must not infer modifier meaning from evdev codes or XKB bit positions.
	// No release without a press in this epoch (password keys must not reach
	// the desktop after unlock). Standalone legacy channel tests remain raw.
	if c.ch.Security != nil {
		if b, ok := ev.(ports.PointerButton); ok && !b.Pressed && !c.buttons[b.Button] {
			return nil, false
		}
		if k, ok := ev.(ports.KeyEvent); ok {
			held := heldKey(k)
			if k.Pressed {
				c.inputKeys[held] = true
			} else {
				if !c.inputKeys[held] {
					return nil, false
				}
				delete(c.inputKeys, held)
			}
		}
	}
	return ev, true
}

func (c *Core) protectedInput(ctx context.Context, ev ports.InputEvent) error {
	switch v := ev.(type) {
	case ports.KeyEvent:
		if id := c.lockKeyboardFocus(); id != 0 {
			return c.command(ctx, ports.ForwardKey{ID: id, Key: v})
		}
	case ports.PointerMotion:
		c.motionMsec = v.TimeMsec
		c.cursorX, c.cursorY = c.layout().Clamp(c.cursorX, c.cursorY, v.X, v.Y)
		id, x, y := c.lockHit(c.cursorX, c.cursorY)
		if id != c.pointer {
			c.pointer = id
			if err := c.command(ctx, ports.PointerFocus{ID: id, X: x, Y: y}); err != nil {
				return err
			}
		}
		c.pointerAt = [2]float64{x, y}
		if id != 0 {
			return c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, DX: v.DX, DY: v.DY, UnaccelDX: v.UnaccelDX, UnaccelDY: v.UnaccelDY, TimeMsec: v.TimeMsec, TimeUsec: v.TimeUsec})
		}
	case ports.PointerButton:
		id := c.pointer
		if c.grab != 0 {
			id = c.grab
		}
		if !v.Pressed && !c.buttons[v.Button] {
			return nil
		}
		if v.Pressed {
			if len(c.buttons) == 0 {
				c.grab = id
			}
			c.buttons[v.Button] = true
		} else {
			delete(c.buttons, v.Button)
		}
		if c.validLockID(id) {
			if v.Pressed && c.lockFocus != id {
				c.lockFocus = id
				if err := c.publishProtected(ctx); err != nil {
					return err
				}
			}
			if err := c.command(ctx, ports.PointerButtonTo{ID: id, Button: v.Button, Pressed: v.Pressed, TimeMsec: v.TimeMsec}); err != nil {
				return err
			}
		}
		if len(c.buttons) == 0 {
			c.grab = 0
		}
	case ports.PointerAxis:
		if c.validLockID(c.pointer) {
			return c.command(ctx, ports.PointerAxisTo{ID: c.pointer, Axis: v})
		}
	}
	return nil
}
