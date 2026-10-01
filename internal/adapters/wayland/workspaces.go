package wayland

import (
	"context"
	"encoding/binary"

	ext "github.com/bnema/go-wayland-bindings/server/extworkspace"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/wayland/imagecapture"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

type workspaceManager struct {
	s       *Server
	res     *ext.ExtWorkspaceManagerV1
	groups  map[string]*workspaceGroup
	handles map[uint64]*workspaceHandle
	pending []uint64
}
type workspaceGroup struct {
	res     *ext.ExtWorkspaceGroupHandleV1
	outputs map[*server.Resource]bool // output_enter sent for each live wl_output
}
type workspaceHandle struct {
	res   *ext.ExtWorkspaceHandleV1
	info  ports.WorkspaceInfo
	group string
}

func registerWorkspaces(d *server.Display, s *Server) error {
	return ext.NewExtWorkspaceManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		m := &workspaceManager{s: s, groups: map[string]*workspaceGroup{}, handles: map[uint64]*workspaceHandle{}}
		r, err := ext.NewExtWorkspaceManagerV1(c, int32(v), id, m)
		if err != nil {
			return
		}
		m.res = r
		s.workspaceManagers = append(s.workspaceManagers, m)
		r.OnDestroy = func() { s.workspaceManagers = removeItem(s.workspaceManagers, m) }
		m.update(s.workspaceSnapshot)
	})
}
func (m *workspaceManager) Commit(*ext.ExtWorkspaceManagerV1) {
	if m.s.protected() {
		m.pending = nil
		return
	}
	ids := make([]uint64, 0, len(m.pending))
	for _, id := range m.pending {
		if h := m.handles[id]; h != nil && h.res.Alive() {
			ids = append(ids, id)
		}
	}
	m.pending = nil
	if len(ids) > 0 {
		m.s.emit(ports.WorkspaceActivate{IDs: ids})
	}
}
func (m *workspaceManager) Stop(*ext.ExtWorkspaceManagerV1) {
	m.res.SendFinished()
	m.res.Destroy()
}
func (*workspaceManager) CreateWorkspace(*ext.ExtWorkspaceGroupHandleV1, string) {}
func (*workspaceManager) Destroy(*ext.ExtWorkspaceGroupHandleV1)                 {}
func (m *workspaceManager) Activate(r *ext.ExtWorkspaceHandleV1) {
	if m.s.protected() {
		return
	}
	for id, h := range m.handles {
		if h.res.Resource == r.Resource {
			m.pending = append(m.pending, id)
			return
		}
	}
}

// workspaceRequests adapts the workspace destroy request separately from group destroy.
type workspaceRequests struct{ m *workspaceManager }

func (*workspaceRequests) Destroy(*ext.ExtWorkspaceHandleV1)                                {}
func (w *workspaceRequests) Activate(r *ext.ExtWorkspaceHandleV1)                           { w.m.Activate(r) }
func (*workspaceRequests) Deactivate(*ext.ExtWorkspaceHandleV1)                             {}
func (*workspaceRequests) Assign(*ext.ExtWorkspaceHandleV1, *ext.ExtWorkspaceGroupHandleV1) {}
func (*workspaceRequests) Remove(*ext.ExtWorkspaceHandleV1)                                 {}

// outputBound announces every wl_output resource bound by this client for an
// assigned output. A client may bind the same global more than once.
func (m *workspaceManager) outputBound(name string) bool {
	if m.s.protected() {
		return false
	}
	g := m.groups[name]
	o := m.s.outputByNameExact(name)
	if g == nil || !g.res.Alive() || o == nil {
		return false
	}
	if g.outputs == nil {
		g.outputs = make(map[*server.Resource]bool)
	}
	changed := false
	for _, r := range o.resources {
		if r.Client() != m.res.Client() || !r.Alive() || g.outputs[r.Resource] {
			continue
		}
		g.outputs[r.Resource] = true
		g.res.SendOutputEnter(r)
		changed = true
	}
	return changed
}

func (m *workspaceManager) leaveOutputs(g *workspaceGroup) {
	if !g.res.Alive() {
		return
	}
	for res := range g.outputs {
		if res.Alive() {
			g.res.SendOutputLeave(wayland.WrapOutput(res))
		}
	}
}
func (s *Server) forwardWorkspaces(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case snapshot, ok := <-s.channels.Workspaces:
			if !ok {
				return
			}
			if !s.display.Do(func() { s.updateWorkspaces(snapshot) }) {
				return
			}
		}
	}
}
func (s *Server) updateWorkspaces(snapshot ports.Workspaces) {
	s.workspaceSnapshot = snapshot
	s.updateWorkspaceManagers(snapshot)
}

// updateWorkspaceManagers also refreshes inventory on protection transitions.
// Keep the last sent state while protected: old client knowledge cannot be
// revoked, but an unlock refresh must compare against it, not suppressed updates.
func (s *Server) updateWorkspaceManagers(snapshot ports.Workspaces) {
	for _, m := range s.workspaceManagers {
		m.update(snapshot)
	}
	s.updateWorkspaceFrames()
	s.refreshSessions()
}

// workspaceFrame is a neferwl_workspace_frame_v1: the frame of one workspace,
// output-local logical, sent when it is created and when it changes. Nothing
// is sent for a workspace that is gone, nor while the session is protected.
type workspaceFrame struct {
	res    *imagecapture.NeferwlWorkspaceFrameV1
	handle *server.Resource
	id     uint64
	sent   bool
	last   ports.Rect
}

func (*workspaceFrame) Destroy(*imagecapture.NeferwlWorkspaceFrameV1) {}

func (s *Server) newWorkspaceFrame(c server.Client, id uint32, ws *ext.ExtWorkspaceHandleV1) {
	f := &workspaceFrame{}
	known := false
	if ws != nil {
		f.handle = ws.Resource
		f.id, known = s.workspaceIDOf(ws.Resource)
	}
	r, err := imagecapture.NewNeferwlWorkspaceFrameV1(c, 1, id, f)
	if err != nil {
		return
	}
	f.res = r
	if !known {
		return
	}
	s.workspaceFrames = append(s.workspaceFrames, f)
	r.OnDestroy = func() { s.workspaceFrames = removeItem(s.workspaceFrames, f) }
	s.updateWorkspaceFrames()
}

// updateWorkspaceFrames sends the frame of each workspace whose last sent one
// differs. A removed workspace gets nothing and its entry is dropped.
func (s *Server) updateWorkspaceFrames() {
	if s.protected() {
		return
	}
	kept := s.workspaceFrames[:0]
	for _, f := range s.workspaceFrames {
		// The handle must still exist: a removed or renamed handle never
		// returns, so its frame object is dropped.
		if id, ok := s.workspaceIDOf(f.handle); !ok || id != f.id {
			continue
		}
		kept = append(kept, f)
		if !f.res.Alive() {
			continue
		}
		_, w, ok := s.workspaceInfo(f.id)
		if !ok || (f.sent && f.last == w.Frame) {
			continue
		}
		f.sent, f.last = true, w.Frame
		f.res.SendFrame(int32(w.Frame.X), int32(w.Frame.Y), int32(w.Frame.W), int32(w.Frame.H))
	}
	clear(s.workspaceFrames[len(kept):])
	s.workspaceFrames = kept
}

func workspaceState(w ports.WorkspaceInfo) uint32 {
	var state uint32
	if w.Active {
		state |= uint32(ext.ExtWorkspaceHandleV1StateActive)
	}
	if w.Hidden {
		state |= uint32(ext.ExtWorkspaceHandleV1StateHidden)
	}
	return state
}
func (m *workspaceManager) sendCoordinates(r *ext.ExtWorkspaceHandleV1, index int) {
	var b [4]byte
	binary.NativeEndian.PutUint32(b[:], uint32(index))
	r.SendCoordinates(b[:])
}
func (m *workspaceManager) update(snapshot ports.Workspaces) {
	if m.s.protected() {
		m.pending = nil
		return
	}
	if !m.res.Alive() {
		return
	}
	wanted := map[string]bool{}
	infos := map[uint64]ports.WorkspaceInfo{}
	for _, out := range snapshot.Outputs {
		wanted[out.Name] = true
		for _, w := range out.Workspaces {
			infos[w.ID] = w
		}
	}
	// Move existing workspaces before retiring their old output group.
	for id, h := range m.handles {
		// A handle's id is fixed: a workspace that became configured or
		// unconfigured (a rename in the config) is a removed handle and a
		// new one.
		if info, exists := infos[id]; !exists || info.Configured != h.info.Configured {
			if g := m.groups[h.group]; g != nil && g.res.Alive() && h.res.Alive() {
				g.res.SendWorkspaceLeave(h.res)
			}
			if h.res.Alive() {
				h.res.SendRemoved()
			}
			delete(m.handles, id)
		}
	}
	for _, out := range snapshot.Outputs {
		g := m.groups[out.Name]
		if g == nil {
			r, err := ext.NewExtWorkspaceGroupHandleV1(m.res.Client(), 1, 0, m)
			if err != nil {
				continue
			}
			g = &workspaceGroup{res: r, outputs: map[*server.Resource]bool{}}
			m.groups[out.Name] = g
			m.res.SendWorkspaceGroup(r)
			r.SendCapabilities(0)
		}
		m.outputBound(out.Name)
		for _, w := range out.Workspaces {
			if !g.res.Alive() {
				continue
			}
			h := m.handles[w.ID]
			if h == nil {
				r, err := ext.NewExtWorkspaceHandleV1(m.res.Client(), 1, 0, &workspaceRequests{m})
				if err != nil {
					continue
				}
				h = &workspaceHandle{res: r}
				m.handles[w.ID] = h
				m.res.SendWorkspace(r)
				r.SendId(m.s.workspaceIDs.ID(w.ID, w.Configured))
				r.SendName(w.Name)
				m.sendCoordinates(r, w.Index)
				r.SendState(workspaceState(w))
				r.SendCapabilities(uint32(ext.ExtWorkspaceHandleV1WorkspaceCapabilitiesActivate))
				h.info = w
			}
			if !h.res.Alive() {
				continue
			}
			if h.info.ID != w.ID {
				continue
			}
			if h.info != w {
				if h.info.Name != w.Name {
					h.res.SendName(w.Name)
				}
				if h.info.Index != w.Index {
					m.sendCoordinates(h.res, w.Index)
				}
				if h.info.Active != w.Active || h.info.Hidden != w.Hidden {
					h.res.SendState(workspaceState(w))
				}
				h.info = w
			}
			if h.group != out.Name {
				if old := m.groups[h.group]; old != nil && old.res.Alive() && h.res.Alive() {
					old.res.SendWorkspaceLeave(h.res)
				}
				g.res.SendWorkspaceEnter(h.res)
				h.group = out.Name
			}
		}
	}
	for name, g := range m.groups {
		if !wanted[name] {
			m.leaveOutputs(g)
			if g.res.Alive() {
				g.res.SendRemoved()
			}
			delete(m.groups, name)
		}
	}
	m.res.SendDone()
}
