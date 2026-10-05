// Package surfaces keeps the latest content of every window for an output's
// renderer, and the last content of a window that closed while the scene
// still draws it.
package surfaces

import (
	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/ports"
)

// Table is what an output passes to Renderer.Render: the latest content per
// window. Its owner goroutine is the only user.
//
// A window that unmaps sends an empty content right after the core learns
// of it; the scene that follows may keep drawing the window while it fades
// out (ports.SceneWindow of a leaving window). The table then keeps the
// window's last content until a scene no longer lists it, so the renderer
// draws it from the buffer it already imported or copied: no new upload,
// no copy. Its Acquire fence is dropped: the first frame that drew the
// content waited on it, and wayland closes it once the buffer is released.
//
// Buffer lifetime: wayland releases the buffer to the client once the
// output reports the empty content as seen, so the client may draw into or
// destroy it during the fade. A dmabuf stays importable through the
// renderer's own duplicated descriptors (a destroyed one shows as stale
// pixels at worst); a wl_shm pool stays mapped by the renderer while it is
// drawn, so a destroyed pool is still readable (a client that truncates the
// file under a mapping has always been able to fault a reader).
type Table struct {
	m map[ports.WindowID]ports.SurfaceContent
	// kept are the windows drawn from a content their client withdrew.
	kept map[ports.WindowID]struct{}
}

// New returns an empty table.
func New() *Table {
	return &Table{m: make(map[ports.WindowID]ports.SurfaceContent), kept: make(map[ports.WindowID]struct{})}
}

// Map is the contents the renderer draws from. It is the table's own map:
// read it, never keep it across an Update or Prune.
func (t *Table) Map() map[ports.WindowID]ports.SurfaceContent { return t.m }

// Update takes a window's new content. An empty one (the window unmapped)
// keeps the previous content, without its Acquire fence, while scene draws
// the window; it drops it otherwise.
func (t *Table) Update(c ports.SurfaceContent, scene ports.Scene) {
	if !c.Empty() {
		t.m[c.ID] = c
		delete(t.kept, c.ID)
		return
	}
	prev, ok := t.m[c.ID]
	if !ok || !capture.Shows(scene, c.ID) {
		delete(t.m, c.ID)
		delete(t.kept, c.ID)
		return
	}
	prev.Acquire = nil
	t.m[c.ID] = prev
	t.kept[c.ID] = struct{}{}
}

// Prune drops the kept contents of windows scene no longer draws. Outputs
// call it with every scene they accept.
func (t *Table) Prune(scene ports.Scene) {
	for id := range t.kept {
		if !capture.Shows(scene, id) {
			delete(t.m, id)
			delete(t.kept, id)
		}
	}
}

// Kept reports whether id is drawn from a content its client withdrew.
func (t *Table) Kept(id ports.WindowID) bool {
	_, ok := t.kept[id]
	return ok
}
