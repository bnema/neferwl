package core

import (
	"testing"
	"testing/synctest"
)

// cacheAgrees checks the Workspace layout cache against the table: the cache
// is filled and it names the layout layoutFor resolves for the same Overflow.
func cacheAgrees(t *testing.T, step string, w *Workspace) {
	t.Helper()
	want, ok := Lookup(w.Overflow)
	if !ok {
		want, _ = Lookup(Default)
	}
	if w.lcache == nil || w.lcacheFor != w.Overflow {
		t.Fatalf("%s: cache %v for %q, overflow %q", step, w.lcache, w.lcacheFor, w.Overflow)
	}
	if got := w.Engine(); got != want || w.lcache != want {
		t.Fatalf("%s: layout %v cache %v, want %v for %q", step, got, w.lcache, want, w.Overflow)
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

// A direct write to the exported field, or a literal that never set the cache,
// still resolves from Overflow: the cache is only a shortcut.
func TestWorkspaceLayoutCacheNeverDisagrees(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var zero Workspace
		if zero.lcache != nil || zero.Engine() != layoutTable[0] {
			t.Fatal("the zero workspace must resolve to the default layout")
		}
		cacheAgrees(t, "zero after first use", &zero)
		w := &Workspace{Lanes: Lanes{Overflow: OverflowFixed}}
		if got, _ := Lookup(OverflowFixed); w.Engine() != got {
			t.Fatal("literal without a cache")
		}
		cacheAgrees(t, "literal after first use", w)
		w.SetOverflow(OverflowCascade)
		cacheAgrees(t, "set", w)
		for _, o := range []Overflow{OverflowScroll, OverflowFixed, "", "unknown", OverflowCascade} {
			w.Overflow = o // a stale cache must not win
			want, ok := Lookup(o)
			if !ok {
				want, _ = Lookup(Default)
			}
			if w.Engine() != want {
				t.Fatalf("direct write %q: layout %v, want %v", o, w.Engine(), want)
			}
		}
		// A test override still wins over both.
		w.Override = scrollLayout{}
		w.SetOverflow(OverflowFixed)
		if w.Engine() != (scrollLayout{}) {
			t.Fatal("lay override lost")
		}
		// Copies carry the cache with the Overflow it was resolved for.
		w.Override = nil
		c := *w
		c.Overflow = OverflowCascade
		if c.Engine() != (cascadeLayout{}) || w.Engine() != (fixedLayout{}) {
			t.Fatal("copy and original disagree with their overflow")
		}

	})
}
