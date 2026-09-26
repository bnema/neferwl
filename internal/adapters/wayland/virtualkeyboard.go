package wayland

import (
	"bytes"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/virtualkeyboard"
	"github.com/bnema/purego-libwayland/protocol/wayland"
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
	pressed map[uint32]bool
	focus   ports.WindowID
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
	if s := k.server; s.keymapOwner == k && text != old {
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
	if k.fd < 0 {
		r.PostError(uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap), "no keymap")
		return nil
	}
	s := k.server
	_, keyboards := s.focusTarget(s.focused)
	if len(keyboards) == 0 {
		return nil
	}
	if k.focus != s.focused {
		clear(k.pressed)
		k.focus = s.focused
	}
	s.useKeymap(k)
	s.serial++
	return keyboards
}

func (k *virtualKeyboard) Key(r *virtualkeyboard.ZwpVirtualKeyboardV1, time, key, state uint32) {
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
	k.mods = ports.ModState{Depressed: depressed, Latched: latched, Locked: locked, Group: group}
	for _, kb := range k.keyboards(r) {
		kb.SendModifiers(k.server.serial, depressed, latched, locked, group)
	}
}

func (*virtualKeyboard) Destroy(*virtualkeyboard.ZwpVirtualKeyboardV1) {}

// release runs when the resource dies, by request or client disconnect:
// keys still held on the focused client are released.
func (k *virtualKeyboard) release() {
	s := k.server
	if len(k.pressed) > 0 && k.focus == s.focused {
		_, keyboards := s.focusTarget(s.focused)
		now := uint32(time.Now().UnixMilli())
		for key := range k.pressed {
			s.serial++
			for _, kb := range keyboards {
				kb.SendKey(s.serial, now, key, 0)
			}
		}
	}
	if s.keymapOwner == k {
		s.useKeymap(nil)
	}
	if k.fd >= 0 {
		unix.Close(k.fd)
		k.fd = -1
	}
}

// useKeymap makes every keyboard carry the keymap of k, or the seat keymap
// when k is nil. Switching back restores the seat modifiers too.
func (s *Server) useKeymap(k *virtualKeyboard) {
	if s.keymapOwner == k {
		return
	}
	old := s.activeKeymapText()
	s.keymapOwner = k
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
	m := s.modState
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
	for _, list := range s.keyboards {
		for _, kb := range list {
			if kb.Resource.Alive() {
				kb.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), fd, size)
			}
		}
	}
}

// activeKeymapText is the text of the keymap keyboards carry.
func (s *Server) activeKeymapText() string {
	if k := s.keymapOwner; k != nil {
		return k.text
	}
	return s.keymapText
}

// currentKeymap is the keymap a new keyboard must start with.
func (s *Server) currentKeymap() (int, uint32) {
	if k := s.keymapOwner; k != nil {
		return k.fd, k.size
	}
	return s.keymapFD, s.keymapSize
}
