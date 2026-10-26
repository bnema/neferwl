package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

// cacheAgrees checks the Workspace layout cache against the table: the cache
// is filled and it names the layout layoutFor resolves for the same Overflow.
func cacheAgrees(t *testing.T, step string, w *Workspace) {
	t.Helper()
	want, ok := layout.Lookup(w.Overflow)
	if !ok {
		want, _ = layout.Lookup(layout.Default)
	}
	if got := w.Engine(); got != want {
		t.Fatalf("%s: engine %v, want %v for %q", step, got, want, w.Overflow)
	}

}

// Every production path that changes Overflow keeps the cache in step.
func TestWorkspaceLayoutCacheFollowsOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		cacheAgrees(t, "new monitor", &m.template)
		cacheAgrees(t, "default workspace", m.Current())

		for _, o := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll, "", OverflowCascade} {
			m.SetOverflow(o)
			cacheAgrees(t, "SetOverflow "+string(o), &m.template)
			for w := range m.all() {
				cacheAgrees(t, "SetOverflow "+string(o), w)
			}
			m.AddWindow(WindowID(len(o) + 1))
			m.FocusNumber(len(m.Workspaces) + 1) // a new dynamic workspace from the template
			for w := range m.all() {
				cacheAgrees(t, "new workspace under "+string(o), w)
			}
		}

		// Named overrides (config reload), then the override dropped.
		m.SetNamed([]NamedWorkspace{{Name: "dev", Overflow: OverflowFixed}})
		m.Apply("workspace dev")
		if m.Current().Name != "dev" || m.Current().Overflow != OverflowFixed {
			t.Fatalf("named workspace: %q %q", m.Current().Name, m.Current().Overflow)
		}
		for w := range m.all() {
			cacheAgrees(t, "SetNamed override", w)
		}
		m.useSpecs([]NamedWorkspace{{Name: "dev", Overflow: OverflowCascade}})
		for w := range m.all() {
			cacheAgrees(t, "useSpecs", w)
		}
		m.SetNamed([]NamedWorkspace{{Name: "dev"}})
		for w := range m.all() {
			cacheAgrees(t, "SetNamed without override", w)
		}
		m.SetNamed(nil)
		for w := range m.all() {
			cacheAgrees(t, "SetNamed(nil)", w)
		}

		// A workspace adopted from a monitor with another layout follows this one.
		other := newMonitorWithIDs("B", "B", m.nextID)
		other.SetOverflow(OverflowCascade)
		other.AddWindow(99)
		w := other.Current()
		other.take(w)
		m.adopt(w, true, 0)
		cacheAgrees(t, "adopt", w)
		if w.Overflow != m.template.Overflow {
			t.Fatalf("adopted overflow %q, want %q", w.Overflow, m.template.Overflow)
		}

		// Unknown values resolve to the default and still agree.
		var u Workspace
		u.SetOverflow("unknown")
		cacheAgrees(t, "unknown", &u)

	})
}
