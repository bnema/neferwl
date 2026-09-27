package wayland

import (
	"context"
	"encoding/binary"
	"reflect"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
	ext "github.com/bnema/purego-libwayland/protocol/extworkspace"
	"github.com/bnema/purego-libwayland/protocol/wayland"
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
	res    *ext.ExtWorkspaceGroupHandleV1
	output *wayland.Output
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
	for _, id := range m.pending {
		if h := m.handles[id]; h != nil && h.res.Alive() {
			m.s.emit(ports.WorkspaceActivate{ID: id})
		}
	}
	m.pending = nil
}
func (m *workspaceManager) Stop(*ext.ExtWorkspaceManagerV1) {
	m.res.SendFinished()
	m.res.Destroy()
}
func (*workspaceManager) CreateWorkspace(*ext.ExtWorkspaceGroupHandleV1, string) {}
func (*workspaceManager) Destroy(*ext.ExtWorkspaceGroupHandleV1)                 {}
func (m *workspaceManager) Activate(r *ext.ExtWorkspaceHandleV1) {
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

func (m *workspaceManager) outputResource(name string) *wayland.Output {
	o := m.s.outputByNameExact(name)
	if o == nil {
		return nil
	}
	for _, r := range o.resources {
		if r.Client() == m.res.Client() && r.Alive() {
			return r
		}
	}
	return nil
}
func (m *workspaceManager) outputBound(name string) {
	g := m.groups[name]
	if g == nil || !g.res.Alive() {
		return
	}
	r := m.outputResource(name)
	if r == g.output {
		return
	}
	if g.output != nil && g.output.Alive() {
		g.res.SendOutputLeave(g.output)
	}
	g.output = r
	if r != nil {
		g.res.SendOutputEnter(r)
	}
	m.res.SendDone()
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
	for _, m := range s.workspaceManagers {
		m.update(snapshot)
	}
}
func (m *workspaceManager) update(snapshot ports.Workspaces) {
	if !m.res.Alive() {
		return
	}
	wanted := map[string]bool{}
	ids := map[uint64]bool{}
	for _, out := range snapshot.Outputs {
		wanted[out.Name] = true
		for _, w := range out.Workspaces {
			ids[w.ID] = true
		}
	}
	// Move existing workspaces before retiring their old output group.
	for id, h := range m.handles {
		if !ids[id] {
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
		if g != nil && !g.res.Alive() {
			delete(m.groups, out.Name)
			g = nil
		}
		if g == nil {
			r, err := ext.NewExtWorkspaceGroupHandleV1(m.res.Client(), 1, 0, m)
			if err != nil {
				continue
			}
			g = &workspaceGroup{res: r}
			m.groups[out.Name] = g
			m.res.SendWorkspaceGroup(r)
			r.SendCapabilities(0)
		}
		m.outputBound(out.Name)
		for _, w := range out.Workspaces {
			h := m.handles[w.ID]
			if h != nil && !h.res.Alive() {
				delete(m.handles, w.ID)
				h = nil
			}
			if h == nil {
				r, err := ext.NewExtWorkspaceHandleV1(m.res.Client(), 1, 0, &workspaceRequests{m})
				if err != nil {
					continue
				}
				h = &workspaceHandle{res: r}
				m.handles[w.ID] = h
				m.res.SendWorkspace(r)
				r.SendCapabilities(uint32(ext.ExtWorkspaceHandleV1WorkspaceCapabilitiesActivate))
			}
			if h.group != out.Name {
				if old := m.groups[h.group]; old != nil && old.res.Alive() && h.res.Alive() {
					old.res.SendWorkspaceLeave(h.res)
				}
				g.res.SendWorkspaceEnter(h.res)
				h.group = out.Name
			}
			if !reflect.DeepEqual(h.info, w) {
				if h.info.Name != w.Name {
					h.res.SendName(w.Name)
				}
				if h.info.Index != w.Index || h.info.ID == 0 {
					var b [4]byte
					binary.NativeEndian.PutUint32(b[:], uint32(w.Index))
					h.res.SendCoordinates(b[:])
				}
				if h.info.Active != w.Active || h.info.Hidden != w.Hidden || h.info.ID == 0 {
					var state uint32
					if w.Active {
						state |= uint32(ext.ExtWorkspaceHandleV1StateActive)
					}
					if w.Hidden {
						state |= uint32(ext.ExtWorkspaceHandleV1StateHidden)
					}
					h.res.SendState(state)
				}
				if h.info.ID == 0 && w.Name != "" {
					h.res.SendId(strconv.FormatUint(w.ID, 10))
				}
				h.info = w
			}
		}
	}
	for name, g := range m.groups {
		if !wanted[name] {
			if g.output != nil && g.output.Alive() && g.res.Alive() {
				g.res.SendOutputLeave(g.output)
			}
			if g.res.Alive() {
				g.res.SendRemoved()
			}
			delete(m.groups, name)
		}
	}
	m.res.SendDone()
}
