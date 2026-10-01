package wayland

import (
	"bytes"
	"time"

	"github.com/bnema/go-wayland-bindings/server/virtualkeyboard"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// maxVirtualKeymap bounds a keymap read from a virtual keyboard client.
const maxVirtualKeymap = 1 << 20

// virtualKeyboard is a zwp_virtual_keyboard_v1 (wtype, dictation tools).
// Its keys go to the focused client. The focused client's keyboards carry
// the keymap of whichever keyboard typed last: a virtual key switches them
// to the virtual keymap, the next real key or focus change switches back.
type virtualKeyboard struct {
	server *Server
	fd     int // sealed copy of the client keymap, -1 before one
	size   uint32
	text   string         // the keymap, compared with the active one
	mods   ports.ModState // last modifiers request, zero before one
	// pressed are keys held on focus; a focus change releases them by leave.
	pressed      map[uint32]bool
	focus        ports.WindowID
	generation   ports.LockGeneration // epoch of held keys; never release across protection
	dropReleases bool                 // after an epoch change, ignore releases without a new press
}

// registerVirtualKeyboard exposes the global to every client, as wlroots
// does by default: any local client may type into the focused window.
func registerVirtualKeyboard(d *server.Display, s *Server) error {
	return virtualkeyboard.NewZwpVirtualKeyboardManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = virtualkeyboard.NewZwpVirtualKeyboardManagerV1(c, int32(v), id, virtualKeyboardManager{s})
	})
}

type virtualKeyboardManager struct{ server *Server }

func (m virtualKeyboardManager) CreateVirtualKeyboard(r *virtualkeyboard.ZwpVirtualKeyboardManagerV1, _ *wayland.Seat, id uint32) {
	k := &virtualKeyboard{server: m.server, fd: -1, pressed: map[uint32]bool{}}
	res, err := virtualkeyboard.NewZwpVirtualKeyboardV1(r.Client(), r.Version(), id, k)
	if err != nil {
		return
	}
	res.OnDestroy = k.release
	m.server.log.Info().Int("pid", r.Client().PID()).Msg("virtual keyboard created")
}

// Keymap copies the client keymap into a sealed memfd: other clients never
// see the client's fd. An unreadable or malformed keymap is ignored.
func (k *virtualKeyboard) Keymap(_ *virtualkeyboard.ZwpVirtualKeyboardV1, format uint32, fd int, size uint32) {
	if k.server.protected() {
		unix.Close(fd)
		k.admitted() // discard held state and ownership without delivery
		if k.fd >= 0 {
			unix.Close(k.fd)
		}
		k.fd, k.size, k.text = -1, 0, ""
		// A rejected replacement must not leave an old layout usable after
		// unlock. The client must send a fresh keymap before typing again.
		return
	}
	text, ok := readKeymap(fd, size)
	unix.Close(fd)
	if format != uint32(wayland.KeyboardKeymapFormatXkbV1) || !ok {
		k.server.log.Debug().Msg("virtual keymap rejected")
		return
	}
	if wide, from := widenKeycodes(text); from != 0 {
		k.server.log.Debug().Int("maximum", from).Int("widened", minKeycodeMax).Msg("virtual keymap keycode range widened")
		text = wide
	}
	copyFD, copySize, err := keymapFile(text)
	if err != nil {
		k.server.log.Warn().Err(err).Msg("virtual keymap copy failed")
		return
	}
	if k.fd >= 0 {
		unix.Close(k.fd)
	}
	k.fd, k.size = copyFD, copySize
	old := k.server.activeKeymapText()
	k.text = text
	if s := k.server; s.seat.keymapOwner == k && text != old {
		// Clients must see the new layout before the next key.
		s.sendKeymapAll(k.fd, k.size)
	}
}

// readKeymap reads a NUL-terminated xkb keymap of size bytes from a
// regular file.
func readKeymap(fd int, size uint32) (string, bool) {
	var st unix.Stat_t
	if size == 0 || size > maxVirtualKeymap || unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size < int64(size) {
		return "", false
	}
	data := make([]byte, size)
	if n, err := unix.Pread(fd, data, 0); err != nil || n != int(size) {
		return "", false
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	if !bytes.Contains(data, []byte("xkb_keymap")) {
		return "", false
	}
	return string(data), true
}

// keyboards switches the focused client to k's keymap and returns its
// keyboards, or nil after a no_keymap error or without focus.
func (k *virtualKeyboard) keyboards(r *virtualkeyboard.ZwpVirtualKeyboardV1) []*wayland.Keyboard {
	if !k.admitted() {
		return nil
	}
	if k.fd < 0 {
		r.PostError(uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap), "no keymap")
		return nil
	}
	s := k.server
	_, keyboards := s.focusTarget(s.seat.focused)
	if len(keyboards) == 0 {
		return nil
	}
	if k.focus != s.seat.focused {
		clear(k.pressed)
		k.focus = s.seat.focused
	}
	s.useKeymap(k)
	s.serial++
	return keyboards
}

func (k *virtualKeyboard) Key(r *virtualkeyboard.ZwpVirtualKeyboardV1, time, key, state uint32) {
	if !k.admitted() || state != 1 && k.dropReleases && !k.pressed[key] {
		return
	}
	keyboards := k.keyboards(r)
	if keyboards == nil {
		return
	}
	if state == 1 {
		k.pressed[key] = true
	} else {
		delete(k.pressed, key)
	}
	for _, kb := range keyboards {
		kb.SendKey(k.server.serial, time, key, state)
	}
}

func (k *virtualKeyboard) Modifiers(r *virtualkeyboard.ZwpVirtualKeyboardV1, depressed, latched, locked, group uint32) {
	if !k.admitted() {
		return
	}
	k.mods = ports.ModState{Depressed: depressed, Latched: latched, Locked: locked, Group: group}
	for _, kb := range k.keyboards(r) {
		kb.SendModifiers(k.server.serial, depressed, latched, locked, group)
	}
}

// admitted forgets old held state even if protection began and ended between
// this keyboard's requests. Snapshot is coherent; DISPLAY owns admission.
func (k *virtualKeyboard) admitted() bool {
	state := ports.SecurityState{}
	if k.server.security != nil {
		state = k.server.security.Snapshot()
	}
	if state.Protected || state.Generation != k.generation {
		clear(k.pressed)
		k.mods = ports.ModState{}
		k.focus = 0
		k.generation = state.Generation
		k.dropReleases = true
		if k.server.seat.keymapOwner == k {
			// Forget ownership silently; the parent resets focus and restores
			// the seat layout at the lock transition.
			k.server.seat.keymapOwner = nil
		}
	}
	return !state.Protected
}

func (*virtualKeyboard) Destroy(*virtualkeyboard.ZwpVirtualKeyboardV1) {}

// release runs when the resource dies, by request or client disconnect:
// keys still held on the focused client are released.
func (k *virtualKeyboard) release() {
	s := k.server
	generation := k.generation
	admitted := k.admitted() && generation == k.generation
	if admitted && len(k.pressed) > 0 && k.focus == s.seat.focused {
		_, keyboards := s.focusTarget(s.seat.focused)
		var ts unix.Timespec
		_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
		// Input events carry CLOCK_MONOTONIC milliseconds (libinput's clock).
		now := uint32(ts.Nano() / int64(time.Millisecond))
		for key := range k.pressed {
			s.serial++
			for _, kb := range keyboards {
				kb.SendKey(s.serial, now, key, 0)
			}
		}
	}
	if s.seat.keymapOwner == k {
		if admitted {
			s.useKeymap(nil)
		} else {
			// Drop ownership without delivering keymap/modifiers into the locker.
			s.seat.keymapOwner = nil
		}
	}
	if k.fd >= 0 {
		unix.Close(k.fd)
		k.fd = -1
	}
}

// useKeymap makes every keyboard carry the keymap of k, or the seat keymap
// when k is nil. Switching back restores the seat modifiers too.
func (s *Server) useKeymap(k *virtualKeyboard) {
	if s.seat.keymapOwner == k {
		return
	}
	old := s.activeKeymapText()
	s.seat.keymapOwner = k
	// Like wlroots and smithay, a keymap equal to the active one is not
	// sent again: tools such as dictation apps create a virtual keyboard
	// with the seat layout per use, and each resend makes every client
	// (Xwayland included) recompile its keymap.
	if s.activeKeymapText() != old {
		s.sendKeymapAll(s.currentKeymap())
	}
	// Modifiers follow the keyboard that types: a skipped keymap no longer
	// resets them, so a virtual keyboard never types through seat Shift or
	// Caps Lock, and the seat gets its own back.
	m := s.seat.modState
	if k != nil {
		m = k.mods
	}
	s.serial++
	for _, kb := range s.clientKeyboards(s.focusClient()) {
		kb.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
	}
	s.log.Debug().Bool("virtual", k != nil).Msg("keymap switched")
}

func (s *Server) sendKeymapAll(fd int, size uint32) {
	for _, list := range s.seat.keyboards {
		for _, kb := range list {
			if kb.Resource.Alive() {
				kb.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), fd, size)
			}
		}
	}
}

// activeKeymapText is the text of the keymap keyboards carry.
func (s *Server) activeKeymapText() string {
	if k := s.seat.keymapOwner; k != nil {
		return k.text
	}
	return s.seat.keymapText
}

// currentKeymap is the keymap a new keyboard must start with.
func (s *Server) currentKeymap() (int, uint32) {
	if k := s.seat.keymapOwner; k != nil {
		return k.fd, k.size
	}
	return s.seat.keymapFD, s.seat.keymapSize
}
