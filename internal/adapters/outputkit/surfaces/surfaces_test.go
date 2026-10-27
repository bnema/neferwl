package surfaces

import (
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
)

func shows(ids ...domain.WindowID) domain.Scene {
	s := domain.Scene{OutputWidth: 100, OutputHeight: 100}
	for _, id := range ids {
		s.Windows = append(s.Windows, domain.SceneWindow{ID: id, Rect: domain.Rect{W: 10, H: 10}})
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
	full := domain.SurfaceContent{ID: 1, Seq: 4, Width: 10, Height: 10, DMABuf: &domain.DMABuf{ID: 7}, Acquire: r}
	tb.Update(full, shows(1))
	tb.Update(domain.SurfaceContent{ID: 1, Seq: 5}, shows(1))
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
	tb.Update(domain.SurfaceContent{ID: 1, SHM: &domain.SHMBuffer{Pool: 1}}, shows(1))
	tb.Update(domain.SurfaceContent{ID: 1}, shows(2))
	if _, ok := tb.Map()[1]; ok || tb.Kept(1) {
		t.Fatal("content of an unshown window kept")
	}
	// Hidden counts as not drawn.
	tb.Update(domain.SurfaceContent{ID: 3, SHM: &domain.SHMBuffer{Pool: 3}}, shows())
	hidden := domain.Scene{OutputWidth: 100, OutputHeight: 100, Windows: []domain.SceneWindow{{ID: 3, Hidden: true, Rect: domain.Rect{W: 10, H: 10}}}}
	tb.Update(domain.SurfaceContent{ID: 3}, hidden)
	if _, ok := tb.Map()[3]; ok {
		t.Fatal("content of a hidden window kept")
	}
}

// A new content replaces a kept one and the window is no longer kept (the
// client mapped again).
func TestNewContentReplacesKept(t *testing.T) {
	tb := New()
	tb.Update(domain.SurfaceContent{ID: 1, Seq: 1, SHM: &domain.SHMBuffer{Pool: 1}}, shows(1))
	tb.Update(domain.SurfaceContent{ID: 1, Seq: 2}, shows(1))
	tb.Update(domain.SurfaceContent{ID: 1, Seq: 3, SHM: &domain.SHMBuffer{Pool: 2}}, shows(1))
	if c := tb.Map()[1]; c.Seq != 3 || c.SHM.Pool != 2 || tb.Kept(1) {
		t.Fatalf("after a new content: %+v kept %t", c, tb.Kept(1))
	}
	// The map handed out is the table's own: the renderer sees updates.
	m := tb.Map()
	tb.Update(domain.SurfaceContent{ID: 2, Seq: 1, SHM: &domain.SHMBuffer{Pool: 9}}, shows(1, 2))
	if _, ok := m[2]; !ok {
		t.Fatal("Map is a copy")
	}
}

// Update and Prune allocate nothing in steady state.
func TestUpdateAllocations(t *testing.T) {
	tb := New()
	s := shows(1, 2)
	c := domain.SurfaceContent{ID: 1, Seq: 1, SHM: &domain.SHMBuffer{Pool: 1}}
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
	children := []domain.Subsurface{{SurfaceContent: domain.SurfaceContent{DMABuf: &domain.DMABuf{ID: 2}, Acquire: r}}}
	full := domain.SurfaceContent{ID: 1, Seq: 1, SHM: &domain.SHMBuffer{Pool: 1}, Children: children}
	tb.Update(full, shows(1))
	tb.Update(domain.SurfaceContent{ID: 1, Seq: 2}, shows(1))
	kept := tb.Map()[1]
	if len(kept.Children) != 1 || kept.Children[0].Acquire != nil || kept.Children[0].DMABuf != children[0].DMABuf {
		t.Fatalf("kept children %+v", kept.Children)
	}
	if children[0].Acquire != r {
		t.Fatal("wayland's slice was modified")
	}
}

// Both backends report a window shown only when the scene draws it and its
// content is not kept.
func TestShowsSnapshotSkipsKept(t *testing.T) {
	kept := func(id domain.WindowID) bool { return id == 2 }
	var snap ShowsSnapshot
	got := snap.FromSeen(shows(1, 2), map[domain.WindowID]uint64{1: 3, 2: 4, 3: 5}, kept)
	if len(got) != 1 || got[1] != 3 {
		t.Fatalf("shown from seen %v", got)
	}
	var fromContents ShowsSnapshot
	contents := map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Seq: 3}, 2: {ID: 2, Seq: 4}, 3: {ID: 3, Seq: 5}}
	got = fromContents.FromContents(shows(1, 2), contents, kept)
	if len(got) != 1 || got[1] != 3 {
		t.Fatalf("shown from contents %v", got)
	}
	if !Shown(shows(2), 2, nil) {
		t.Fatal("nil kept counts nothing as kept")
	}
}

// An unchanged snapshot is reused without allocating; a changed one never
// mutates a map already handed out.
func TestShowsSnapshotImmutable(t *testing.T) {
	var snap ShowsSnapshot
	scene := shows(1)
	seen := map[domain.WindowID]uint64{1: 1, 2: 2}
	first := snap.FromSeen(scene, seen, nil)
	if allocs := testing.AllocsPerRun(100, func() { snap.FromSeen(scene, seen, nil) }); allocs != 0 {
		t.Errorf("unchanged shows: %.1f allocs, want 0", allocs)
	}
	seen[1] = 3
	second := snap.FromSeen(scene, seen, nil)
	if first[1] != 1 || second[1] != 3 {
		t.Fatalf("snapshot mutated: old %v new %v", first, second)
	}
}

// Apply takes the batch into the table, advances seen, reports what needs a
// frame or a report, then resets the batch before giving it back.
func TestApply(t *testing.T) {
	tb := New()
	seen := map[domain.WindowID]uint64{1: 5}
	b := &domain.ContentBatch{Items: []domain.SurfaceContent{
		{ID: 1, Seq: 4, SHM: &domain.SHMBuffer{Pool: 1}}, // older than seen
		{ID: 2, Seq: 1, SHM: &domain.SHMBuffer{Pool: 2}},
	}}
	ret := portsmocks.NewMockContentReturn(t)
	ret.EXPECT().Return(b).Run(func(got *domain.ContentBatch) {
		if len(got.Items) != 0 {
			t.Errorf("batch returned before its reset: %d items", len(got.Items))
		}
	}).Once()
	dirty, changed := tb.Apply(b, shows(2), seen, ret)
	if !dirty || !changed {
		t.Fatalf("dirty %v changed %v, want both", dirty, changed)
	}
	if seen[1] != 5 || seen[2] != 1 {
		t.Fatalf("seen %v", seen)
	}
	if len(tb.Map()) != 2 {
		t.Fatalf("table %v", tb.Map())
	}

	// A window no scene draws and no newer Seq: nothing to do.
	b = &domain.ContentBatch{Items: []domain.SurfaceContent{{ID: 1, Seq: 5, SHM: &domain.SHMBuffer{Pool: 1}}}}
	ret.EXPECT().Return(b).Once()
	dirty, changed = tb.Apply(b, shows(2), seen, ret)
	if dirty || changed {
		t.Fatalf("dirty %v changed %v, want neither", dirty, changed)
	}
}
