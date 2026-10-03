package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/inputmethod"
	"github.com/bnema/go-wayland-bindings/server/textinput"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// Text input (zwp_text_input_v3) lets clients take IME text; one input
// method (zwp_input_method_v2, e.g. fcitx5 or IBus) composes it. Text
// input focus follows keyboard focus. The first focused text input that
// commits enable is the active one: the input method serves only it.

// maxIMEText is the spec limit for strings carried by both protocols.
const maxIMEText = 4000

// textState is the double-buffered state of a text input.
type textState struct {
	enabled        bool
	surrounding    string
	cursor, anchor uint32
	hasSurrounding bool
	cause          uint32
	hint, purpose  uint32
	rect           ports.Rect
	hasRect        bool
}

type textInput struct {
	server *Server
	res    *textinput.ZwpTextInputV3
	client server.Client
	// focus is the entered surface, nil after leave.
	focus            *wayland.Surface
	pending, current textState
	// toggled: an enable or disable request is pending.
	toggled bool
	// serial counts commit requests, the serial of done events.
	serial uint32
}

func registerTextInput(d *server.Display, s *Server) error {
	return textinput.NewZwpTextInputManagerV3Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = textinput.NewZwpTextInputManagerV3(c, int32(v), id, textInputManager{s})
	})
}

type textInputManager struct{ server *Server }

func (textInputManager) Destroy(*textinput.ZwpTextInputManagerV3) {}

func (m textInputManager) GetTextInput(r *textinput.ZwpTextInputManagerV3, id uint32, _ *wayland.Seat) {
	s := m.server
	t := &textInput{server: s, client: r.Client()}
	res, err := textinput.NewZwpTextInputV3(r.Client(), r.Version(), id, t)
	if err != nil {
		return
	}
	t.res = res
	s.textInputs = append(s.textInputs, t)
	res.OnDestroy = func() {
		s.textInputs = removeItem(s.textInputs, t)
		if s.activeText == t {
			s.deactivateText()
		}
		t.focus = nil
	}
	if surf, _ := s.focusTarget(s.seat.focused); surf != nil && surf.Client() == t.client {
		t.enter(surf)
	}
}

func (*textInput) Destroy(*textinput.ZwpTextInputV3) {}

// Enable resets the pending state, as the spec asks.
func (t *textInput) Enable(*textinput.ZwpTextInputV3) {
	t.pending, t.toggled = textState{enabled: true}, true
}

// Disable invalidates the pending state.
func (t *textInput) Disable(*textinput.ZwpTextInputV3) {
	t.pending, t.toggled = textState{}, true
}

// SetSurroundingText ignores text past the spec limit or a cursor or
// anchor outside it.
func (t *textInput) SetSurroundingText(_ *textinput.ZwpTextInputV3, text string, cursor, anchor int32) {
	if t.server.protected() {
		return
	}
	if len(text) > maxIMEText || cursor < 0 || anchor < 0 || int(cursor) > len(text) || int(anchor) > len(text) {
		t.server.log.Debug().Int("bytes", len(text)).Msg("surrounding text ignored")
		return
	}
	p := &t.pending
	p.surrounding, p.cursor, p.anchor, p.hasSurrounding = text, uint32(cursor), uint32(anchor), true
}

func (t *textInput) SetTextChangeCause(_ *textinput.ZwpTextInputV3, cause uint32) {
	t.pending.cause = cause
}

func (t *textInput) SetContentType(_ *textinput.ZwpTextInputV3, hint, purpose uint32) {
	t.pending.hint, t.pending.purpose = hint, purpose
}

func (t *textInput) SetCursorRectangle(_ *textinput.ZwpTextInputV3, x, y, w, h int32) {
	t.pending.rect, t.pending.hasRect = ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}, true
}

// Commit applies the pending state. Every commit counts for the done
// serial, but after leave the state is ignored until the next enter.
func (t *textInput) Commit(*textinput.ZwpTextInputV3) {
	t.serial++
	if t.server.protected() {
		t.pending, t.current, t.toggled = textState{}, textState{}, false
		return
	}
	if t.focus == nil {
		return
	}
	reenabled := t.toggled && t.pending.enabled
	t.current = t.pending
	t.pending.cause = uint32(textinput.ZwpTextInputV3ChangeCauseInputMethod)
	t.toggled = false
	t.server.textCommitted(t, reenabled)
}

// Version 2 requests: the global is version 1.
func (*textInput) SetAvailableActions(*textinput.ZwpTextInputV3, []byte) {}
func (*textInput) ShowInputPanel(*textinput.ZwpTextInputV3)              {}
func (*textInput) HideInputPanel(*textinput.ZwpTextInputV3)              {}

// enter focuses the text input on surf; its state starts over.
func (t *textInput) enter(surf *wayland.Surface) {
	if t.server.protected() {
		return
	}
	t.focus = surf
	t.pending, t.current, t.toggled = textState{}, textState{}, false
	t.res.SendEnter(surf)
}

// leave clears the focus; a destroyed surface gets no leave event.
func (t *textInput) leave() {
	s := t.server
	if s.activeText == t {
		s.deactivateText()
	}
	if t.focus.Resource.Alive() {
		t.res.SendLeave(t.focus)
	}
	t.focus = nil
	t.pending, t.current, t.toggled = textState{}, textState{}, false
}

// textCommitted activates, updates or deactivates the input method for a
// committed text input. Enabling a second text input is ignored.
func (s *Server) textCommitted(t *textInput, reenabled bool) {
	if s.protected() {
		return
	}
	switch {
	case t.current.enabled && s.activeText == nil:
		s.activeText = t
		s.activateIM()
	case t.current.enabled && s.activeText == t:
		if reenabled {
			s.activateIM()
		} else {
			s.sendIMState()
		}
	case !t.current.enabled && s.activeText == t:
		s.deactivateText()
	}
}

// textInputFocus moves text input focus to the keyboard focus: leave for
// text inputs on another surface, then enter for those of the focused
// client, as the spec orders them.
func (s *Server) textInputFocus() {
	surf, _ := s.focusTarget(s.seat.focused)
	for _, t := range s.textInputs {
		if t.focus != nil && (surf == nil || t.focus.Resource != surf.Resource) {
			t.leave()
		}
	}
	if surf == nil {
		return
	}
	for _, t := range s.textInputs {
		if t.focus == nil && t.client == surf.Client() {
			t.enter(surf)
		}
	}
}

// deactivateText drops the active text input and tells the input method.
func (s *Server) deactivateText() {
	s.activeText = nil
	im := s.inputMethod
	if im == nil {
		return
	}
	im.res.SendDeactivate()
	im.done()
}

// activateIM sends activate and the active text input's state.
func (s *Server) activateIM() {
	im := s.inputMethod
	if s.protected() || im == nil || s.activeText == nil {
		return
	}
	im.pending = imState{}
	im.activated = im.dones
	im.res.SendActivate()
	s.sendIMState()
}

// sendIMState sends the active text input's state, then done.
func (s *Server) sendIMState() {
	im, t := s.inputMethod, s.activeText
	if s.protected() || im == nil || t == nil {
		return
	}
	c := t.current
	if c.hasSurrounding {
		im.res.SendSurroundingText(c.surrounding, c.cursor, c.anchor)
	}
	im.res.SendTextChangeCause(c.cause)
	im.res.SendContentType(c.hint, c.purpose)
	for _, p := range im.popups {
		p.sendRect()
	}
	im.done()
}

// imState is the double-buffered state of the input method.
type imState struct {
	preedit       string
	begin, end    int32
	hasPreedit    bool
	commit        string
	hasCommit     bool
	before, after uint32
}

type inputMethod struct {
	server *Server
	res    *inputmethod.ZwpInputMethodV2
	// inert: another input method was bound first; requests are ignored.
	inert bool
	dones uint32
	// activated is dones when the last activate was sent: a commit with
	// a serial up to it predates the activation.
	activated uint32
	pending   imState
	grab      *keyboardGrab
	popups    []*inputPopup
}

func registerInputMethod(d *server.Display, s *Server) error {
	return inputmethod.NewZwpInputMethodManagerV2Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = inputmethod.NewZwpInputMethodManagerV2(c, int32(v), id, inputMethodManager{s})
	})
}

type inputMethodManager struct{ server *Server }

func (inputMethodManager) Destroy(*inputmethod.ZwpInputMethodManagerV2) {}

// GetInputMethod makes the first input method the seat's; later ones get
// unavailable until it is gone.
func (m inputMethodManager) GetInputMethod(r *inputmethod.ZwpInputMethodManagerV2, _ *wayland.Seat, id uint32) {
	s := m.server
	im := &inputMethod{server: s, inert: s.inputMethod != nil}
	res, err := inputmethod.NewZwpInputMethodV2(r.Client(), r.Version(), id, im)
	if err != nil {
		return
	}
	im.res = res
	if im.inert {
		res.SendUnavailable()
		s.log.Debug().Int("pid", r.Client().PID()).Msg("input method unavailable")
		return
	}
	s.inputMethod = im
	res.OnDestroy = func() { s.inputMethodGone(im) }
	s.log.Info().Int("pid", r.Client().PID()).Msg("input method bound")
	s.activateIM()
}

// inputMethodGone frees the seat slot. A grab ends; the active text
// input's preedit is cleared by an empty done.
func (s *Server) inputMethodGone(im *inputMethod) {
	if s.inputMethod != im {
		return
	}
	s.inputMethod = nil
	if im.grab != nil {
		im.grab = nil
		s.grabEnded()
	}
	if t := s.activeText; t != nil {
		t.res.SendDone(t.serial)
	}
	s.log.Info().Msg("input method gone")
}

// done sends done and counts it for commit serials.
func (im *inputMethod) done() {
	im.res.SendDone()
	im.dones++
}

func (im *inputMethod) CommitString(_ *inputmethod.ZwpInputMethodV2, text string) {
	if im.server.protected() || im.inert || !im.fits(text) {
		return
	}
	im.pending.commit, im.pending.hasCommit = text, true
}

func (im *inputMethod) SetPreeditString(_ *inputmethod.ZwpInputMethodV2, text string, begin, end int32) {
	if im.server.protected() || im.inert || !im.fits(text) {
		return
	}
	p := &im.pending
	p.preedit, p.begin, p.end, p.hasPreedit = text, begin, end, true
}

func (im *inputMethod) fits(text string) bool {
	if len(text) > maxIMEText {
		im.server.log.Debug().Int("bytes", len(text)).Msg("input method text too long")
		return false
	}
	return true
}

func (im *inputMethod) DeleteSurroundingText(_ *inputmethod.ZwpInputMethodV2, before, after uint32) {
	if im.server.protected() || im.inert {
		return
	}
	im.pending.before, im.pending.after = before, after
}

// Commit sends the pending state to the active text input, in the
// spec's order, with done carrying the text input's commit count.
// A commit from before the last activate is dropped: its text belongs to
// the previous text input. Within one activation, like wlroots, a stale
// serial is only logged: dropping the commit would lose typed text.
func (im *inputMethod) Commit(_ *inputmethod.ZwpInputMethodV2, serial uint32) {
	if im.inert {
		return
	}
	st := im.pending
	im.pending = imState{}
	s := im.server
	if s.protected() {
		return
	}
	t := s.activeText
	if t == nil {
		return
	}
	if serial <= im.activated {
		s.log.Debug().Uint32("serial", serial).Uint32("activated", im.activated).Msg("input method commit before activation dropped")
		return
	}
	if serial != im.dones {
		s.log.Debug().Uint32("serial", serial).Uint32("done", im.dones).Msg("input method commit with stale serial")
	}
	if st.hasPreedit {
		t.res.SendPreeditString(st.preedit, st.begin, st.end)
	}
	if st.hasCommit {
		t.res.SendCommitString(st.commit)
	}
	if st.before != 0 || st.after != 0 {
		t.res.SendDeleteSurroundingText(st.before, st.after)
	}
	t.res.SendDone(t.serial)
}

// GetInputPopupSurface gives a surface the input_popup role. The popup
// learns the text cursor rectangle; it is not drawn.
func (im *inputMethod) GetInputPopupSurface(r *inputmethod.ZwpInputMethodV2, id uint32, surf *wayland.Surface) {
	s := im.server
	if im.inert {
		// An unavailable input method's requests have no effect.
		_, _ = inputmethod.NewZwpInputPopupSurfaceV2(r.Client(), r.Version(), id, &inputPopup{im: im})
		return
	}
	if surf == nil {
		return
	}
	state := s.surfaces[surf.Resource]
	if state == nil {
		return
	}
	if state.kind != roleNone {
		r.PostError(uint32(inputmethod.ZwpInputMethodV2ErrorRole), "surface already has a role")
		return
	}
	p := &inputPopup{im: im}
	res, err := inputmethod.NewZwpInputPopupSurfaceV2(r.Client(), r.Version(), id, p)
	if err != nil {
		return
	}
	state.kind, p.res = roleInputPopup, res
	im.popups = append(im.popups, p)
	res.OnDestroy = func() { im.popups = removeItem(im.popups, p) }
	if s.activeText != nil {
		p.sendRect()
	}
}

// GrabKeyboard routes seat keys to the input method. A second grab
// while one is held stays inert.
func (im *inputMethod) GrabKeyboard(r *inputmethod.ZwpInputMethodV2, id uint32) {
	g := &keyboardGrab{}
	res, err := inputmethod.NewZwpInputMethodKeyboardGrabV2(r.Client(), r.Version(), id, g)
	if err != nil || im.server.protected() || im.inert || im.grab != nil {
		return
	}
	g.res, im.grab = res, g
	res.OnDestroy = func() {
		if im.grab == g {
			im.grab = nil
			im.server.grabEnded()
		}
	}
	s := im.server
	if s.seat.keymapFD >= 0 {
		res.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), s.seat.keymapFD, s.seat.keymapSize)
	}
	res.SendRepeatInfo(int32(s.seat.repeatRate), int32(s.seat.repeatDelay))
	s.serial++
	m := s.seat.modState
	res.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
	s.log.Debug().Msg("input method grabbed keyboard")
}

func (*inputMethod) Destroy(*inputmethod.ZwpInputMethodV2) {}

// inputPopup is an input method's candidate surface.
type inputPopup struct {
	im  *inputMethod
	res *inputmethod.ZwpInputPopupSurfaceV2
}

func (*inputPopup) Destroy(*inputmethod.ZwpInputPopupSurfaceV2) {}

// sendRect tells the popup where the active text cursor is.
func (p *inputPopup) sendRect() {
	if p.im.server.protected() {
		return
	}
	t := p.im.server.activeText
	if t == nil || !t.current.hasRect {
		return
	}
	r := t.current.rect
	p.res.SendTextInputRectangle(int32(r.X), int32(r.Y), int32(r.W), int32(r.H))
}

type keyboardGrab struct {
	res *inputmethod.ZwpInputMethodKeyboardGrabV2
}

func (*keyboardGrab) Release(*inputmethod.ZwpInputMethodKeyboardGrabV2) {}

// grab is the live keyboard grab, nil without one.
func (s *Server) grab() *keyboardGrab {
	if s.protected() {
		return nil
	}
	if im := s.inputMethod; im != nil {
		return im.grab
	}
	return nil
}

// grabKey sends a focused key to the input method grab and reports
// whether it consumed it. The release of a key the client got pressed
// goes to the client; the release of a key the grab got pressed never
// does, even after the grab ends.
func (s *Server) grabKey(c ports.ForwardKey) bool {
	code := c.Key.Keycode
	if s.protected() {
		if !c.Key.Pressed && s.seat.grabKeys[code] {
			delete(s.seat.grabKeys, code)
			return true
		}
		return false // protected seat routing belongs to the parent
	}
	g := s.grab()
	owned := !c.Key.Pressed && s.seat.grabKeys[code]
	if owned {
		delete(s.seat.grabKeys, code)
		if g == nil {
			s.grabReleaseDropped(c.Key.State)
			return true
		}
	} else if g == nil || c.ID != s.seat.focused || !c.Key.Pressed && s.seat.heldKeys[code] {
		return false
	}
	state := uint32(0)
	if c.Key.Pressed {
		state = 1
		if s.seat.grabKeys == nil {
			s.seat.grabKeys = make(map[uint32]bool)
		}
		s.seat.grabKeys[code] = true
	}
	s.serial++
	g.res.SendKey(s.serial, wireMsec(c.Key.Time), c.Key.Keycode, state)
	if c.Key.State != s.seat.modState {
		s.seat.modState = c.Key.State
		m := s.seat.modState
		s.serial++
		g.res.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
	}
	return true
}

// grabReleaseDropped keeps the focused client's modifiers right when the
// release of a grab-owned key is dropped.
func (s *Server) grabReleaseDropped(m ports.ModState) {
	if s.protected() {
		return
	}
	if m == s.seat.modState {
		return
	}
	s.seat.modState = m
	s.serial++
	for _, k := range s.clientKeyboards(s.focusClient()) {
		s.sendModifiers(k)
	}
}

// grabEnded gives the focused client the seat modifiers it missed.
func (s *Server) grabEnded() {
	if s.protected() {
		return
	}
	s.serial++
	for _, k := range s.clientKeyboards(s.focusClient()) {
		s.sendModifiers(k)
	}
	s.log.Debug().Msg("input method keyboard grab ended")
}

// sendGrabKeymap updates a grab after a seat keymap or repeat change.
func (s *Server) sendGrabKeymap(keymap bool) {
	g := s.grab()
	if g == nil {
		return
	}
	if keymap && s.seat.keymapFD >= 0 {
		g.res.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), s.seat.keymapFD, s.seat.keymapSize)
	}
	g.res.SendRepeatInfo(int32(s.seat.repeatRate), int32(s.seat.repeatDelay))
}
