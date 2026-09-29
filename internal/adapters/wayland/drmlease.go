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
		delete(s.pendingLeases, v.ID)
		defer delete(s.pendingLeaseCards, v.ID)
		if obj == nil || !obj.res.Alive() {
			if v.FD != nil {
				v.FD.Close()
			}
			if v.LeaseID != 0 {
				s.sendLeaseRequest(ports.LeaseRevoke{Card: s.pendingLeaseCards[v.ID], LeaseID: v.LeaseID})
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
		s.activeLeases[v.LeaseID] = obj
		obj.res.SendLeaseFd(int(v.FD.Fd()))
		v.FD.Close()
	case ports.LeaseFinished:
		if obj := s.activeLeases[v.LeaseID]; obj != nil && obj.card == v.Card {
			obj.finish()
		}
	}
}
func (s *Server) sendLeaseRequest(event ports.LeaseMessage) {
	if s.channels.LeaseRequests == nil {
		return
	}
	select {
	case s.channels.LeaseRequests <- event:
	default:
		// Never block the display goroutine on a backend doing a KMS ioctl.
		go func() {
			select {
			case s.channels.LeaseRequests <- event:
			case <-s.ctx.Done():
			}
		}()
	}
}
func (s *Server) updateLeaseConnectors(msg ports.LeaseConnectors) {
	// The backend owns Device and may close it as soon as it stops. Keep a
	// display-owned duplicate, so binds cannot race shutdown or a rescan.
	var copyFD *os.File
	if msg.Device != nil {
		fd, err := unix.FcntlInt(msg.Device.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			s.log.Warn().Err(err).Str("card", msg.Card).Msg("duplicate lease device")
			return
		}
		copyFD = os.NewFile(uintptr(fd), msg.Card)
	}
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
	if copyFD == nil {
		d.global.Remove()
		if d.fd != nil {
			d.fd.Close()
			d.fd = nil
		}
		for _, b := range d.binds {
			for r := range b.connectors {
				drmlease.WrapWpDrmLeaseConnectorV1(r).SendWithdrawn()
			}
			b.res.SendDone()
			b.res.SendReleased()
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
		delete(o.server.activeLeases, o.id)
		o.server.sendLeaseRequest(ports.LeaseRevoke{Card: o.card, LeaseID: o.id})
	}
	for id, p := range o.server.pendingLeases {
		if p == o {
			delete(o.server.pendingLeases, id)
		}
	}
}
func (o *leaseObject) finish() {
	if o.finished {
		return
	}
	o.finished = true
	if o.id != 0 {
		delete(o.server.activeLeases, o.id)
	}
	if o.res.Alive() {
		o.res.SendFinished()
	}
}
