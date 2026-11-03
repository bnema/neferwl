package core

import (
	"github.com/bnema/neferwl/internal/domain"
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
