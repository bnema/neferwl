package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// Role-independent accessors of a window. Code outside xdg.go goes through
// them instead of dereferencing w.xdg, which is nil for windows of another
// role.

// isToplevel reports whether w is a toplevel (xdg or X11), not a popup.
func (w *window) isToplevel() bool { return w.toplevel != nil || (w.x11 != nil && !w.x11.override) }

// srv is the server the window belongs to.
func (w *window) srv() *Server {
	if w.xdg != nil {
		return w.xdg.server
	}
	return w.x11.server
}

// surf is the window's surface, nil when it has none.
func (w *window) surf() *surface {
	switch {
	case w.xdg != nil:
		return w.xdg.surface
	case w.x11 != nil:
		return w.x11.surface
	}
	return nil
}

// alive reports whether the window's role object is alive.
func (w *window) alive() bool {
	switch {
	case w.xdg != nil:
		return w.xdg.resource.Resource.Alive()
	case w.x11 != nil:
		return w.x11.surface != nil && w.x11.surface.wl.Resource.Alive()
	}
	return false
}

// client is the window's client; the zero Client when it has no role object.
func (w *window) client() server.Client {
	switch {
	case w.xdg != nil:
		return w.xdg.resource.Client()
	case w.x11 != nil && w.x11.surface != nil:
		return w.x11.surface.wl.Client()
	}
	return server.Client{}
}

// geometry is the committed window geometry; empty means the whole surface.
func (w *window) geometry() ports.Rect {
	if w.xdg != nil {
		return w.xdg.geometry
	}
	return ports.Rect{}
}

// sendConfigure sends w.last to the client.
func (w *window) sendConfigure() {
	switch {
	case w.xdg != nil:
		w.sendXDGConfigure()
	case w.x11 != nil:
		w.x11.server.x11Configure(w)
	}
}

// sendClose asks the client to close the window.
func (w *window) sendClose() {
	switch {
	case w.toplevel != nil:
		w.toplevel.SendClose()
	case w.x11 != nil:
		w.x11.server.x11Close(w)
	}
}

// toplevelAlive reports whether the window is a toplevel whose protocol
// object is alive.
func (w *window) toplevelAlive() bool {
	if w.x11 != nil {
		return w.alive()
	}
	return w.toplevel != nil && w.toplevel.Resource.Alive()
}

// surfaceResource is the window's wl_surface, nil when it has none or it died.
func (w *window) surfaceResource() *wayland.Surface {
	switch {
	case w.xdg != nil:
		return w.xdg.surfaceResource()
	case w.x11 != nil && w.alive():
		return w.x11.surface.wl
	}
	return nil
}

// toplevelWindow is the toplevel window (xdg or X11) the surface is the role
// surface of, nil for other roles and popups' parents are not followed.
func (s *surface) toplevelWindow() *window {
	if s.xdg != nil && s.xdg.window != nil {
		return s.xdg.window
	}
	return s.x11win
}
