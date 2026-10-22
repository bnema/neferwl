package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// Role-independent accessors of a window. Code outside xdg.go goes through
// them instead of dereferencing w.xdg, which is nil for windows of another
// role.

// isToplevel reports whether w is a toplevel (not a popup).
func (w *window) isToplevel() bool { return w.toplevel != nil }

// surf is the window's surface, nil when it has none.
func (w *window) surf() *surface {
	if w.xdg != nil {
		return w.xdg.surface
	}
	return nil
}

// alive reports whether the window's role object is alive.
func (w *window) alive() bool {
	return w.xdg != nil && w.xdg.resource.Resource.Alive()
}

// client is the window's client; the zero Client when it has no role object.
func (w *window) client() server.Client {
	if w.xdg != nil {
		return w.xdg.resource.Client()
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
	if w.xdg != nil {
		w.sendXDGConfigure()
	}
}

// sendClose asks the client to close the window.
func (w *window) sendClose() {
	if w.toplevel != nil {
		w.toplevel.SendClose()
	}
}

// toplevelAlive reports whether the window is a toplevel whose protocol
// object is alive.
func (w *window) toplevelAlive() bool {
	return w.toplevel != nil && w.toplevel.Resource.Alive()
}

// surfaceResource is the window's wl_surface, nil when it has none or it died.
func (w *window) surfaceResource() *wayland.Surface {
	if w.xdg != nil {
		return w.xdg.surfaceResource()
	}
	return nil
}
