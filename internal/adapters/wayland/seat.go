package wayland

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/bnema/go-wayland-bindings/server/inputtimestamps"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// seatState is the wl_seat state: who has keyboard and pointer focus, the
// bound keyboards and pointers, the keymap they carry, and the last press.
// It belongs to the display goroutine.
type seatState struct {
	focused      domain.WindowID
	pointerFocus domain.WindowID
	entered      *wayland.Surface
	buttons      map[uint32]bool
	// pointerX, pointerY is the pointer as core placed it in pointerFocus;
	// pointerSurface and surfacePoint turn it into surface coordinates.
	pointerX, pointerY float64
	// enters holds the serial of the enter event each wl_pointer received
	// for pointerFocus; a pointer warp must name its pointer's serial.
	enters map[*server.Resource]uint32
	// gesture is the touchpad gesture sent to pointerFocus's client.
	gesture     *activeGesture
	wheelRest   [2]int32   // v120 not yet sent as axis_discrete, per axis
	wheelHeld   [2]float64 // axis value held back with it for pre-v8 clients
	pointers    map[server.Client][]*wayland.Pointer
	keyboards   map[server.Client][]*wayland.Keyboard
	modState    domain.ModState
	heldKeys    map[uint32]bool
	grabKeys    map[uint32]bool // pressed through the input method grab
	keymapFD    int
	keymapSize  uint32
	keymapText  string           // the seat keymap, to compare virtual keymaps with
	keymapOwner *virtualKeyboard // nil: keyboards carry the seat keymap
	repeatRate  int
	repeatDelay int
	// press is the serial of the last button or key press, sent to
	// pressClient: popup grabs must come from it.
	press       uint32
	pressClient server.Client
	// grabSerial is the serial of the press that started the implicit
	// grab (core marks it), grabButton its button: the only serial
	// start_drag accepts (wlr_seat_validate_pointer_grab_serial). The
	// grab's release clears it; core may consume that release (a window
	// drag), and then the next grab press replaces it.
	grabSerial uint32
	grabButton uint32
	// pressAt and focusAt date the last press and keyboard focus change,
	// for xdg-activation tokens.
	pressAt, focusAt time.Time
	// keyStamps and pointerStamps hold the zwp_input_timestamps_v1 objects
	// subscribed to each device; the lists are replaced, never mutated.
	keyStamps, pointerStamps map[*server.Resource][]*inputtimestamps.ZwpInputTimestampsV1
}

// applyInput delivers a core input command to the focused client.
func (s *Server) applyInput(cmd ports.ClientCommand) {
	if s.security != nil && s.security.Snapshot().Protected {
		var target domain.WindowID
		switch c := cmd.(type) {
		case ports.SetKeymap:
			// Configuration updates do not authorize normal-client input.
		case ports.PointerFocus:
			target = c.ID
		case ports.PointerMotionTo:
			target = c.ID
		case ports.PointerButtonTo:
			target = c.ID
		case ports.PointerAxisTo:
			target = c.ID
		case ports.GestureBeginTo:
			target = c.ID
		case ports.GestureUpdateTo:
			target = c.ID
		case ports.GestureEndTo:
			target = c.ID
		case ports.FocusWindow:
			target = c.ID
		case ports.ForwardKey:
			target = c.ID
		case ports.SetModifiers:
			target = c.ID
		}
		if target != 0 && !s.lockInputTarget(target) {
			return
		}
	}
	switch c := cmd.(type) {
	case ports.PointerFocus:
		s.changePointerFocus(c.ID, c.X, c.Y)
	case ports.PointerMotionTo:
		if l := s.lockSurfaces[c.ID]; l != nil && s.lockInputTarget(c.ID) && c.ID == s.seat.pointerFocus {
			s.seat.pointerX, s.seat.pointerY = c.X, c.Y
			for _, p := range s.clientPointers(l.resource.Client()) {
				s.stampPointer(p, c.Time)
				p.SendMotion(wireMsec(c.Time), server.FixedFromFloat(c.X), server.FixedFromFloat(c.Y))
				pointerFrame(p)
			}
			return
		}
		if w := s.windows[c.ID]; w != nil {
			if s.relativeMotion(w, c) && s.locked(w) {
				for _, p := range s.windowPointers(w) {
					pointerFrame(p)
				}
			}
			if s.locked(w) {
				// Only relative motion reaches a locked pointer.
				return
			}
			s.pointerMotionSurface(c)
			s.seat.pointerX, s.seat.pointerY = c.X, c.Y
			// A constraint waits for the pointer to enter its region.
			if s.constraint == nil {
				s.updateConstraint()
			}
		} else if s.layers[c.ID] != nil {
			s.pointerMotionSurface(c)
		}
	case ports.PointerButtonTo:
		// Releases may target the implicit-grab window after focus has moved.
		if c.Pressed && c.Grab || !c.Pressed && c.Button == s.seat.grabButton {
			// A new grab, or the end of this one even when its window is
			// gone.
			s.seat.grabSerial, s.seat.grabButton = 0, 0
		}
		if client, pointers, ok := s.pointerTarget(c.ID); ok && s.seat.entered != nil {
			if s.seat.buttons == nil {
				s.seat.buttons = make(map[uint32]bool)
			}
			if c.Pressed {
				s.seat.buttons[c.Button] = true
			} else {
				delete(s.seat.buttons, c.Button)
			}
			s.serial++
			state := uint32(0)
			if c.Pressed {
				state = 1
				s.seat.press, s.seat.pressClient, s.seat.pressAt = s.serial, client, time.Now()
				if c.Grab {
					s.seat.grabSerial, s.seat.grabButton = s.serial, c.Button
				}
			}
			for _, p := range pointers {
				s.stampPointer(p, c.Time)
				p.SendButton(s.serial, wireMsec(c.Time), c.Button, state)
				pointerFrame(p)
			}
			// The final release belongs to the grab, then focus follows the
			// current position even if the next click arrives without motion.
			if !c.Pressed && len(s.seat.buttons) == 0 {
				s.changePointerFocus(s.seat.pointerFocus, s.seat.pointerX, s.seat.pointerY)
			}
		}
	case ports.PointerAxisTo:
		// Scroll applies to the entered surface only.
		if c.ID != s.seat.pointerFocus {
			return
		}
		steps, values := s.seat.wheelSteps(c.Axis)
		_, pointers, _ := s.pointerTarget(c.ID)
		for _, p := range pointers {
			s.sendAxis(p, c.Axis, steps, values)
		}
	case ports.GestureBeginTo:
		s.beginGesture(c)
	case ports.GestureUpdateTo:
		s.updateGesture(c)
	case ports.GestureEndTo:
		s.endGesture(c)
	case ports.SetKeymap:
		s.setKeymap(c)
	case ports.KeyboardReset:
		s.resetKeyboard(c.State)
	case ports.FocusWindow:
		if s.seat.focused != c.ID {
			s.changeFocus(c.ID)
		}
	case ports.SetModifiers:
		s.setModifiers(c)
	case ports.ForwardKey:
		if !(s.security != nil && s.security.Snapshot().Protected) && s.grabKey(c) {
			return
		}
		_, keyboards := s.focusTarget(c.ID)
		if c.ID != s.seat.focused || len(keyboards) == 0 {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("ignored forward key")
			return
		}
		s.useKeymap(nil)
		if s.seat.heldKeys == nil {
			s.seat.heldKeys = make(map[uint32]bool)
		}
		if c.Key.Pressed {
			s.seat.heldKeys[c.Key.Keycode] = true
		} else {
			delete(s.seat.heldKeys, c.Key.Keycode)
		}
		s.serial++
		state := uint32(0)
		if c.Key.Pressed {
			state = 1
		}
		if c.Key.Pressed {
			if surf, _ := s.focusTarget(c.ID); surf != nil {
				s.seat.press, s.seat.pressClient, s.seat.pressAt = s.serial, surf.Client(), time.Now()
			}
		}
		for _, k := range keyboards {
			s.stampKeyboard(k, c.Key.Time)
			k.SendKey(s.serial, wireMsec(c.Key.Time), c.Key.Keycode, state)
		}
		if c.Key.State != s.seat.modState {
			s.seat.modState = c.Key.State
			s.serial++
			for _, k := range keyboards {
				s.sendModifiers(k)
			}
		}
	}
}

// xkb modifier masks of the default keymaps: Shift, Control, Mod1 (Alt) and
// Mod4 (Super). The server does not parse the keymap for its modifier indices.
const (
	xkbShift   = 1 << 0
	xkbControl = 1 << 2
	xkbMod1    = 1 << 3
	xkbMod4    = 1 << 6
)

// depressedMask maps core modifier flags to xkb depressed masks.
func depressedMask(m domain.Mods) uint32 {
	var d uint32
	for _, f := range []struct {
		flag domain.Mods
		mask uint32
	}{{domain.ModShift, xkbShift}, {domain.ModCtrl, xkbControl}, {domain.ModAlt, xkbMod1}, {domain.ModSuper, xkbMod4}} {
		if m&f.flag != 0 {
			d |= f.mask
		}
	}
	return d
}

// setModifiers overrides, then restores, the modifiers the focused window's
// keyboards (or the input method grab) see. The override replaces the
// depressed modifiers and keeps latched, locked and group; the seat's real
// modState is only replaced by a Restore, with the state core carries.
func (s *Server) setModifiers(c ports.SetModifiers) {
	_, keyboards := s.focusTarget(c.ID)
	if c.ID != s.seat.focused || len(keyboards) == 0 {
		s.log.Debug().Uint64("id", uint64(c.ID)).Msg("ignored set modifiers")
		return
	}
	s.useKeymap(nil)
	s.serial++
	if c.Restore {
		// Swallowed keys never reached the seat: core's snapshot is the
		// real state.
		s.seat.modState = c.State
	}
	m := s.seat.modState
	if !c.Restore {
		m.Depressed = depressedMask(c.Mods)
	}
	for _, k := range keyboards {
		k.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
	}
	if g := s.grab(); g != nil {
		g.res.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
	}
}

func (s *Server) windowKeyboards(w *window) []*wayland.Keyboard {
	if w == nil || !w.mapped || !w.xdg.resource.Resource.Alive() {
		return nil
	}
	return s.clientKeyboards(w.xdg.resource.Client())
}

// clientKeyboards returns c's wl_keyboards without allocating. The list holds
// only live resources (OnDestroy removes a keyboard when it dies) and is
// replaced, never mutated, so callers may iterate it while sending.
//
// This relies on purego-libwayland: destroyed() sets gone and runs OnDestroy
// synchronously on the display goroutine, and PostEvent never re-enters Go, so
// a list ranged over while sending is never changed under the caller.
func (s *Server) clientKeyboards(c server.Client) []*wayland.Keyboard {
	return s.seat.keyboards[c]
}

// focusTarget resolves a window or layer ID to its wl_surface and keyboards.
func (s *Server) focusTarget(id domain.WindowID) (*wayland.Surface, []*wayland.Keyboard) {
	if l := s.lockSurfaces[id]; l != nil && !l.closed && l.surface.wl.Alive() {
		// Outgoing leaves also resolve a role whose OnDestroy resource is
		// already dead. Admission is enforced before entering or delivering.
		return l.surface.wl, s.clientKeyboards(l.surface.wl.Client())
	}
	if w := s.windows[id]; w != nil && w.mapped {
		if surf := w.xdg.surfaceResource(); surf != nil {
			return surf, s.windowKeyboards(w)
		}
	}
	if l := s.layers[id]; l != nil && l.mapped && l.resource.Resource.Alive() {
		if surf := l.surfaceResource(); surf != nil {
			return surf, s.clientKeyboards(l.resource.Client())
		}
	}
	return nil, nil
}

// pointerTarget resolves a mapped window or layer ID to its client and
// live pointers.
func (s *Server) pointerTarget(id domain.WindowID) (server.Client, []*wayland.Pointer, bool) {
	if l := s.lockSurfaces[id]; l != nil && s.lockInputTarget(id) {
		return l.resource.Client(), s.clientPointers(l.resource.Client()), true
	}
	if w := s.windows[id]; w != nil && w.mapped && w.xdg.resource.Resource.Alive() {
		return w.xdg.resource.Client(), s.windowPointers(w), true
	}
	if l := s.layers[id]; l != nil && l.mapped && l.resource.Resource.Alive() {
		return l.resource.Client(), s.layerPointers(l), true
	}
	return server.Client{}, nil, false
}

// pointerSurface is the wl_surface of a mapped window or layer and the
// point in its surface coordinates.
func (s *Server) pointerSurface(id domain.WindowID, x, y float64) (*wayland.Surface, []*wayland.Pointer, float64, float64) {
	if l := s.lockSurfaces[id]; l != nil && !l.closed && l.surface.wl.Alive() {
		return l.surface.wl, s.clientPointers(l.surface.wl.Client()), x, y
	}
	if w := s.windows[id]; w != nil && w.mapped {
		if surf := w.xdg.surface.wl; surf != nil && surf.Resource.Alive() {
			x, y := w.surfacePoint(x, y)
			return s.resolvePointerSurface(surf, s.windowPointers(w), x, y)
		}
	}
	if l := s.layers[id]; l != nil && l.mapped {
		if surf := l.surface.wl; surf != nil {
			return s.resolvePointerSurface(surf, s.layerPointers(l), x, y)
		}
	}
	return nil, nil, 0, 0
}

// keymapFile writes a sealed memfd holding the NUL-terminated keymap.
func keymapFile(keymap string) (int, uint32, error) {
	if uint64(len(keymap))+1 > uint64(^uint32(0)) {
		return -1, 0, fmt.Errorf("keymap too large")
	}
	fd, err := unix.MemfdCreate("neferwl-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, 0, err
	}
	n, err := unix.Write(fd, append([]byte(keymap), 0))
	if err == nil && n != len(keymap)+1 {
		err = fmt.Errorf("short keymap write")
	}
	if err == nil {
		_, err = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL)
	}
	if err != nil {
		unix.Close(fd)
		return -1, 0, err
	}
	return fd, uint32(n), nil
}

// setKeymap sends repeat info, and a new keymap when one is given, to every keyboard.
// A new keymap resets held keys and modifiers: the focused client gets leave, then
// enter with no keys.
func (s *Server) setKeymap(c ports.SetKeymap) {
	s.seat.repeatRate, s.seat.repeatDelay = c.RepeatRate, c.RepeatDelay
	if c.Keymap == "" {
		for _, list := range s.seat.keyboards {
			for _, k := range list {
				if k.Version() >= 4 {
					k.SendRepeatInfo(int32(s.seat.repeatRate), int32(s.seat.repeatDelay))
				}
			}
		}
		s.sendGrabKeymap(false)
		s.log.Info().Int("rate", s.seat.repeatRate).Int("delay", s.seat.repeatDelay).Msg("repeat updated")
		return
	}
	fd, size, err := keymapFile(c.Keymap)
	if err != nil {
		s.log.Warn().Err(err).Msg("keymap update failed")
		return
	}
	if s.seat.keymapFD >= 0 {
		unix.Close(s.seat.keymapFD)
	}
	s.seat.keymapFD, s.seat.keymapSize, s.seat.keymapText = fd, size, c.Keymap
	s.seat.keymapOwner = nil
	focused := s.seat.focused
	s.changeFocus(0)
	s.seat.heldKeys, s.seat.grabKeys = nil, nil
	s.seat.modState = domain.ModState{}
	for _, list := range s.seat.keyboards {
		for _, k := range list {
			k.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), s.seat.keymapFD, s.seat.keymapSize)
			if k.Version() >= 4 {
				k.SendRepeatInfo(int32(s.seat.repeatRate), int32(s.seat.repeatDelay))
			}
		}
	}
	s.sendGrabKeymap(true)
	s.changeFocus(focused)
	s.log.Info().Uint32("size", size).Int("rate", s.seat.repeatRate).Int("delay", s.seat.repeatDelay).Msg("keymap updated")
}

// resetKeyboard forgets the held keys and modifiers: the focused client gets
// leave, then enter with no keys and modifiers 0.
// An input-method grab gets a release for each key it holds and the kept
// state first; the seat then adopts that state (locked modifiers and group
// survive), so the first key after the reset is read against it.
func (s *Server) resetKeyboard(kept domain.ModState) {
	if g := s.grab(); g != nil && !s.protected() {
		for code := range s.seat.grabKeys {
			s.serial++
			g.res.SendKey(s.serial, 0, code, 0)
		}
		s.serial++
		g.res.SendModifiers(s.serial, kept.Depressed, kept.Latched, kept.Locked, kept.Group)
	}
	focused := s.seat.focused
	s.changeFocus(0)
	s.seat.heldKeys, s.seat.grabKeys = nil, nil
	s.seat.modState = kept
	s.changeFocus(focused)
}

func (s *Server) sendModifiers(k *wayland.Keyboard) {
	m := s.seat.modState
	k.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
}

func (s *Server) changeFocus(id domain.WindowID) {
	if s.security != nil && s.security.Snapshot().Protected && id != 0 && !s.lockInputTarget(id) {
		return
	}
	// Enter carries seat held keys and modifiers: they need the seat keymap.
	s.useKeymap(nil)
	old := s.focusClient()
	defer func() {
		// Tokens of a client losing the focus die with it.
		if now := s.focusClient(); now != old {
			s.dropTokens(old)
			s.seat.focusAt = time.Now()
		}
	}()
	if surf, keyboards := s.focusTarget(s.seat.focused); surf != nil {
		for _, k := range keyboards {
			s.serial++
			k.SendLeave(s.serial, surf)
		}
	}
	s.seat.focused = 0
	if surf, keyboards := s.focusTarget(id); surf != nil {
		s.seat.focused = id
		s.focusSelections()
		for _, k := range keyboards {
			s.serial++
			var keys []byte
			for code := range s.seat.heldKeys {
				keys = binary.LittleEndian.AppendUint32(keys, code)
			}
			k.SendEnter(s.serial, surf, keys)
			s.sendModifiers(k)
		}
	}
	s.textInputFocus()
	s.log.Debug().Uint64("id", uint64(s.seat.focused)).Msg("keyboard focus")
	s.updateConstraint()
}

// wheelSteps adds wheel v120 to the rest per axis and returns the whole
// detents for axis_discrete (seat before v8). High-resolution wheels send
// fractions of 120; a direction change drops the rest, as wlroots does.
// wheelHeld keeps the axis value of the frames without a step; values is
// what a pre-v8 client gets with a step: the held value plus this frame's.
func (st *seatState) wheelSteps(a ports.PointerAxis) (steps [2]int32, values [2]float64) {
	if a.Source != domain.AxisWheel {
		return steps, values
	}
	for i, ax := range [2]domain.ScrollAxis{a.Vertical, a.Horizontal} {
		if !ax.Set || ax.V120 == 0 {
			continue
		}
		if (st.wheelRest[i] < 0) != (ax.V120 < 0) {
			st.wheelRest[i], st.wheelHeld[i] = 0, 0
		}
		st.wheelRest[i] += ax.V120
		st.wheelHeld[i] += ax.Value
		steps[i] = st.wheelRest[i] / 120
		st.wheelRest[i] -= steps[i] * 120
		if steps[i] != 0 {
			values[i], st.wheelHeld[i] = st.wheelHeld[i], 0
		}
	}
	return steps, values
}

// sendAxis sends one scroll frame, each event gated by the pointer version.
// steps are whole wheel detents for axis_discrete. A pre-v8 client gets a
// wheel axis only with a step, carrying values (all the value since the
// last step), so it never counts smooth and discrete scroll.
func (s *Server) sendAxis(p *wayland.Pointer, a ports.PointerAxis, steps [2]int32, values [2]float64) {
	v := p.Version()
	axes := [2]domain.ScrollAxis{a.Vertical, a.Horizontal}
	for i := range axes {
		if v < 8 && a.Source == domain.AxisWheel && axes[i].V120 != 0 && steps[i] == 0 {
			axes[i].Set = false
		}
	}
	if !axes[0].Set && !axes[1].Set {
		return
	}
	if v >= 5 {
		p.SendAxisSource(uint32(a.Source))
	}
	for axis, ax := range axes {
		if !ax.Set {
			continue
		}
		if ax.Stop {
			if v >= 5 {
				s.stampPointer(p, a.Time)
				p.SendAxisStop(wireMsec(a.Time), uint32(axis))
			}
			continue
		}
		value := ax.Value
		if a.Source == domain.AxisWheel && ax.V120 != 0 {
			switch {
			case v >= 8:
				p.SendAxisValue120(uint32(axis), ax.V120)
			default:
				value = values[axis]
				if v >= 5 {
					p.SendAxisDiscrete(uint32(axis), steps[axis])
				}
			}
		}
		s.stampPointer(p, a.Time)
		p.SendAxis(wireMsec(a.Time), uint32(axis), server.FixedFromFloat(value))
	}
	pointerFrame(p)
}

// wireMsec is a device timestamp as the 32-bit millisecond time of wl_pointer
// and wl_keyboard events (wrapping, like the protocol).
func wireMsec(t time.Duration) uint32 { return uint32(t / time.Millisecond) }

func pointerFrame(p *wayland.Pointer) {
	if p.Version() >= 5 {
		p.SendFrame()
	}
}
func (s *Server) windowPointers(w *window) []*wayland.Pointer {
	if w == nil || !w.mapped || !w.xdg.resource.Resource.Alive() {
		return nil
	}
	return s.clientPointers(w.xdg.resource.Client())
}

func (s *Server) layerPointers(l *layerSurface) []*wayland.Pointer {
	if l == nil || !l.mapped || !l.resource.Resource.Alive() {
		return nil
	}
	return s.clientPointers(l.resource.Client())
}

// clientPointers returns c's wl_pointers without allocating. The list holds
// only live resources (OnDestroy removes a pointer when it dies) and is
// replaced, never mutated, so callers may iterate it while sending. See
// clientKeyboards for the libwayland invariant this relies on.
func (s *Server) clientPointers(c server.Client) []*wayland.Pointer {
	return s.seat.pointers[c]
}
func (s *Server) changePointerFocus(id domain.WindowID, x, y float64) {
	surface, pointers, sx, sy := s.resolvePointerFocus(id, x, y)
	s.enterPointerSurface(id, x, y, surface, pointers, sx, sy)
}

// resolvePointerFocus respects core's window selection before sampling the tree.
func (s *Server) resolvePointerFocus(id domain.WindowID, x, y float64) (*wayland.Surface, []*wayland.Pointer, float64, float64) {
	if s.security != nil && s.security.Snapshot().Protected && id != 0 && !s.lockInputTarget(id) {
		return nil, nil, 0, 0
	}
	if id != s.seat.pointerFocus {
		clear(s.seat.buttons)
	}
	return s.pointerSurface(id, x, y)
}

// enterPointerSurface applies a resolved target; motion shares this result.
func (s *Server) enterPointerSurface(id domain.WindowID, x, y float64, surface *wayland.Surface, pointers []*wayland.Pointer, sx, sy float64) {
	if s.security != nil && s.security.Snapshot().Protected && id != 0 && !s.lockInputTarget(id) {
		return
	}
	if len(s.seat.buttons) != 0 && s.seat.entered != nil {
		return
	}
	if id == s.seat.pointerFocus && surface == s.seat.entered {
		return
	}
	s.seat.wheelRest, s.seat.wheelHeld = [2]int32{}, [2]float64{}
	// A gesture does not follow the pointer to another surface.
	s.cancelGesture()
	// The new client sets its own cursor on enter; until then, the arrow.
	s.cursorSurface = nil
	s.setCursor(domain.CursorChange{})
	if old := s.seat.entered; old != nil && old.Alive() {
		for _, p := range s.clientPointers(old.Client()) {
			s.serial++
			p.SendLeave(s.serial, old)
			pointerFrame(p)
		}
	}
	s.seat.pointerFocus = 0
	s.seat.entered = nil
	clear(s.seat.enters)
	if surface != nil {
		s.seat.entered = surface
		s.seat.pointerFocus, s.seat.pointerX, s.seat.pointerY = id, x, y
		for _, p := range pointers {
			s.serial++
			s.seat.enters[p.Resource] = s.serial
			p.SendEnter(s.serial, surface, server.FixedFromFloat(sx), server.FixedFromFloat(sy))
			pointerFrame(p)
		}
	}
	s.updateConstraint()
}
