package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// popup is the xdg_popup role of a window. Core places it (PopupRequest →
// ConfigurePopup); it maps on the first buffer after its configure is
// acked, and is drawn over its parent like a window without border.
type popup struct {
	w        *window
	resource *xdgshell.Popup
	parent   *window
	// layer is the parent set through zwlr_layer_surface_v1.get_popup,
	// for a popup created without an xdg parent.
	layer *layerSurface
	pos   ports.Positioner
	grab  bool
	// sent is set once core got the PopupRequest; done once dismissed.
	sent, done bool
}

func (s *Server) newPopup(x *xdgSurface, r *xdgshell.Surface, id uint32, parentXDG *xdgshell.Surface, pos *xdgshell.Positioner) {
	p, ok := s.positionerOf(x.wm, pos)
	if !ok {
		return
	}
	var parent *window
	if parentXDG != nil {
		for _, w := range s.windows {
			if w.xdg.resource != nil && w.xdg.resource.Resource == parentXDG.Resource {
				parent = w
			}
		}
	}
	w := &window{id: s.nextWindow, xdg: x}
	pp := &popup{w: w, parent: parent, pos: p}
	res, err := xdgshell.NewPopup(r.Client(), r.Version(), id, pp)
	if err != nil {
		return
	}
	pp.resource, w.popup = res, pp
	s.nextWindow++
	s.windows[w.id] = w
	x.window = w
	x.surface.role = func(buffer bool) {
		if x.surface.destroyed {
			w.unmap()
			return
		}
		switch {
		case !buffer && !w.mapped && !x.configured && !pp.done:
			// Without a parent (xdg-shell lets clients set it through
			// layer-shell before the first commit) the popup has nowhere
			// to go.
			if pp.parentID() == 0 {
				pp.dismiss()
				return
			}
			if !pp.sent {
				pp.sent = true
				s.emit(ports.PopupRequest{ID: w.id, Parent: pp.parentID(), Positioner: pp.pos, Grab: pp.grab})
			}
		case buffer && !w.mapped && x.acked && !pp.done:
			w.mapped = true
			s.emit(ports.PopupMapped{ID: w.id})
			s.log.Debug().Uint64("id", uint64(w.id)).Uint64("parent", uint64(pp.parentID())).Msg("popup mapped")
		case !buffer && w.mapped:
			// Unmapped, it may map again after a new initial commit.
			w.unmap()
			pp.sent, pp.grab = false, false
		}
	}
	res.OnDestroy = func() {
		// Core forgets the popup, placed or mapped.
		if !w.mapped && pp.sent && !pp.done {
			s.emit(ports.WindowUnmapped{ID: w.id})
		}
		w.unmap()
		pp.done = true
		x.surface.dropQueue()
		x.surface.current, x.surface.next.buffer = nil, nil
		x.surface.next.attached = false
		delete(s.windows, w.id)
		x.window = nil
		x.surface.role = nil
		x.configured, x.acked, x.serials = false, false, nil
		x.geometry, x.pendingGeometry = ports.Rect{}, ports.Rect{}
	}
}

func (*popup) Destroy(*xdgshell.Popup) {}

// parentID is the window or layer surface the popup hangs from, 0 if none.
func (p *popup) parentID() ports.WindowID {
	switch {
	case p.parent != nil:
		return p.parent.id
	case p.layer != nil:
		return p.layer.id
	}
	return 0
}

// Grab is only valid before the first commit; core then gives the popup the
// keyboard and closes it on a click outside its popup chain.
func (p *popup) Grab(r *xdgshell.Popup, _ *wayland.Seat, serial uint32) {
	if p.sent || p.w.mapped {
		r.PostError(uint32(xdgshell.PopupErrorInvalidGrab), "grab after the popup was committed")
		return
	}
	// Only a grab answering the client's last press may take the keyboard,
	// and a popup over a popup grabs only if its parent does.
	s := p.w.xdg.server
	fresh := serial == s.seat.press && s.seat.pressClient == r.Client()
	if !fresh || p.parentID() == 0 || !s.topGrab(r.Client(), p.parent) {
		p.dismiss()
		return
	}
	p.grab = true
}

func (p *popup) Reposition(r *xdgshell.Popup, pos *xdgshell.Positioner, token uint32) {
	s := p.w.xdg.server
	next, ok := s.positionerOf(p.w.xdg.wm, pos)
	if !ok || p.done {
		return
	}
	p.pos = next
	if p.sent {
		s.emit(ports.PopupRequest{ID: p.w.id, Parent: p.parentID(), Positioner: next, Grab: p.grab, Reposition: true, Token: token})
	}
}

// configure sends core's placement: xdg_popup.configure then
// xdg_surface.configure.
func (p *popup) configure(c ports.ConfigurePopup) {
	if p.done || !p.resource.Resource.Alive() || !p.w.xdg.resource.Resource.Alive() {
		return
	}
	if c.Reposition && p.resource.Version() >= 3 {
		p.resource.SendRepositioned(c.Token)
	}
	p.resource.SendConfigure(int32(c.Rect.X), int32(c.Rect.Y), int32(c.Rect.W), int32(c.Rect.H))
	s := p.w.xdg.server
	s.serial++
	p.w.xdg.resource.SendConfigure(s.serial)
	p.w.xdg.serials = append(p.w.xdg.serials, s.serial)
	p.w.xdg.configured = true
	if surf := p.w.xdg.surface; surf != nil {
		surf.sendTreeScale()
	}
}

// topGrab reports whether a grabbing popup of client may go on parent:
// parent is the client's newest live grabbing popup, or a toplevel (nil
// for a layer surface) when it has none.
func (s *Server) topGrab(client server.Client, parent *window) bool {
	var top *window
	for _, w := range s.windows {
		if w.popup != nil && w.popup.grab && !w.popup.done && w.xdg.resource.Client() == client && (top == nil || w.id > top.id) {
			top = w
		}
	}
	if top == nil {
		return parent == nil || parent.popup == nil
	}
	return top == parent
}

// dismiss sends popup_done once; the client then destroys the popup.
func (p *popup) dismiss() {
	if p.done {
		return
	}
	p.done = true
	if p.resource.Resource.Alive() {
		p.resource.SendPopupDone()
	}
	p.w.unmap()
}
