package wayland

import (
	"slices"
	"strconv"

	ext "github.com/bnema/go-wayland-bindings/server/extforeigntoplevellist"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// ext-foreign-toplevel-list lists mapped toplevels, read-only: title, app_id
// and a stable identifier. xdg-desktop-portal-wlr lists the windows it can
// share with it, and ext_foreign_toplevel_image_capture_source_manager_v1
// takes its handles (window capture, imagecopy.go). It follows the same
// window state and session-protection rules as wlr-foreign-toplevel.

type extToplevelList struct {
	s       *Server
	res     *ext.ExtForeignToplevelListV1
	stopped bool
	handles map[ports.WindowID]*extToplevelHandle
}

// extToplevelHandle is one window seen by one list, with what it was last
// sent so that only changes go out.
type extToplevelHandle struct {
	res          *ext.ExtForeignToplevelHandleV1
	window       ports.WindowID
	gen          uint32 // the window's mapping it was made for
	closed       bool
	title, appID string
}

func (extToplevelHandle) Destroy(*ext.ExtForeignToplevelHandleV1) {}

// toplevelIdentifier is the ext-foreign-toplevel identifier of a window's
// mapping: window IDs are never reused and a remap counts a new mapping, so
// neither is the identifier.
func toplevelIdentifier(id ports.WindowID, gen uint32) string {
	return "neferwl-" + strconv.FormatUint(uint64(id), 10) + "-" + strconv.FormatUint(uint64(gen), 10)
}

func registerExtForeignToplevel(d *server.Display, s *Server) error {
	return ext.NewExtForeignToplevelListV1Global(d, 1, func(c server.Client, v, id uint32) {
		l := &extToplevelList{s: s, handles: map[ports.WindowID]*extToplevelHandle{}}
		r, err := ext.NewExtForeignToplevelListV1(c, int32(v), id, l)
		if err != nil {
			return
		}
		l.res = r
		s.extToplevelLists = append(s.extToplevelLists, l)
		r.OnDestroy = func() { s.extToplevelLists = removeItem(s.extToplevelLists, l) }
		for _, id := range s.mappedToplevels() {
			l.refresh(s.windows[id])
		}
	})
}

// Stop ends the list: finished, and no more toplevel events.
func (l *extToplevelList) Stop(*ext.ExtForeignToplevelListV1) {
	if l.stopped {
		return
	}
	l.stopped = true
	l.res.SendFinished()
}

func (*extToplevelList) Destroy(*ext.ExtForeignToplevelListV1) {}

// mappedToplevels lists the mapped toplevels, by ID.
func (s *Server) mappedToplevels() []ports.WindowID {
	ids := make([]ports.WindowID, 0, len(s.windows))
	for id, w := range s.windows {
		if w.toplevel != nil && w.mapped {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// refresh announces w or sends what changed since the last done. After
// stop no new toplevel is announced; the handles already sent stay live.
func (l *extToplevelList) refresh(w *window) {
	if l.s.protected() || !l.res.Alive() {
		return
	}
	h := l.handles[w.id]
	if h != nil && (h.closed || h.gen != w.gen) {
		l.closed(w.id)
		h = nil
	}
	fresh := h == nil
	if fresh && l.stopped {
		return
	}
	if fresh {
		h = &extToplevelHandle{window: w.id, gen: w.gen}
		r, err := ext.NewExtForeignToplevelHandleV1(l.res.Client(), l.res.Version(), 0, h)
		if err != nil {
			return
		}
		h.res = r
		l.handles[w.id] = h
		l.res.SendToplevel(r)
		r.SendIdentifier(toplevelIdentifier(w.id, w.gen))
	}
	if !h.res.Alive() {
		return
	}
	changed := fresh
	if fresh || h.title != w.title {
		h.title = w.title
		h.res.SendTitle(w.title)
		changed = true
	}
	if fresh || h.appID != w.appID {
		h.appID = w.appID
		h.res.SendAppId(w.appID)
		changed = true
	}
	if changed {
		h.res.SendDone()
	}
}

// closed sends closed for a window; like wlr-foreign-toplevel, a close
// during session protection is sent at the unlock.
func (l *extToplevelList) closed(id ports.WindowID) {
	h := l.handles[id]
	if h == nil {
		return
	}
	h.closed = true
	if l.s.protected() {
		return
	}
	delete(l.handles, id)
	if h.res.Alive() {
		h.res.SendClosed()
	}
}

// extHandleWindow is the window mapping behind an
// ext_foreign_toplevel_handle_v1 of any list, while it is open.
func (s *Server) extHandleWindow(res *server.Resource) (ports.WindowID, uint32, bool) {
	for _, l := range s.extToplevelLists {
		for id, h := range l.handles {
			if h.res.Resource == res && !h.closed {
				return id, h.gen, true
			}
		}
	}
	return 0, 0, false
}
