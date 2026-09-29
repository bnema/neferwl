package wayland

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// seatState is the wl_seat state: who has keyboard and pointer focus, the
// bound keyboards and pointers, the keymap they carry, and the last press.
// It belongs to the display goroutine.
type seatState struct {
	focused      ports.WindowID
	pointerFocus ports.WindowID
	// pointerX, pointerY is the pointer as core placed it in pointerFocus;
	// pointerSurface and surfacePoint turn it into surface coordinates.
	pointerX, pointerY float64
	// enters holds the serial of the enter event each wl_pointer received
	// for pointerFocus; a pointer warp must name its pointer's serial.
	enters      map[*server.Resource]uint32
	wheelRest   [2]int32   // v120 not yet sent as axis_discrete, per axis
	wheelHeld   [2]float64 // axis value held back with it for pre-v8 clients
	pointers    map[server.Client][]*wayland.Pointer
	keyboards   map[server.Client][]*wayland.Keyboard
	modState    ports.ModState
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
	// pressAt and focusAt date the last press and keyboard focus change,
	// for xdg-activation tokens.
	pressAt, focusAt time.Time
}

// applyInput delivers a core input command to the focused client.
func (s *Server) applyInput(cmd ports.ClientCommand) {
	switch c := cmd.(type) {
	case ports.PointerFocus:
		s.changePointerFocus(c.ID, c.X, c.Y)
	case ports.PointerMotionTo:
		if l := s.layers[c.ID]; l != nil && c.ID == s.seat.pointerFocus {
			s.seat.pointerX, s.seat.pointerY = c.X, c.Y
			for _, p := range s.layerPointers(l) {
				p.SendMotion(c.TimeMsec, server.FixedFromFloat(c.X), server.FixedFromFloat(c.Y))
				pointerFrame(p)
			}
			return
		}
		if w := s.windows[c.ID]; w != nil && c.ID == s.seat.pointerFocus {
			if s.relativeMotion(w, c) && s.locked(w) {
				for _, p := range s.windowPointers(w) {
					pointerFrame(p)
				}
			}
			if s.locked(w) {
				// Only relative motion reaches a locked pointer.
				return
			}
			x, y := w.surfacePoint(c.X, c.Y)
			s.seat.pointerX, s.seat.pointerY = c.X, c.Y
			// A constraint waits for the pointer to enter its region.
			if s.constraint == nil {
				s.updateConstraint()
			}
			for _, p := range s.windowPointers(w) {
				p.SendMotion(c.TimeMsec, server.FixedFromFloat(x), server.FixedFromFloat(y))
				pointerFrame(p)
			}
		}
	case ports.PointerButtonTo:
		// Releases may target the implicit-grab window after focus has moved.
		if client, pointers, ok := s.pointerTarget(c.ID); ok {
			s.serial++
			state := uint32(0)
			if c.Pressed {
				state = 1
				s.seat.press, s.seat.pressClient, s.seat.pressAt = s.serial, client, time.Now()
			}
			for _, p := range pointers {
				p.SendButton(s.serial, c.TimeMsec, c.Button, state)
				pointerFrame(p)
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
			sendAxis(p, c.Axis, steps, values)
		}
	case ports.SetKeymap:
		s.setKeymap(c)
	case ports.FocusWindow:
		if s.seat.focused != c.ID {
			s.changeFocus(c.ID)
		}
	case ports.ForwardKey:
		if s.grabKey(c) {
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
			k.SendKey(s.serial, c.Key.TimeMsec, c.Key.Keycode, state)
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

func (s *Server) windowKeyboards(w *window) []*wayland.Keyboard {
	if w == nil || !w.mapped || !w.xdg.resource.Resource.Alive() {
		return nil
	}
	return s.clientKeyboards(w.xdg.resource.Client())
}

func (s *Server) clientKeyboards(c server.Client) []*wayland.Keyboard {
	var alive []*wayland.Keyboard
	for _, k := range s.seat.keyboards[c] {
		if k.Resource.Alive() {
			alive = append(alive, k)
		}
	}
	return alive
}

// focusTarget resolves a window or layer ID to its wl_surface and keyboards.
func (s *Server) focusTarget(id ports.WindowID) (*wayland.Surface, []*wayland.Keyboard) {
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
func (s *Server) pointerTarget(id ports.WindowID) (server.Client, []*wayland.Pointer, bool) {
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
func (s *Server) pointerSurface(id ports.WindowID, x, y float64) (*wayland.Surface, []*wayland.Pointer, float64, float64) {
	if w := s.windows[id]; w != nil {
		if surf := w.xdg.surfaceResource(); surf != nil && surf.Resource.Alive() {
			x, y := w.surfacePoint(x, y)
			return surf, s.windowPointers(w), x, y
		}
	}
	if l := s.layers[id]; l != nil && l.mapped {
		if surf := l.surfaceResource(); surf != nil {
			return surf, s.layerPointers(l), x, y
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
	s.seat.modState = ports.ModState{}
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

func (s *Server) sendModifiers(k *wayland.Keyboard) {
	m := s.seat.modState
	k.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
}

func (s *Server) changeFocus(id ports.WindowID) {
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
	if a.Source != ports.AxisWheel {
		return steps, values
	}
	for i, ax := range [2]ports.ScrollAxis{a.Vertical, a.Horizontal} {
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
func sendAxis(p *wayland.Pointer, a ports.PointerAxis, steps [2]int32, values [2]float64) {
	v := p.Version()
	axes := [2]ports.ScrollAxis{a.Vertical, a.Horizontal}
	for i := range axes {
		if v < 8 && a.Source == ports.AxisWheel && axes[i].V120 != 0 && steps[i] == 0 {
			axes[i].Set = false
		}
	}
	if !axes[0].Set && !axes[1].Set {
		return
	}
	if v >= 5 {
		p.SendAxisSource(uint32(a.Source))
	}
	for axis, s := range axes {
		if !s.Set {
			continue
		}
		if s.Stop {
			if v >= 5 {
				p.SendAxisStop(a.TimeMsec, uint32(axis))
			}
			continue
		}
		value := s.Value
		if a.Source == ports.AxisWheel && s.V120 != 0 {
			switch {
			case v >= 8:
				p.SendAxisValue120(uint32(axis), s.V120)
			default:
				value = values[axis]
				if v >= 5 {
					p.SendAxisDiscrete(uint32(axis), steps[axis])
				}
			}
		}
		p.SendAxis(a.TimeMsec, uint32(axis), server.FixedFromFloat(value))
	}
	pointerFrame(p)
}

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

func (s *Server) clientPointers(c server.Client) []*wayland.Pointer {
	var result []*wayland.Pointer
	for _, p := range s.seat.pointers[c] {
		if p.Resource.Alive() {
			result = append(result, p)
		}
	}
	return result
}
func (s *Server) changePointerFocus(id ports.WindowID, x, y float64) {
	if id == s.seat.pointerFocus {
		return
	}
	s.seat.wheelRest, s.seat.wheelHeld = [2]int32{}, [2]float64{}
	// The new client sets its own cursor on enter; until then, the arrow.
	s.cursorSurface = nil
	s.setCursor(ports.CursorChange{})
	if surface, pointers, _, _ := s.pointerSurface(s.seat.pointerFocus, 0, 0); surface != nil {
		for _, p := range pointers {
			s.serial++
			p.SendLeave(s.serial, surface)
			pointerFrame(p)
		}
	}
	s.seat.pointerFocus = 0
	clear(s.seat.enters)
	if surface, pointers, sx, sy := s.pointerSurface(id, x, y); surface != nil {
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
