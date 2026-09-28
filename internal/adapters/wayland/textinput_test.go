package wayland

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/inputmethod"
	"github.com/bnema/purego-libwayland/protocol/textinput"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// imeLog records events of one object as "name arg arg" strings, from
// the event signatures: u, i, o (IDs), s (strings) and h (fds, closed).
type imeLog struct {
	wlturbo.BaseProxy
	events []string
	sigs   []struct{ name, args string }
}

func (l *imeLog) Dispatch(e *wlturbo.Event) {
	if int(e.Opcode) >= len(l.sigs) {
		l.events = append(l.events, fmt.Sprint("unknown ", e.Opcode))
		return
	}
	sig := l.sigs[e.Opcode]
	parts := []string{sig.name}
	for _, a := range sig.args {
		switch a {
		case 'u', 'o':
			parts = append(parts, fmt.Sprint(e.Uint32()))
		case 'i':
			parts = append(parts, fmt.Sprint(e.Int32()))
		case 's':
			parts = append(parts, fmt.Sprintf("%q", e.String()))
		case 'h':
			if fd := int(e.Fd()); fd > 0 {
				unix.Close(fd)
			}
			parts = append(parts, "fd")
		}
	}
	l.events = append(l.events, strings.Join(parts, " "))
}

// take returns the recorded events and forgets them.
func (l *imeLog) take() []string {
	ev := l.events
	l.events = nil
	return ev
}

func newIMELog(c *wlturbo.Display, id uint32, sigs ...string) *imeLog {
	l := &imeLog{}
	for _, s := range sigs {
		name, args, _ := strings.Cut(s, ":")
		l.sigs = append(l.sigs, struct{ name, args string }{name, args})
	}
	l.SetID(id)
	c.Context().Register(l)
	return l
}

var (
	textInputEvents   = []string{"enter:o", "leave:o", "preedit_string:sii", "commit_string:s", "delete_surrounding_text:uu", "done:u"}
	inputMethodEvents = []string{"activate", "deactivate", "surrounding_text:suu", "text_change_cause:u", "content_type:uu", "done", "unavailable"}
	grabEvents        = []string{"keymap:uhu", "key:uuuu", "modifiers:uuuuu", "repeat_info:ii"}
)

// textApp is a focused client window with a text input.
type textApp struct {
	c        *wlturbo.Display
	ti, surf uint32
	top      uint32
	window   ports.WindowID
	log      *imeLog
	keys     *eventsProxy
}

func newTextApp(t *testing.T, s *Server, events chan ports.ClientEvent, commands chan ports.ClientCommand, dir string) *textApp {
	t.Helper()
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	keys := &eventsProxy{}
	keys.SetID(c.AllocateID())
	c.Context().Register(keys)
	requestProtocol(t, c, seat, wayland.SeatRequestGetKeyboard, keys.ID())
	w, surf, xdg := surfaceMapper(t, c, events)()
	manager := bindProtocol(t, c, "zwp_text_input_manager_v3")
	registerProtocol(t, c, manager)
	ti := c.AllocateID()
	log := newIMELog(c, ti, textInputEvents...)
	requestProtocol(t, c, manager, textinput.ZwpTextInputManagerV3RequestGetTextInput, ti, seat)
	commands <- ports.FocusWindow{ID: w.ID}
	waitFocus(t, s, w.ID)
	roundtrip(t, c)
	// surfaceMapper allocated the toplevel right after the xdg_surface.
	return &textApp{c: c, ti: ti, surf: surf, top: xdg + 1, window: w.ID, log: log, keys: keys}
}

func (a *textApp) request(t *testing.T, op uint32, args ...any) {
	t.Helper()
	requestProtocol(t, a.c, a.ti, op, args...)
}

// enable commits an enabled text input with surrounding text and content type.
func (a *textApp) enable(t *testing.T) {
	t.Helper()
	a.request(t, textinput.ZwpTextInputV3RequestEnable)
	a.request(t, textinput.ZwpTextInputV3RequestSetSurroundingText, "hello", int32(5), int32(5))
	a.request(t, textinput.ZwpTextInputV3RequestSetContentType, uint32(textinput.ZwpTextInputV3ContentHintSpellcheck), uint32(textinput.ZwpTextInputV3ContentPurposeNormal))
	a.request(t, textinput.ZwpTextInputV3RequestCommit)
}

// imeApp is an input method client.
type imeApp struct {
	c   *wlturbo.Display
	im  uint32
	log *imeLog
}

func newIMEApp(t *testing.T, s *Server, dir string) *imeApp {
	t.Helper()
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	manager := bindProtocol(t, c, "zwp_input_method_manager_v2")
	registerProtocol(t, c, manager)
	im := c.AllocateID()
	log := newIMELog(c, im, inputMethodEvents...)
	requestProtocol(t, c, manager, inputmethod.ZwpInputMethodManagerV2RequestGetInputMethod, seat, im)
	roundtrip(t, c)
	return &imeApp{c: c, im: im, log: log}
}

func (m *imeApp) request(t *testing.T, op uint32, args ...any) {
	t.Helper()
	requestProtocol(t, m.c, m.im, op, args...)
}

func (m *imeApp) grab(t *testing.T) (uint32, *imeLog) {
	t.Helper()
	g := m.c.AllocateID()
	log := newIMELog(m.c, g, grabEvents...)
	m.request(t, inputmethod.ZwpInputMethodV2RequestGrabKeyboard, g)
	return g, log
}

// settle lets the server handle every connection's requests, in order.
func settle(t *testing.T, cs ...*wlturbo.Display) {
	t.Helper()
	roundtrip(t, cs...)
	roundtrip(t, cs...)
}

func expectEvents(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s events:\n got %q\nwant %q", what, got, want)
	}
}

// Text input focus follows keyboard focus across clients and window close.
func TestTextInputFollowsFocus(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	a := newTextApp(t, s, events, commands, dir)
	expectEvents(t, "a", a.log.take(), fmt.Sprint("enter ", a.surf))
	b := newTextApp(t, s, events, commands, dir)
	settle(t, a.c, b.c)
	expectEvents(t, "a", a.log.take(), fmt.Sprint("leave ", a.surf))
	expectEvents(t, "b", b.log.take(), fmt.Sprint("enter ", b.surf))

	commands <- ports.FocusWindow{ID: a.window}
	waitFocus(t, s, a.window)
	settle(t, a.c, b.c)
	expectEvents(t, "a", a.log.take(), fmt.Sprint("enter ", a.surf))
	expectEvents(t, "b", b.log.take(), fmt.Sprint("leave ", b.surf))

	// Closing the focused window leaves.
	requestProtocol(t, a.c, a.top, xdgshell.ToplevelRequestDestroy)
	settle(t, a.c)
	waitFocus(t, s, 0)
	settle(t, a.c)
	expectEvents(t, "a", a.log.take(), fmt.Sprint("leave ", a.surf))
}

// Enable activates the input method with the client state; its preedit,
// commit and delete reach the client in order with the commit count.
func TestTextInputIMERoundTrip(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	ime := newIMEApp(t, s, dir)
	app := newTextApp(t, s, events, commands, dir)
	app.log.take()
	app.enable(t)
	settle(t, app.c, ime.c)
	expectEvents(t, "ime", ime.log.take(), "activate", `surrounding_text "hello" 5 5`, "text_change_cause 0", "content_type 2 0", "done")

	ime.request(t, inputmethod.ZwpInputMethodV2RequestSetPreeditString, "nihon", int32(5), int32(5))
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommit, uint32(1))
	settle(t, ime.c, app.c)
	expectEvents(t, "app", app.log.take(), `preedit_string "nihon" 5 5`, "done 1")

	ime.request(t, inputmethod.ZwpInputMethodV2RequestDeleteSurroundingText, uint32(1), uint32(0))
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommitString, "日本")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommit, uint32(1))
	settle(t, ime.c, app.c)
	expectEvents(t, "app", app.log.take(), `commit_string "日本"`, "delete_surrounding_text 1 0", "done 1")

	// State updates reach the input method; done carries the latest
	// client commit count even when the input method is behind.
	app.request(t, textinput.ZwpTextInputV3RequestSetSurroundingText, "hello日本", int32(11), int32(11))
	app.request(t, textinput.ZwpTextInputV3RequestSetTextChangeCause, uint32(textinput.ZwpTextInputV3ChangeCauseOther))
	app.request(t, textinput.ZwpTextInputV3RequestCommit)
	app.request(t, textinput.ZwpTextInputV3RequestCommit)
	settle(t, app.c, ime.c)
	expectEvents(t, "ime", ime.log.take(), `surrounding_text "hello日本" 11 11`, "text_change_cause 1", "content_type 2 0", "done", `surrounding_text "hello日本" 11 11`, "text_change_cause 0", "content_type 2 0", "done")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommitString, "x")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommit, uint32(1))
	settle(t, ime.c, app.c)
	expectEvents(t, "app", app.log.take(), `commit_string "x"`, "done 3")

	app.request(t, textinput.ZwpTextInputV3RequestDisable)
	app.request(t, textinput.ZwpTextInputV3RequestCommit)
	settle(t, app.c, ime.c)
	expectEvents(t, "ime", ime.log.take(), "deactivate", "done")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommitString, "lost")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestCommit, uint32(4))
	settle(t, ime.c, app.c)
	expectEvents(t, "app", app.log.take())
}

// Focus loss deactivates; a second text input cannot take over; state
// past the spec limit is ignored.
func TestTextInputFocusLossAndLimits(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	ime := newIMEApp(t, s, dir)
	app := newTextApp(t, s, events, commands, dir)
	app.enable(t)
	settle(t, app.c, ime.c)
	ime.log.take()

	app.request(t, textinput.ZwpTextInputV3RequestSetSurroundingText, strings.Repeat("a", maxIMEText+1), int32(0), int32(0))
	app.request(t, textinput.ZwpTextInputV3RequestCommit)
	settle(t, app.c, ime.c)
	expectEvents(t, "ime", ime.log.take(), `surrounding_text "hello" 5 5`, "text_change_cause 0", "content_type 2 0", "done")

	other := newTextApp(t, s, events, commands, dir)
	settle(t, app.c, ime.c)
	expectEvents(t, "ime", ime.log.take(), "deactivate", "done")
	// After leave, app's requests are ignored until the next enter.
	app.enable(t)
	settle(t, app.c, ime.c)
	expectEvents(t, "ime", ime.log.take())
	other.enable(t)
	settle(t, other.c, ime.c)
	expectEvents(t, "ime", ime.log.take(), "activate", `surrounding_text "hello" 5 5`, "text_change_cause 0", "content_type 2 0", "done")
}

// Only one input method at a time; the slot frees when it disconnects,
// and the next one is activated for the enabled text input.
func TestInputMethodSingleSlot(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	first := newIMEApp(t, s, dir)
	second := newIMEApp(t, s, dir)
	expectEvents(t, "second", second.log.take(), "unavailable")
	// An inert input method's requests do nothing.
	second.request(t, inputmethod.ZwpInputMethodV2RequestCommitString, "x")
	second.request(t, inputmethod.ZwpInputMethodV2RequestCommit, uint32(0))
	app := newTextApp(t, s, events, commands, dir)
	app.enable(t)
	settle(t, second.c, app.c, first.c)
	app.log.take()
	_ = first.c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(app.log.events) == 0 && time.Now().Before(deadline) {
		roundtrip(t, app.c)
		time.Sleep(5 * time.Millisecond)
	}
	// The preedit is cleared by an empty done.
	expectEvents(t, "app", app.log.take(), "done 1")
	third := newIMEApp(t, s, dir)
	expectEvents(t, "third", third.log.take(), "activate", `surrounding_text "hello" 5 5`, "text_change_cause 0", "content_type 2 0", "done")
}

// until roundtrips cs until ok holds: commands reach the display
// goroutine on their own channel, not in order with requests.
func until(t *testing.T, what string, ok func() bool, cs ...*wlturbo.Display) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		roundtrip(t, cs...)
	}
}

// A keyboard grab takes the focused client's keys until released; a key
// the client saw pressed is released to the client.
func TestInputMethodKeyboardGrab(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	app := newTextApp(t, s, events, commands, dir)
	ime := newIMEApp(t, s, dir)
	app.keys.opcodes = nil
	key := func(code uint32, pressed bool) {
		commands <- ports.ForwardKey{ID: app.window, Key: ports.KeyEvent{Keycode: code, Pressed: pressed, TimeMsec: 7}}
	}
	k := uint16(wayland.KeyboardEventKey)
	key(31, true) // held by the client before the grab
	until(t, "client did not get the key before the grab", func() bool { return len(app.keys.opcodes) == 1 }, app.c)
	g, grab := ime.grab(t)
	settle(t, ime.c)
	got := grab.take()
	if len(got) != 3 || got[0] != "keymap 1 fd "+fmt.Sprint(len(keymapText(t))+1) || got[1] != "repeat_info 25 600" || !strings.HasPrefix(got[2], "modifiers ") {
		t.Fatalf("grab setup %q", got)
	}
	key(30, true)
	key(30, false)
	key(31, false)
	until(t, "client did not get the release of 31", func() bool { return len(app.keys.opcodes) == 2 }, app.c)
	settle(t, ime.c)
	if len(grab.events) != 2 || !strings.HasSuffix(grab.events[0], " 7 30 1") || !strings.HasSuffix(grab.events[1], " 7 30 0") {
		t.Fatalf("grab keys %q", grab.events)
	}
	if !slices.Equal(app.keys.opcodes, []uint16{k, k}) {
		t.Fatalf("client keys %v, want the press and release of 31", app.keys.opcodes)
	}
	requestProtocol(t, ime.c, g, inputmethod.ZwpInputMethodKeyboardGrabV2RequestRelease)
	settle(t, ime.c)
	app.keys.opcodes = nil
	key(32, true)
	until(t, "key after release did not reach the client", func() bool { return slices.Contains(app.keys.opcodes, k) }, app.c)
	settle(t, ime.c)
	if len(grab.events) != 2 {
		t.Fatalf("released grab got %q", grab.events)
	}
}

// Disconnects and destroyed surfaces in active states do not break the
// seat: keys return to the client, the input method is deactivated.
func TestTextInputTeardown(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	ime := newIMEApp(t, s, dir)
	app := newTextApp(t, s, events, commands, dir)
	app.enable(t)
	ime.grab(t)
	settle(t, app.c, ime.c)
	ime.log.take()

	// The focused surface dies with an active text input.
	requestProtocol(t, app.c, app.top, xdgshell.ToplevelRequestDestroy)
	requestProtocol(t, app.c, app.surf, wayland.SurfaceRequestDestroy)
	settle(t, app.c, ime.c)
	unmapped(t, events, app.window)
	waitFocus(t, s, 0)
	settle(t, ime.c)
	expectEvents(t, "ime", ime.log.take(), "deactivate", "done")

	other := newTextApp(t, s, events, commands, dir)
	other.enable(t)
	settle(t, other.c, ime.c)
	ime.log.take()
	_ = other.c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(ime.log.events) == 0 && time.Now().Before(deadline) {
		roundtrip(t, ime.c)
		time.Sleep(5 * time.Millisecond)
	}
	expectEvents(t, "ime", ime.log.take(), "deactivate", "done")
	unmapped(t, events, other.window)

	// With the grabbing input method gone, keys reach the client again.
	last := newTextApp(t, s, events, commands, dir)
	_ = ime.c.Close()
	last.keys.opcodes = nil
	deadline = time.Now().Add(2 * time.Second)
	for !slices.Contains(last.keys.opcodes, uint16(wayland.KeyboardEventKey)) && time.Now().Before(deadline) {
		commands <- ports.ForwardKey{ID: last.window, Key: ports.KeyEvent{Keycode: 30, Pressed: true}}
		commands <- ports.ForwardKey{ID: last.window, Key: ports.KeyEvent{Keycode: 30}}
		roundtrip(t, last.c)
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Contains(last.keys.opcodes, uint16(wayland.KeyboardEventKey)) {
		t.Fatal("keys did not return to the client")
	}
}

// An input popup gets the text cursor rectangle; a surface with a role
// cannot become one.
func TestInputPopupSurface(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	ime := newIMEApp(t, s, dir)
	app := newTextApp(t, s, events, commands, dir)
	comp := bindProtocol(t, ime.c, "wl_compositor")
	surf, popup := ime.c.AllocateID(), ime.c.AllocateID()
	registerProtocol(t, ime.c, surf)
	requestProtocol(t, ime.c, comp, wayland.CompositorRequestCreateSurface, surf)
	rects := newIMELog(ime.c, popup, "text_input_rectangle:iiii")
	ime.request(t, inputmethod.ZwpInputMethodV2RequestGetInputPopupSurface, popup, surf)
	app.request(t, textinput.ZwpTextInputV3RequestEnable)
	app.request(t, textinput.ZwpTextInputV3RequestSetCursorRectangle, int32(10), int32(20), int32(2), int32(16))
	app.request(t, textinput.ZwpTextInputV3RequestCommit)
	settle(t, ime.c, app.c, ime.c)
	expectEvents(t, "popup", rects.take(), "text_input_rectangle 10 20 2 16")

	again := ime.c.AllocateID()
	registerProtocol(t, ime.c, again)
	ime.request(t, inputmethod.ZwpInputMethodV2RequestGetInputPopupSurface, again, surf)
	expectProtocolError(t, ime.c, ime.im, uint32(inputmethod.ZwpInputMethodV2ErrorRole))
}
