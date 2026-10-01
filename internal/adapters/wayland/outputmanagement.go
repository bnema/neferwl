package wayland

import (
	"reflect"
	"strings"

	wlr "github.com/bnema/go-wayland-bindings/server/wlroutputmanagement"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

type managementHead struct {
	res           *wlr.ZwlrOutputHeadV1
	modes         map[*server.Resource]ports.OutputMode
	modeResources []*wlr.ZwlrOutputModeV1
	info          ports.OutputHead
}
type outputManager struct {
	s       *Server
	res     *wlr.ZwlrOutputManagerV1
	heads   map[string]*managementHead
	stopped bool
	places  map[string]ports.OutputPlacement
}

func registerOutputManagement(d *server.Display, s *Server) error {
	return wlr.NewZwlrOutputManagerV1Global(d, 4, func(c server.Client, v, id uint32) {
		m := &outputManager{s: s, heads: map[string]*managementHead{}, places: map[string]ports.OutputPlacement{}}
		res, err := wlr.NewZwlrOutputManagerV1(c, int32(v), id, m)
		if err != nil {
			return
		}
		m.res = res
		s.outputManagers = append(s.outputManagers, m)
		res.OnDestroy = func() { s.outputManagers = removeItem(s.outputManagers, m) }
		for _, h := range s.outputHeads.Heads {
			m.addHead(h)
		}
		m.update()
		res.SendDone(s.managementSerial)
	})
}
func (m *outputManager) Stop(_ *wlr.ZwlrOutputManagerV1) {
	m.stopped = true
	m.res.SendFinished()
	m.res.Destroy()
}
func (m *outputManager) CreateConfiguration(r *wlr.ZwlrOutputManagerV1, id, serial uint32) {
	c := &outputConfiguration{manager: m, heads: map[string]*headConfiguration{}, serial: serial}
	res, err := wlr.NewZwlrOutputConfigurationV1(r.Client(), r.Version(), id, c)
	if err != nil {
		return
	}
	c.res = res
	res.OnDestroy = func() {
		for _, h := range c.heads {
			if h.res != nil {
				h.res.Destroy()
			}
		}
		for id, pending := range m.s.outputReplies {
			if pending == c {
				delete(m.s.outputReplies, id)
			}
		}
	}
	if serial != m.s.managementSerial {
		c.cancelled = true
		c.used = true
		res.SendCancelled()
	}
}
func (m *outputManager) addHead(info ports.OutputHead) {
	h := &managementHead{info: info, modes: map[*server.Resource]ports.OutputMode{}}
	res, err := wlr.NewZwlrOutputHeadV1(m.res.Client(), m.res.Version(), 0, managementHeadHandler{})
	if err != nil {
		return
	}
	h.res = res
	m.heads[info.Info.Name] = h
	m.res.SendHead(res)
	res.SendName(info.Info.Name)
	desc := strings.Join(strings.Fields(info.Info.Make+" "+info.Info.Model+" "+info.Info.Serial+" ("+info.Info.Name+")"), " ")
	res.SendDescription(desc)
	if info.Info.PhysicalW > 0 && info.Info.PhysicalH > 0 {
		res.SendPhysicalSize(int32(info.Info.PhysicalW), int32(info.Info.PhysicalH))
	}
	if res.Version() >= 2 {
		res.SendMake(info.Info.Make)
		res.SendModel(info.Info.Model)
		res.SendSerialNumber(info.Info.Serial)
	}
	for _, mode := range info.Modes {
		mr, e := wlr.NewZwlrOutputModeV1(res.Client(), min(res.Version(), 3), 0, managementModeHandler{})
		if e != nil {
			continue
		}
		h.modes[mr.Resource] = mode
		h.modeResources = append(h.modeResources, mr)
		mr.OnDestroy = func() {
			delete(h.modes, mr.Resource)
			h.modeResources = removeItem(h.modeResources, mr)
		}
		res.SendMode(mr)
		mr.SendSize(int32(mode.Width), int32(mode.Height))
		mr.SendRefresh(int32(mode.RefreshMilli))
		if mode.Preferred {
			mr.SendPreferred()
		}
	}
	m.sendState(h, info)
}
func (m *outputManager) update() {
	if m.stopped {
		return
	}
	wanted := map[string]ports.OutputHead{}
	for _, h := range m.s.outputHeads.Heads {
		wanted[h.Info.Name] = h
	}
	for name, h := range m.heads {
		if _, ok := wanted[name]; !ok {
			h.res.SendFinished()
			for _, mode := range h.modeResources {
				mode.SendFinished()
			}
			delete(m.heads, name)
			delete(m.places, name)
		}
	}
	for _, info := range m.s.outputHeads.Heads {
		h := m.heads[info.Info.Name]
		if h != nil && (!reflect.DeepEqual(h.info.Modes, info.Modes) || h.info.Info.Make != info.Info.Make || h.info.Info.Model != info.Info.Model || h.info.Info.Serial != info.Info.Serial || h.info.Info.PhysicalW != info.Info.PhysicalW || h.info.Info.PhysicalH != info.Info.PhysicalH) {
			h.res.SendFinished()
			for _, mode := range h.modeResources {
				mode.SendFinished()
			}
			delete(m.heads, info.Info.Name)
			delete(m.places, info.Info.Name)
			h = nil
		}
		if h == nil {
			m.addHead(info)
			continue // addHead has already sent the initial state.
		}
		changed := !reflect.DeepEqual(h.info, info)
		placementChanged := m.places[info.Info.Name] != m.s.placementFor(info.Info.Name)
		if !changed && !placementChanged {
			continue
		}
		m.sendState(h, info)
	}
}
func (m *outputManager) sendState(h *managementHead, info ports.OutputHead) {
	h.info = info
	enabled := int32(0)
	if info.Enabled {
		enabled = 1
	}
	h.res.SendEnabled(enabled)
	if info.Enabled {
		for res, mode := range h.modes {
			if info.Current != nil && mode.Width == info.Current.Width && mode.Height == info.Current.Height && mode.RefreshMilli == info.Current.RefreshMilli {
				h.res.SendCurrentMode(wlr.WrapZwlrOutputModeV1(res))
				break
			}
		}
		place := m.s.placementFor(info.Info.Name)
		if place.Info.Name != "" {
			h.res.SendPosition(int32(place.X), int32(place.Y))
			h.res.SendScale(server.FixedFromFloat(place.Scale))
		}
		h.res.SendTransform(0)
	}
	m.places[info.Info.Name] = m.s.placementFor(info.Info.Name)
}
func (s *Server) placementFor(name string) ports.OutputPlacement {
	for _, p := range s.outputPlaces {
		if p.Info.Name == name {
			return p
		}
	}
	return ports.OutputPlacement{}
}
func (s *Server) setOutputHeads(h ports.OutputHeads) {
	if reflect.DeepEqual(s.outputHeads, h) {
		return
	}
	s.outputHeads = h
	s.refreshOutputManagers()
}
func (s *Server) refreshOutputManagers() {
	s.managementSerial++
	if s.managementSerial == 0 {
		s.managementSerial++
	}
	for _, m := range s.outputManagers {
		if m.stopped {
			continue
		}
		m.update()
		m.res.SendDone(s.managementSerial)
	}
}

type managementHeadHandler struct{}

func (managementHeadHandler) Release(*wlr.ZwlrOutputHeadV1) {}

type managementModeHandler struct{}

func (managementModeHandler) Release(*wlr.ZwlrOutputModeV1) {}
