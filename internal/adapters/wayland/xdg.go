package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
)

func registerXDG(d *server.Display, s *Server) error {
	return xdgshell.NewWmBaseGlobal(d, 6, func(c server.Client, v, id uint32) { xdgshell.NewWmBase(c, int32(v), id, wm{s}) })
}

type wm struct{ server *Server }

func (wm) Destroy(*xdgshell.WmBase)      {}
func (wm) Pong(*xdgshell.WmBase, uint32) {}
func (wm) CreatePositioner(r *xdgshell.WmBase, id uint32) {
	xdgshell.NewPositioner(r.Client(), r.Version(), id, positioner{})
}
func (m wm) GetXdgSurface(r *xdgshell.WmBase, id uint32, w *wayland.Surface) {
	if w == nil {
		return
	}
	state := m.server.surfaces[w.Resource]
	if state == nil {
		return
	}
	if state.role != nil {
		r.PostError(0, "surface already has role")
		return
	}
	x := &xdgSurface{server: m.server, surface: state}
	resource, err := xdgshell.NewSurface(r.Client(), r.Version(), id, x)
	if err == nil {
		x.resource = resource
	}

}

type positioner struct{}

func (positioner) Destroy(*xdgshell.Positioner)                                   {}
func (positioner) SetSize(*xdgshell.Positioner, int32, int32)                     {}
func (positioner) SetAnchorRect(*xdgshell.Positioner, int32, int32, int32, int32) {}
func (positioner) SetAnchor(*xdgshell.Positioner, uint32)                         {}
func (positioner) SetGravity(*xdgshell.Positioner, uint32)                        {}
func (positioner) SetConstraintAdjustment(*xdgshell.Positioner, uint32)           {}
func (positioner) SetOffset(*xdgshell.Positioner, int32, int32)                   {}
func (positioner) SetReactive(*xdgshell.Positioner)                               {}
func (positioner) SetParentSize(*xdgshell.Positioner, int32, int32)               {}
func (positioner) SetParentConfigure(*xdgshell.Positioner, uint32)                {}

type xdgSurface struct {
	resource   *xdgshell.Surface
	server     *Server
	surface    *surface
	configured bool
	serial     uint32
}

func (x *xdgSurface) Destroy(*xdgshell.Surface) {}
func (x *xdgSurface) GetToplevel(r *xdgshell.Surface, id uint32) {
	t, err := xdgshell.NewToplevel(r.Client(), r.Version(), id, &top{x})
	if err != nil {
		return
	}
	x.surface.role = func(buffer bool) {
		if !buffer && !x.configured {
			x.configured = true
			x.server.serial++
			t.SendConfigure(0, 0, nil)
			r.SendConfigure(x.server.serial)
		}
	}
	t.OnDestroy = func() { x.surface.role = nil }
}
func (x *xdgSurface) GetPopup(r *xdgshell.Surface, id uint32, _ *xdgshell.Surface, _ *xdgshell.Positioner) {
	if p, e := xdgshell.NewPopup(r.Client(), r.Version(), id, popup{}); e == nil {
		p.SendPopupDone()
	}
}
func (x *xdgSurface) SetWindowGeometry(*xdgshell.Surface, int32, int32, int32, int32) {}
func (x *xdgSurface) AckConfigure(_ *xdgshell.Surface, serial uint32)                 { x.serial = serial }

type top struct{ x *xdgSurface }

func (top) Destroy(*xdgshell.Toplevel)                                             {}
func (top) SetParent(*xdgshell.Toplevel, *xdgshell.Toplevel)                       {}
func (top) SetTitle(*xdgshell.Toplevel, string)                                    {}
func (top) SetAppId(*xdgshell.Toplevel, string)                                    {}
func (top) ShowWindowMenu(*xdgshell.Toplevel, *wayland.Seat, uint32, int32, int32) {}
func (top) Move(*xdgshell.Toplevel, *wayland.Seat, uint32)                         {}
func (top) Resize(*xdgshell.Toplevel, *wayland.Seat, uint32, uint32)               {}
func (top) SetMaxSize(*xdgshell.Toplevel, int32, int32)                            {}
func (top) SetMinSize(*xdgshell.Toplevel, int32, int32)                            {}
func (top) SetMaximized(*xdgshell.Toplevel)                                        {}
func (top) UnsetMaximized(*xdgshell.Toplevel)                                      {}
func (top) SetFullscreen(*xdgshell.Toplevel, *wayland.Output)                      {}
func (top) UnsetFullscreen(*xdgshell.Toplevel)                                     {}
func (top) SetMinimized(*xdgshell.Toplevel)                                        {}

type popup struct{}

func (popup) Destroy(*xdgshell.Popup)                                  {}
func (popup) Grab(*xdgshell.Popup, *wayland.Seat, uint32)              {}
func (popup) Reposition(*xdgshell.Popup, *xdgshell.Positioner, uint32) {}
