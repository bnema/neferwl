package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// lockState is the session lock's surfaces and keyboard focus. A new
// security epoch resets it whole.
type lockState struct {
	surfaces []ports.LockSurfacePlacement
	focus    WindowID
	pinned   bool // focus was clicked or typed into: it keeps focus
}

// syncSecurity runs only on the owner. The gate, not event selection order,
// establishes the epoch; surface events can only fill in that exact epoch.
func (c *Core) syncSecurity() bool {
	if c.inputActive {
		return false
	}
	if c.opts.Security == nil {
		return false
	}
	state := c.opts.Security.Snapshot()
	if state == c.security {
		return false
	}
	c.security = state
	c.lock = lockState{}
	c.pressed, c.buttons, c.inputKeys = map[string]bool{}, map[uint32]bool{}, map[string]bool{}
	c.pointer, c.grab, c.pointerOutput = 0, 0, ""
	c.drag, c.swallow, c.mods, c.lastButton = nil, map[uint32]bool{}, 0, 0
	c.pointerAt, c.motionTime = [2]float64{}, 0
	c.keyboard = keyboard{}
	c.constrained, c.constraint = ports.PointerConstrained{}, ports.PointerConstraint{}
	if c.ch.Constraints != nil {
		latest(c.ch.Constraints, c.constraint)
	}
	c.dropSwipe()
	c.swipe = nil
	// The gesture ends with the pointer focus: wayland cancels it when the
	// protected epoch clears that focus.
	c.gesture = clientGesture{}
	c.stopAnimations()
	c.stopPulse()
	if state.Protected {
		c.dropCaptureSessions()
	}
	return true
}

// Check spawns without adopting a new epoch midway through processing an
// event: commands emitted by that event must retain its original owner epoch.
func (c *Core) inputEpochChanged() bool {
	return c.inputActive && c.opts.Security != nil && c.opts.Security.Snapshot() != c.security
}

func (c *Core) protectionRequested() bool {
	return c.security.Protected || c.opts.Security != nil && c.opts.Security.Snapshot() != c.security
}

func (c *Core) securityCheckpoint(ctx context.Context) error {
	if !c.syncSecurity() {
		return nil
	}
	// Clear display focus even if the new epoch has no mapped lock surfaces.
	if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
		return err
	}
	if err := c.command(ctx, ports.FocusWindow{}); err != nil {
		return err
	}
	return c.publish(ctx)
}

// blockProtected reports whether a protected session drops ev. A dropped
// fullscreen request is still answered, at the first publish after unlock.
func (c *Core) blockProtected(ev ports.ClientEvent) bool {
	if !c.security.Protected || !blockedProtectedEvent(ev) {
		return false
	}
	if v, ok := ev.(ports.WindowFullscreenRequest); ok {
		c.configures.answer[v.ID] = true
	}
	return true
}

func blockedProtectedEvent(ev ports.ClientEvent) bool {
	switch ev.(type) {
	case ports.WindowActivate, ports.WorkspaceActivate, ports.PointerWarp, ports.PointerConstrained,
		ports.PopupRequest, ports.ShortcutsInhibit, ports.WindowFullscreenRequest,
		ports.CaptureSessionOpen, ports.CaptureFrameTaken, ports.CaptureExclusionBegin, ports.CaptureExclusionLayer,
		ports.WindowMoveRequest:
		return true
	}
	return false
}

func (c *Core) applyLockChanged(v ports.SessionLockChanged) bool {
	if c.opts.Security == nil || v.State != c.security || v.State != c.opts.Security.Snapshot() {
		return false
	}
	if !c.security.Protected {
		return true
	}
	c.lock.surfaces = slices.Clone(v.Surfaces)
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
	for _, s := range c.lock.surfaces {
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

// lockKeyboardFocus prefers the lock surface of the focused output, where the
// user is looking, then any other. A surface the user clicked or typed into is
// pinned and keeps focus. Until then, focus moves to the focused output's
// surface once it maps: lock clients map their surfaces one by one, in any
// order.
func (c *Core) lockKeyboardFocus() WindowID {
	valid := c.validLockID(c.lock.focus)
	if valid && c.lock.pinned {
		return c.lock.focus
	}
	if c.focusScreen >= 0 && c.focusScreen < len(c.screens) {
		if s, ok := c.lockSurface(c.screens[c.focusScreen]); ok {
			c.lock.focus, c.lock.pinned = s.ID, false
			return c.lock.focus
		}
	}
	if valid {
		return c.lock.focus
	}
	c.lock.focus, c.lock.pinned = 0, false
	for _, sc := range c.screens {
		if s, ok := c.lockSurface(sc); ok {
			c.lock.focus = s.ID
			break
		}
	}
	return c.lock.focus
}

func (c *Core) lockHit(x, y float64) (WindowID, float64, float64) {
	i := c.screenAt(x, y)
	if i < 0 {
		return 0, 0, 0
	}
	sc := c.screens[i]
	if s, ok := c.lockSurface(sc); ok {
		return s.ID, x - float64(sc.x), y - float64(sc.y)
	}
	return 0, 0, 0
}

func (c *Core) publishProtected(ctx context.Context) error {
	if err := c.syncOutputs(ctx); err != nil {
		return err
	}
	focus := c.lockKeyboardFocus()
	scenes := make([]ports.Scene, 0, len(c.screens))
	for _, sc := range c.screens {
		c.seq++
		// The desktop scene that follows must not reuse a Seq from before.
		sc.last = ports.Scene{}
		sc.settledLayout, sc.shown = nil, nil
		o := sc.mon.Output()
		scene := ports.Scene{Security: c.security, Output: sc.name(), Seq: c.seq, OutputWidth: o.W, OutputHeight: o.H, Scale: sc.scale, Transform: sc.transform, Off: sc.off, Background: "#000000"}
		if s, ok := c.lockSurface(sc); ok {
			scene.Windows = []ports.SceneWindow{{ID: s.ID, Rect: Rect{W: o.W, H: o.H}, Fullscreen: true, Focused: s.ID == focus}}
		}
		scenes = append(scenes, scene)
	}
	if err := c.syncFocus(ctx, focus); err != nil {
		return err
	}
	// Surface updates may invalidate the role under a stationary pointer.
	if !c.validLockID(c.pointer) {
		c.pointer = 0
	}
	if err := c.pointerFocus(ctx, ports.PointerFocus{ID: c.pointer, X: c.pointerAt[0], Y: c.pointerAt[1]}); err != nil {
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
	} else if c.opts.Security != nil || c.security != (ports.SecurityState{}) {
		return nil, false
	}
	// The producer resets native keymap state and quarantines physically held
	// keys on transitions. Core preserves that keymap-aware modifier state;
	// it must not infer modifier meaning from evdev codes or XKB bit positions.
	// No release without a press in this epoch (password keys must not reach
	// the desktop after unlock). Standalone legacy channel tests remain raw.
	if c.opts.Security != nil {
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
			c.lock.pinned = true // typing must not move focus mid-secret
			return c.command(ctx, ports.ForwardKey{ID: id, Key: v})
		}
	case ports.PointerMotion:
		c.motionTime = v.Time
		c.cursorX, c.cursorY = c.clampPointer(c.cursorX, c.cursorY, v.X, v.Y)
		id, x, y := c.lockHit(c.cursorX, c.cursorY)
		if id != c.pointer {
			c.pointer = id
			if err := c.pointerFocus(ctx, ports.PointerFocus{ID: id, X: x, Y: y}); err != nil {
				return err
			}
		}
		c.pointerAt = [2]float64{x, y}
		if id != 0 {
			return c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, DX: v.DX, DY: v.DY, UnaccelDX: v.UnaccelDX, UnaccelDY: v.UnaccelDY, Time: v.Time})
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
			if v.Pressed {
				changed := c.lock.focus != id
				c.lock.focus, c.lock.pinned = id, true
				if changed {
					if err := c.publishProtected(ctx); err != nil {
						return err
					}
				}
			}
			if err := c.command(ctx, ports.PointerButtonTo{ID: id, Button: v.Button, Pressed: v.Pressed, Time: v.Time}); err != nil {
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
