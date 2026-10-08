// Package surfaces keeps the latest content of every window for an output's
// renderer, and the last content of a window that closed while the scene
// still draws it.
package surfaces

import (
	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
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
// output reports the empty content as seen, so the client may draw into,
// destroy or reuse it during the fade. A kept content is therefore only
// ever composed by the renderer, from what it already holds: the dmabuf
// import (its own duplicated descriptors keep the memory alive; a reused
// buffer shows stale pixels at worst) or the GPU copy of the wl_shm buffer
// (the GPU never reads the pool mapping, and a pool's fd close does not
// unmap it). A kept content is never scanned out on a plane and never
// reported as shown: outputs ask Kept. Window capture never reaches it:
// core resolves a window session through its workspace, which the closed
// window has left, so the session ends (CaptureReasonWindowGone) before
// the leaving scene; an output or workspace capture composes the fading
// window like the screen does.
// Every Acquire fence of the kept content (root and children) is dropped:
// the frame that first drew it waited on them, and wayland closes them
// when the buffers are released.
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
	if len(prev.Children) > 0 {
		// The slice is shared with wayland's publication: copy before
		// zeroing (one allocation per unmap).
		children := make([]ports.Subsurface, len(prev.Children))
		copy(children, prev.Children)
		for i := range children {
			children[i].Acquire = nil
		}
		prev.Children = children
	}
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

// Kept reports whether id is drawn from a content its client withdrew: a
// buffer the client may have got back. Outputs compose it only: no plane
// scans it out, no capture or presentation report counts it as shown.
func (t *Table) Kept(id ports.WindowID) bool {
	_, ok := t.kept[id]
	return ok
}

// Shown is the presentation rule both output backends report with: id was
// shown to its client when scene draws it and its content is not kept. A
// nil kept counts nothing as kept.
func Shown(scene ports.Scene, id ports.WindowID, kept func(ports.WindowID) bool) bool {
	return scene.Shows(id) && (kept == nil || !kept(id))
}

// FillShown clears dst and fills it with the windows of seen that scene
// showed to their client (Shown), at their seen Seq.
func FillShown(dst, seen map[ports.WindowID]uint64, scene ports.Scene, kept func(ports.WindowID) bool) {
	clear(dst)
	for id, seq := range seen {
		if Shown(scene, id, kept) {
			dst[id] = seq
		}
	}
}
