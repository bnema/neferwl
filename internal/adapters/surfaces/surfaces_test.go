package surfaces

import (
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func shows(ids ...ports.WindowID) ports.Scene {
	s := ports.Scene{OutputWidth: 100, OutputHeight: 100}
	for _, id := range ids {
		s.Windows = append(s.Windows, ports.SceneWindow{ID: id, Rect: ports.Rect{W: 10, H: 10}})
	}
	return s
}

// An empty content of a window the scene draws keeps the previous one with
// its Acquire fence dropped; the content goes once a scene no longer lists
// the window.
func TestEmptyContentKeptWhileShown(t *testing.T) {
	tb := New()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	full := ports.SurfaceContent{ID: 1, Seq: 4, Width: 10, Height: 10, DMABuf: &ports.DMABuf{ID: 7}, Acquire: r}
	tb.Update(full, shows(1))
	tb.Update(ports.SurfaceContent{ID: 1, Seq: 5}, shows(1))
	kept, ok := tb.Map()[1]
	if !ok || kept.DMABuf != full.DMABuf || kept.Seq != 4 {
		t.Fatalf("kept %+v, want the previous content", kept)
	}
	if kept.Acquire != nil {
		t.Fatal("the kept content keeps its Acquire fence")
	}
	if !tb.Kept(1) {
		t.Fatal("Kept false for a content drawn after its empty one")
	}
	// A scene still listing it keeps it; one without it drops it.
	tb.Prune(shows(1, 2))
	if _, ok := tb.Map()[1]; !ok {
		t.Fatal("dropped while still listed")
	}
	tb.Prune(shows(2))
	if _, ok := tb.Map()[1]; ok || tb.Kept(1) {
		t.Fatal("not dropped once unlisted")
	}
}

// An empty content of a window the scene does not draw drops it at once.
func TestEmptyContentOfUnshownWindowDropped(t *testing.T) {
	tb := New()
	tb.Update(ports.SurfaceContent{ID: 1, SHM: &ports.SHMBuffer{Pool: 1}}, shows(1))
	tb.Update(ports.SurfaceContent{ID: 1}, shows(2))
	if _, ok := tb.Map()[1]; ok || tb.Kept(1) {
		t.Fatal("content of an unshown window kept")
	}
	// Hidden counts as not drawn.
	tb.Update(ports.SurfaceContent{ID: 3, SHM: &ports.SHMBuffer{Pool: 3}}, shows())
	hidden := ports.Scene{OutputWidth: 100, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 3, Hidden: true, Rect: ports.Rect{W: 10, H: 10}}}}
	tb.Update(ports.SurfaceContent{ID: 3}, hidden)
	if _, ok := tb.Map()[3]; ok {
		t.Fatal("content of a hidden window kept")
	}
}

// A new content replaces a kept one and the window is no longer kept (the
// client mapped again).
func TestNewContentReplacesKept(t *testing.T) {
	tb := New()
	tb.Update(ports.SurfaceContent{ID: 1, Seq: 1, SHM: &ports.SHMBuffer{Pool: 1}}, shows(1))
	tb.Update(ports.SurfaceContent{ID: 1, Seq: 2}, shows(1))
	tb.Update(ports.SurfaceContent{ID: 1, Seq: 3, SHM: &ports.SHMBuffer{Pool: 2}}, shows(1))
	if c := tb.Map()[1]; c.Seq != 3 || c.SHM.Pool != 2 || tb.Kept(1) {
		t.Fatalf("after a new content: %+v kept %t", c, tb.Kept(1))
	}
	// The map handed out is the table's own: the renderer sees updates.
	m := tb.Map()
	tb.Update(ports.SurfaceContent{ID: 2, Seq: 1, SHM: &ports.SHMBuffer{Pool: 9}}, shows(1, 2))
	if _, ok := m[2]; !ok {
		t.Fatal("Map is a copy")
	}
}

// Update and Prune allocate nothing in steady state.
func TestUpdateAllocations(t *testing.T) {
	tb := New()
	s := shows(1, 2)
	c := ports.SurfaceContent{ID: 1, Seq: 1, SHM: &ports.SHMBuffer{Pool: 1}}
	tb.Update(c, s)
	if n := testing.AllocsPerRun(100, func() {
		c.Seq++
		tb.Update(c, s)
		tb.Prune(s)
	}); n != 0 {
		t.Fatalf("%v allocations per update", n)
	}
}

// The kept content's children lose their Acquire fences too, on a copy:
// wayland's own slice (shared with its publication) is left alone.
func TestKeptChildrenFencesDroppedOnACopy(t *testing.T) {
	tb := New()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	children := []ports.Subsurface{{SurfaceContent: ports.SurfaceContent{DMABuf: &ports.DMABuf{ID: 2}, Acquire: r}}}
	full := ports.SurfaceContent{ID: 1, Seq: 1, SHM: &ports.SHMBuffer{Pool: 1}, Children: children}
	tb.Update(full, shows(1))
	tb.Update(ports.SurfaceContent{ID: 1, Seq: 2}, shows(1))
	kept := tb.Map()[1]
	if len(kept.Children) != 1 || kept.Children[0].Acquire != nil || kept.Children[0].DMABuf != children[0].DMABuf {
		t.Fatalf("kept children %+v", kept.Children)
	}
	if children[0].Acquire != r {
		t.Fatal("wayland's slice was modified")
	}
}
