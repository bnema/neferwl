package wayland

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"github.com/bnema/wlturbo/protocol/core"
)

// This fixture wires real channel ports before starting the display owner.
func inputEpochLockServer(t *testing.T) (*Server, *sessionsecurity.Gate, chan ports.SecurityState, chan ports.ClientEvent, string) {
	t.Helper()
	gate := &sessionsecurity.Gate{}
	changes := make(chan ports.SecurityState, 16)
	events := make(chan ports.ClientEvent, 128)
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, Security: gate, Keymap: keymapText(t)}, Channels{Events: events, SecurityChanges: changes, SecurityEvents: make(chan ports.SecurityBackendEvent, 16)}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("input epoch server did not stop")
		}
	})
	return s, gate, changes, events, dir
}

func inputEpochKeyboard(t *testing.T, c *wlturbo.Display) *core.Keyboard {
	t.Helper()
	seat := core.NewSeat(c.Context())
	g, ok := c.Registry().FindGlobal("wl_seat")
	if !ok {
		t.Fatal("seat missing")
	}
	if err := c.Registry().Bind(g.Name, g.Interface, min(g.Version, 4), seat); err != nil {
		t.Fatal(err)
	}
	keyboard, err := seat.GetKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	return keyboard
}

func inputEpochCommitLocker(t *testing.T, s *Server, c *wlturbo.Display, lockSurface *core.Surface, ack func(uint32) error, onConfigure func(func(uint32, uint32, uint32))) (ports.WindowID, lockConfigure) {
	t.Helper()
	var cfg lockConfigure
	onConfigure(func(serial, w, h uint32) { cfg = lockConfigure{serial: serial, width: int(w), height: int(h)} })
	roundtrip(t, c)
	if cfg.serial == 0 {
		t.Fatal("missing lock configure")
	}
	if err := ack(cfg.serial); err != nil {
		t.Fatal(err)
	}
	lockViewport(t, c, lockSurface.ID(), cfg.width, cfg.height)
	requestProtocol(t, c, lockSurface.ID(), wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, lockSurface.ID(), wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	var id ports.WindowID
	s.display.Do(func() {
		surf := s.surfaceOf(findTestSurface(s, lockSurface.ID()))
		if surf == nil || surf.lock == nil || !surf.lock.mapped {
			t.Error("acknowledged buffered lock role not mapped")
			return
		}
		id = surf.lock.id
	})
	return id, cfg
}

// Marker follows display-owned event emission; no sleeps or polling are
// needed to establish that all earlier SessionLockChanged events were read.
func inputEpochLockEvents(t *testing.T, s *Server, events <-chan ports.ClientEvent) []ports.SessionLockChanged {
	t.Helper()
	const marker ports.WindowID = ^ports.WindowID(0)
	if !s.display.Do(func() { s.emit(ports.WindowAppID{ID: marker, AppID: "input-epoch-marker"}) }) {
		t.Fatal("display stopped")
	}
	deadline := time.After(2 * time.Second)
	var locks []ports.SessionLockChanged
	for {
		select {
		case ev := <-events:
			if v, ok := ev.(ports.WindowAppID); ok && v.ID == marker {
				return locks
			}
			if v, ok := ev.(ports.SessionLockChanged); ok {
				locks = append(locks, v)
			}
		case <-deadline:
			t.Fatal("event marker missing")
			return nil
		}
	}
}

func TestSessionLockInputEpochHeldPasswordClearedBeforeDesktopEnter(t *testing.T) {
	s, gate, changes, events, dir := inputEpochLockServer(t)
	desktop := protocolClient(t, s, dir)
	normal, normalSurface, _ := surfaceMapper(t, desktop, events)()
	keys := inputEpochKeyboard(t, desktop)
	var enters [][]byte
	var enterSurfaces []uint32
	var mods []ports.ModState
	var delivered []uint32
	keys.OnEnter(func(_ uint32, surface uint32, held []byte) {
		enters = append(enters, slices.Clone(held))
		enterSurfaces = append(enterSurfaces, surface)
	})
	keys.OnModifiers(func(_ uint32, d, l, k, g uint32) {
		mods = append(mods, ports.ModState{Depressed: d, Latched: l, Locked: k, Group: g})
	})
	keys.OnKey(func(_, _, key, _ uint32) { delivered = append(delivered, key) })
	roundtrip(t, desktop)
	s.display.Do(func() {
		s.apply(ports.SecurityCommand{State: gate.Snapshot(), Command: ports.FocusWindow{ID: normal.ID}})
	})
	roundtrip(t, desktop)
	enters, enterSurfaces, mods, delivered = nil, nil, nil, nil
	ime := newIMEApp(t, s, dir)
	_, grab := ime.grab(t)
	roundtrip(t, ime.c)
	grab.take()
	locker := protocolClient(t, s, dir)
	lock, locked := lifecycleAcquire(t, locker)
	state := lifecycleTransition(t, changes)
	surf, out := lifecycleSurfaceOutput(t, locker)
	role, err := lock.GetLockSurface(surf, out)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := inputEpochCommitLocker(t, s, locker, surf, role.AckConfigure, role.OnConfigure)
	lockerKeys := inputEpochKeyboard(t, locker)
	var passwordKeys []uint32
	lockerKeys.OnKey(func(_, _, key, _ uint32) { passwordKeys = append(passwordKeys, key) })
	roundtrip(t, locker)
	s.display.Do(func() { s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation}) })
	roundtrip(t, locker)
	if *locked != 1 {
		t.Fatal("lock not confirmed")
	}
	password := ports.KeyEvent{Keycode: 30, Pressed: true, Time: 1 * time.Millisecond, State: ports.ModState{Depressed: 65, Latched: 2, Locked: 16, Group: 1}}
	s.display.Do(func() {
		s.apply(ports.SecurityCommand{State: state, Command: ports.FocusWindow{ID: id}})
		s.apply(ports.SecurityCommand{State: state, Command: ports.ForwardKey{ID: id, Key: password}})
		if !s.seat.heldKeys[30] || s.seat.modState != password.State {
			t.Error("test did not seed held password/native seat modifier state")
		}
	})
	settle(t, locker, ime.c, desktop)
	if !slices.Equal(passwordKeys, []uint32{30}) {
		t.Fatalf("locker key events %v", passwordKeys)
	}
	if len(delivered) != 0 {
		t.Fatalf("locker password reached desktop before release: %v", delivered)
	}
	for _, event := range grab.take() {
		if strings.HasPrefix(event, "key ") {
			t.Fatalf("password reached IME: %s", event)
		}
	}
	if err := lock.UnlockAndDestroy(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, locker)
	released := lifecycleTransition(t, changes)
	if released.Protected || released == state {
		t.Fatal(released)
	}
	s.display.Do(func() {
		if len(s.seat.heldKeys) != 0 || len(s.seat.grabKeys) != 0 || s.seat.modState != (ports.ModState{}) {
			t.Error("release retained password input state")
		}
		s.apply(ports.SecurityCommand{State: state, Command: ports.FocusWindow{ID: normal.ID}})
		s.apply(ports.FocusWindow{ID: normal.ID})
		if s.seat.focused != 0 {
			t.Error("stale or raw focus crossed release")
		}
		s.apply(ports.SecurityCommand{State: released, Command: ports.FocusWindow{ID: normal.ID}})
		// Old envelopes aimed at the newly focused desktop are still rejected.
		s.apply(ports.SecurityCommand{State: state, Command: ports.ForwardKey{ID: normal.ID, Key: password}})
		password.Pressed = false
		s.apply(ports.SecurityCommand{State: state, Command: ports.ForwardKey{ID: normal.ID, Key: password}})
		s.apply(ports.ForwardKey{ID: normal.ID, Key: password})
	})
	settle(t, desktop, locker, ime.c)
	if len(delivered) != 0 {
		t.Fatalf("password/stale key reached desktop: %v", delivered)
	}
	if len(enters) != 1 || enterSurfaces[0] != normalSurface || len(enters[0]) != 0 {
		t.Fatalf("desktop enter surfaces=%v held=%v", enterSurfaces, enters)
	}
	if len(mods) == 0 || mods[len(mods)-1] != (ports.ModState{}) {
		t.Fatalf("desktop modifiers not reset: %+v", mods)
	}
	for _, event := range grab.take() {
		if strings.HasPrefix(event, "key ") {
			t.Fatalf("stale password reached IME after release: %s", event)
		}
	}
}

func TestSessionLockInputEpochMappedRolePublishedOnlyAfterEveryProof(t *testing.T) {
	s, _, changes, events, dir := inputEpochLockServer(t)
	c := protocolClient(t, s, dir)
	s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 11, Output: "HEADLESS-1"})
		s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 12, Output: "HEADLESS-2"})
	})
	lock, locked := lifecycleAcquire(t, c)
	state := lifecycleTransition(t, changes)
	surf, out := lifecycleSurfaceOutput(t, c)
	role, err := lock.GetLockSurface(surf, out)
	if err != nil {
		t.Fatal(err)
	}
	id, cfg := inputEpochCommitLocker(t, s, c, surf, role.AckConfigure, role.OnConfigure)
	before := inputEpochLockEvents(t, s, events)
	if len(before) == 0 {
		t.Fatal("no core protection event")
	}
	for _, ev := range before {
		if ev.State != state || len(ev.Surfaces) != 0 {
			t.Fatalf("preconfirm placement leaked %+v", ev)
		}
	}
	s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation})
		lifecycleProof(s, state, 11)
	})
	roundtrip(t, c)
	if *locked != 0 {
		t.Fatal("locked without second output proof")
	}
	for _, ev := range inputEpochLockEvents(t, s, events) {
		if len(ev.Surfaces) != 0 {
			t.Fatalf("one-proof placement leaked %+v", ev)
		}
	}
	s.display.Do(func() { lifecycleProof(s, state, 12) })
	roundtrip(t, c)
	if *locked != 1 {
		t.Fatalf("locked callbacks %d", *locked)
	}
	after := inputEpochLockEvents(t, s, events)
	if cfg.width != testOutputs[0].Width || cfg.height != testOutputs[0].Height {
		t.Fatalf("lock configure %+v does not match output %+v", cfg, testOutputs[0])
	}
	want := ports.LockSurfacePlacement{ID: id, Output: "HEADLESS-1", Width: cfg.width, Height: cfg.height}
	if len(after) != 1 || after[0].State != state || !slices.Equal(after[0].Surfaces, []ports.LockSurfacePlacement{want}) {
		t.Fatalf("validated placement events %+v want %+v", after, want)
	}
}
