package wayland

import (
	"github.com/bnema/neferwl/internal/domain"
	"time"

	"bnema.dev/purego-libwayland/server"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xwaylandshell"
	"github.com/bnema/neferwl/internal/adapters/wayland/xwm"
	"github.com/bnema/neferwl/internal/ports"
)

// x11Window is the X11 role of a window: the XWM slot, the window's X id and
// the wl_surface Xwayland paired with it. Display goroutine only.
type x11Window struct {
	server  *Server
	slot    xwm.Slot
	xid     uint32
	surface *surface
	info    xwm.WindowInfo
	// ready is set once the XWM read the window's properties (Ready).
	ready bool
	// override marks an override-redirect window: tracked and drawn, never a
	// toplevel nor keyboard focus. rect is its layout rect, noInput keeps the
	// pointer off it.
	override bool
	noInput  bool
	rect     domain.Rect
}

// xwaylandRestricted are the globals only the Xwayland client may see.
func xwaylandRestricted(iface string) bool {
	return iface == "xwayland_shell_v1" || iface == "zwp_xwayland_keyboard_grab_manager_v1"
}

// globalVisible is the display's global filter: the Xwayland-only globals
// are visible to, and bindable by, the Xwayland client alone.
func (s *Server) globalVisible(c server.Client, g *server.Global) bool {
	if g.Interface() == "wl_output" {
		for _, o := range s.outputs {
			if g == o.global {
				return c != s.xwayland.client
			}
			if g == o.xglobal {
				return c == s.xwayland.client && c != (server.Client{})
			}
		}
		if s.creatingPrivateOutput {
			return c == s.xwayland.client && c != (server.Client{})
		}
		return c != s.xwayland.client
	}
	if g == s.xdgPrivate {
		return s.xwayland.client != (server.Client{}) && c == s.xwayland.client
	}
	if g == s.xdgPublic {
		return c != s.xwayland.client
	}
	if xwaylandRestricted(g.Interface()) {
		return s.xwayland.client != (server.Client{}) && c == s.xwayland.client
	}
	return true
}

func registerXwaylandShell(d *server.Display, s *Server) error {
	return xwaylandshell.NewXwaylandShellV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xwaylandshell.NewXwaylandShellV1(c, int32(v), id, xwaylandShell{s})
	})
}

type xwaylandShell struct{ s *Server }

func (xwaylandShell) Destroy(*xwaylandshell.XwaylandShellV1) {}

func (h xwaylandShell) GetXwaylandSurface(r *xwaylandshell.XwaylandShellV1, id uint32, w *wayland.Surface) {
	surf := h.s.surfaceOf(w)
	if surf == nil {
		return
	}
	if surf.kind != roleNone {
		r.PostError(uint32(xwaylandshell.XwaylandShellV1ErrorRole), "surface already has a role")
		return
	}
	if _, err := xwaylandshell.NewXwaylandSurfaceV1(r.Client(), r.Version(), id, xwaylandSurface{h.s, surf}); err != nil {
		return
	}
	surf.kind = roleX11
}

type xwaylandSurface struct {
	s    *Server
	surf *surface
}

func (x xwaylandSurface) SetSerial(r *xwaylandshell.XwaylandSurfaceV1, lo, hi uint32) {
	serial := uint64(hi)<<32 | uint64(lo)
	switch {
	case x.surf.x11Serial != 0:
		r.PostError(uint32(xwaylandshell.XwaylandSurfaceV1ErrorAlreadyAssociated), "serial already set")
	case serial == 0:
		r.PostError(uint32(xwaylandshell.XwaylandSurfaceV1ErrorInvalidSerial), "serial is zero")
	default:
		x.surf.x11Serial = serial
		if x.s.xwm != nil {
			x.s.xwm.SetSerial(x.surf.identity, serial)
		}
	}
}

func (xwaylandSurface) Destroy(*xwaylandshell.XwaylandSurfaceV1) {}

// installXWM makes x the server's window manager. Display goroutine only.
func (s *Server) installXWM(x *xwm.XWM) {
	s.teardownXWM()
	s.xwm = x
	s.xwmSel = x
	x.UseSpace(&s.xspace)
	if err := x.Attach(s.display, hostImpl{s}); err != nil {
		s.log.Warn().Err(err).Msg("X11 window management disabled")
		s.xwm, s.xwmSel = nil, nil
		x.CloseConn()
		return
	}
	// Serials sent before the XWM was attached pair now.
	for _, surf := range s.surfaces {
		if surf.kind == roleX11 && surf.x11Serial != 0 && surf.x11win == nil {
			x.SetSerial(surf.identity, surf.x11Serial)
		}
	}
	for kind, src := range s.selections { // what Wayland offered before the XWM attached
		if src != nil && src.alive() {
			s.exposeToX(kind, src)
		}
	}
	x.LayoutChanged(true)
}

// teardownXWM closes the window manager, dropping its windows. Idempotent.
func (s *Server) teardownXWM() {
	for kind := range s.xsel {
		s.setXSelection(kind, nil)
	}
	if x := s.xwm; x != nil {
		s.xwm, s.xwmSel = nil, nil
		x.Close()
	}
}

// x11Configure forwards the latest core configure of an X11 window.
func (s *Server) x11Configure(w *window) {
	if s.xwm != nil {
		s.xwm.Configure(w.x11.slot, w.last)
	}
}

// x11Close asks an X11 window to close.
func (s *Server) x11Close(w *window) {
	if s.xwm != nil {
		s.xwm.CloseWindow(w.x11.slot)
	}
}

// x11Place forwards core's position of an X11 window's client rect.
func (s *Server) x11Place(c ports.PlaceWindow) {
	if w := s.windows[c.ID]; w != nil && w.x11 != nil && s.xwm != nil {
		s.xwm.Place(w.x11.slot, c.X, c.Y)
	}
}

// x11Focus tells the XWM which X window holds the keyboard focus, none when a
// native window does.
func (s *Server) x11Focus() {
	if s.xwm == nil {
		return
	}
	var slot xwm.Slot
	if w := s.windows[s.seat.focused]; w != nil && w.x11 != nil {
		slot = w.x11.slot
	}
	s.xwm.Focus(slot)
}

// hostImpl is the server as the XWM sees it.
type hostImpl struct{ s *Server }

func (h hostImpl) window(slot xwm.Slot) *window { return h.s.x11Slots[slot] }

func (h hostImpl) Created(slot xwm.Slot, xid uint32) domain.WindowID {
	s := h.s
	w := &window{id: s.nextWindow}
	w.x11 = &x11Window{server: s, slot: slot, xid: xid}
	s.nextWindow++
	s.windows[w.id] = w
	s.x11Slots[slot] = w
	return w.id
}

func (h hostImpl) Ready(slot xwm.Slot, info xwm.WindowInfo) {
	w := h.window(slot)
	if w == nil {
		return
	}
	w.x11.info, w.x11.ready = info, true
	w.title, w.appID, w.modal = info.Title, info.AppID, info.Modal
	w.parent = h.window(info.Parent)
	// A surface associated and committed earlier maps now.
	w.x11.tryMap()
}

func (h hostImpl) Associate(slot xwm.Slot, surfaceID uint64) {
	w := h.window(slot)
	surf := h.s.surfaceByIdentity[surfaceID]
	if w == nil || surf == nil || surf.kind != roleX11 {
		return
	}
	x := w.x11
	if x.surface != nil && x.surface != surf {
		// A new surface replaces the shown one: core sees a remap.
		w.unmap()
	}
	x.drop()
	x.surface, surf.x11win = surf, w
	surf.role = func(buffer bool) {
		switch {
		case surf.destroyed:
			w.unmap()
			if x.surface == surf {
				x.surface = nil
			}
			surf.x11win = nil
		case buffer:
			x.tryMap()
		case w.mapped:
			w.unmap()
		}
	}
	// The buffer may have been committed before the pairing.
	x.tryMap()
}

func (h hostImpl) Dissociate(slot xwm.Slot, destroyed bool) {
	w := h.window(slot)
	if w == nil {
		return
	}
	w.unmap()
	w.x11.drop()
	w.x11.ready = false
	if destroyed {
		if w.gen > 0 {
			h.s.emit(ports.WindowDestroyed{ID: w.id})
		}
		for _, c := range h.s.windows {
			if c.parent == w {
				c.parent = nil
			}
		}
		delete(h.s.windows, w.id)
		delete(h.s.x11Slots, slot)
		h.s.foreignToplevelGone(w)
	}
}

func (h hostImpl) Title(slot xwm.Slot, title string) {
	if w := h.window(slot); w != nil && w.title != title {
		w.title = title
		h.s.toplevelChanged(w)
		if w.mapped {
			h.s.emit(ports.WindowTitle{ID: w.id, Title: title})
			h.s.castInventoryChanged()
		}
	}
}

func (h hostImpl) AppID(slot xwm.Slot, appID string) {
	w := h.window(slot)
	if w == nil || w.appID == appID {
		return
	}
	w.appID = appID
	if w.mapped {
		h.s.emit(ports.WindowAppID{ID: w.id, AppID: appID})
		h.s.toplevelChanged(w)
		h.s.castInventoryChanged()
	}
}

func (h hostImpl) Parent(slot xwm.Slot, parent xwm.Slot) {
	if w := h.window(slot); w != nil {
		w.setParent(h.window(parent))
	}
}

func (h hostImpl) Modal(slot xwm.Slot, on bool) {
	if w := h.window(slot); w != nil {
		w.x11.info.Modal = on
		w.setModal(on)
	}
}

func (h hostImpl) Fullscreen(slot xwm.Slot, on bool) {
	w := h.window(slot)
	if w == nil {
		return
	}
	w.x11.info.Fullscreen = on
	if w.mapped {
		h.s.log.Info().Uint64("id", uint64(w.id)).Uint32("xid", w.x11.xid).
			Bool("on", on).Msg("client fullscreen request")
		h.s.emit(ports.WindowFullscreenRequest{ID: w.id, Fullscreen: on})
	}
}

// drop unlinks the window from its surface.
func (x *x11Window) drop() {
	if surf := x.surface; surf != nil {
		surf.x11win, surf.role = nil, nil
		x.surface = nil
	}
}

// tryMap maps the window once the XWM knows it and its surface shows a buffer.
func (x *x11Window) tryMap() {
	surf := x.surface
	if surf == nil {
		return
	}
	w := surf.x11win
	if w == nil || w.mapped || !x.ready || surf.destroyed || surf.current == nil {
		return
	}
	s := x.server
	w.mapped = true
	// X11 readiness may follow its first buffer commit. Account for that size
	// now, before a compositor Configure can resize it to the output.
	w.sized = false
	if fw, fh := w.size(); fw > 0 && fh > 0 {
		w.sized = true
	}
	w.gen++
	w.floating = x.info.Floating
	w.floatW, w.floatH = 0, 0
	if w.floating {
		w.floatW, w.floatH = x.info.Width, x.info.Height
	}
	var parent domain.WindowID
	if w.parent != nil && w.parent.mapped {
		parent = w.parent.id
	}
	s.emit(ports.WindowMapped{ID: w.id, AppID: w.appID, Title: w.title, ColorDescription: !surf.outputColor().PlainSDR(), PID: x.info.PID, Xwayland: true, Floating: w.floating, Width: w.floatW, Height: w.floatH, Parent: parent, Positioned: true, Modal: w.modal, Remap: w.gen > 1, HasAt: x.info.HasAt, AtX: x.info.AtX, AtY: x.info.AtY, Fullscreen: x.info.Fullscreen})
	s.syncInhibitors()
	s.toplevelChanged(w)
	s.castInventoryChanged()
	s.log.Info().Uint64("id", uint64(w.id)).Uint32("xid", x.xid).Str("app_id", w.appID).Bool("floating", w.floating).Msg("x11 window mapped")
	// Called from a commit, the content follows right after; called from the
	// pairing, the surface already shows a buffer and is published now.
	if surf.has {
		surf.resetInputEmission()
		surf.redraw()
		surf.emitInput()
	}
}

// OverrideMapped starts tracking an override-redirect window. It is announced
// to core on the paired surface's first buffer commit, so it is never drawn
// without content.
func (h hostImpl) OverrideMapped(slot xwm.Slot, xid uint32, surfaceID uint64, x, y, w, h2 int, noInput bool) {
	s := h.s
	surf := s.surfaceByIdentity[surfaceID]
	if surf == nil || surf.kind != roleX11 || surf.x11win != nil {
		return
	}
	if old := s.x11Slots[slot]; old != nil {
		h.OverrideGone(slot)
	}
	win := &window{id: s.nextWindow}
	win.x11 = &x11Window{server: s, slot: slot, xid: xid, override: true, noInput: noInput, rect: domain.Rect{X: x, Y: y, W: w, H: h2}}
	s.nextWindow++
	s.windows[win.id] = win
	s.x11Slots[slot] = win
	ox := win.x11
	ox.surface, surf.x11win = surf, win
	surf.role = func(buffer bool) {
		switch {
		case surf.destroyed:
			ox.hide()
			ox.surface = nil
			surf.x11win = nil
		case buffer:
			ox.show()
		case win.mapped:
			ox.hide()
		}
	}
	ox.show()
}

// OverrideMoved records the new layout rect of an override window and, once
// shown, tells core. An unchanged rect costs nothing.
func (h hostImpl) OverrideMoved(slot xwm.Slot, x, y, w, h2 int) {
	win := h.s.x11Slots[slot]
	if win == nil || win.x11 == nil || !win.x11.override {
		return
	}
	r := domain.Rect{X: x, Y: y, W: w, H: h2}
	if r == win.x11.rect {
		return
	}
	win.x11.rect = r
	if win.mapped {
		h.s.emit(ports.UnmanagedMoved{ID: win.id, X: x, Y: y, Width: w, Height: h2})
		win.x11.surface.sendTreeScale()
	}
}

// OverrideGone ends an override window: unmapped, or destroyed.
func (h hostImpl) SelectionOwned(kind int, mimes []string) {
	h.s.log.Debug().Int("kind", kind).Int("mimes", len(mimes)).Msg("X selection owner changed")
	h.s.setXSelection(kind, mimes)
}

func (h hostImpl) SelectionRead(kind int, mime string) int {
	return h.s.xSelectionRead(kind, mime)
}

func (h hostImpl) After(d time.Duration, fn func()) func() {
	t := h.s.clock.AfterFunc(d, func() { h.s.display.Post(fn) })
	return func() { t.Stop() }
}

func (h hostImpl) OverrideGone(slot xwm.Slot) {
	s := h.s
	win := s.x11Slots[slot]
	if win == nil || win.x11 == nil || !win.x11.override {
		return
	}
	win.x11.hide()
	win.x11.drop()
	delete(s.windows, win.id)
	delete(s.x11Slots, slot)
}

// show announces the window to core once its surface has content.
func (x *x11Window) show() {
	surf := x.surface
	if surf == nil || surf.x11win == nil || surf.destroyed || surf.current == nil {
		return
	}
	w := surf.x11win
	if w.mapped {
		return
	}
	s := x.server
	w.mapped = true
	r := x.rect
	s.emit(ports.UnmanagedMapped{ID: w.id, X: r.X, Y: r.Y, Width: r.W, Height: r.H, NoInput: x.noInput})
	surf.sendTreeScale()
	if surf.has {
		surf.resetInputEmission()
		surf.redraw()
		surf.emitInput()
	}
}

// hide withdraws a shown override window from core and the seat.
func (x *x11Window) hide() {
	if x.surface == nil || x.surface.x11win == nil {
		return
	}
	w := x.surface.x11win
	s := x.server
	if s.seat.pointerFocus == w.id {
		s.changePointerFocus(0, 0, 0)
	}
	if !w.mapped {
		return
	}
	w.mapped = false
	x.surface.resetInputEmission()
	s.emit(ports.UnmanagedUnmapped{ID: w.id})
	s.emitContent(domain.SurfaceContent{ID: w.id}, damage{full: true})
}

// isOverride reports whether w is an override-redirect X11 window.
func (w *window) isOverride() bool { return w.x11 != nil && w.x11.override }

// overrideOutput is the name of the output under the origin of an override
// window's rect, "" when none.
func (s *Server) overrideOutput(w *window) string {
	if o, ok := s.outputPlaces.At(float64(w.x11.rect.X), float64(w.x11.rect.Y)); ok {
		return o.Info.Name
	}
	return ""
}
