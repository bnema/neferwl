package wayland

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/drmlease"
	serverlock "github.com/bnema/purego-libwayland/protocol/extsessionlock"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/wlturbo"
	"github.com/bnema/wlturbo/protocol/core"
	lockclient "github.com/bnema/wlturbo/protocol/extsessionlock"
	"golang.org/x/sys/unix"
)

// A real display with capture completion wiring established before Run starts.
func lifecycleLockServer(t *testing.T, leaseRequests ...chan ports.LeaseMessage) (*Server, *sessionsecurity.Gate, chan ports.SecurityState, chan ports.CaptureDone, string) {
	t.Helper()
	gate := &sessionsecurity.Gate{}
	changes := make(chan ports.SecurityState, 16)
	captured := make(chan ports.CaptureDone, 16)
	dir := t.TempDir()
	channels := Channels{SecurityChanges: changes, SecurityEvents: make(chan ports.SecurityBackendEvent, 16), Captured: captured}
	if len(leaseRequests) != 0 {
		channels.LeaseRequests = leaseRequests[0]
	}
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, Security: gate, Keymap: keymapText(t)}, channels, logging.For(context.Background(), "wayland"))
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
			t.Error("lock lifecycle server did not stop")
		}
	})
	return s, gate, changes, captured, dir
}

func lifecycleTransition(t *testing.T, changes <-chan ports.SecurityState) ports.SecurityState {
	t.Helper()
	select {
	case state := <-changes:
		return state
	case <-time.After(2 * time.Second):
		t.Fatal("missing security transition")
		return ports.SecurityState{}
	}
}

func lifecycleAcquire(t *testing.T, c *wlturbo.Display) (*lockclient.ExtSessionLock, *int) {
	t.Helper()
	lock, err := bindLockManager(t, c).Lock()
	if err != nil {
		t.Fatal(err)
	}
	count := new(int)
	lock.OnLocked(func() { *count++ })
	roundtrip(t, c)
	return lock, count
}

func lifecycleProof(s *Server, state ports.SecurityState, instance ports.OutputInstance) {
	s.applySecurityEvent(ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: state.Generation, Instance: instance, Kind: ports.ProtectionProtectedFrame}})
}

func TestSessionLockLifecycleCaptureCompletionAndProofEpoch(t *testing.T) {
	s, gate, changes, captured, dir := lifecycleLockServer(t)
	c := protocolClient(t, s, dir)
	s.display.Do(func() {
		s.captureInflight[77] = struct{}{}
		s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 1, Output: "HEADLESS-1"})
	})
	_, locked := lifecycleAcquire(t, c)
	state := lifecycleTransition(t, changes)
	s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation})
		lifecycleProof(s, ports.SecurityState{Generation: state.Generation + 1}, 1)
		lifecycleProof(s, state, 99) // unregistered output must not satisfy inventory
		lifecycleProof(s, state, 1)
		if s.sessionLock.locked {
			t.Error("capture credit bypassed")
		}
	})
	roundtrip(t, c)
	if *locked != 0 {
		t.Fatal("locked before capture completion")
	}
	// Unknown completion cannot retire an outstanding credit.
	unknown := make(chan struct{})
	s.display.Do(func() { s.captureReplies[88] = func(ports.CaptureDone) { close(unknown) } })
	captured <- ports.CaptureDone{ID: 88}
	select {
	case <-unknown:
	case <-time.After(2 * time.Second):
		t.Fatal("unknown completion not consumed")
	}
	s.display.Do(func() {
		if _, exists := s.captureInflight[77]; !exists || s.sessionLock.locked {
			t.Error("unknown completion retired real credit")
		}
	})
	processed := make(chan struct{})
	s.display.Do(func() { s.captureReplies[77] = func(ports.CaptureDone) { close(processed) } })
	captured <- ports.CaptureDone{ID: 77}
	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("capture completion not consumed")
	}
	roundtrip(t, c)
	if *locked != 1 || !gate.Snapshot().Protected {
		t.Fatalf("completion locked=%d gate=%+v", *locked, gate.Snapshot())
	}
	s.display.Do(func() { lifecycleProof(s, state, 1); s.maybeLocked() })
	roundtrip(t, c)
	if *locked != 1 {
		t.Fatal("locked event repeated")
	}
}

func TestSessionLockLifecycleUnlockProtocolErrors(t *testing.T) {
	for _, scenario := range []string{"unlock before locked", "destroy after locked"} {
		t.Run(scenario, func(t *testing.T) {
			s, gate, changes, _, dir := lockManagerServer(t)
			c := protocolClient(t, s, dir)
			lock, locked := lifecycleAcquire(t, c)
			state := lifecycleTransition(t, changes)
			code := serverlock.ExtSessionLockV1ErrorInvalidUnlock
			id := lock.ID()
			if scenario == "destroy after locked" {
				s.display.Do(func() { s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation}) })
				roundtrip(t, c)
				if *locked != 1 {
					t.Fatal("zero-output backend barrier did not confirm")
				}
				code = serverlock.ExtSessionLockV1ErrorInvalidDestroy
				if err := lock.Destroy(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := lock.UnlockAndDestroy(); err != nil {
					t.Fatal(err)
				}
			}
			expectProtocolError(t, c, id, uint32(code))
			if !gate.Snapshot().Protected {
				t.Fatal("invalid destructor unlocked")
			}
		})
	}
}

func TestSessionLockLifecycleOrphanTakeoverAndValidUnlock(t *testing.T) {
	s, gate, changes, _, dir := lockManagerServer(t)
	c := protocolClient(t, s, dir)
	_, locked := lifecycleAcquire(t, c)
	firstState := lifecycleTransition(t, changes)
	s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 1, Output: "HEADLESS-1"})
		s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: firstState.Generation})
		lifecycleProof(s, firstState, 1)
	})
	roundtrip(t, c)
	if *locked != 1 {
		t.Fatal("first lock not confirmed")
	}
	orphaned := make(chan struct{})
	s.display.Do(func() { r := s.sessionLock.res; old := r.OnDestroy; r.OnDestroy = func() { old(); close(orphaned) } })
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-orphaned:
	case <-time.After(2 * time.Second):
		t.Fatal("owner disconnect not processed")
	}
	s.display.Do(func() {
		if s.sessionLock != nil || !gate.Snapshot().Protected {
			t.Error("orphan released protection")
		}
	})
	next := protocolClient(t, s, dir)
	second, secondLocked := lifecycleAcquire(t, next)
	state := lifecycleTransition(t, changes)
	if state.Generation <= firstState.Generation || !state.Protected {
		t.Fatalf("takeover state %+v", state)
	}
	s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation})
		lifecycleProof(s, firstState, 1)
	})
	roundtrip(t, next)
	if *secondLocked != 0 {
		t.Fatal("stale proof legalized takeover")
	}
	s.display.Do(func() { lifecycleProof(s, state, 1) })
	roundtrip(t, next)
	if *secondLocked != 1 {
		t.Fatal("takeover not confirmed")
	}
	if err := second.UnlockAndDestroy(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, next) // explicit server processing before client exit
	released := lifecycleTransition(t, changes)
	s.display.Do(func() {
		if s.sessionLock != nil || gate.Snapshot().Protected || len(s.seat.heldKeys) != 0 || len(s.seat.grabKeys) != 0 {
			t.Error("valid unlock failed to reset ownership/input")
		}
	})
	if released.Protected || released.Generation <= state.Generation {
		t.Fatalf("unlock transition %+v", released)
	}
}

func lifecycleSurfaceOutput(t *testing.T, c *wlturbo.Display) (*core.Surface, *core.Output) {
	t.Helper()
	comp := core.NewCompositor(c.Context())
	g, ok := c.Registry().FindGlobal("wl_compositor")
	if !ok {
		t.Fatal("compositor missing")
	}
	if err := c.Registry().Bind(g.Name, g.Interface, 6, comp); err != nil {
		t.Fatal(err)
	}
	surf, err := comp.CreateSurface()
	if err != nil {
		t.Fatal(err)
	}
	out := core.NewOutput(c.Context())
	g, ok = c.Registry().FindGlobal("wl_output")
	if !ok {
		t.Fatal("output missing")
	}
	if err := c.Registry().Bind(g.Name, g.Interface, 4, out); err != nil {
		t.Fatal(err)
	}
	return surf, out
}

func TestSessionLockLifecycleRefusedPipelinedSurfaceInert(t *testing.T) {
	s, gate, changes, _, dir := lockManagerServer(t)
	c := protocolClient(t, s, dir)
	manager := bindLockManager(t, c)
	accepted, err := manager.Lock()
	if err != nil {
		t.Fatal(err)
	}
	refused, err := manager.Lock()
	if err != nil {
		t.Fatal(err)
	}
	finished, configured := 0, 0
	refused.OnFinished(func() { finished++ })
	surf, out := lifecycleSurfaceOutput(t, c)
	role, err := refused.GetLockSurface(surf, out)
	if err != nil {
		t.Fatal(err)
	}
	role.OnConfigure(func(uint32, uint32, uint32) { configured++ })
	requestProtocol(t, c, surf.ID(), wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, surf.ID(), wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	state := lifecycleTransition(t, changes)
	s.display.Do(func() {
		stateSurf := s.surfaceOf(findTestSurface(s, surf.ID()))
		if stateSurf.kind != roleNone || stateSurf.lock != nil || len(s.lockSurfaces) != 0 || s.sessionLock.res.ID() != accepted.ID() || s.lockInputTarget(stateSurf.windowID()) {
			t.Error("refused surface gained active role/input")
		}
	})
	if finished != 1 || configured != 0 || gate.Snapshot() != state {
		t.Fatalf("refused finished=%d configure=%d", finished, configured)
	}
	if err := role.Destroy(); err != nil {
		t.Fatal(err)
	}
	if err := refused.Destroy(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, c)
}

func TestSessionLockLifecycleIMEGrabCannotReceiveLockerKey(t *testing.T) {
	s, _, changes, _, dir := lifecycleLockServer(t)
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
	var cfg lockConfigure
	role.OnConfigure(func(serial, w, h uint32) { cfg = lockConfigure{serial: serial, width: int(w), height: int(h)} })
	roundtrip(t, locker)
	if cfg.serial == 0 {
		t.Fatal("configure missing")
	}
	if err := role.AckConfigure(cfg.serial); err != nil {
		t.Fatal(err)
	}
	lockViewport(t, locker, surf.ID(), cfg.width, cfg.height)
	requestProtocol(t, locker, surf.ID(), wayland.SurfaceRequestAttach, shmBuffer(t, locker), int32(0), int32(0))
	requestProtocol(t, locker, surf.ID(), wayland.SurfaceRequestCommit)
	seat := bindProtocol(t, locker, "wl_seat")
	keys := &eventsProxy{}
	keys.SetID(locker.AllocateID())
	registerWireProxy(locker, keys)
	requestProtocol(t, locker, seat, wayland.SeatRequestGetKeyboard, keys.ID())
	roundtrip(t, locker)
	s.display.Do(func() { s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation}) })
	roundtrip(t, locker)
	if *locked != 1 {
		t.Fatal("lock not confirmed")
	}
	s.display.Do(func() {
		l := s.surfaceOf(findTestSurface(s, surf.ID())).lock
		s.apply(ports.SecurityCommand{State: state, Command: ports.FocusWindow{ID: l.id}})
		s.apply(ports.SecurityCommand{State: state, Command: ports.ForwardKey{ID: l.id, Key: ports.KeyEvent{Keycode: 30, Pressed: true, TimeMsec: 1}}})
	})
	settle(t, locker, ime.c)
	for _, event := range grab.take() {
		if strings.HasPrefix(event, "key ") {
			t.Fatalf("locker key leaked to IME: %s", event)
		}
	}
	if !slices.Contains(keys.opcodes, uint16(wayland.KeyboardEventKey)) {
		t.Fatal("locker did not receive key")
	}
}

func TestSessionLockLifecycleLatePrelockLeaseRevokedAfterUnlock(t *testing.T) {
	requests := make(chan ports.LeaseMessage, 16)
	s, gate, changes, _, dir := lifecycleLockServer(t, requests)
	c := protocolClient(t, s, dir)
	const card = "/dev/dri/card-lifecycle"
	s.display.Do(func() {
		s.applyLeaseEvent(ports.LeaseConnectors{Card: card, Device: leaseFD(t), Connectors: []ports.LeaseConnector{{Card: card, Name: "DP-1", ConnectorID: 42}}})
	})
	roundtrip(t, c)
	dev := bindProtocol(t, c, "wp_drm_lease_device_v1")
	device := &leaseDeviceEvents{c: c}
	device.SetID(dev)
	registerWireProxy(c, device)
	roundtrip(t, c)
	for _, fd := range device.fds {
		defer unix.Close(fd)
	}
	if len(device.connectors) != 1 {
		t.Fatal("lease connector missing")
	}
	req := c.AllocateID()
	registerProtocol(t, c, req)
	requestProtocol(t, c, dev, drmlease.WpDrmLeaseDeviceV1RequestCreateLeaseRequest, req)
	requestProtocol(t, c, req, drmlease.WpDrmLeaseRequestV1RequestRequestConnector, device.connectors[0])
	result := &leaseResultEvents{}
	result.SetID(c.AllocateID())
	registerWireProxy(c, result)
	requestProtocol(t, c, req, drmlease.WpDrmLeaseRequestV1RequestSubmit, result.ID())
	roundtrip(t, c)
	var pending ports.LeaseRequest
	select {
	case message := <-requests:
		var ok bool
		pending, ok = message.(ports.LeaseRequest)
		if !ok {
			t.Fatalf("request %T", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease request not sent")
	}
	lock, locked := lifecycleAcquire(t, c)
	state := lifecycleTransition(t, changes)
	s.display.Do(func() { s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation}) })
	roundtrip(t, c)
	if *locked != 1 || result.finished != 1 {
		t.Fatalf("acquire locked=%d lease finished=%d", *locked, result.finished)
	}
	if err := lock.UnlockAndDestroy(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, c)
	lifecycleTransition(t, changes)
	if gate.Snapshot().Protected {
		t.Fatal("valid unlock failed")
	}
	fd := leaseFD(t)
	s.display.Do(func() { s.applyLeaseEvent(ports.LeaseReply{ID: pending.ID, LeaseID: 123, FD: fd}) })
	roundtrip(t, c)
	if _, err := fd.Stat(); err == nil {
		t.Fatal("late lease FD not closed")
	}
	select {
	case message := <-requests:
		revoke, ok := message.(ports.LeaseRevoke)
		if !ok || revoke.Card != card || revoke.LeaseID != 123 {
			t.Fatalf("late reply request %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late lease was not revoked")
	}
	s.display.Do(func() {
		if len(s.pendingLeases) != 0 || len(s.pendingLeaseCards) != 0 || len(s.activeLeases) != 0 {
			t.Error("late reply retained authority")
		}
	})
	if len(result.fds) != 0 {
		for _, fd := range result.fds {
			unix.Close(fd)
		}
		t.Fatal("late lease FD delivered after unlock")
	}
}

func TestSessionLockLifecycleRoleUnmapSendsSeatLeaves(t *testing.T) {
	for _, kind := range []string{"role destroy", "output removal"} {
		t.Run(kind, func(t *testing.T) {
			s, gate, changes, _, dir := lifecycleLockServer(t)
			c := protocolClient(t, s, dir)
			lock, locked := lifecycleAcquire(t, c)
			state := lifecycleTransition(t, changes)
			surf, out := lifecycleSurfaceOutput(t, c)
			role, err := lock.GetLockSurface(surf, out)
			if err != nil {
				t.Fatal(err)
			}
			var cfg lockConfigure
			role.OnConfigure(func(serial, w, h uint32) { cfg = lockConfigure{serial: serial, width: int(w), height: int(h)} })
			roundtrip(t, c)
			if err := role.AckConfigure(cfg.serial); err != nil {
				t.Fatal(err)
			}
			lockViewport(t, c, surf.ID(), cfg.width, cfg.height)
			requestProtocol(t, c, surf.ID(), wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
			requestProtocol(t, c, surf.ID(), wayland.SurfaceRequestCommit)
			seat := bindProtocol(t, c, "wl_seat")
			keys := &eventsProxy{}
			keys.SetID(c.AllocateID())
			registerWireProxy(c, keys)
			requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, keys.ID())
			pointer := newIMELog(c, c.AllocateID(), "enter:uoff", "leave:uo", "motion:uff", "button:uuuu", "axis:uuf")
			requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer.ID())
			roundtrip(t, c)
			s.display.Do(func() { s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation}) })
			roundtrip(t, c)
			if *locked != 1 {
				t.Fatal("lock not confirmed")
			}
			s.display.Do(func() {
				l := s.surfaceOf(findTestSurface(s, surf.ID())).lock
				s.changeFocus(l.id)
				s.changePointerFocus(l.id, 1, 2)
			})
			roundtrip(t, c)
			if !slices.Contains(keys.opcodes, uint16(wayland.KeyboardEventEnter)) {
				t.Fatal("keyboard enter missing")
			}
			keys.opcodes = nil
			pointer.take()
			if kind == "role destroy" {
				if err := role.Destroy(); err != nil {
					t.Fatal(err)
				}
			} else {
				s.display.Do(func() { s.setOutputs(ports.SetOutputs{}) })
			}
			roundtrip(t, c)
			if !slices.Contains(keys.opcodes, uint16(wayland.KeyboardEventLeave)) {
				t.Fatal("live lock keyboard did not receive leave")
			}
			leave := false
			for _, event := range pointer.take() {
				if strings.HasPrefix(event, "leave ") {
					leave = true
				}
			}
			if !leave {
				t.Fatal("live lock pointer did not receive leave")
			}
			s.display.Do(func() {
				if s.seat.focused != 0 || s.seat.pointerFocus != 0 || !gate.Snapshot().Protected || !findTestSurface(s, surf.ID()).Alive() {
					t.Error("unmap lost protection/live wl_surface or retained seat focus")
				}
			})
		})
	}
}

func TestSessionLockLifecycleBackendOutboxPreservesBlockedTransitions(t *testing.T) {
	s, gate, changes, _, dir := lockManagerServer(t)
	// Fill the backend channel before any transition; display remains responsive.
	sentinel := ports.SecurityState{Generation: 999}
	changes <- sentinel
	c := protocolClient(t, s, dir)
	lock, locked := lifecycleAcquire(t, c)
	state := gate.Snapshot()
	s.display.Do(func() { s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation}) })
	roundtrip(t, c)
	if *locked != 1 {
		t.Fatal("test protection proof not consumed")
	}
	if err := lock.UnlockAndDestroy(); err != nil {
		t.Fatal(err)
	}
	roundtrip(t, c)
	released := gate.Snapshot()
	s.display.Do(func() {
		if len(s.securityPending) < 2 || s.securityPending[0] != state || s.securityPending[1] != released {
			t.Errorf("blocked outbox lost ordered transitions: %+v", s.securityPending)
		}
	})
	if got := lifecycleTransition(t, changes); got != sentinel {
		t.Fatalf("sentinel %+v", got)
	}
	if got := lifecycleTransition(t, changes); got != state {
		t.Fatalf("acquire %+v", got)
	}
	if got := lifecycleTransition(t, changes); got != released {
		t.Fatalf("release %+v", got)
	}
	if released.Protected || released.Generation <= state.Generation {
		t.Fatal("unlock generation did not advance")
	}
}
