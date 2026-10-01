package core

import (
	"context"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestProtectedDropsPrivateOffscreenCaptureAndDesktopEffects(t *testing.T) {
	c, ws, commands := hiddenCaptureCommands(t)
	state := ports.SecurityState{Generation: 1, Protected: true}
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state })
	c.ch.Security = gate
	c.captureOpen(ports.CaptureSessionOpen{ID: 7, Workspace: ws.ID})
	c.captureExclusionBegin(ports.CaptureExclusionBegin{Session: 7})
	c.capt.flashes = []capFlash{{target: capTarget{output: "A"}, until: c.now().Add(time.Hour)}}
	c.screens[0].layers = []ports.LayerSurface{{ID: 50, Layer: ports.LayerOverlay, Keyboard: 1}}
	c.pressed["password"] = true
	c.buttons[0x110] = true
	c.pointer, c.grab = 1, 1
	c.keyboard = keyboard{layer: 50, sent: 1, inhibiting: 1}
	c.constrained = ports.PointerConstrained{ID: 1, Mode: ports.ConstraintLock}
	c.swipe = &swipeGesture{}
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	scenes := <-c.ch.Scenes
	s := scenes[0]
	if s.Security != state || s.Capture != nil || s.CaptureScene != nil || len(s.CaptureIndicators) != 0 || len(s.Windows) != 0 || len(s.Layers) != 0 || len(s.Separators) != 0 || s.Background != "#000000" {
		t.Fatalf("unsafe protected scene %+v", s)
	}
	if len(c.capt.sessions) != 0 || c.capt.excl != nil || len(c.capt.flashes) != 0 || c.configures.cw.active() || len(c.pressed) != 0 || len(c.buttons) != 0 || c.pointer != 0 || c.grab != 0 || c.keyboard.layer != 0 || c.constrained.ID != 0 || c.swipe != nil {
		t.Fatal("transition kept desktop input/capture state")
	}
	// Logical dimensions must match exactly, including after output changes.
	c.applyLockChanged(ports.SessionLockChanged{State: state, Surfaces: []ports.LockSurfacePlacement{{ID: 10, Output: "A", Width: 299, Height: 200}}})
	if c.lockKeyboardFocus() != 0 {
		t.Fatal("wrong dimensions accepted")
	}
	c.applyLockChanged(ports.SessionLockChanged{State: state, Surfaces: []ports.LockSurfacePlacement{{ID: 10, Output: "A", Width: 300, Height: 200}}})
	if c.lockKeyboardFocus() != 10 {
		t.Fatal("mapped role not focused")
	}
	for _, ev := range []ports.ClientEvent{ports.WindowActivate{ID: 1}, ports.WorkspaceActivate{}, ports.PointerWarp{ID: 1}, ports.PointerConstrained{ID: 1}, ports.PopupRequest{}, ports.CaptureSessionOpen{}, ports.CaptureFrameTaken{}, ports.CaptureExclusionBegin{}, ports.CaptureExclusionLayer{}} {
		if !blockedProtectedEvent(ev) {
			t.Fatalf("allowed desktop effect %T", ev)
		}
	}
	for len(commands) > 0 {
		if _, ok := (<-commands).(ports.SecurityCommand); !ok {
			t.Fatal("unwrapped protected command")
		}
	}
	// Release is a transition too: password presses, button grabs and
	// keyboard ownership cannot survive it, even with freshly stamped releases.
	c.inputKeys["#30"] = true
	c.buttons[0x110] = true
	c.pointer, c.grab = 10, 10
	c.keyboard = keyboard{sent: 10}
	state = ports.SecurityState{Generation: 2}
	if !c.syncSecurity() || len(c.inputKeys) != 0 || c.grab != 0 || c.keyboard.sent != 0 || len(c.lockSurfaces) != 0 {
		t.Fatal("release did not reset input ownership")
	}
	for _, ev := range []ports.InputEvent{ports.KeyEvent{Keycode: 30}, ports.PointerButton{Button: 0x110}} {
		if _, ok := c.admitInput(ports.SecurityInput{State: state, Event: ev}); ok {
			t.Fatalf("release crossed epoch: %T", ev)
		}
	}
}

func TestWiredZeroEpochRejectsRawInputAndCommandKeepsOwnerEpoch(t *testing.T) {
	c, _, commands := hiddenCaptureCommands(t)
	gate := portsmocks.NewMockSessionSecurity(t)
	next := ports.SecurityState{Generation: 1, Protected: true}
	gate.EXPECT().Snapshot().Return(next)
	c.ch.Security = gate
	if _, ok := c.admitInput(ports.KeyEvent{Pressed: true}); ok {
		t.Fatal("raw wired input admitted")
	}
	if _, ok := c.admitInput(ports.SecurityInput{Event: ports.KeyEvent{Pressed: true}}); !ok {
		t.Fatal("zero epoch wrapped input rejected")
	}
	// Gate changes after selection must not relabel an old command as new.
	if err := c.command(context.Background(), ports.ForwardKey{ID: 1}); err != nil {
		t.Fatal(err)
	}
	v := (<-commands).(ports.SecurityCommand)
	if v.State != (ports.SecurityState{}) {
		t.Fatalf("relabeled stale command: %+v", v)
	}
	c.syncSecurity()
	if _, ok := c.admitInput(ports.SecurityInput{Event: ports.KeyEvent{Pressed: true}}); ok {
		t.Fatal("stale input admitted")
	}
}
