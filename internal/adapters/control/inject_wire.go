package control

import (
	"errors"

	"github.com/bnema/neferwl/internal/ports"
)

// translate serializes XKB access across connections and keymap reloads. The
// output batch is immutable; core receives it through the normal control port.
func (s *Server) translate(req request, announce bool) ([]ports.InputEvent, error) {
	s.injectMu.Lock()
	defer s.injectMu.Unlock()
	select {
	case <-s.done:
		return nil, errors.New("control closed")
	default:
	}
	if s.injector == nil {
		return nil, errors.New("input unavailable")
	}
	for s.keymaps != nil {
		select {
		case next, ok := <-s.keymaps:
			if !ok {
				s.keymaps = nil
				continue
			}
			if next == nil {
				continue
			}
			previous := s.injector.km
			if s.injector.security != nil {
				next.QuarantineFrom(previous)
			}
			s.injector.km = next
			previous.Close()
		default:
			goto ready
		}
	}
ready:
	i := s.injector
	var events []ports.InputEvent
	var err error
	// Snapshot an announcement before translating the first event, but don't
	// deliver it or consume the connection's announcement on validation failure.
	var device ports.InputEvent
	if announce {
		device = i.Device()
	}
	switch req.Kind {
	case "key":
		events, err = i.Key(req.Combo)
	case "type":
		events, err = i.Type(req.Text)
	case "press":
		events, err = i.Press(req.Combo)
	case "release":
		events, err = i.Release(req.Combo)
	case "pointer":
		events, err = i.Pointer(req.Cmd, req.Args)
	case "swipe":
		events, err = i.Swipe(req.Fingers, req.Dir)
	default:
		return nil, errors.New("unknown injection kind")
	}
	if err != nil {
		return nil, err
	}
	if announce {
		events = append([]ports.InputEvent{device}, events...)
	}
	return events, nil
}
