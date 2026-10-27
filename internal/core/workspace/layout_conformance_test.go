package workspace

import (
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

// testLayouts are layouts only tests know. A test file adds its own from an
// init function; they reach a Workspace through Lanes.Override and never enter
// the registry.
var testLayouts []layout.Layout

// conformanceLayouts is every layout the conformance tables cover: the
// registry's, plus the test-only ones.
func conformanceLayouts() []layout.Layout {
	return append(registeredLayouts(), testLayouts...)
}

// conformanceModes are the column states every invariant is checked in.
var conformanceModes = []string{"normal", "maximized", "fullscreen"}

// conformanceWorkspace is the one fixture of the conformance tables: n columns
// of two windows (the second focused in its column) in k-column capacity,
// the focus on column n/2, which is maximized or holds the fullscreen window
// per mode, scrolled into view. The layout is injected through Lanes.Override.
func conformanceWorkspace(l layout.Layout, k, n int, mode string) *Workspace {
	w := &Workspace{Lanes: layout.Lanes{Overflow: l.Name(), Override: l, MaxColumns: k, Gaps: 8,
		Output: Rect{X: 10, Y: 20, W: 1200, H: 900}, Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}}}
	for i := 0; i < n; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(2*i + 1), WindowID(2*i + 2)}, Focus: 1})
	}
	if n > 0 {
		w.Focus = n / 2
		switch mode {
		case "maximized":
			w.Columns[w.Focus].FullWidth = true
		case "fullscreen":
			w.Fullscreen = w.Columns[w.Focus].Windows[1]
		}
	}
	w.scroll()
	return w
}

// forEachConformance runs f on every layout x capacity 1-4 x column count
// (0, 1, k, k+1, 2k+3) x mode.
func forEachConformance(t *testing.T, f func(t *testing.T, l layout.Layout, w *Workspace, k, n int, mode string)) {
	t.Helper()
	for _, l := range conformanceLayouts() {
		for k := 1; k <= 4; k++ {
			for _, n := range []int{0, 1, k, k + 1, 2*k + 3} {
				for _, mode := range conformanceModes {
					t.Run(fmt.Sprintf("%s/k=%d/n=%d/%s", l.Name(), k, n, mode), func(t *testing.T) {
						synctest.Test(t, func(t *testing.T) {
							f(t, l, conformanceWorkspace(l, k, n, mode), k, n, mode)

						})
					})
				}
			}
		}
	}
}

func TestLayoutRegistryResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		seen := map[Overflow]bool{}
		for _, l := range registeredLayouts() {
			key := l.Name()
			if seen[key] {
				t.Fatalf("duplicate layout %q", key)
			}
			seen[key] = true
			got, ok := layout.Lookup(key)
			if !ok || got != l {
				t.Fatalf("missing %q", key)
			}
			w := Workspace{Lanes: layout.Lanes{Overflow: key}}
			if w.Engine() != l {
				t.Fatal("workspace resolution")
			}
		}
		def, _ := layout.Lookup(layout.Default)
		if l, ok := layout.Lookup(""); !ok || l != def || def.Name() != layout.Default {
			t.Fatal("empty layout must default to " + string(layout.Default))
		}
		if _, ok := layout.Lookup("unknown"); ok {
			t.Fatal("unknown layout accepted")
		}
		if w := (Workspace{Lanes: layout.Lanes{Overflow: "unknown"}}); w.Engine() != def {
			t.Fatal("unknown layout must fall back to the default")
		}

	})
}
