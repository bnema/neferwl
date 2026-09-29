package wayland

import (
	"context"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/drmlease"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

type leaseDevice struct {
	server     *Server
	card       string
	global     *server.Global
	fd         *os.File // owned by the display after receiving a duplicated DRM fd
	connectors map[string]ports.LeaseConnector
	binds      map[*drmlease.WpDrmLeaseDeviceV1]*leaseBind
}
type leaseBind struct {
	device     *leaseDevice
	res        *drmlease.WpDrmLeaseDeviceV1
	connectors map[*server.Resource]string
	known      map[*server.Resource]string
}
type leaseRequestHandler struct {
	bind      *leaseBind
	names     []string
	withdrawn bool
}

// DRM lessee IDs are allocated independently on each card.
type leaseKey struct {
	card string
	id   uint32
}

type leaseObject struct {
	server   *Server
	res      *drmlease.WpDrmLeaseV1
	card     string
	names    []string
	id       uint32
	finished bool
}
type leaseConnectorHandler struct{}

func (leaseConnectorHandler) Destroy(*drmlease.WpDrmLeaseConnectorV1) {}

func (s *Server) forwardLeases(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case event, ok := <-s.channels.LeaseEvents:
			if !ok {
				return
			}
			if !s.display.Do(func() { s.applyLeaseEvent(event) }) {
				return
			}
		}
	}
}
func (s *Server) applyLeaseEvent(event ports.LeaseMessage) {
	switch v := event.(type) {
	case ports.LeaseConnectors:
		s.updateLeaseConnectors(v)
	case ports.LeaseReply:
		obj := s.pendingLeases[v.ID]
		card := s.pendingLeaseCards[v.ID]
		delete(s.pendingLeases, v.ID)
		delete(s.pendingLeaseCards, v.ID)
		if obj == nil || !obj.res.Alive() {
			if v.FD != nil {
				v.FD.Close()
			}
			if v.LeaseID != 0 && card != "" {
				s.sendLeaseRequest(ports.LeaseRevoke{Card: card, LeaseID: v.LeaseID})
			}
			return
		}
		if v.Err != nil || v.FD == nil {
			if v.FD != nil {
				v.FD.Close()
			}
			obj.finish()
			return
		}
		obj.id = v.LeaseID
		s.activeLeases[leaseKey{obj.card, v.LeaseID}] = obj
		obj.res.SendLeaseFd(int(v.FD.Fd()))
		v.FD.Close()
	case ports.LeaseFinished:
		if obj := s.activeLeases[leaseKey{v.Card, v.LeaseID}]; obj != nil {
			obj.finish()
		}
	}
}

// sendLeaseRequest enqueues without blocking the display. One sender drains
// this queue in order even when the backend channel is full.
func (s *Server) sendLeaseRequest(event ports.LeaseMessage) {
	if s.channels.LeaseRequests == nil {
		return
	}
	s.leaseMu.Lock()
	s.leaseOut = append(s.leaseOut, event)
	s.leaseMu.Unlock()
	select {
	case s.leaseReady <- struct{}{}:
	default:
	}
}
func (s *Server) forwardLeaseRequests(ctx context.Context) {
	for {
		s.leaseMu.Lock()
		if len(s.leaseOut) == 0 {
			s.leaseMu.Unlock()
			select {
			case <-s.leaseReady:
				continue
			case <-ctx.Done():
				return
			case <-s.display.Stopped():
				return
			}
		}
		event := s.leaseOut[0]
		s.leaseMu.Unlock()
		select {
		case s.channels.LeaseRequests <- event:
			s.leaseMu.Lock()
			s.leaseOut[0] = nil
			s.leaseOut = s.leaseOut[1:]
			s.leaseMu.Unlock()
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		}
	}
}
func (s *Server) updateLeaseConnectors(msg ports.LeaseConnectors) {
	// The display owns Device from here on.
	copyFD := msg.Device
	d := s.leaseDevices[msg.Card]
	if d == nil {
		if copyFD == nil || len(msg.Connectors) == 0 {
			if copyFD != nil {
				copyFD.Close()
			}
			return
		}
		d = &leaseDevice{server: s, card: msg.Card, fd: copyFD, connectors: map[string]ports.LeaseConnector{}, binds: map[*drmlease.WpDrmLeaseDeviceV1]*leaseBind{}}
		g, err := s.display.AddGlobal(drmlease.WpDrmLeaseDeviceV1Interface, 1, func(c server.Client, v, id uint32) { d.bind(c, v, id) })
		if err != nil {
			copyFD.Close()
			s.log.Warn().Err(err).Str("card", msg.Card).Msg("drm lease global")
			return
		}
		d.global = g
		s.leaseDevices[msg.Card] = d
	}
	if copyFD == nil || len(msg.Connectors) == 0 {
		if copyFD != nil {
			copyFD.Close()
		}
		d.global.Remove()
		if d.fd != nil {
			d.fd.Close()
			d.fd = nil
		}
		// Bound devices stay until the client sends release, as the
		// protocol asks after global_remove; they offer nothing meanwhile.
		for _, b := range d.binds {
			for r := range b.connectors {
				drmlease.WrapWpDrmLeaseConnectorV1(r).SendWithdrawn()
				delete(b.connectors, r)
			}
			b.res.SendDone()
		}
		delete(s.leaseDevices, msg.Card)
		return
	}
	if d.fd != copyFD {
		if d.fd != nil {
			d.fd.Close()
		}
		d.fd = copyFD
	}
	next := map[string]ports.LeaseConnector{}
	for _, c := range msg.Connectors {
		next[c.Name] = c
	}
	for _, b := range d.binds {
		changed := false
		for r, name := range b.connectors {
			if _, ok := next[name]; !ok && r.Alive() {
				drmlease.WrapWpDrmLeaseConnectorV1(r).SendWithdrawn()
				delete(b.connectors, r)
				changed = true
			}
		}
		for name, c := range next {
			found := false
			for _, n := range b.connectors {
				if n == name {
					found = true
					break
				}
			}
			if !found {
				b.add(c)
				changed = true
			}
		}
		if changed {
			b.res.SendDone()
		}
	}
	d.connectors = next
}
func (d *leaseDevice) bind(c server.Client, v, id uint32) {
	b := &leaseBind{device: d, connectors: map[*server.Resource]string{}, known: map[*server.Resource]string{}}
	r, err := drmlease.NewWpDrmLeaseDeviceV1(c, int32(v), id, b)
	if err != nil {
		return
	}
	b.res = r
	d.binds[r] = b
	r.OnDestroy = func() { delete(d.binds, r) }
	if d.fd != nil {
		fd, err := unix.FcntlInt(d.fd.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err == nil {
			r.SendDrmFd(fd)
			unix.Close(fd)
		} else {
			d.server.log.Warn().Err(err).Str("card", d.card).Msg("lease drm fd")
			r.SendReleased()
			r.Destroy()
			return
		}
	}
	for _, c := range d.connectors {
		b.add(c)
	}
	r.SendDone()
}
func (b *leaseBind) add(c ports.LeaseConnector) {
	r, err := drmlease.NewWpDrmLeaseConnectorV1(b.res.Client(), 1, 0, leaseConnectorHandler{})
	if err != nil {
		return
	}
	b.connectors[r.Resource] = c.Name
	b.known[r.Resource] = c.Name
	r.OnDestroy = func() { delete(b.connectors, r.Resource); delete(b.known, r.Resource) }
	b.res.SendConnector(r)
	r.SendName(c.Name)
	r.SendDescription(c.Description)
	r.SendConnectorId(c.ConnectorID)
	r.SendDone()
}
func (b *leaseBind) CreateLeaseRequest(_ *drmlease.WpDrmLeaseDeviceV1, id uint32) {
	_, _ = drmlease.NewWpDrmLeaseRequestV1(b.res.Client(), 1, id, &leaseRequestHandler{bind: b})
}
func (b *leaseBind) Release(self *drmlease.WpDrmLeaseDeviceV1) { self.SendReleased(); self.Destroy() }
func (r *leaseRequestHandler) RequestConnector(self *drmlease.WpDrmLeaseRequestV1, c *drmlease.WpDrmLeaseConnectorV1) {
	if c == nil {
		self.PostError(uint32(drmlease.WpDrmLeaseRequestV1ErrorWrongDevice), "connector belongs to another device")
		return
	}
	name, ok := r.bind.known[c.Resource]
	if !ok {
		self.PostError(uint32(drmlease.WpDrmLeaseRequestV1ErrorWrongDevice), "connector belongs to another device")
		return
	}
	if _, available := r.bind.connectors[c.Resource]; !available {
		r.withdrawn = true
	}
	if slices.Contains(r.names, name) {
		self.PostError(uint32(drmlease.WpDrmLeaseRequestV1ErrorDuplicateConnector), "duplicate connector")
		return
	}
	r.names = append(r.names, name)
}
func (r *leaseRequestHandler) Submit(self *drmlease.WpDrmLeaseRequestV1, id uint32) {
	if len(r.names) == 0 {
		self.PostError(uint32(drmlease.WpDrmLeaseRequestV1ErrorEmptyLease), "empty lease")
		return
	}
	s := r.bind.device.server
	obj := &leaseObject{server: s, card: r.bind.device.card, names: append([]string(nil), r.names...)}
	res, err := drmlease.NewWpDrmLeaseV1(self.Client(), 1, id, obj)
	if err != nil {
		return
	}
	obj.res = res
	res.OnDestroy = func() { obj.destroy() }
	for _, name := range r.names {
		if _, ok := r.bind.device.connectors[name]; !ok || r.withdrawn {
			obj.finish()
			return
		}
	}
	s.nextLeaseID++
	s.pendingLeases[s.nextLeaseID] = obj
	s.pendingLeaseCards[s.nextLeaseID] = obj.card
	s.sendLeaseRequest(ports.LeaseRequest{ID: s.nextLeaseID, Card: obj.card, Connectors: obj.names})
}
func (o *leaseObject) Destroy(*drmlease.WpDrmLeaseV1) { o.destroy() }
func (o *leaseObject) destroy() {
	if o.finished {
		return
	}
	o.finished = true
	if o.id != 0 {
		delete(o.server.activeLeases, leaseKey{o.card, o.id})
		o.server.sendLeaseRequest(ports.LeaseRevoke{Card: o.card, LeaseID: o.id})
	}
	for id, p := range o.server.pendingLeases {
		if p == o {
			delete(o.server.pendingLeases, id)
			// Keep pendingLeaseCards as a tombstone for a late successful
			// reply; applyLeaseEvent revokes it and then removes the tombstone.
		}
	}
}
func (o *leaseObject) finish() {
	if o.finished {
		return
	}
	o.finished = true
	if o.id != 0 {
		delete(o.server.activeLeases, leaseKey{o.card, o.id})
	}
	if o.res.Alive() {
		o.res.SendFinished()
	}
}
