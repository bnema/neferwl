package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestDeviceInventorySurvivesSecurityEpochChange(t *testing.T) {
	c := &Core{security: domain.SecurityState{Generation: 2, Protected: true}}
	old := domain.SecurityState{Generation: 1}
	for _, event := range []ports.InputEvent{
		ports.InputDeviceAdded{ID: 7, Name: "bound mouse"},
		ports.InputDeviceRemoved{ID: 7},
	} {
		for _, wrapped := range []bool{false, true} {
			var queued = event
			if wrapped {
				queued = ports.SecurityInput{State: old, Event: event}
			}
			got, ok := c.admitInput(queued)
			if !ok || got != event {
				t.Fatalf("inventory rejected across epoch: %#v -> %#v, %v", queued, got, ok)
			}
		}
	}
	// Only lifecycle metadata bypasses epoch admission, never device input.
	for _, event := range []ports.InputEvent{
		ports.PointerMotion{Device: 7, X: 10, Y: 20},
		ports.PointerButton{Device: 7, Pressed: true},
		ports.PointerAxis{Device: 7},
		ports.KeyEvent{Device: 7, Pressed: true},
	} {
		if _, ok := c.admitInput(ports.SecurityInput{State: old, Event: event}); ok {
			t.Fatalf("stale input admitted: %T", event)
		}
	}
}
