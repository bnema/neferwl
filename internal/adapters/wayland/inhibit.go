package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/idleinhibit"
	"github.com/bnema/go-wayland-bindings/server/keyboardshortcutsinhibit"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// Inhibitors. keyboard-shortcuts-inhibit: a window asks for every key,
// compositor binds included, while it has keyboard focus; core decides
// when it is in effect (ports.ShortcutsInhibitState) and the client hears
// active/inactive. idle-inhibit: a window keeps the session awake while
// it is mapped. Both are reported to core per window.

// inhibitor is one inhibitor object on a surface.
type inhibitor struct {
	surf     *surface
	shortcut *keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitorV1 // nil: idle
	window   ports.WindowID                                            // reported to core (0: not yet)
	active   bool                                                      // shortcuts: last state sent
}

func registerInhibit(d *server.Display, s *Server) error {
	if err := keyboardshortcutsinhibit.NewZwpKeyboardShortcutsInhibitManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = keyboardshortcutsinhibit.NewZwpKeyboardShortcutsInhibitManagerV1(c, int32(v), id, shortcutsManager{s})
	}); err != nil {
		return err
	}
	return idleinhibit.NewZwpIdleInhibitManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = idleinhibit.NewZwpIdleInhibitManagerV1(c, int32(v), id, idleManager{s})
	})
}

type shortcutsManager struct{ server *Server }

func (shortcutsManager) Destroy(*keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1) {}

// InhibitShortcuts adds a surface's inhibitor; one per surface and seat
// (there is one seat).
func (m shortcutsManager) InhibitShortcuts(r *keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1, id uint32, surf *wayland.Surface, _ *wayland.Seat) {
	state := m.server.surfaceOf(surf)
	if state == nil {
		return
	}
	for _, in := range m.server.inhibitors {
		if in.surf == state && in.shortcut != nil {
			r.PostError(uint32(keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitManagerV1ErrorAlreadyInhibited), "surface already inhibits shortcuts")
			return
		}
	}
	res, err := keyboardshortcutsinhibit.NewZwpKeyboardShortcutsInhibitorV1(r.Client(), r.Version(), id, shortcutsInhibitor{})
	if err != nil {
		return
	}
	in := &inhibitor{surf: state, shortcut: res}
	m.server.addInhibitor(in)
	res.OnDestroy = func() { m.server.removeInhibitor(in) }
}

type shortcutsInhibitor struct{}

func (shortcutsInhibitor) Destroy(*keyboardshortcutsinhibit.ZwpKeyboardShortcutsInhibitorV1) {}

type idleManager struct{ server *Server }

func (idleManager) Destroy(*idleinhibit.ZwpIdleInhibitManagerV1) {}

// CreateInhibitor adds an idle inhibitor; a surface may have several.
func (m idleManager) CreateInhibitor(r *idleinhibit.ZwpIdleInhibitManagerV1, id uint32, surf *wayland.Surface) {
	state := m.server.surfaceOf(surf)
	if state == nil {
		return
	}
	res, err := idleinhibit.NewZwpIdleInhibitorV1(r.Client(), r.Version(), id, idleInhibitor{})
	if err != nil {
		return
	}
	in := &inhibitor{surf: state}
	m.server.addInhibitor(in)
	res.OnDestroy = func() { m.server.removeInhibitor(in) }
}

type idleInhibitor struct{}

func (idleInhibitor) Destroy(*idleinhibit.ZwpIdleInhibitorV1) {}

func (s *Server) addInhibitor(in *inhibitor) {
	s.inhibitors = append(s.inhibitors, in)
	s.syncInhibitors()
}

func (s *Server) removeInhibitor(in *inhibitor) {
	for i, x := range s.inhibitors {
		if x == in {
			s.inhibitors = append(s.inhibitors[:i], s.inhibitors[i+1:]...)
			break
		}
	}
	in.surf = nil
	s.syncInhibitors()
}

// syncInhibitors tells core, per window, whether it has inhibitors of
// each kind, after any change: an inhibitor added or removed, a window
// mapped or unmapped.
func (s *Server) syncInhibitors() {
	shortcuts, idle := map[ports.WindowID]bool{}, map[ports.WindowID]bool{}
	for _, in := range s.inhibitors {
		in.window = 0
		if in.surf != nil && !in.surf.destroyed {
			in.window = in.surf.root().windowID()
		}
		if in.window == 0 {
			continue
		}
		if in.shortcut != nil {
			shortcuts[in.window] = true
		} else {
			idle[in.window] = true
		}
	}
	for id, on := range diffSets(s.shortcutWindows, shortcuts) {
		s.emit(ports.ShortcutsInhibit{Window: id, Active: on})
	}
	for id, on := range diffSets(s.idleWindows, idle) {
		s.emit(ports.IdleInhibit{Window: id, Active: on})
	}
	wasHeld := s.inhibited()
	s.shortcutWindows, s.idleWindows = shortcuts, idle
	s.syncIdle(wasHeld)
}

// diffSets is the membership changes from old to cur.
func diffSets(old, cur map[ports.WindowID]bool) map[ports.WindowID]bool {
	out := map[ports.WindowID]bool{}
	for id := range cur {
		if !old[id] {
			out[id] = true
		}
	}
	for id := range old {
		if !cur[id] {
			out[id] = false
		}
	}
	return out
}

// setShortcutsInhibit applies core's decision to a window's shortcuts
// inhibitor.
func (s *Server) setShortcutsInhibit(c ports.ShortcutsInhibitState) {
	for _, in := range s.inhibitors {
		if in.shortcut == nil || in.window != c.Window || in.active == c.Active || !in.shortcut.Resource.Alive() {
			continue
		}
		in.active = c.Active
		if c.Active {
			in.shortcut.SendActive()
		} else {
			in.shortcut.SendInactive()
		}
	}
}
