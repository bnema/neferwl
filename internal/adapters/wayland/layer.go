package wayland

import (
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
	"sort"
)

type layerShell struct{ server *Server }
type layerState struct {
	width, height, anchor uint32
	zone                  int32
	margin                [4]int32
	keyboard              uint32
	layer                 ports.Layer
}
type layerSurface struct {
	resource                  *wlrlayershell.ZwlrLayerSurfaceV1
	shell                     layerShell
	surface                   *surface
	id                        ports.WindowID
	namespace                 string
	pending, current          layerState
	configured, acked, mapped bool
	serials                   []uint32
	// output is the output the client asked for, else the focused one at
	// creation. The surface is closed when it is unplugged.
	output *output
	closed bool // output gone: commits are ignored until destroy
}

func registerLayer(d *server.Display, s *Server) error {
	return wlrlayershell.NewZwlrLayerShellV1Global(d, 4, func(c server.Client, v, id uint32) {
		_, _ = wlrlayershell.NewZwlrLayerShellV1(c, int32(v), id, layerShell{s})
	})
}
func (h layerShell) Destroy(*wlrlayershell.ZwlrLayerShellV1) {}
func (h layerShell) GetLayerSurface(r *wlrlayershell.ZwlrLayerShellV1, id uint32, w *wayland.Surface, wl *wayland.Output, layer uint32, namespace string) {
	if w == nil || h.server.surfaces[w.Resource] == nil {
		return
	}
	state := h.server.surfaces[w.Resource]
	if state.kind != roleNone || (state.attached && state.pending != nil) || state.current != nil {
		r.PostError(uint32(wlrlayershell.ZwlrLayerShellV1ErrorAlreadyConstructed), "surface already constructed")
		return
	}
	if layer > 3 {
		r.PostError(uint32(wlrlayershell.ZwlrLayerShellV1ErrorInvalidLayer), "invalid layer")
		return
	}
	out := h.server.outputOf(wl)
	gone := wl != nil && out == nil // its output was unplugged
	if wl == nil {
		out = h.server.outputByName("")
	}
	l := &layerSurface{shell: h, surface: state, id: h.server.nextWindow, namespace: namespace, output: out}
	l.pending.layer = ports.Layer(layer)
	resource, err := wlrlayershell.NewZwlrLayerSurfaceV1(r.Client(), r.Version(), id, l)
	if err != nil {
		return
	}
	l.resource = resource
	h.server.nextWindow++
	state.kind = roleLayer
	state.layer = l
	state.role = l.commit
	h.server.layers[l.id] = l
	if gone {
		l.close()
	}
	resource.OnDestroy = func() {
		l.unmap()
		delete(h.server.layers, l.id)
		state.layer = nil
		state.role = nil
		state.dropQueue()
		state.current, state.pending = nil, nil
		state.attached = false
	}
}
func (l *layerSurface) SetSize(_ *wlrlayershell.ZwlrLayerSurfaceV1, w, h uint32) {
	l.pending.width, l.pending.height = w, h
}
func (l *layerSurface) SetAnchor(r *wlrlayershell.ZwlrLayerSurfaceV1, a uint32) {
	if a > 15 {
		r.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidAnchor), "invalid anchor")
		return
	}
	l.pending.anchor = a
}
func (l *layerSurface) SetExclusiveZone(_ *wlrlayershell.ZwlrLayerSurfaceV1, z int32) {
	l.pending.zone = z
}
func (l *layerSurface) SetMargin(_ *wlrlayershell.ZwlrLayerSurfaceV1, t, r, b, left int32) {
	l.pending.margin = [4]int32{t, r, b, left}
}

func (l *layerSurface) SetKeyboardInteractivity(r *wlrlayershell.ZwlrLayerSurfaceV1, k uint32) {
	if k > 2 {
		r.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidKeyboardInteractivity), "invalid keyboard interactivity")
		return
	}
	l.pending.keyboard = k
}

// GetPopup makes the layer surface the parent of an xdg_popup created
// without one. Core places it relative to the layer.
func (l *layerSurface) GetPopup(_ *wlrlayershell.ZwlrLayerSurfaceV1, r *xdgshell.Popup) {
	if r == nil {
		return
	}
	for _, w := range l.shell.server.windows {
		if p := w.popup; p != nil && p.resource.Resource == r.Resource {
			if p.parent == nil && !p.sent && !p.done {
				p.layer = l
			}
			return
		}
	}
}

// surfaceResource is the layer's live wl_surface, if any.
func (l *layerSurface) surfaceResource() *wayland.Surface {
	for resource, state := range l.shell.server.surfaces {
		if state == l.surface && resource.Alive() {
			return wayland.WrapSurface(resource)
		}
	}
	return nil
}
func (l *layerSurface) AckConfigure(r *wlrlayershell.ZwlrLayerSurfaceV1, serial uint32) {
	for i, v := range l.serials {
		if v == serial {
			l.serials = l.serials[i+1:]
			l.acked = true
			return
		}
	}
	r.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState), "unknown configure serial")
}
func (*layerSurface) Destroy(*wlrlayershell.ZwlrLayerSurfaceV1) {}
func (l *layerSurface) SetLayer(r *wlrlayershell.ZwlrLayerSurfaceV1, v uint32) {
	if v > 3 {
		r.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState), "invalid layer")
		return
	}
	l.pending.layer = ports.Layer(v)
}

// Exclusive edge is not implemented; layer-shell is capped at v4.
func (*layerSurface) SetExclusiveEdge(*wlrlayershell.ZwlrLayerSurfaceV1, uint32) {}
func (l *layerSurface) unmap() {
	if !l.mapped {
		return
	}
	if s := l.shell.server; s.focused == l.id {
		s.changeFocus(0)
	}
	if s := l.shell.server; s.pointerFocus == l.id {
		s.changePointerFocus(0, 0, 0)
	}
	l.mapped = false
	l.configured = false
	l.acked = false
	l.serials = nil
	l.shell.server.layerChanged()
	l.shell.server.emitContent(ports.SurfaceContent{ID: l.id})
}

// close tells the client its output is gone; the surface stays unmapped
// until the client destroys it.
func (l *layerSurface) close() {
	l.unmap()
	l.output = nil
	l.closed = true
	if l.resource.Resource.Alive() {
		l.resource.SendClosed()
	}
}

func (s *Server) layerChanged() {
	list := make([]ports.LayerSurface, 0)
	for _, l := range s.layers {
		if !l.mapped {
			continue
		}
		v := ports.LayerSurface{ID: l.id, Layer: l.current.layer, Anchor: l.current.anchor, ExclusiveZone: l.current.zone, Margin: l.current.margin, Namespace: l.namespace, Keyboard: l.current.keyboard}
		if l.output != nil {
			v.Output = l.output.name()
		}
		if l.surface.current != nil {
			if b := s.buffers[l.surface.current.Resource]; b != nil {
				v.Width, v.Height = l.surface.logicalSize(b.size())
			}
		}
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	s.emit(ports.LayerChanged{Layers: list})
}

// sendConfigure sizes the surface from committed state; zero means "fill",
// the logical output size.
func (l *layerSurface) sendConfigure() {
	w, h := l.current.width, l.current.height
	var ow, oh int
	if l.output != nil {
		ow, oh = l.output.place.Width, l.output.place.Height
	}
	if w == 0 {
		w = uint32(ow)
	}
	if h == 0 {
		h = uint32(oh)
	}
	l.shell.server.serial++
	l.resource.SendConfigure(l.shell.server.serial, w, h)
	l.serials = append(l.serials, l.shell.server.serial)
	l.configured = true
}

func (l *layerSurface) commit(buffer bool) {
	if l.surface.destroyed {
		l.unmap()
		delete(l.shell.server.layers, l.id)
		return
	}
	if l.closed {
		return
	}
	p := l.pending
	if (p.width == 0 && p.anchor&(ports.AnchorLeft|ports.AnchorRight) != ports.AnchorLeft|ports.AnchorRight) || (p.height == 0 && p.anchor&(ports.AnchorTop|ports.AnchorBottom) != ports.AnchorTop|ports.AnchorBottom) {
		l.resource.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSize), "zero size requires opposing anchors")
		return
	}
	changed := p != l.current
	sizeChanged := p.width != l.current.width || p.height != l.current.height
	l.current = p
	if !l.configured || sizeChanged {
		l.sendConfigure()
	}
	if !buffer {
		if l.mapped {
			l.unmap()
			l.configured = false
			l.acked = false
			l.serials = nil
		}
		return
	}
	if l.acked {
		wasMapped := l.mapped
		l.mapped = true
		if changed || !wasMapped {
			l.shell.server.layerChanged()
		}
	}
}
