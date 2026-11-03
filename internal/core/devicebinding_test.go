package core

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
)

func TestDeviceBindingPrecedenceAndReload(t *testing.T) {
	c := layoutCore(0, tile{"desktop", 0, 0, 100, 100}, tile{"tv", 300, 0, 100, 100})
	c.inputNames = map[domain.InputDeviceID]string{1: "shared mouse", 2: "other"}
	c.cfg.Outputs = []domain.OutputConfig{{Name: "absent", Inputs: []string{"shared*"}}, {Name: "tv", Inputs: []string{"shared*"}}, {Name: "desktop", Inputs: []string{"*"}}}
	if got := c.boundInput(1); got != 1 {
		t.Fatal("first connected config match lost", got)
	}
	l := c.layout()
	if len(l[1].Devices) != 1 || l[1].Devices[0] != 1 || len(l[0].Devices) != 1 || l[0].Devices[0] != 2 {
		t.Fatal(l)
	}
	c.cfg.Outputs = []domain.OutputConfig{{Name: "desktop", Inputs: []string{"*"}}, {Name: "tv", Inputs: []string{"shared*"}}}
	if got := c.boundInput(1); got != 0 {
		t.Fatal("reload binding not recomputed", got)
	}
	if l[1].Devices[0] != 1 {
		t.Fatal("old snapshot changed")
	}
	c.screens = c.screens[:1]
	c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", Inputs: []string{"shared*"}}}
	if c.boundInput(1) != -1 {
		t.Fatal("removed target remains bound")
	}
	delete(c.inputNames, 1)
	if c.boundInput(1) != -1 {
		t.Fatal("removed device remains bound")
	}
}

func TestUnmatchedGlobsKeepBindingPathEnabled(t *testing.T) {
	c := inputBenchCore(t)
	cfg := c.cfg
	cfg.Outputs = []domain.OutputConfig{{Name: "A", Inputs: []string{"missing*"}}}
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	c.inputNames = map[domain.InputDeviceID]string{1: "ordinary mouse"}
	if !c.inputBindings {
		t.Fatal("unmatched configured globs incorrectly enabled fast path")
	}
	if c.boundInput(1) != -1 {
		t.Fatal("unmatched device bound")
	}
}

func TestStationaryDeviceSwitchRestoresPointerBeforeDispatch(t *testing.T) {
	c := inputBenchCore(t)
	c.inputBindings = true
	c.inputNames = map[domain.InputDeviceID]string{1: "one", 2: "two"}
	c.devicePointers = map[domain.InputDeviceID][2]float64{1: {10, 20}, 2: {50, 60}}
	c.activePointerDevice = 1
	c.cursorX, c.cursorY = 10, 20
	for _, ev := range []ports.InputEvent{ports.PointerButton{Device: 2}, ports.PointerAxis{Device: 1}} {
		if err := c.activateInput(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		id, _ := inputDevice(ev)
		want := c.devicePointers[id]
		if c.cursorX != want[0] || c.cursorY != want[1] {
			t.Fatalf("device %d pointer=%v,%v want %v", id, c.cursorX, c.cursorY, want)
		}
	}
	c.grab = 1
	if err := c.activateInput(context.Background(), ports.PointerAxis{Device: 2}); err != nil {
		t.Fatal(err)
	}
	if c.cursorX != 10 || c.cursorY != 20 {
		t.Fatal("switch broke grab")
	}
}
