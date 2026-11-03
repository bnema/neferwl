package libinput

import (
	"reflect"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestDeviceLifecycle(t *testing.T) {
	s := &inputState{}
	first := s.addDevice(10, "same name")
	second := s.addDevice(20, "same name")
	if !reflect.DeepEqual(first, []ports.InputEvent{ports.InputDeviceAdded{ID: 1, Name: "same name"}}) || !reflect.DeepEqual(second, []ports.InputEvent{ports.InputDeviceAdded{ID: 2, Name: "same name"}}) {
		t.Fatalf("announcements: %v %v", first, second)
	}
	if got := s.addDevice(10, "duplicate"); len(got) != 0 {
		t.Fatal(got)
	}
	s.gestures.begin(10, domain.GestureSwipe, 3, 0)
	events := s.removeDevice(10)
	want := []ports.InputEvent{ports.SwipeEnd{Device: 1, Cancelled: true}, ports.InputDeviceRemoved{ID: 1}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("got %v want %v", events, want)
	}
	security := portsmocks.NewMockSessionSecurity(t)
	state := domain.SecurityState{Generation: 42, Protected: true}
	stamped := secureBatch(events, security, state)
	for i, raw := range stamped {
		e := raw.(ports.SecurityInput)
		if e.State != state || e.Event != want[i] {
			t.Fatal(e)
		}
	}
	if got := s.removeDevice(10); len(got) != 0 {
		t.Fatal(got)
	}
	if got := s.addDevice(10, "replugged")[0].(ports.InputDeviceAdded); got.ID != 3 {
		t.Fatal(got)
	}
	if got := s.removeDevice(20); !reflect.DeepEqual(got, []ports.InputEvent{ports.InputDeviceRemoved{ID: 2}}) {
		t.Fatal(got)
	}
}

func TestWithDevice(t *testing.T) {
	for _, event := range []ports.InputEvent{
		ports.KeyEvent{Keycode: 30}, ports.PointerMotion{DX: 2}, ports.PointerButton{Button: 272}, ports.PointerAxis{Source: domain.AxisFinger},
		ports.SwipeBegin{Fingers: 3}, ports.SwipeUpdate{DX: 2}, ports.SwipeEnd{Cancelled: true},
		ports.PinchBegin{Fingers: 2}, ports.PinchUpdate{Scale: 1.5}, ports.PinchEnd{Cancelled: true}, ports.HoldBegin{Fingers: 2}, ports.HoldEnd{Cancelled: true},
	} {
		got := reflect.ValueOf(withDevice(event, 17))
		if got.FieldByName("Device").Uint() != 17 {
			t.Fatal(got)
		}
		original := reflect.New(got.Type()).Elem()
		original.Set(got)
		original.FieldByName("Device").SetUint(0)
		if original.Interface() != event {
			t.Fatalf("payload changed: %v", got)
		}
	}
	if got := withDevice(ports.SeatChanged{Active: true}, 17); got != (ports.SeatChanged{Active: true}) {
		t.Fatal(got)
	}
}

func TestForwarderKeepsDevicesSeparate(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		f := newForwarder(zerowrap.Default())
		for _, m := range []ports.PointerMotion{{Device: 1, DX: 1}, {Device: 2, DX: 2}, {Device: 2, DX: 3}, {Device: 1, DX: 4}} {
			var event ports.InputEvent = m
			if wrapped {
				event = ports.SecurityInput{State: domain.SecurityState{Generation: 1}, Event: m}
			}
			f.push(event)
		}
		if len(f.queue) != 3 {
			t.Fatal(f.queue)
		}
		for i, want := range []ports.PointerMotion{{Device: 1, DX: 1}, {Device: 2, DX: 5}, {Device: 1, DX: 4}} {
			got, _, _, ok := queuedMotion(f.queue[i])
			if !ok || got != want {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	}
}
