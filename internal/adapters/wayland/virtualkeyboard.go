package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/virtualkeyboard"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// virtualKeyboard is a zwp_virtual_keyboard_v1 (wtype, dictation tools).
// Its keys go to the focused client. The focused client's keyboards carry
// the keymap of whichever keyboard typed last: a virtual key switches them
// to the virtual keymap, the next real key switches them back.
type virtualKeyboard struct {
	server *Server
	fd     int
	size   uint32
}

func registerVirtualKeyboard(d *server.Display, s *Server) error {
	return virtualkeyboard.NewZwpVirtualKeyboardManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = virtualkeyboard.NewZwpVirtualKeyboardManagerV1(c, int32(v), id, virtualKeyboardManager{s})
	})
}

type virtualKeyboardManager struct{ server *Server }

func (m virtualKeyboardManager) CreateVirtualKeyboard(r *virtualkeyboard.ZwpVirtualKeyboardManagerV1, _ *wayland.Seat, id uint32) {
	k := &virtualKeyboard{server: m.server, fd: -1}
	res, err := virtualkeyboard.NewZwpVirtualKeyboardV1(r.Client(), r.Version(), id, k)
	if err != nil {
		return
	}
	res.OnDestroy = k.release
	m.server.log.Info().Int("pid", r.Client().PID()).Msg("virtual keyboard created")
}

func (k *virtualKeyboard) Keymap(_ *virtualkeyboard.ZwpVirtualKeyboardV1, format uint32, fd int, size uint32) {
	if format != uint32(wayland.KeyboardKeymapFormatXkbV1) || size == 0 {
		unix.Close(fd)
		return
	}
	if k.fd >= 0 {
		unix.Close(k.fd)
	}
	k.fd, k.size = fd, size
	if s := k.server; s.keymapOwner == k {
		// Clients must see the new layout before the next key.
		s.sendKeymapAll(fd, size)
	}
}

func (k *virtualKeyboard) Key(r *virtualkeyboard.ZwpVirtualKeyboardV1, time, key, state uint32) {
	if k.fd < 0 {
		r.PostError(uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap), "key before keymap")
		return
	}
	s := k.server
	_, keyboards := s.focusTarget(s.focused)
	if len(keyboards) == 0 {
		return
	}
	s.useKeymap(k)
	s.serial++
	for _, kb := range keyboards {
		kb.SendKey(s.serial, time, key, state)
	}
}

func (k *virtualKeyboard) Modifiers(r *virtualkeyboard.ZwpVirtualKeyboardV1, depressed, latched, locked, group uint32) {
	if k.fd < 0 {
		r.PostError(uint32(virtualkeyboard.ZwpVirtualKeyboardV1ErrorNoKeymap), "modifiers before keymap")
		return
	}
	s := k.server
	_, keyboards := s.focusTarget(s.focused)
	if len(keyboards) == 0 {
		return
	}
	s.useKeymap(k)
	s.serial++
	for _, kb := range keyboards {
		kb.SendModifiers(s.serial, depressed, latched, locked, group)
	}
}

func (*virtualKeyboard) Destroy(*virtualkeyboard.ZwpVirtualKeyboardV1) {}

// release runs when the resource dies, by request or client disconnect.
func (k *virtualKeyboard) release() {
	if s := k.server; s.keymapOwner == k {
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
	s.keymapOwner = k
	s.sendKeymapAll(s.currentKeymap())
	if k == nil {
		s.serial++
		for _, kb := range s.clientKeyboards(s.focusClient()) {
			s.sendModifiers(kb)
		}
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

// currentKeymap is the keymap a new keyboard must start with.
func (s *Server) currentKeymap() (int, uint32) {
	if k := s.keymapOwner; k != nil {
		return k.fd, k.size
	}
	return s.keymapFD, s.keymapSize
}
