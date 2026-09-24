package wayland

import (
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
	"sort"
)

type layerShell struct {
	server        *Server
	width, height uint32
}
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
}

func registerLayer(d *server.Display, o Options, s *Server) error {
	return wlrlayershell.NewZwlrLayerShellV1Global(d, 4, func(c server.Client, v, id uint32) {
		_, _ = wlrlayershell.NewZwlrLayerShellV1(c, int32(v), id, layerShell{s, uint32(o.OutputWidth), uint32(o.OutputHeight)})
	})
}
func (h layerShell) Destroy(*wlrlayershell.ZwlrLayerShellV1) {}
func (h layerShell) GetLayerSurface(r *wlrlayershell.ZwlrLayerShellV1, id uint32, w *wayland.Surface, _ *wayland.Output, layer uint32, namespace string) {
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
	l := &layerSurface{shell: h, surface: state, id: h.server.nextWindow, namespace: namespace}
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
	resource.OnDestroy = func() {
		l.unmap()
		delete(h.server.layers, l.id)
		state.layer = nil
		state.role = nil
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

// Layer popups are deferred; layer-shell is capped at v4.
func (*layerSurface) GetPopup(*wlrlayershell.ZwlrLayerSurfaceV1, *xdgshell.Popup) {}
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
	l.mapped = false
	l.configured = false
	l.acked = false
	l.serials = nil
	l.shell.server.layerChanged()
	l.shell.server.emitContent(ports.SurfaceContent{ID: l.id})
}
func (s *Server) layerChanged() {
	list := make([]ports.LayerSurface, 0)
	for _, l := range s.layers {
		if !l.mapped {
			continue
		}
		v := ports.LayerSurface{ID: l.id, Layer: l.current.layer, Anchor: l.current.anchor, ExclusiveZone: l.current.zone, Margin: l.current.margin, Namespace: l.namespace, Keyboard: l.current.keyboard}
		if l.surface.current != nil {
			if b := s.buffers[l.surface.current.Resource]; b != nil {
				v.Width, v.Height = b.width, b.height
			}
		}
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	s.emit(ports.LayerChanged{Layers: list})
}
func (l *layerSurface) commit(buffer bool) {
	if l.surface.destroyed {
		l.unmap()
		delete(l.shell.server.layers, l.id)
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
		w, h := p.width, p.height
		if w == 0 {
			w = l.shell.width
		}
		if h == 0 {
			h = l.shell.height
		}
		l.shell.server.serial++
		l.resource.SendConfigure(l.shell.server.serial, w, h)
		l.serials = append(l.serials, l.shell.server.serial)
		l.configured = true

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
