package wayland

import (
	"time"

	"github.com/bnema/go-wayland-bindings/server/pointergestures"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// zwp_pointer_gestures_v1: touchpad swipes (other than the compositor's own
// three and four fingers), pinches and holds go to the client under the
// pointer. A gesture belongs to the window that had the pointer when it
// began; it ends cancelled when the pointer leaves that window.

// gestureObject is one swipe, pinch or hold object of a client. Exactly one
// of the three is set.
type gestureObject struct {
	swipe *pointergestures.ZwpPointerGestureSwipeV1
	pinch *pointergestures.ZwpPointerGesturePinchV1
	hold  *pointergestures.ZwpPointerGestureHoldV1
}

func (g *gestureObject) alive() bool {
	switch {
	case g.swipe != nil:
		return g.swipe.Resource.Alive()
	case g.pinch != nil:
		return g.pinch.Resource.Alive()
	case g.hold != nil:
		return g.hold.Resource.Alive()
	}
	return false
}

func (g *gestureObject) kind() ports.GestureKind {
	switch {
	case g.pinch != nil:
		return ports.GesturePinch
	case g.hold != nil:
		return ports.GestureHold
	}
	return ports.GestureSwipe
}

func (g *gestureObject) begin(serial uint32, at uint32, surf *wayland.Surface, fingers uint32) {
	switch {
	case g.swipe != nil:
		g.swipe.SendBegin(serial, at, surf, fingers)
	case g.pinch != nil:
		g.pinch.SendBegin(serial, at, surf, fingers)
	case g.hold != nil:
		g.hold.SendBegin(serial, at, surf, fingers)
	}
}

func (g *gestureObject) update(u ports.GestureUpdateTo) {
	at := wireMsec(u.Time)
	dx, dy := server.FixedFromFloat(u.DX), server.FixedFromFloat(u.DY)
	switch {
	case g.swipe != nil:
		g.swipe.SendUpdate(at, dx, dy)
	case g.pinch != nil:
		g.pinch.SendUpdate(at, dx, dy, server.FixedFromFloat(u.Scale), server.FixedFromFloat(u.Rotation))
	}
}

func (g *gestureObject) end(serial uint32, at uint32, cancelled int32) {
	switch {
	case g.swipe != nil:
		g.swipe.SendEnd(serial, at, cancelled)
	case g.pinch != nil:
		g.pinch.SendEnd(serial, at, cancelled)
	case g.hold != nil:
		g.hold.SendEnd(serial, at, cancelled)
	}
}

// activeGesture is the gesture sent to a window: the objects that got its
// begin are the ones that get the rest, even when a client makes more
// meanwhile.
type activeGesture struct {
	id   ports.WindowID
	kind ports.GestureKind
	objs []*gestureObject
	// at is the latest device time sent, for a cancellation.
	at time.Duration
}

func registerPointerGestures(d *server.Display, s *Server) error {
	return pointergestures.NewZwpPointerGesturesV1Global(d, 3, func(c server.Client, v, id uint32) {
		_, _ = pointergestures.NewZwpPointerGesturesV1(c, int32(v), id, gestureManager{s})
	})
}

// gestureManager is zwp_pointer_gestures_v1. Releasing it leaves the
// gesture objects it made alive, as the protocol says.
type gestureManager struct{ server *Server }

func (gestureManager) Release(*pointergestures.ZwpPointerGesturesV1) {}

// gestureDestroy handles the destroy request of the three gesture objects;
// the resource's OnDestroy forgets the object.
type gestureDestroy struct{}

func (gestureDestroy) Destroy(*pointergestures.ZwpPointerGestureSwipeV1) {}

type pinchDestroy struct{}

func (pinchDestroy) Destroy(*pointergestures.ZwpPointerGesturePinchV1) {}

type holdDestroy struct{}

func (holdDestroy) Destroy(*pointergestures.ZwpPointerGestureHoldV1) {}

func (m gestureManager) GetSwipeGesture(r *pointergestures.ZwpPointerGesturesV1, id uint32, _ *wayland.Pointer) {
	if res, err := pointergestures.NewZwpPointerGestureSwipeV1(r.Client(), r.Version(), id, gestureDestroy{}); err == nil {
		m.server.addGesture(r.Client(), &gestureObject{swipe: res}, res.Resource)
	}
}

func (m gestureManager) GetPinchGesture(r *pointergestures.ZwpPointerGesturesV1, id uint32, _ *wayland.Pointer) {
	if res, err := pointergestures.NewZwpPointerGesturePinchV1(r.Client(), r.Version(), id, pinchDestroy{}); err == nil {
		m.server.addGesture(r.Client(), &gestureObject{pinch: res}, res.Resource)
	}
}

func (m gestureManager) GetHoldGesture(r *pointergestures.ZwpPointerGesturesV1, id uint32, _ *wayland.Pointer) {
	if res, err := pointergestures.NewZwpPointerGestureHoldV1(r.Client(), r.Version(), id, holdDestroy{}); err == nil {
		m.server.addGesture(r.Client(), &gestureObject{hold: res}, res.Resource)
	}
}

// addGesture lists a client's gesture object until its resource dies.
func (s *Server) addGesture(c server.Client, o *gestureObject, res *server.Resource) {
	s.gestures[c] = append(s.gestures[c], o)
	res.OnDestroy = func() {
		if list := removeItem(s.gestures[c], o); len(list) > 0 {
			s.gestures[c] = list
		} else {
			delete(s.gestures, c)
		}
	}
}

// beginGesture sends the begin of a gesture to the objects of the kind that
// the client of the pointer-focus window made. A gesture still running
// ends cancelled first. Nothing is recorded when the client has no object.
func (s *Server) beginGesture(c ports.GestureBeginTo) {
	s.cancelGesture()
	if c.ID != s.seat.pointerFocus || c.Fingers <= 0 {
		return
	}
	surf, _, _, _ := s.pointerSurface(c.ID, 0, 0)
	if surf == nil {
		return
	}
	var objs []*gestureObject
	for _, o := range s.gestures[surf.Client()] {
		if o.kind() == c.Kind && o.alive() {
			objs = append(objs, o)
		}
	}
	if len(objs) == 0 {
		return
	}
	s.seat.gesture = &activeGesture{id: c.ID, kind: c.Kind, objs: objs, at: c.Time}
	s.serial++
	for _, o := range objs {
		o.begin(s.serial, wireMsec(c.Time), surf, uint32(c.Fingers))
	}
}

// updateGesture moves the running gesture of the window.
func (s *Server) updateGesture(c ports.GestureUpdateTo) {
	g := s.seat.gesture
	if g == nil || g.id != c.ID || g.kind != c.Kind {
		return
	}
	g.at = c.Time
	for _, o := range g.objs {
		if o.alive() {
			o.update(c)
		}
	}
}

// endGesture ends the running gesture of the window.
func (s *Server) endGesture(c ports.GestureEndTo) {
	g := s.seat.gesture
	if g == nil || g.id != c.ID || g.kind != c.Kind {
		return
	}
	g.at = c.Time
	s.finishGesture(c.Cancelled)
}

// cancelGesture ends the running gesture, if any, as cancelled: the
// pointer left its window or the window went.
func (s *Server) cancelGesture() {
	if s.seat.gesture != nil {
		s.finishGesture(true)
	}
}

func (s *Server) finishGesture(cancelled bool) {
	g := s.seat.gesture
	s.seat.gesture = nil
	state := int32(0)
	if cancelled {
		state = 1
	}
	s.serial++
	for _, o := range g.objs {
		if o.alive() {
			o.end(s.serial, wireMsec(g.at), state)
		}
	}
}
