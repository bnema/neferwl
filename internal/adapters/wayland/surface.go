package wayland

import "github.com/bnema/purego-libwayland/protocol/wayland"

type surface struct {
	server           *Server
	current, pending *wayland.Buffer
	attached         bool
	callbacks        []*wayland.Callback
	role             func(bool)
	destroyed        bool
}

func (s *surface) Destroy(*wayland.Surface) {
	s.destroyed = true
	s.callbacks = nil
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
	if s.attached {
		if s.current != nil && (s.pending == nil || s.current.Resource != s.pending.Resource) {
			s.current.SendRelease()
		}
		s.current = s.pending
		s.pending = nil
		s.attached = false
	}
	s.server.awaiting = append(s.server.awaiting, s.callbacks...)
	s.callbacks = nil
	if s.role != nil {
		s.role(s.current != nil)
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
