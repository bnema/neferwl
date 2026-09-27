package wayland

import (
	"bytes"
	"encoding/binary"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrforeigntoplevel"
	"github.com/bnema/purego-libwayland/server"
)

// wlr-foreign-toplevel-management lists mapped toplevels for taskbars and
// notification daemons (Dunst reads the fullscreen state). Every client
// may bind it, as on other compositors: it reveals less than screencopy.
// Requests reuse the messages of xdg-shell and xdg-activation.

type toplevelManager struct {
	s       *Server
	res     *wlr.ZwlrForeignToplevelManagerV1
	handles map[ports.WindowID]*toplevelHandle
}

// toplevelHandle is one window seen by one manager, with what it was last
// sent so that only changes go out.
type toplevelHandle struct {
	res          *wlr.ZwlrForeignToplevelHandleV1
	title, appID string
	state        []byte
	outputs      map[*server.Resource]bool // output_enter sent
}

func registerForeignToplevel(d *server.Display, s *Server) error {
	return wlr.NewZwlrForeignToplevelManagerV1Global(d, 3, func(c server.Client, v, id uint32) {
		m := &toplevelManager{s: s, handles: map[ports.WindowID]*toplevelHandle{}}
		r, err := wlr.NewZwlrForeignToplevelManagerV1(c, int32(v), id, m)
		if err != nil {
			return
		}
		m.res = r
		s.toplevelManagers = append(s.toplevelManagers, m)
		r.OnDestroy = func() { s.toplevelManagers = removeItem(s.toplevelManagers, m) }
		ids := make([]ports.WindowID, 0, len(s.windows))
		for id, w := range s.windows {
			if w.toplevel != nil && w.mapped {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			m.refresh(s.windows[id])
		}
	})
}

func (m *toplevelManager) Stop(*wlr.ZwlrForeignToplevelManagerV1) {
	m.res.SendFinished()
	m.res.Destroy()
}

// refresh announces w or sends what changed since the last done.
func (m *toplevelManager) refresh(w *window) {
	if !m.res.Alive() {
		return
	}
	h := m.handles[w.id]
	fresh := h == nil
	if fresh {
		r, err := wlr.NewZwlrForeignToplevelHandleV1(m.res.Client(), m.res.Version(), 0, toplevelRequests{s: m.s, id: w.id})
		if err != nil {
			return
		}
		h = &toplevelHandle{res: r, outputs: map[*server.Resource]bool{}}
		m.handles[w.id] = h
		m.res.SendToplevel(r)
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
	if state := toplevelState(w); fresh || !bytes.Equal(h.state, state) {
		h.state = state
		h.res.SendState(state)
		changed = true
	}
	if h.syncOutputs(m.s, m.res.Client(), w) {
		changed = true
	}
	if changed {
		h.res.SendDone()
	}
}

// closed sends closed for w; the client then destroys the handle.
func (m *toplevelManager) closed(id ports.WindowID) {
	h := m.handles[id]
	if h == nil {
		return
	}
	delete(m.handles, id)
	if h.res.Alive() {
		h.res.SendClosed()
	}
}

// syncOutputs sends output_enter for every wl_output the client bound for
// the window's output and output_leave for the others.
func (h *toplevelHandle) syncOutputs(s *Server, c server.Client, w *window) bool {
	want := map[*server.Resource]bool{}
	if o := s.outputByNameExact(w.last.Output); o != nil && w.hasLast {
		for _, r := range o.resources {
			if r.Client() == c && r.Alive() {
				want[r.Resource] = true
			}
		}
	}
	changed := false
	for r := range h.outputs {
		if !want[r] {
			delete(h.outputs, r)
			if r.Alive() {
				h.res.SendOutputLeave(wayland.WrapOutput(r))
				changed = true
			}
		}
	}
	for r := range want {
		if !h.outputs[r] {
			h.outputs[r] = true
			h.res.SendOutputEnter(wayland.WrapOutput(r))
			changed = true
		}
	}
	return changed
}

// toplevelState mirrors the xdg_toplevel states of the last configure:
// tiled windows are maximized.
func toplevelState(w *window) []byte {
	if !w.hasLast {
		return []byte{}
	}
	c := w.last
	var states []byte
	add := func(st wlr.ZwlrForeignToplevelHandleV1State) {
		states = binary.NativeEndian.AppendUint32(states, uint32(st))
	}
	if !c.Fullscreen && !c.Floating {
		add(wlr.ZwlrForeignToplevelHandleV1StateMaximized)
	}
	if c.Activated {
		add(wlr.ZwlrForeignToplevelHandleV1StateActivated)
	}
	if c.Fullscreen {
		add(wlr.ZwlrForeignToplevelHandleV1StateFullscreen)
	}
	if states == nil {
		return []byte{}
	}
	return states
}

// toplevelChanged updates every manager after w mapped or changed.
func (s *Server) toplevelChanged(w *window) {
	if w.toplevel == nil || !w.mapped {
		return
	}
	for _, m := range s.toplevelManagers {
		m.refresh(w)
	}
}

// toplevelClosed tells every manager that w unmapped.
func (s *Server) toplevelClosed(id ports.WindowID) {
	for _, m := range s.toplevelManagers {
		m.closed(id)
	}
}

// refreshToplevels resends the output events of every handle after a
// wl_output was bound or an output removed.
func (s *Server) refreshToplevels() {
	for _, m := range s.toplevelManagers {
		for id := range m.handles {
			if w := s.windows[id]; w != nil {
				m.refresh(w)
			}
		}
	}
}

// toplevelRequests handles requests on one handle. Its window may be gone:
// requests on a closed handle are ignored.
type toplevelRequests struct {
	s  *Server
	id ports.WindowID
}

func (t toplevelRequests) window() *window {
	if w := t.s.windows[t.id]; w != nil && w.toplevel != nil && w.mapped && w.toplevel.Resource.Alive() {
		return w
	}
	return nil
}

// Tiled windows are always maximized and nothing minimizes: these are no-ops.
func (toplevelRequests) SetMaximized(*wlr.ZwlrForeignToplevelHandleV1)   {}
func (toplevelRequests) UnsetMaximized(*wlr.ZwlrForeignToplevelHandleV1) {}
func (toplevelRequests) SetMinimized(*wlr.ZwlrForeignToplevelHandleV1)   {}
func (toplevelRequests) UnsetMinimized(*wlr.ZwlrForeignToplevelHandleV1) {}
func (toplevelRequests) Destroy(*wlr.ZwlrForeignToplevelHandleV1)        {}

func (t toplevelRequests) Activate(*wlr.ZwlrForeignToplevelHandleV1, *wayland.Seat) {
	if w := t.window(); w != nil {
		t.s.emit(ports.WindowActivate{ID: w.id})
	}
}
func (t toplevelRequests) Close(*wlr.ZwlrForeignToplevelHandleV1) {
	if w := t.window(); w != nil {
		w.toplevel.SendClose()
	}
}
func (toplevelRequests) SetRectangle(r *wlr.ZwlrForeignToplevelHandleV1, _ *wayland.Surface, _, _, w, h int32) {
	if w < 0 || h < 0 {
		r.PostError(uint32(wlr.ZwlrForeignToplevelHandleV1ErrorInvalidRectangle), "negative rectangle size")
	}
}
func (t toplevelRequests) SetFullscreen(*wlr.ZwlrForeignToplevelHandleV1, *wayland.Output) {
	if w := t.window(); w != nil {
		t.s.emit(ports.WindowFullscreenRequest{ID: w.id, Fullscreen: true})
	}
}
func (t toplevelRequests) UnsetFullscreen(*wlr.ZwlrForeignToplevelHandleV1) {
	if w := t.window(); w != nil {
		t.s.emit(ports.WindowFullscreenRequest{ID: w.id})
	}
}
