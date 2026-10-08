package wayland

import (
	"encoding/binary"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

func registerXDG(d *server.Display, s *Server) error {
	return xdgshell.NewWmBaseGlobal(d, 6, func(c server.Client, v, id uint32) { xdgshell.NewWmBase(c, int32(v), id, wm{s}) })
}

type wm struct{ server *Server }

func (wm) Destroy(*xdgshell.WmBase)      {}
func (wm) Pong(*xdgshell.WmBase, uint32) {}
func (m wm) CreatePositioner(r *xdgshell.WmBase, id uint32) {
	p := &positioner{}
	if res, err := xdgshell.NewPositioner(r.Client(), r.Version(), id, p); err == nil {
		m.server.positioners[res.Resource] = p
		res.OnDestroy = func() { delete(m.server.positioners, res.Resource) }
	}
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
	x := &xdgSurface{wm: r, server: m.server, surface: state}
	resource, err := xdgshell.NewSurface(r.Client(), r.Version(), id, x)
	if err == nil {
		x.resource = resource
		state.kind = roleXDG
		state.xdg = x
		resource.OnDestroy = func() { state.xdg = nil; state.role = nil }
	}

}

// positioner is an xdg_positioner: popups copy it when they use it.
type positioner struct {
	p                  ports.Positioner
	sizeSet, anchorSet bool
}

func (*positioner) Destroy(*xdgshell.Positioner) {}
func (p *positioner) SetSize(r *xdgshell.Positioner, w, h int32) {
	if w <= 0 || h <= 0 {
		r.PostError(uint32(xdgshell.PositionerErrorInvalidInput), "size must be positive")
		return
	}
	p.p.Width, p.p.Height, p.sizeSet = int(w), int(h), true
}
func (p *positioner) SetAnchorRect(r *xdgshell.Positioner, x, y, w, h int32) {
	if w < 0 || h < 0 {
		r.PostError(uint32(xdgshell.PositionerErrorInvalidInput), "anchor rect size must not be negative")
		return
	}
	p.p.AnchorRect, p.anchorSet = ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}, true
}
func (p *positioner) SetAnchor(r *xdgshell.Positioner, v uint32) {
	if v > ports.EdgeBottomRight {
		r.PostError(uint32(xdgshell.PositionerErrorInvalidInput), "invalid anchor")
		return
	}
	p.p.Anchor = v
}
func (p *positioner) SetGravity(r *xdgshell.Positioner, v uint32) {
	if v > ports.EdgeBottomRight {
		r.PostError(uint32(xdgshell.PositionerErrorInvalidInput), "invalid gravity")
		return
	}
	p.p.Gravity = v
}
func (p *positioner) SetConstraintAdjustment(_ *xdgshell.Positioner, v uint32) { p.p.Adjust = v }
func (p *positioner) SetOffset(_ *xdgshell.Positioner, x, y int32) {
	p.p.OffsetX, p.p.OffsetY = int(x), int(y)
}

// Reactive popups, parent size and parent configure hints are not used:
// core places popups when they are created or repositioned.
func (*positioner) SetReactive(*xdgshell.Positioner)                 {}
func (*positioner) SetParentSize(*xdgshell.Positioner, int32, int32) {}
func (*positioner) SetParentConfigure(*xdgshell.Positioner, uint32)  {}

// positionerOf returns a complete positioner, or posts invalid_positioner.
func (s *Server) positionerOf(wm *xdgshell.WmBase, p *xdgshell.Positioner) (ports.Positioner, bool) {
	if p != nil && p.Resource != nil {
		if state := s.positioners[p.Resource]; state != nil && state.sizeSet && state.anchorSet {
			return state.p, true
		}
	}
	if wm != nil {
		wm.PostError(uint32(xdgshell.WmBaseErrorInvalidPositioner), "incomplete positioner")
	}
	return ports.Positioner{}, false
}

type xdgSurface struct {
	wm         *xdgshell.WmBase
	resource   *xdgshell.Surface
	server     *Server
	surface    *surface
	configured bool
	acked      bool
	window     *window
	serials    []uint32
	// geometry is the committed window geometry: the visible window inside
	// client shadows. Empty means the whole surface.
	geometry, pendingGeometry ports.Rect
}

func (x *xdgSurface) Destroy(r *xdgshell.Surface) {
	if x.window != nil {
		r.PostError(uint32(xdgshell.SurfaceErrorDefunctRoleObject), "role object still exists")
	}
}

// window is an xdg_toplevel, or an xdg_popup when popup is set.
type window struct {
	id           ports.WindowID
	toplevel     *xdgshell.Toplevel
	popup        *popup
	xdg          *xdgSurface
	appID, title string
	mapped       bool
	// gen counts the mappings: an unmap and a remap is a new toplevel to
	// ext-foreign-toplevel-list (identifier) and to its capture sources.
	gen uint32
	// parent is set_parent (dialogs); min and max are the size hints.
	parent *window
	// dialog is set while an xdg_dialog_v1 object exists for the
	// toplevel; modal is its state.
	dialog, modal  bool
	minW, minH     int32
	maxW, maxH     int32
	floating       bool
	floatW, floatH int // size last sent to core
	// last is the most recent core configure, resent when decoration mode changes.
	last    ports.ConfigureWindow
	hasLast bool
}

// sendConfigure sends w.last as xdg_toplevel + xdg_surface configure.
func (w *window) sendConfigure() {
	c := w.last
	w.toplevel.SendConfigure(int32(c.Width), int32(c.Height), toplevelStates(c, w.toplevel.Version()))
	s := w.xdg.server
	s.serial++
	w.xdg.resource.SendConfigure(s.serial)
	w.xdg.serials = append(w.xdg.serials, s.serial)
}

// toplevelStates encodes the xdg_toplevel states of configure c.
func toplevelStates(c ports.ConfigureWindow, version int32) []byte {
	var states []byte
	add := func(st xdgshell.ToplevelState) {
		states = binary.LittleEndian.AppendUint32(states, uint32(st))
	}
	if c.Fullscreen {
		add(xdgshell.ToplevelStateFullscreen)
	}
	if c.Activated {
		add(xdgshell.ToplevelStateActivated)
	}
	if !c.Visible && !c.Captured && version >= 6 {
		add(xdgshell.ToplevelStateSuspended)
	}
	if c.Fullscreen || c.Floating {
		return states
	}
	// Tiled windows must use exactly this size. Maximized is the strict
	// size state every client honours: Wine ignores a size change that only
	// carries tiled states on a window it considers monitor-sized.
	add(xdgshell.ToplevelStateMaximized)
	if version >= 2 {
		for _, st := range []xdgshell.ToplevelState{xdgshell.ToplevelStateTiledLeft, xdgshell.ToplevelStateTiledRight, xdgshell.ToplevelStateTiledTop, xdgshell.ToplevelStateTiledBottom} {
			add(st)
		}
	}
	return states
}

func (w *window) unmap() {
	if w.xdg.server.seat.pointerFocus == w.id {
		w.xdg.server.changePointerFocus(0, 0, 0)
	}
	if w.xdg.server.seat.focused == w.id {
		w.xdg.server.changeFocus(0)
	}
	if !w.mapped {
		return
	}
	w.mapped = false
	w.xdg.server.toplevelClosed(w.id)
	w.xdg.surface.resetInputEmission()
	w.xdg.configured = false
	w.xdg.acked = false
	w.xdg.serials = nil
	w.xdg.server.emit(ports.WindowUnmapped{ID: w.id})
	w.xdg.server.syncInhibitors()
	w.xdg.server.emitContent(ports.SurfaceContent{ID: w.id}, damage{full: true})
}
func (x *xdgSurface) GetToplevel(r *xdgshell.Surface, id uint32) {
	if x.window != nil {
		r.PostError(uint32(xdgshell.SurfaceErrorAlreadyConstructed), "role object already constructed")
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
			w.gen++
			slot := x.server.slotToken(r.Client())
			// Its size comes with the commit, in WindowResized (afterCommit).
			w.floating = w.floats()
			var parent ports.WindowID
			if w.parent != nil && w.parent.mapped {
				parent = w.parent.id
			}
			x.server.emit(ports.WindowMapped{ID: w.id, AppID: w.appID, Slot: slot, PID: r.Client().PID(), Floating: w.floating, Width: w.floatW, Height: w.floatH, Parent: parent, Modal: w.modal, Remap: w.gen > 1})
			x.server.syncInhibitors()
			x.server.toplevelChanged(w)
			x.server.log.Info().Uint64("id", uint64(w.id)).Str("app_id", w.appID).Str("slot", slot).Bool("floating", w.floating).Msg("window mapped")
		} else if !buffer && w.mapped {
			w.unmap()
		}
	}
	t.OnDestroy = func() {
		w.unmap()
		x.server.foreignToplevelGone(w)
		x.surface.dropQueue()
		x.surface.current, x.surface.next.buffer = nil, nil
		x.surface.next.attached = false
		delete(x.server.windows, w.id)
		x.window = nil
		x.surface.role = nil
		x.configured = false
		x.acked = false
		x.serials = nil
		x.geometry, x.pendingGeometry = ports.Rect{}, ports.Rect{}
	}
}
func (x *xdgSurface) GetPopup(r *xdgshell.Surface, id uint32, parent *xdgshell.Surface, pos *xdgshell.Positioner) {
	if x.window != nil {
		r.PostError(uint32(xdgshell.SurfaceErrorAlreadyConstructed), "role object already constructed")
		return
	}
	x.server.newPopup(x, r, id, parent, pos)
}

// SetWindowGeometry sets the visible window inside the surface; it
// applies on the next commit.
func (x *xdgSurface) SetWindowGeometry(r *xdgshell.Surface, gx, gy, gw, gh int32) {
	if gw <= 0 || gh <= 0 {
		r.PostError(uint32(xdgshell.SurfaceErrorInvalidSize), "invalid window geometry")
		return
	}
	x.pendingGeometry = ports.Rect{X: int(gx), Y: int(gy), W: int(gw), H: int(gh)}
	id := uint64(0)
	if x.window != nil {
		id = uint64(x.window.id)
	}
	x.server.log.Debug().Uint64("id", id).Int32("x", gx).Int32("y", gy).Int32("w", gw).Int32("h", gh).Msg("window geometry")
}
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

func (top) Destroy(*xdgshell.Toplevel) {}
func (t top) SetParent(_ *xdgshell.Toplevel, parent *xdgshell.Toplevel) {
	var p *window
	if parent != nil {
		for _, w := range t.w.xdg.server.windows {
			if w.toplevel != nil && w.toplevel.Resource == parent.Resource && w != t.w {
				p = w
			}
		}
	}
	t.w.setParent(p)
}

// setParent changes the dialog parent; core follows it once mapped (the
// map carries the parent of the first frame).
func (w *window) setParent(p *window) {
	if w.parent == p {
		return
	}
	w.parent = p
	if !w.mapped {
		return
	}
	var id ports.WindowID
	if p != nil && p.mapped {
		id = p.id
	}
	w.xdg.server.emit(ports.WindowParent{ID: w.id, Parent: id})
}
func (t top) SetTitle(_ *xdgshell.Toplevel, title string) {
	if title == t.w.title {
		return
	}
	t.w.title = title
	t.w.xdg.server.toplevelChanged(t.w)
}
func (t top) SetAppId(_ *xdgshell.Toplevel, appID string) {
	if appID == t.w.appID {
		return
	}
	t.w.appID = appID
	if t.w.mapped {
		t.w.xdg.server.emit(ports.WindowAppID{ID: t.w.id, AppID: appID})
		t.w.xdg.server.toplevelChanged(t.w)
	}
}
func (top) ShowWindowMenu(*xdgshell.Toplevel, *wayland.Seat, uint32, int32, int32) {}

// Move and Resize start a pointer drag in core when they answer the
// client's current press; stale serials are ignored.
func (t top) Move(r *xdgshell.Toplevel, _ *wayland.Seat, serial uint32) {
	t.moveRequest(r, serial, false, 0)
}
func (t top) Resize(r *xdgshell.Toplevel, _ *wayland.Seat, serial, edges uint32) {
	if edges > uint32(xdgshell.ToplevelResizeEdgeBottomRight) || edges&3 == 3 || edges&12 == 12 {
		r.PostError(uint32(xdgshell.ToplevelErrorInvalidResizeEdge), "invalid resize edge")
		return
	}
	t.moveRequest(r, serial, true, ports.ResizeEdges(edges))
}
func (t top) moveRequest(r *xdgshell.Toplevel, serial uint32, resize bool, edges ports.ResizeEdges) {
	s := t.w.xdg.server
	if !t.w.mapped || serial != s.seat.press || s.seat.pressClient != r.Client() {
		return
	}
	s.emit(ports.WindowMoveRequest{ID: t.w.id, Resize: resize, Edges: edges})
}
func (t top) SetMaxSize(r *xdgshell.Toplevel, w, h int32) {
	if w < 0 || h < 0 {
		r.PostError(uint32(xdgshell.ToplevelErrorInvalidSize), "negative max size")
		return
	}
	t.w.maxW, t.w.maxH = w, h
}
func (t top) SetMinSize(r *xdgshell.Toplevel, w, h int32) {
	if w < 0 || h < 0 {
		r.PostError(uint32(xdgshell.ToplevelErrorInvalidSize), "negative min size")
		return
	}
	t.w.minW, t.w.minH = w, h
}
func (top) SetMaximized(*xdgshell.Toplevel)   {}
func (top) UnsetMaximized(*xdgshell.Toplevel) {}
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

// afterCommit runs once the commit applied content and geometry: a
// floating window keeps the size it draws.
func (w *window) afterCommit() {
	if !w.mapped || !w.floating {
		return
	}
	if fw, fh := w.size(); fw > 0 && fh > 0 && (fw != w.floatW || fh != w.floatH) {
		w.floatW, w.floatH = fw, fh
		w.xdg.server.emit(ports.WindowResized{ID: w.id, Width: fw, Height: fh})
	}
}

// floats reports whether the toplevel should float over the columns, as
// on niri: dialogs (a parent) and fixed-size windows such as splash
// screens.
func (w *window) floats() bool {
	fixed := w.minW > 0 && w.minH > 0 && w.minW == w.maxW && w.minH == w.maxH
	return w.parent != nil || fixed
}

// size is the window geometry size, else the surface size, in logical pixels.
func (w *window) size() (int, int) {
	if g := w.xdg.geometry; g.W > 0 && g.H > 0 {
		return g.W, g.H
	}
	c := w.xdg.surface.content
	return c.LogicalW, c.LogicalH
}

// surfacePoint turns window coordinates (core's, from the geometry origin)
// into surface coordinates.
func (w *window) surfacePoint(x, y float64) (float64, float64) {
	g := w.xdg.geometry
	return x + float64(g.X), y + float64(g.Y)
}

func (x *xdgSurface) surfaceResource() *wayland.Surface {
	for resource, state := range x.server.surfaces {
		if state == x.surface && resource.Alive() {
			return wayland.WrapSurface(resource)
		}
	}
	return nil
}
