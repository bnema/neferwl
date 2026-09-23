package wayland

import (
	"github.com/bnema/nefertty/internal/ports"
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
	if state.kind != roleNone && state.kind != roleXDG || state.xdg != nil {
		r.PostError(uint32(xdgshell.WmBaseErrorRole), "surface already has role")
		return
	}
	x := &xdgSurface{server: m.server, surface: state}
	resource, err := xdgshell.NewSurface(r.Client(), r.Version(), id, x)
	if err == nil {
		x.resource = resource
		state.kind = roleXDG
		state.xdg = x
		resource.OnDestroy = func() { state.xdg = nil; state.role = nil }
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
	acked      bool
	window     *window
	serials    []uint32
}

func (x *xdgSurface) Destroy(r *xdgshell.Surface) {
	if x.window != nil {
		r.PostError(uint32(xdgshell.SurfaceErrorDefunctRoleObject), "toplevel still exists")
	}
}

type window struct {
	id           ports.WindowID
	toplevel     *xdgshell.Toplevel
	xdg          *xdgSurface
	appID, title string
	mapped       bool
}

func (w *window) unmap() {
	if !w.mapped {
		return
	}
	w.mapped = false
	w.xdg.configured = false
	w.xdg.acked = false
	w.xdg.serials = nil
	w.xdg.server.emit(ports.WindowUnmapped{ID: w.id})
}
func (x *xdgSurface) GetToplevel(r *xdgshell.Surface, id uint32) {
	if x.window != nil {
		r.PostError(uint32(xdgshell.SurfaceErrorAlreadyConstructed), "toplevel already constructed")
		return
	}
	w := &window{id: x.server.nextWindow, xdg: x}
	t, err := xdgshell.NewToplevel(r.Client(), r.Version(), id, &top{w})
	if err != nil {
		return
	}
	w.toplevel = t
	x.window = w
	x.server.nextWindow++
	x.server.windows[w.id] = w
	x.surface.role = func(buffer bool) {
		if x.surface.destroyed {
			w.unmap()
			return
		}
		if !buffer && !w.mapped && !x.configured {
			x.configured = true
			x.server.serial++
			t.SendConfigure(0, 0, nil)
			r.SendConfigure(x.server.serial)
			x.serials = append(x.serials, x.server.serial)
		} else if buffer && !w.mapped && x.acked {
			w.mapped = true
			x.server.emit(ports.WindowMapped{ID: w.id, AppID: w.appID})
			x.server.log.Info().Uint64("id", uint64(w.id)).Str("app_id", w.appID).Msg("window mapped")
		} else if !buffer && w.mapped {
			w.unmap()
		}
	}
	t.OnDestroy = func() {
		w.unmap()
		delete(x.server.windows, w.id)
		x.window = nil
		x.surface.role = nil
		x.configured = false
		x.acked = false
		x.serials = nil
	}
}
func (x *xdgSurface) GetPopup(r *xdgshell.Surface, id uint32, _ *xdgshell.Surface, _ *xdgshell.Positioner) {
	if p, e := xdgshell.NewPopup(r.Client(), r.Version(), id, popup{}); e == nil {
		p.SendPopupDone()
	}
}
func (x *xdgSurface) SetWindowGeometry(*xdgshell.Surface, int32, int32, int32, int32) {}
func (x *xdgSurface) AckConfigure(r *xdgshell.Surface, serial uint32) {
	for i, issued := range x.serials {
		if issued == serial {
			x.serials = x.serials[i+1:]
			x.acked = true
			return
		}
	}
	r.PostError(uint32(xdgshell.SurfaceErrorInvalidSerial), "unknown configure serial")
}

type top struct{ w *window }

func (top) Destroy(*xdgshell.Toplevel)                                             {}
func (top) SetParent(*xdgshell.Toplevel, *xdgshell.Toplevel)                       {}
func (t top) SetTitle(_ *xdgshell.Toplevel, title string)                          { t.w.title = title }
func (t top) SetAppId(_ *xdgshell.Toplevel, appID string)                          { t.w.appID = appID }
func (top) ShowWindowMenu(*xdgshell.Toplevel, *wayland.Seat, uint32, int32, int32) {}
func (top) Move(*xdgshell.Toplevel, *wayland.Seat, uint32)                         {}
func (top) Resize(*xdgshell.Toplevel, *wayland.Seat, uint32, uint32)               {}
func (top) SetMaxSize(*xdgshell.Toplevel, int32, int32)                            {}
func (top) SetMinSize(*xdgshell.Toplevel, int32, int32)                            {}
func (top) SetMaximized(*xdgshell.Toplevel)                                        {}
func (top) UnsetMaximized(*xdgshell.Toplevel)                                      {}
func (t top) SetFullscreen(*xdgshell.Toplevel, *wayland.Output) {
	if t.w.mapped {
		t.w.xdg.server.emit(ports.WindowFullscreenRequest{ID: t.w.id, Fullscreen: true})
	}
}
func (t top) UnsetFullscreen(*xdgshell.Toplevel) {
	if t.w.mapped {
		t.w.xdg.server.emit(ports.WindowFullscreenRequest{ID: t.w.id})
	}
}
func (top) SetMinimized(*xdgshell.Toplevel) {}

type popup struct{}

func (popup) Destroy(*xdgshell.Popup)                                  {}
func (popup) Grab(*xdgshell.Popup, *wayland.Seat, uint32)              {}
func (popup) Reposition(*xdgshell.Popup, *xdgshell.Positioner, uint32) {}
