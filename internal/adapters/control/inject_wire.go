package control

import (
	"errors"

	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/ports"
)

// translate requires injectMu through admission. Reloads happen before Begin,
// never while the tentative translation is awaiting core admission.
func (s *Server) translate(req request, announce bool) ([]ports.InputEvent, *xkb.Transaction, error) {
	select {
	case <-s.done:
		return nil, nil, errors.New("control closed")
	default:
	}
	if s.injector == nil {
		return nil, nil, errors.New("input unavailable")
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
	tx, err := i.km.Begin()
	if err != nil {
		return nil, nil, err
	}
	success := false
	defer func() {
		if !success {
			tx.Rollback()
		}
	}()
	var events []ports.InputEvent
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
		return nil, nil, errors.New("unknown injection kind")
	}
	if err != nil {
		return nil, nil, err
	}
	if announce {
		events = append([]ports.InputEvent{device}, events...)
	}
	success = true
	return events, tx, nil
}
