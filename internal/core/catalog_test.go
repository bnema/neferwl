package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/core/workspace"
	"github.com/bnema/neferwl/internal/domain"
)

// TestCommandCatalogMatchesBindActions keeps the domain catalog in lockstep
// with workspace actions; domain does not depend on core.
func TestCommandCatalogMatchesBindActions(t *testing.T) {
	actions := map[string]domain.CommandKind{
		// Config's explicit no-op has no workspace Action constant.
		"none": domain.CommandAction,
	}
	add := func(name string, kind domain.CommandKind) {
		if _, exists := actions[name]; exists {
			t.Errorf("duplicate workspace action %q", name)
		}
		actions[name] = kind
	}
	for _, a := range workspace.Actions() {
		add(string(a), domain.CommandAction)
	}
	for _, name := range workspace.ArgPrefixes() {
		add(name, domain.CommandActionArg)
	}
	catalog := map[string]domain.CommandKind{}
	for _, spec := range domain.Commands() {
		if spec.Kind != domain.CommandAction && spec.Kind != domain.CommandActionArg {
			continue
		}
		if _, exists := catalog[spec.Name]; exists {
			t.Errorf("duplicate catalog action %q", spec.Name)
		}
		catalog[spec.Name] = spec.Kind
		if kind, ok := actions[spec.Name]; !ok || kind != spec.Kind {
			t.Errorf("catalog action %q (%v) has no matching bind action", spec.Name, spec.Kind)
		}
	}
	for name, kind := range actions {
		if got, ok := catalog[name]; !ok || got != kind {
			t.Errorf("bind action %q (%v) missing from catalog", name, kind)
		}
	}
}
