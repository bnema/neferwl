package routing

import (
	"context"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// leaseInventoryCard is what the lease publisher reads from a DRM card.
type leaseInventoryCard interface {
	Path() string
	FinishedLeases() []uint32
	Leasable() []domain.LeaseConnector
	ClientFD() (*os.File, error)
}

// LeasePublisher tells wayland which connectors of each card can be leased
// (wp_drm_lease_device_v1), and which leases ended. It keeps one non-master
// client fd per advertised card and resends an inventory only when it
// changed. The DRM backend loop owns it.
type LeasePublisher struct {
	ctx    context.Context
	events chan<- ports.LeaseMessage
	log    zerowrap.Logger
	// seatActive and protected say whether leases may be offered: only on an
	// active seat in an unprotected session.
	seatActive, protected func() bool
	clientFDs             map[leaseInventoryCard]*os.File
	last                  map[leaseInventoryCard][]domain.LeaseConnector
}

func NewLeasePublisher(ctx context.Context, events chan<- ports.LeaseMessage, seatActive, protected func() bool, log zerowrap.Logger) *LeasePublisher {
	return &LeasePublisher{ctx: ctx, events: events, seatActive: seatActive, protected: protected, log: log, clientFDs: map[leaseInventoryCard]*os.File{}, last: map[leaseInventoryCard][]domain.LeaseConnector{}}
}

// Send delivers msg, closing its files when ctx ends first.
func (p *LeasePublisher) Send(msg ports.LeaseMessage) {
	select {
	case p.events <- msg:
	case <-p.ctx.Done():
		ports.CloseLeaseFiles(msg)
	}
}

// Publish reports the finished leases of c and its leasable connectors. An
// inactive seat or a protected session withdraws the inventory; protection
// always tells wayland so.
func (p *LeasePublisher) Publish(c leaseInventoryCard) {
	for _, id := range c.FinishedLeases() {
		p.Send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
	}
	var next []domain.LeaseConnector
	if p.seatActive() && !p.protected() {
		next = c.Leasable()
	}
	if len(next) == 0 {
		if p.protected() || p.clientFDs[c] != nil {
			p.Send(ports.LeaseConnectors{Card: c.Path()})
		}
		if p.clientFDs[c] != nil {
			p.clientFDs[c].Close()
			delete(p.clientFDs, c)
		}
		delete(p.last, c)
		return
	}
	if slices.Equal(p.last[c], next) && p.clientFDs[c] != nil {
		return
	}
	if p.clientFDs[c] == nil {
		f, err := c.ClientFD()
		if err != nil {
			p.log.Warn().Err(err).Str("card", c.Path()).Msg("lease client fd")
			return
		}
		p.clientFDs[c] = f
	}
	// Each message owns its fd: a queued inventory outlives clientFDs[c].
	fd, err := unix.FcntlInt(p.clientFDs[c].Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		p.log.Warn().Err(err).Str("card", c.Path()).Msg("lease client fd")
		return
	}
	p.last[c] = next
	p.Send(ports.LeaseConnectors{Card: c.Path(), Device: os.NewFile(uintptr(fd), c.Path()), Connectors: next})
}

// Forget withdraws c for good (its card was unplugged): finished leases are
// reported, wayland is told the device has no connectors, the client fd closes.
func (p *LeasePublisher) Forget(c leaseInventoryCard) {
	for _, id := range c.FinishedLeases() {
		p.Send(ports.LeaseFinished{Card: c.Path(), LeaseID: id})
	}
	if p.clientFDs[c] != nil || p.protected() {
		p.Send(ports.LeaseConnectors{Card: c.Path()})
	}
	if p.clientFDs[c] != nil {
		p.clientFDs[c].Close()
	}
	delete(p.clientFDs, c)
	delete(p.last, c)
}

// Close releases the client fds.
func (p *LeasePublisher) Close() {
	for _, f := range p.clientFDs {
		f.Close()
	}
	clear(p.clientFDs)
}
