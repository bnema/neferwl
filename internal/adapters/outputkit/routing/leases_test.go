package routing

import (
	"context"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func leaseRig(t *testing.T, active, protected *bool) (*LeasePublisher, chan ports.LeaseMessage, *mockleaseInventoryCard) {
	t.Helper()
	events := make(chan ports.LeaseMessage, 8)
	p := NewLeasePublisher(context.Background(), events, func() bool { return *active }, func() bool { return *protected }, logging.For(context.Background(), "app"))
	t.Cleanup(p.Close)
	card := newMockleaseInventoryCard(t)
	card.EXPECT().Path().Return("/dev/dri/card0").Maybe()
	return p, events, card
}

func clientFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "card")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// An inventory is sent once per change, each with its own fd, and finished
// leases go first.
func TestLeasePublisherSendsChangesOnly(t *testing.T) {
	active, protected := true, false
	p, events, card := leaseRig(t, &active, &protected)
	connectors := []domain.LeaseConnector{{Name: "HDMI-A-1"}}
	card.EXPECT().FinishedLeases().Return([]uint32{7}).Once()
	card.EXPECT().FinishedLeases().Return(nil)
	card.EXPECT().Leasable().Return(connectors)
	card.EXPECT().ClientFD().Return(clientFile(t), nil).Once()

	p.Publish(card)
	if fin, ok := (<-events).(ports.LeaseFinished); !ok || fin.LeaseID != 7 {
		t.Fatalf("finished lease first: %+v", fin)
	}
	inv, ok := (<-events).(ports.LeaseConnectors)
	if !ok || inv.Device == nil || len(inv.Connectors) != 1 {
		t.Fatalf("inventory: %+v", inv)
	}
	defer inv.Device.Close()
	if inv.Device.Fd() == p.clientFDs[card].Fd() {
		t.Fatal("message shares the publisher's fd")
	}
	p.Publish(card)
	if len(events) != 0 {
		t.Fatalf("unchanged inventory resent: %+v", <-events)
	}
}

// Protection withdraws the inventory and closes the client fd, even when
// nothing was offered before.
func TestLeasePublisherProtectedWithdraws(t *testing.T) {
	active, protected := true, true
	p, events, card := leaseRig(t, &active, &protected)
	card.EXPECT().FinishedLeases().Return(nil)
	p.Publish(card)
	if inv, ok := (<-events).(ports.LeaseConnectors); !ok || inv.Device != nil || len(inv.Connectors) != 0 {
		t.Fatalf("protected inventory: %+v", inv)
	}

	protected = false
	card.EXPECT().Leasable().Return([]domain.LeaseConnector{{Name: "DP-2"}}).Once()
	card.EXPECT().ClientFD().Return(clientFile(t), nil).Once()
	p.Publish(card)
	inv := (<-events).(ports.LeaseConnectors)
	inv.Device.Close()
	held := p.clientFDs[card]

	active = false // an inactive seat withdraws what was offered
	p.Publish(card)
	if inv, ok := (<-events).(ports.LeaseConnectors); !ok || inv.Device != nil {
		t.Fatalf("withdrawn inventory: %+v", inv)
	}
	if _, err := held.Stat(); err == nil {
		t.Fatal("client fd kept after withdrawal")
	}
	if p.clientFDs[card] != nil {
		t.Fatal("client fd still tracked")
	}
}

// An unplugged card is withdrawn once: finished leases, an empty inventory,
// and the client fd closes so the dead card is not kept open.
func TestLeasePublisherForgetWithdrawsCard(t *testing.T) {
	active, protected := true, false
	p, events, card := leaseRig(t, &active, &protected)
	card.EXPECT().FinishedLeases().Return(nil).Once()
	card.EXPECT().Leasable().Return([]domain.LeaseConnector{{Name: "DP-2"}}).Once()
	card.EXPECT().ClientFD().Return(clientFile(t), nil).Once()
	p.Publish(card)
	(<-events).(ports.LeaseConnectors).Device.Close()
	held := p.clientFDs[card]

	card.EXPECT().FinishedLeases().Return([]uint32{3}).Once()
	p.Forget(card)
	if fin, ok := (<-events).(ports.LeaseFinished); !ok || fin.LeaseID != 3 {
		t.Fatalf("finished lease first: %+v", fin)
	}
	if inv, ok := (<-events).(ports.LeaseConnectors); !ok || inv.Device != nil || len(inv.Connectors) != 0 {
		t.Fatalf("withdrawn inventory: %+v", inv)
	}
	if p.clientFDs[card] != nil || len(p.last) != 0 {
		t.Fatal("card state kept after forget")
	}
	if _, err := held.Stat(); err == nil {
		t.Fatal("client fd still open")
	}
	card.EXPECT().FinishedLeases().Return(nil).Once()
	p.Forget(card)
	if len(events) != 0 {
		t.Fatalf("second forget sent %+v", <-events)
	}
}
