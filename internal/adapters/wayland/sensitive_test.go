package wayland

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/adapters/wayland/imagecapture"
	"github.com/bnema/purego-libwayland/protocol/cursorshape"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrforeigntoplevel"
	screencopy "github.com/bnema/purego-libwayland/protocol/wlrscreencopy"
	"github.com/bnema/purego-libwayland/protocol/xdgactivation"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	source "github.com/bnema/purego-libwayland/protocol/extimagecapturesource"
	ext "github.com/bnema/purego-libwayland/protocol/extimagecopycapture"
	"github.com/bnema/purego-libwayland/protocol/inputmethod"
	"github.com/bnema/purego-libwayland/protocol/textinput"
	"github.com/bnema/purego-libwayland/protocol/virtualkeyboard"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"golang.org/x/sys/unix"
)

// installSecurity uses the canonical generated mock, with coherent atomic
// snapshots; transitions are serialized through the real DISPLAY loop.
func installSecurity(t *testing.T, s *Server) *atomic.Pointer[ports.SecurityState] {
	t.Helper()
	state := &atomic.Pointer[ports.SecurityState]{}
	state.Store(&ports.SecurityState{})
	security := portsmocks.NewMockSessionSecurity(t)
	security.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return *state.Load() }).Maybe()
	if !s.display.Do(func() { s.security = security }) {
		t.Fatal("display stopped")
	}
	return state
}

func protect(t *testing.T, s *Server, state *atomic.Pointer[ports.SecurityState], generation ports.LockGeneration, protected bool) {
	t.Helper()
	if !s.display.Do(func() { state.Store(&ports.SecurityState{Generation: generation, Protected: protected}) }) {
		t.Fatal("display stopped")
	}
}

func TestProtectedClipboardPreexistingOffer(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	state := installSecurity(t, s)
	a := newClipClient(t, s, dir, events)
	a.focus(t, commands)
	src := a.copy(t)
	offer, ok := a.selection(t)
	if !ok || offer == 0 {
		t.Fatal("no unlocked offer")
	}
	protect(t, s, state, 1, true)
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	if err := wireRequest(a.c, offer, uint16(wayland.DataOfferRequestReceive), []int{fds[1]}, "text/plain"); err != nil {
		t.Fatal(err)
	}
	unix.Close(fds[1])
	roundtrip(t, a.c)
	select {
	case <-src.sends:
		t.Fatal("protected receive handed pipe to source")
	default:
	}
	var b [1]byte
	n, err := unix.Read(fds[0], b[:])
	if err != nil || n != 0 {
		t.Fatalf("receive pipe not closed: n=%d err=%v", n, err)
	}
	rejected := a.copy(t)
	select {
	case <-rejected.cancelled:
	default:
		t.Fatal("protected selection not rejected")
	}
	if id, ok := a.selection(t); ok {
		t.Fatalf("protected offer announced %d", id)
	}
	protect(t, s, state, 2, false)
	// Existing clipboard data remains usable when unlocked.
	a.copy(t)
	if id, ok := a.selection(t); !ok || id == 0 {
		t.Fatal("unlocked selection not restored")
	}
}

func TestProtectedCaptureAdmission(t *testing.T) {
	h := newAdmissionHarness(t, 4)
	state := installSecurity(t, h.s)
	a := h.client(t)
	// Create the ext session before protection, then test its late frame.
	p := a.captureExt(t)
	req, ok := h.accepted(t, p)
	if !ok {
		t.Fatal("unlocked ext capture rejected")
	}
	a.destroy(t, p)
	h.complete(t, req, 0)
	protect(t, h.s, state, 1, true)
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {
		t.Fatal("protected wlr capture admitted")
	}
	if _, ok := h.accepted(t, a.captureExt(t)); ok {
		t.Fatal("protected ext capture admitted")
	}
	if h.inflight(t) != 0 {
		t.Fatal("protected captures reserved credits")
	}
	var next uint64
	if !h.s.display.Do(func() { next = h.s.nextCapture }) {
		t.Fatal("display stopped")
	}
	if next != req.ID {
		t.Fatalf("protected capture allocated ID: %d", next)
	}
	// A newly created ext session is stopped and never admits capture.
	b := h.client(t)
	sourceManager := bindProtocol(t, b.c, "ext_output_image_capture_source_manager_v1")
	manager := bindProtocol(t, b.c, "ext_image_copy_capture_manager_v1")
	src, session := b.c.AllocateID(), b.c.AllocateID()
	registerProtocol(t, b.c, src)
	requestProtocol(t, b.c, sourceManager, source.ExtOutputImageCaptureSourceManagerV1RequestCreateSource, src, b.output)
	ev := &captureEvents{events: make(chan uint16, 16)}
	ev.SetID(session)
	registerWireProxy(b.c, ev)
	requestProtocol(t, b.c, manager, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, session, src, uint32(0))
	roundtrip(t, b.c)
	select {
	case op := <-ev.events:
		if op != uint16(ext.ExtImageCopyCaptureSessionV1EventStopped) {
			t.Fatalf("new session event %d", op)
		}
	default:
		t.Fatal("new protected ext session not stopped")
	}
	if len(ev.events) != 0 {
		t.Fatal("protected session disclosed constraints")
	}
	b.session = session
	if _, ok := h.accepted(t, b.captureExt(t)); ok {
		t.Fatal("new protected session admitted")
	}
	protect(t, h.s, state, 2, false)
	req, ok = h.accepted(t, a.captureWlr(t))
	if !ok {
		t.Fatal("unlocked wlr capture rejected")
	}
	h.complete(t, req, 0)
}

func TestProtectedVirtualKeyboard(t *testing.T) {
	for _, destroyProtected := range []bool{true, false} {
		t.Run(map[bool]string{true: "protected-destroy", false: "post-unlock-destroy"}[destroyProtected], func(t *testing.T) {
			s, events, commands, dir := keyboardServer(t)
			state := installSecurity(t, s)
			target, log := focusedTarget(t, s, events, commands, dir)
			typer, kb := virtualTyper(t, s, dir)
			sendVirtualKeymap(t, typer, kb, keymapText(t))
			requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
			roundtrip(t, typer, target)
			if len(log.opcodes) == 0 {
				t.Fatal("unlocked virtual key missing")
			}
			log.opcodes = nil
			protect(t, s, state, 1, true)
			requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(2), uint32(31), uint32(1))
			requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestModifiers, uint32(1), uint32(0), uint32(0), uint32(0))
			roundtrip(t, typer, target)
			if len(log.opcodes) != 0 {
				t.Fatalf("protected virtual events: %v", log.opcodes)
			}
			if !destroyProtected {
				protect(t, s, state, 2, false)
			}
			requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestDestroy)
			roundtrip(t, typer, target)
			if len(log.opcodes) != 0 {
				t.Fatalf("old virtual held state injected on destroy: %v", log.opcodes)
			}
		})
	}
}

func TestProtectedInputMethod(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	state := installSecurity(t, s)
	ime := newIMEApp(t, s, dir)
	app := newTextApp(t, s, events, commands, dir)
	app.enable(t)
	_, grab := ime.grab(t)
	settle(t, app.c, ime.c)
	app.log.take()
	ime.log.take()
	grab.take()
	protect(t, s, state, 1, true)
	app.request(t, textinput.ZwpTextInputV3RequestSetSurroundingText, "locker-secret", int32(13), int32(13))
	app.request(t, textinput.ZwpTextInputV3RequestCommit)
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommitString, "injected")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommit, uint32(1))
	// Exercise the live grab's delivery entrypoint; normal seat routing/reset is
	// deliberately outside this adapter assignment.
	if !s.display.Do(func() {
		s.grabKey(ports.ForwardKey{ID: app.window, Key: ports.KeyEvent{Keycode: 30, Pressed: true, State: ports.ModState{Depressed: 1}}})
		s.sendGrabKeymap(true)
	}) {
		t.Fatal("display stopped")
	}
	_, late := ime.grab(t)
	settle(t, app.c, ime.c)
	expectEvents(t, "protected IM state", ime.log.take())
	expectEvents(t, "protected IM injection", app.log.take())
	expectEvents(t, "existing protected grab", grab.take())
	expectEvents(t, "new protected grab", late.take())
	protect(t, s, state, 2, false)
	app.enable(t)
	settle(t, app.c, ime.c)
	if len(ime.log.take()) == 0 {
		t.Fatal("unlocked IM state not delivered")
	}
}

// While the session is protected nothing of it is captured: ext sessions stop
// or fail their frames, wlr-screencopy fails, a new session is stopped, an
// exclusion attach is refused, and none of it reaches core or the renderer.
func TestProtectedCapture(t *testing.T) {
	h := newCaptureHarness(t)
	state := installSecurity(t, h.s)
	owner, session, open, _, token := h.exclusionOwner(t)
	h.send(ports.CaptureSessionState{ID: open.ID, Output: "HEADLESS-1", Rect: ports.Rect{W: 4, H: 4}, Active: true, Exclusion: true, Revision: 1})
	h.settle(t, owner)
	// Unprotected, the same frame is admitted (positive control).
	owner.extFrame(t, session, 4, 4)
	accepted := receiveCapture(t, h.captures)
	if !accepted.Exclude || accepted.Session != open.ID {
		t.Fatalf("owner capture not excluded: %+v", accepted)
	}
	h.captured <- ports.CaptureDone{ID: accepted.ID, Output: accepted.Output, Time: time.Now()}
	hd := h.hud(t, ports.LayerTop, 0)
	protect(t, h.s, state, 1, true)
	if !eventsInclude(owner.extFrame(t, session, 4, 4), frameFailed) {
		t.Fatal("protected ext frame not failed")
	}
	if !eventsInclude(owner.wlrFrame(t), uint16(screencopy.ZwlrScreencopyFrameV1EventFailed)) {
		t.Fatal("protected wlr frame not failed")
	}
	noCapture(t, h.captures)
	att := hd.attach(t, token)
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureUnauthorized))
	// A session made while protected is stopped, for every kind of source.
	other := h.client(t)
	for name, src := range map[string]uint32{"output": other.outputSource(t), "region": other.regionSource(t, 0, 0, 2, 2)} {
		_, ev := other.session(t, src)
		if got := nextSession(t, other, ev); got[0] != evStopped {
			t.Fatalf("%s session while protected: %v", name, got)
		}
	}
	for len(h.events) > 0 {
		switch ev := (<-h.events).(type) {
		case ports.CaptureSessionOpen, ports.CaptureExclusionBegin, ports.CaptureExclusionLayer:
			t.Fatalf("protected capture request reached core: %+v", ev)
		}
	}
	// Unlocked again, capture works for a new session.
	protect(t, h.s, state, 2, false)
	cc := h.client(t)
	again, ev := cc.session(t, cc.outputSource(t))
	constraints(t, cc, ev)
	h.open(t)
	cc.extFrame(t, again, 4, 4)
	receiveCapture(t, h.captures)
}

// Engaging the lock ends every capture session and its exclusion and tells
// core, so a session never outlives the lock and no hidden-workspace or
// region session keeps rendering.
func TestSessionLockEndsCaptureSessions(t *testing.T) {
	gate := &sessionsecurity.Gate{}
	h := newCaptureHarnessWith(t, func(o *Options, c *Channels) {
		o.Security = gate
		c.SecurityChanges = make(chan ports.SecurityState, 1)
		c.SecurityEvents = make(chan ports.SecurityBackendEvent, 16)
	})
	owner, session, open, msgs, _ := h.exclusionOwner(t)
	region, rev := owner.session(t, owner.regionSource(t, 0, 0, 2, 2))
	constraints(t, owner, rev)
	regionOpen := h.open(t)
	manager := bindLockManager(t, owner.c)
	if _, err := manager.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := owner.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !gate.Snapshot().Protected {
		t.Fatal("lock did not engage")
	}
	closed := map[uint64]bool{}
	for len(closed) < 2 {
		closed[nextEvent[ports.CaptureSessionClose](t, h.events).ID] = true
	}
	if !closed[open.ID] || !closed[regionOpen.ID] {
		t.Fatalf("core not told: %v", closed)
	}
	// Both sessions are stopped and the exclusion failed with its session.
	if !eventsInclude(owner.extFrame(t, session, 4, 4), frameFailed) || !eventsInclude(owner.extFrame(t, region, 2, 2), frameFailed) {
		t.Fatal("frame of a locked session not failed")
	}
	noCapture(t, h.captures)
	_ = msgs
}

func TestProtectedActivationAndForeignInventory(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	state := installSecurity(t, s)
	c := protocolClient(t, s, dir)
	w, surf, xdg := surfaceMapper(t, c, events)()
	commands <- ports.FocusWindow{ID: w.ID}
	waitFocus(t, s, w.ID)
	manager := bindProtocol(t, c, "xdg_activation_v1")
	token := newToken(t, c, manager)
	foreign := bindVersion(t, c, "zwlr_foreign_toplevel_manager_v1", 3)
	log := &toplevelManagerEvents{client: c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	log.SetID(foreign)
	registerWireProxy(c, log)
	takeEvents(t, c, log.events)
	handle := <-log.handles
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	protect(t, s, state, 1, true)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestActivate, seat)
	requestProtocol(t, c, handle, wlr.ZwlrForeignToplevelHandleV1RequestSetFullscreen, uint32(0))
	requestProtocol(t, c, xdg+1, xdgshell.ToplevelRequestSetTitle, "sensitive-title")
	noActivation(t, c, events)
	if len(log.events) != 0 {
		t.Fatal("foreign inventory updated while protected")
	}
	lateID := bindVersion(t, c, "zwlr_foreign_toplevel_manager_v1", 3)
	late := &toplevelManagerEvents{client: c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	late.SetID(lateID)
	registerWireProxy(c, late)
	roundtrip(t, c)
	if len(late.handles) != 0 || len(late.events) != 0 {
		t.Fatal("new foreign manager disclosed protected inventory")
	}
	protect(t, s, state, 2, false)
	requestProtocol(t, c, manager, xdgactivation.ActivationV1RequestActivate, token, surf)
	noActivation(t, c, events) // the protected attempt consumed the token
}

func TestProtectedCursorChanges(t *testing.T) {
	s, events, commands, cursors, dir := cursorServer(t)
	state := installSecurity(t, s)
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
	manager := bindProtocol(t, c, "wp_cursor_shape_manager_v1")
	device := c.AllocateID()
	registerProtocol(t, c, device)
	requestProtocol(t, c, manager, cursorshape.WpCursorShapeManagerV1RequestGetPointer, device, pointer)
	w := toplevelMapper(t, c, events)()
	commands <- ports.PointerFocus{ID: w.ID}
	nextCursor(t, cursors)
	protect(t, s, state, 1, true)
	requestProtocol(t, c, device, cursorshape.WpCursorShapeDeviceV1RequestSetShape, uint32(1), uint32(cursorshape.WpCursorShapeDeviceV1ShapeText))
	requestProtocol(t, c, pointer, wayland.PointerRequestSetCursor, uint32(1), uint32(0), int32(0), int32(0))
	roundtrip(t, c)
	select {
	case change := <-cursors:
		t.Fatalf("normal cursor overrode protection: %+v", change)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProtectedVirtualKeyboardQuietEpoch(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	state := installSecurity(t, s)
	target, log := focusedTarget(t, s, events, commands, dir)
	typer, kb := virtualTyper(t, s, dir)
	sendVirtualKeymap(t, typer, kb, keymapText(t))
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
	roundtrip(t, typer, target)
	log.opcodes = nil
	protect(t, s, state, 1, true)
	protect(t, s, state, 2, false)
	// No requests during protection: generation must still invalidate held keys.
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(2), uint32(30), uint32(0))
	requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestDestroy)
	roundtrip(t, typer, target)
	if len(log.opcodes) != 0 {
		t.Fatalf("old virtual state crossed quiet lock epoch: %v", log.opcodes)
	}
}

func TestProtectedVirtualKeymapRequiresResend(t *testing.T) {
	for _, resend := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-keymap-rejected", true: "fresh-keymap-accepted"}[resend], func(t *testing.T) {
			s, events, commands, dir := keyboardServer(t)
			state := installSecurity(t, s)
			target, log := focusedTarget(t, s, events, commands, dir)
			typer, kb := virtualTyper(t, s, dir)
			sendVirtualKeymap(t, typer, kb, keymapText(t))
			requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(1), uint32(30), uint32(1))
			roundtrip(t, typer, target)
			log.opcodes = nil
			var owner *virtualKeyboard
			var oldFD int
			if !s.display.Do(func() { owner = s.seat.keymapOwner; oldFD = owner.fd }) {
				t.Fatal("display stopped")
			}
			protect(t, s, state, 1, true)
			sendVirtualKeymap(t, typer, kb, keymapText(t))
			roundtrip(t, typer, target)
			var cleared bool
			if !s.display.Do(func() { cleared = owner.fd == -1 && owner.text == "" && owner.size == 0 }) {
				t.Fatal("display stopped")
			}
			if !cleared {
				t.Fatal("protected keymap retained old layout")
			}
			if _, err := unix.FcntlInt(uintptr(oldFD), unix.F_GETFD, 0); err != unix.EBADF {
				t.Fatalf("old keymap FD not closed: %v", err)
			}
			if len(log.opcodes) != 0 {
				t.Fatalf("protected keymap events: %v", log.opcodes)
			}
			protect(t, s, state, 2, false)
			if resend {
				sendVirtualKeymap(t, typer, kb, keymapText(t))
			}
			requestProtocol(t, typer, kb, virtualkeyboard.ZwpVirtualKeyboardV1RequestKey, uint32(2), uint32(31), uint32(1))
			if !resend {
				expectProtocolError(t, typer, kb, uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap))
				roundtrip(t, target)
				if len(log.opcodes) != 0 {
					t.Fatalf("old keymap injected after unlock: %v", log.opcodes)
				}
			} else {
				roundtrip(t, typer, target)
				found := false
				for _, op := range log.opcodes {
					if op == uint16(wayland.KeyboardEventKey) {
						found = true
					}
				}
				if !found {
					t.Fatal("fresh post-unlock keymap cannot type")
				}
			}
		})
	}
}

func TestProtectedForeignCloseDeferredUntilRefresh(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	state := installSecurity(t, s)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	foreign := bindVersion(t, c, "zwlr_foreign_toplevel_manager_v1", 3)
	log := &toplevelManagerEvents{client: c, handles: make(chan uint32, 4), events: make(chan string, 64)}
	log.SetID(foreign)
	registerWireProxy(c, log)
	takeEvents(t, c, log.events)
	<-log.handles
	protect(t, s, state, 1, true)
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	roundtrip(t, c)
	unmapped(t, events, w.ID)
	if !s.display.Do(func() { s.refreshToplevels() }) {
		t.Fatal("display stopped")
	}
	roundtrip(t, c)
	if len(log.events) != 0 {
		t.Fatal("protected foreign close leaked timing")
	}
	var retained bool
	if !s.display.Do(func() { retained = s.toplevelManagers[0].handles[w.ID] != nil }) {
		t.Fatal("display stopped")
	}
	if !retained {
		t.Fatal("deferred foreign handle lost before unlock")
	}
	protect(t, s, state, 2, false)
	if !s.display.Do(func() { s.refreshToplevels() }) {
		t.Fatal("display stopped")
	}
	got := takeEvents(t, c, log.events)
	if len(got) != 1 || got[0] != "closed" {
		t.Fatalf("unlock deferred events: %v", got)
	}
	if !s.display.Do(func() { retained = s.toplevelManagers[0].handles[w.ID] != nil; s.refreshToplevels() }) {
		t.Fatal("display stopped")
	}
	if retained {
		t.Fatal("stale foreign inventory retained on unlock")
	}
	roundtrip(t, c)
	if len(log.events) != 0 {
		t.Fatal("deferred close sent twice")
	}
}
