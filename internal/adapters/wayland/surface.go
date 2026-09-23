package wayland

import (
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
)

type roleKind uint8

const (
	roleNone roleKind = iota
	roleXDG
	roleSubsurface
	roleLayer
)

type surface struct {
	kind             roleKind
	xdg              *xdgSurface
	layer            *layerSurface
	server           *Server
	current, pending *wayland.Buffer
	attached         bool
	callbacks        []*wayland.Callback
	role             func(bool)
	destroyed        bool
	released         bool
}

func (s *surface) Destroy(*wayland.Surface) {
	s.destroyed = true
	for _, cb := range s.callbacks {
		cb.Destroy()
	}
	s.callbacks = nil
	s.current, s.pending = nil, nil
	if s.role != nil {
		s.role(false)
	}
}
func (s *surface) Attach(_ *wayland.Surface, b *wayland.Buffer, _, _ int32) {
	s.pending = b
	s.attached = true
}
func (s *surface) Frame(r *wayland.Surface, id uint32) {
	cb, err := wayland.NewCallback(r.Client(), 1, id, struct{}{})
	if err == nil {
		s.callbacks = append(s.callbacks, cb)
	}
}
func (s *surface) Commit(*wayland.Surface) {
	fresh := s.attached && s.pending != nil
	if s.layer != nil && s.attached && s.pending != nil && !s.layer.acked {
		s.layer.resource.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState), "buffer before configure ack")
		return
	}
	if s.xdg != nil && s.attached && s.pending != nil && !s.xdg.acked {
		s.xdg.resource.PostError(uint32(xdgshell.SurfaceErrorUnconfiguredBuffer), "buffer before initial configure ack")
		return
	}
	if s.attached {
		if s.current != nil && (s.pending == nil || s.current.Resource != s.pending.Resource) {
			if !s.released && s.current.Resource.Alive() {
				s.current.SendRelease()
			}
		}
		s.current = s.pending
		s.released = false
		s.pending = nil
		s.attached = false
	}
	s.server.awaiting = append(s.server.awaiting, s.callbacks...)
	s.callbacks = nil
	if s.role != nil {
		s.role(s.current != nil)
	}
	id := ports.WindowID(0)
	if s.xdg != nil && s.xdg.window != nil && s.xdg.window.mapped {
		id = s.xdg.window.id
	}
	if s.layer != nil && s.layer.mapped {
		id = s.layer.id
	}
	if fresh && id != 0 && s.server.channels.Contents != nil {
		b := s.current
		if state, ok := s.server.buffers[b.Resource]; ok {
			if c, ok := state.content(id); ok {
				s.server.emitContent(c)
			} else {
				state.pool.shm.PostError(uint32(wayland.ShmErrorInvalidFd), "SHM backing file truncated")
			}
			if b.Resource.Alive() {
				b.SendRelease()
			}
			s.released = true
		}
	}
}
func (*surface) Damage(*wayland.Surface, int32, int32, int32, int32)       {}
func (*surface) DamageBuffer(*wayland.Surface, int32, int32, int32, int32) {}
func (*surface) SetOpaqueRegion(*wayland.Surface, *wayland.Region)         {}
func (*surface) SetInputRegion(*wayland.Surface, *wayland.Region)          {}
func (*surface) SetBufferTransform(*wayland.Surface, int32)                {}
func (*surface) SetBufferScale(*wayland.Surface, int32)                    {}
func (*surface) Offset(*wayland.Surface, int32, int32)                     {}
func (*surface) GetRelease(*wayland.Surface, uint32)                       {}
