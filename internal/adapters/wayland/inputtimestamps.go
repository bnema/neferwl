package wayland

import (
	"slices"
	"time"

	"github.com/bnema/go-wayland-bindings/server/inputtimestamps"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// zwp_input_timestamps_v1: a client subscribes a wl_keyboard or wl_pointer
// and gets the device's high-resolution timestamp (libinput microseconds, in
// the protocol's nanosecond fields) right before each key, motion,
// button, axis and axis_stop event it sends. Subscriptions are per device
// resource and die with the device (the object becomes inert) or when the
// client destroys the timestamps object. There are no touch events, so touch
// subscriptions only create an inert object.
//
// The stamp lists are replaced, never mutated, like the seat device lists.

func registerInputTimestamps(d *server.Display, s *Server) error {
	return inputtimestamps.NewZwpInputTimestampsManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = inputtimestamps.NewZwpInputTimestampsManagerV1(c, int32(v), id, timestampsManager{s})
	})
}

type timestampsManager struct{ server *Server }

func (timestampsManager) Destroy(*inputtimestamps.ZwpInputTimestampsManagerV1) {}

func (m timestampsManager) GetKeyboardTimestamps(r *inputtimestamps.ZwpInputTimestampsManagerV1, id uint32, kb *wayland.Keyboard) {
	var dev *server.Resource
	if kb != nil {
		dev = kb.Resource
	}
	m.subscribe(r, id, dev, m.server.seat.keyStamps)
}

func (m timestampsManager) GetPointerTimestamps(r *inputtimestamps.ZwpInputTimestampsManagerV1, id uint32, p *wayland.Pointer) {
	var dev *server.Resource
	if p != nil {
		dev = p.Resource
	}
	m.subscribe(r, id, dev, m.server.seat.pointerStamps)
}

// GetTouchTimestamps creates the object only: the seat has no touch events.
func (timestampsManager) GetTouchTimestamps(r *inputtimestamps.ZwpInputTimestampsManagerV1, id uint32, _ *wayland.Touch) {
	_, _ = inputtimestamps.NewZwpInputTimestampsV1(r.Client(), r.Version(), id, timestampsHandler{})
}

// subscribe creates the timestamps object for dev and records it in stamps.
// A missing or dead device leaves the object inert.
func (m timestampsManager) subscribe(r *inputtimestamps.ZwpInputTimestampsManagerV1, id uint32, dev *server.Resource, stamps map[*server.Resource][]*inputtimestamps.ZwpInputTimestampsV1) {
	ts, err := inputtimestamps.NewZwpInputTimestampsV1(r.Client(), r.Version(), id, timestampsHandler{})
	if err != nil || dev == nil || !dev.Alive() {
		return
	}
	stamps[dev] = append(slices.Clone(stamps[dev]), ts)
	ts.OnDestroy = func() {
		// The device may have died first and dropped its entry.
		list, ok := stamps[dev]
		if !ok {
			return
		}
		list = slices.DeleteFunc(slices.Clone(list), func(x *inputtimestamps.ZwpInputTimestampsV1) bool { return x == ts })
		if len(list) == 0 {
			delete(stamps, dev)
		} else {
			stamps[dev] = list
		}
	}
}

type timestampsHandler struct{}

func (timestampsHandler) Destroy(*inputtimestamps.ZwpInputTimestampsV1) {}

// stampKeyboard sends t to the subscribers of k; call it right before a
// wl_keyboard.key. It allocates nothing.
func (s *Server) stampKeyboard(k *wayland.Keyboard, t time.Duration) {
	sendStamps(s.seat.keyStamps[k.Resource], t)
}

// stampPointer sends t to the subscribers of p; call it right before a
// wl_pointer motion, button, axis or axis_stop.
func (s *Server) stampPointer(p *wayland.Pointer, t time.Duration) {
	sendStamps(s.seat.pointerStamps[p.Resource], t)
}

func sendStamps(list []*inputtimestamps.ZwpInputTimestampsV1, t time.Duration) {
	if len(list) == 0 {
		return
	}
	sec := uint64(t / time.Second)
	ns := uint32(t % time.Second)
	for _, ts := range list {
		ts.SendTimestamp(uint32(sec>>32), uint32(sec), ns)
	}
}
