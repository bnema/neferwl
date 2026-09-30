package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/extdatacontrol"
	"github.com/bnema/purego-libwayland/protocol/primaryselection"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// The clipboard (wl_data_device) and the primary selection
// (zwp_primary_selection_v1) each hold one source. Only the client with
// keyboard focus may set them and sees them; clipboard managers such as
// cliphist and wl-clipboard use ext_data_control_v1, which sees and sets
// both without focus. Like wlroots, any client may bind it. Pasting hands the reader's pipe to the source client:
// the data never goes through neferwl. Drag and drop is not implemented.

// protected is a defensive DISPLAY admission check, not lock ownership.
func (s *Server) protected() bool {
	return s.security != nil && s.security.Snapshot().Protected
}

const (
	selClipboard = iota
	selPrimary
)

// clipSource is a client's offer: its MIME types and how to ask for data.
type clipSource struct {
	client server.Client
	mimes  []string
	send   func(mime string, fd int)
	cancel func()
	alive  func() bool
	// used: the source was set, rejected or dragged; it cannot be set
	// again and takes no more MIME types.
	used bool
}

// addMime records a MIME type offered before the source is used.
func (src *clipSource) addMime(mime string) bool {
	if src.used {
		return false
	}
	src.mimes = append(src.mimes, mime)
	return true
}

// clipDevice receives selections for one client.
type clipDevice struct {
	client server.Client
	// control devices (ext_data_control_v1) see every selection.
	control bool
	kinds   [2]bool
	alive   func() bool
	// offer announces src (nil clears) as the selection of kind.
	offer func(kind int, src *clipSource)
}

func registerClipboard(d *server.Display, s *Server) error {
	for _, register := range []func() error{
		func() error {
			return wayland.NewDataDeviceManagerGlobal(d, 3, func(c server.Client, v, id uint32) {
				_, _ = wayland.NewDataDeviceManager(c, int32(v), id, dataManager{s})
			})
		},
		func() error {
			return primaryselection.NewZwpPrimarySelectionDeviceManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
				_, _ = primaryselection.NewZwpPrimarySelectionDeviceManagerV1(c, int32(v), id, primaryManager{s})
			})
		},
		func() error {
			return extdatacontrol.NewExtDataControlManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
				_, _ = extdatacontrol.NewExtDataControlManagerV1(c, int32(v), id, controlManager{s})
			})
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// focusedClient is the client with keyboard focus.
func (s *Server) focusedClient() (server.Client, bool) {
	surf, _ := s.focusTarget(s.seat.focused)
	if surf == nil {
		return server.Client{}, false
	}
	return surf.Client(), true
}

// setSelection makes src the selection of kind and announces it. A
// replaced source is cancelled.
func (s *Server) setSelection(kind int, src *clipSource) {
	if s.protected() {
		if src != nil && src != s.selections[kind] && src.alive() {
			src.cancel()
		}
		return
	}
	old := s.selections[kind]
	if old == src {
		return
	}
	s.selections[kind] = src
	if old != nil && old.alive() {
		old.cancel()
	}
	s.announceAll(kind)
}

// announceAll sends the selection of kind to the focused client and to
// clipboard managers.
func (s *Server) announceAll(kind int) {
	c, ok := s.focusedClient()
	s.announce(kind, func(d *clipDevice) bool { return d.control || ok && d.client == c })
}

// announce sends the selection of kind to the devices matching want.
func (s *Server) announce(kind int, want func(*clipDevice) bool) {
	if s.protected() {
		return
	}
	src := s.selections[kind]
	if src != nil && !src.alive() {
		src, s.selections[kind] = nil, nil
	}
	alive := s.clipDevices[:0]
	for _, d := range s.clipDevices {
		if !d.alive() {
			continue
		}
		alive = append(alive, d)
		if d.kinds[kind] && want(d) {
			d.offer(kind, src)
		}
	}
	clear(s.clipDevices[len(alive):])
	s.clipDevices = alive
}

// focusSelections gives a client that just got keyboard focus the current
// selections, as the protocol asks before its keyboard enter.
func (s *Server) focusSelections() {
	c, ok := s.focusedClient()
	if !ok {
		return
	}
	for kind := range s.selections {
		s.announce(kind, func(d *clipDevice) bool { return !d.control && d.client == c })
	}
}

// addClipDevice tracks a device and sends it the selections it may see.
func (s *Server) addClipDevice(d *clipDevice) {
	s.clipDevices = append(s.clipDevices, d)
	if s.protected() {
		return
	}
	c, ok := s.focusedClient()
	if !d.control && (!ok || d.client != c) {
		return
	}
	for kind, src := range s.selections {
		if d.kinds[kind] {
			if src != nil && !src.alive() {
				src = nil
			}
			d.offer(kind, src)
		}
	}
}

// sourceGone clears the selections held by a destroyed source.
func (s *Server) sourceGone(src *clipSource) {
	for kind := range s.selections {
		if s.selections[kind] == src {
			s.selections[kind] = nil
			s.announceAll(kind)
		}
	}
}

// maySet reports whether a client may set the selection: only the
// focused one. Any client holding an offer may read it.
func (s *Server) maySet(c server.Client) bool {
	f, ok := s.focusedClient()
	return !s.protected() && ok && f == c
}

// setFocused sets a selection from a focus-bound device. A used source is
// ignored (it was already cancelled unless it is the current one); an
// unfocused client's source is cancelled.
func (s *Server) setFocused(c server.Client, kind int, src *clipSource) {
	if src != nil && src.used {
		return
	}
	if src != nil {
		src.used = true
	}
	if !s.maySet(c) {
		if src != nil {
			src.cancel()
		}
		return
	}
	s.setSelection(kind, src)
}

// receive hands the reader's pipe to the source if it is still the
// selection; the fd is always closed here (the event dups it). Protection
// rejects even previously announced offers. Transfers accepted before protection
// are client-to-client: their already handed-off pipes cannot be revoked here.
func (s *Server) receive(kind int, src *clipSource, mime string, fd int) {
	defer unix.Close(fd)
	if s.protected() || s.selections[kind] != src || !src.alive() {
		return
	}
	for _, m := range src.mimes {
		if m == mime {
			src.send(mime, fd)
			return
		}
	}
}

// wl_data_device_manager

type dataManager struct{ server *Server }

func (m dataManager) CreateDataSource(r *wayland.DataDeviceManager, id uint32) {
	src := &clipSource{client: r.Client()}
	res, err := wayland.NewDataSource(r.Client(), r.Version(), id, dataSource{src})
	if err != nil {
		return
	}
	src.send = func(mime string, fd int) { res.SendSend(mime, fd) }
	src.cancel = res.SendCancelled
	src.alive = res.Resource.Alive
	m.server.dataSources[res.Resource] = src
	res.OnDestroy = func() {
		delete(m.server.dataSources, res.Resource)
		m.server.sourceGone(src)
	}
}
func (m dataManager) GetDataDevice(r *wayland.DataDeviceManager, id uint32, _ *wayland.Seat) {
	s := m.server
	res, err := wayland.NewDataDevice(r.Client(), r.Version(), id, dataDevice{s})
	if err != nil {
		return
	}
	s.addClipDevice(&clipDevice{client: r.Client(), kinds: [2]bool{selClipboard: true}, alive: res.Resource.Alive, offer: func(kind int, src *clipSource) {
		if src == nil {
			res.SendSelection(nil)
			return
		}
		offer, err := wayland.NewDataOffer(res.Client(), res.Version(), 0, dataOffer{s, src})
		if err != nil {
			return
		}
		res.SendDataOffer(offer)
		for _, mime := range src.mimes {
			offer.SendOffer(mime)
		}
		res.SendSelection(offer)
	}})
}
func (dataManager) Release(r *wayland.DataDeviceManager) { r.Destroy() }

type dataSource struct{ src *clipSource }

func (d dataSource) Offer(_ *wayland.DataSource, mime string) { d.src.addMime(mime) }
func (dataSource) Destroy(*wayland.DataSource)                {}
func (dataSource) SetActions(*wayland.DataSource, uint32)     {}

type dataDevice struct{ server *Server }

// StartDrag is not implemented: the drag is cancelled.
func (d dataDevice) StartDrag(_ *wayland.DataDevice, src *wayland.DataSource, _, _ *wayland.Surface, _ uint32) {
	if src == nil {
		return
	}
	cs := d.server.dataSources[src.Resource]
	if cs == nil || cs.used {
		// A used source is the selection or already cancelled.
		return
	}
	cs.used = true
	if src.Resource.Alive() {
		src.SendCancelled()
	}
}
func (d dataDevice) SetSelection(r *wayland.DataDevice, res *wayland.DataSource, _ uint32) {
	s := d.server
	var src *clipSource
	if res != nil {
		src = s.dataSources[res.Resource]
		if src == nil {
			return
		}
	}
	s.setFocused(r.Client(), selClipboard, src)
}
func (dataDevice) Release(r *wayland.DataDevice) { r.Destroy() }

type dataOffer struct {
	server *Server
	src    *clipSource
}

func (dataOffer) Accept(*wayland.DataOffer, uint32, string) {}
func (o dataOffer) Receive(_ *wayland.DataOffer, mime string, fd int) {
	o.server.receive(selClipboard, o.src, mime, fd)
}
func (dataOffer) Destroy(*wayland.DataOffer)                    {}
func (dataOffer) Finish(*wayland.DataOffer)                     {}
func (dataOffer) SetActions(*wayland.DataOffer, uint32, uint32) {}

// zwp_primary_selection_device_manager_v1

type primaryManager struct{ server *Server }

func (m primaryManager) CreateSource(r *primaryselection.ZwpPrimarySelectionDeviceManagerV1, id uint32) {
	src := &clipSource{client: r.Client()}
	res, err := primaryselection.NewZwpPrimarySelectionSourceV1(r.Client(), r.Version(), id, primarySource{src})
	if err != nil {
		return
	}
	src.send = func(mime string, fd int) { res.SendSend(mime, fd) }
	src.cancel = res.SendCancelled
	src.alive = res.Resource.Alive
	m.server.primarySources[res.Resource] = src
	res.OnDestroy = func() {
		delete(m.server.primarySources, res.Resource)
		m.server.sourceGone(src)
	}
}
func (m primaryManager) GetDevice(r *primaryselection.ZwpPrimarySelectionDeviceManagerV1, id uint32, _ *wayland.Seat) {
	s := m.server
	res, err := primaryselection.NewZwpPrimarySelectionDeviceV1(r.Client(), r.Version(), id, primaryDevice{s})
	if err != nil {
		return
	}
	s.addClipDevice(&clipDevice{client: r.Client(), kinds: [2]bool{selPrimary: true}, alive: res.Resource.Alive, offer: func(kind int, src *clipSource) {
		if src == nil {
			res.SendSelection(nil)
			return
		}
		offer, err := primaryselection.NewZwpPrimarySelectionOfferV1(res.Client(), res.Version(), 0, primaryOffer{s, src})
		if err != nil {
			return
		}
		res.SendDataOffer(offer)
		for _, mime := range src.mimes {
			offer.SendOffer(mime)
		}
		res.SendSelection(offer)
	}})
}
func (primaryManager) Destroy(*primaryselection.ZwpPrimarySelectionDeviceManagerV1) {}

type primarySource struct{ src *clipSource }

func (p primarySource) Offer(_ *primaryselection.ZwpPrimarySelectionSourceV1, mime string) {
	p.src.addMime(mime)
}
func (primarySource) Destroy(*primaryselection.ZwpPrimarySelectionSourceV1) {}

type primaryDevice struct{ server *Server }

func (d primaryDevice) SetSelection(r *primaryselection.ZwpPrimarySelectionDeviceV1, res *primaryselection.ZwpPrimarySelectionSourceV1, _ uint32) {
	s := d.server
	var src *clipSource
	if res != nil {
		src = s.primarySources[res.Resource]
		if src == nil {
			return
		}
	}
	s.setFocused(r.Client(), selPrimary, src)
}
func (primaryDevice) Destroy(*primaryselection.ZwpPrimarySelectionDeviceV1) {}

type primaryOffer struct {
	server *Server
	src    *clipSource
}

func (o primaryOffer) Receive(_ *primaryselection.ZwpPrimarySelectionOfferV1, mime string, fd int) {
	o.server.receive(selPrimary, o.src, mime, fd)
}
func (primaryOffer) Destroy(*primaryselection.ZwpPrimarySelectionOfferV1) {}

// ext_data_control_manager_v1

type controlManager struct{ server *Server }

func (m controlManager) CreateDataSource(r *extdatacontrol.ExtDataControlManagerV1, id uint32) {
	src := &clipSource{client: r.Client()}
	res, err := extdatacontrol.NewExtDataControlSourceV1(r.Client(), r.Version(), id, controlSource{src})
	if err != nil {
		return
	}
	src.send = func(mime string, fd int) { res.SendSend(mime, fd) }
	src.cancel = res.SendCancelled
	src.alive = res.Resource.Alive
	m.server.controlSources[res.Resource] = src
	res.OnDestroy = func() {
		delete(m.server.controlSources, res.Resource)
		m.server.sourceGone(src)
	}
}
func (m controlManager) GetDataDevice(r *extdatacontrol.ExtDataControlManagerV1, id uint32, _ *wayland.Seat) {
	s := m.server
	res, err := extdatacontrol.NewExtDataControlDeviceV1(r.Client(), r.Version(), id, controlDevice{s})
	if err != nil {
		return
	}
	s.addClipDevice(&clipDevice{client: r.Client(), control: true, kinds: [2]bool{true, true}, alive: res.Resource.Alive, offer: func(kind int, src *clipSource) {
		send := res.SendSelection
		if kind == selPrimary {
			send = res.SendPrimarySelection
		}
		if src == nil {
			send(nil)
			return
		}
		offer, err := extdatacontrol.NewExtDataControlOfferV1(res.Client(), res.Version(), 0, controlOffer{s, kind, src})
		if err != nil {
			return
		}
		res.SendDataOffer(offer)
		for _, mime := range src.mimes {
			offer.SendOffer(mime)
		}
		send(offer)
	}})
}
func (controlManager) Destroy(*extdatacontrol.ExtDataControlManagerV1) {}

type controlSource struct{ src *clipSource }

func (c controlSource) Offer(r *extdatacontrol.ExtDataControlSourceV1, mime string) {
	if !c.src.addMime(mime) {
		r.PostError(uint32(extdatacontrol.ExtDataControlSourceV1ErrorInvalidOffer), "offer after the source was used")
	}
}
func (controlSource) Destroy(*extdatacontrol.ExtDataControlSourceV1) {}

type controlDevice struct{ server *Server }

func (d controlDevice) SetSelection(r *extdatacontrol.ExtDataControlDeviceV1, res *extdatacontrol.ExtDataControlSourceV1) {
	d.set(r, selClipboard, res)
}
func (d controlDevice) SetPrimarySelection(r *extdatacontrol.ExtDataControlDeviceV1, res *extdatacontrol.ExtDataControlSourceV1) {
	d.set(r, selPrimary, res)
}

// set lets a clipboard manager set a selection without focus. A source
// may be used once.
func (d controlDevice) set(r *extdatacontrol.ExtDataControlDeviceV1, kind int, res *extdatacontrol.ExtDataControlSourceV1) {
	s := d.server
	var src *clipSource
	if res != nil {
		src = s.controlSources[res.Resource]
		if src == nil {
			return
		}
		if src.used {
			r.PostError(uint32(extdatacontrol.ExtDataControlDeviceV1ErrorUsedSource), "source already used")
			return
		}
		src.used = true
	}
	s.setSelection(kind, src)
}
func (controlDevice) Destroy(*extdatacontrol.ExtDataControlDeviceV1) {}

type controlOffer struct {
	server *Server
	kind   int
	src    *clipSource
}

func (o controlOffer) Receive(_ *extdatacontrol.ExtDataControlOfferV1, mime string, fd int) {
	o.server.receive(o.kind, o.src, mime, fd)
}
func (controlOffer) Destroy(*extdatacontrol.ExtDataControlOfferV1) {}
