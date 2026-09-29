package wayland

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	dl "github.com/bnema/purego-libwayland/protocol/drmlease"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

type leaseDeviceEvents struct {
	wlturbo.BaseProxy
	c          *wlturbo.Display
	connectors []uint32
	fds        []int
}

func (p *leaseDeviceEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(dl.WpDrmLeaseDeviceV1EventDrmFd):
		p.fds = append(p.fds, int(e.Fd()))
	case uint16(dl.WpDrmLeaseDeviceV1EventConnector):
		id := e.Uint32()
		p.connectors = append(p.connectors, id)
		child := &leaseConnectorEvents{}
		child.SetID(id)
		p.c.Context().Register(child)
	}
}

type leaseConnectorEvents struct {
	wlturbo.BaseProxy
	name string
}

func (p *leaseConnectorEvents) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(dl.WpDrmLeaseConnectorV1EventName) {
		p.name = e.String()
	}
}

type leaseResultEvents struct {
	wlturbo.BaseProxy
	fds      []int
	finished int
}

func (p *leaseResultEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(dl.WpDrmLeaseV1EventLeaseFd):
		p.fds = append(p.fds, int(e.Fd()))
	case uint16(dl.WpDrmLeaseV1EventFinished):
		p.finished++
	}
}
func leaseServer(t *testing.T) (*wlturbo.Display, chan ports.LeaseMessage, chan ports.LeaseMessage, *os.File) {
	t.Helper()
	dir := t.TempDir()
	requests := make(chan ports.LeaseMessage, 16)
	events := make(chan ports.LeaseMessage, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{LeaseRequests: requests, LeaseEvents: events}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server hung")
		}
	})
	c := protocolClient(t, s, dir)
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	// f is borrowed by the server; close it after Run has stopped.
	events <- ports.LeaseConnectors{Card: "/dev/dri/card-test", Device: f, Connectors: []ports.LeaseConnector{{Card: "/dev/dri/card-test", Name: "DP-1", Description: "VR headset", ConnectorID: 42}}}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Registry().FindGlobal("wp_drm_lease_device_v1"); ok {
			return c, requests, events, f
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no lease global")
	return nil, nil, nil, nil
}
func TestDRMLeaseFlow(t *testing.T) {
	c, requests, events, _ := leaseServer(t)
	dev := bindProtocol(t, c, "wp_drm_lease_device_v1")
	p := &leaseDeviceEvents{c: c}
	p.SetID(dev)
	c.Context().Register(p)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(p.fds) != 1 || len(p.connectors) != 1 {
		t.Fatalf("device: fds=%v connectors=%v", p.fds, p.connectors)
	}
	defer unix.Close(p.fds[0])
	conn := p.connectors[0]
	req := c.AllocateID()
	registerProtocol(t, c, req)
	requestProtocol(t, c, dev, dl.WpDrmLeaseDeviceV1RequestCreateLeaseRequest, req)
	requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestRequestConnector, conn)
	lease := c.AllocateID()
	l := &leaseResultEvents{}
	l.SetID(lease)
	c.Context().Register(l)
	requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestSubmit, lease)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var got ports.LeaseRequest
	select {
	case ev := <-requests:
		var ok bool
		got, ok = ev.(ports.LeaseRequest)
		if !ok {
			t.Fatalf("message %T", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request")
	}
	if len(got.Connectors) != 1 || got.Connectors[0] != "DP-1" {
		t.Fatalf("request %+v", got)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events <- ports.LeaseReply{ID: got.ID, FD: w, LeaseID: 7}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); len(l.fds) == 0 && time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.fds) != 1 {
		t.Fatalf("lease fd count %d", len(l.fds))
	}
	defer unix.Close(l.fds[0])
	requestProtocol(t, c, lease, dl.WpDrmLeaseV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-requests:
		if v, ok := ev.(ports.LeaseRevoke); !ok || v.LeaseID != 7 {
			t.Fatalf("revoke %v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no revoke")
	}
}
func TestDRMLeaseErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		duplicate bool
		code      uint32
	}{{"empty", false, uint32(dl.WpDrmLeaseRequestV1ErrorEmptyLease)}, {"duplicate", true, uint32(dl.WpDrmLeaseRequestV1ErrorDuplicateConnector)}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _, _ := leaseServer(t)
			dev := bindProtocol(t, c, "wp_drm_lease_device_v1")
			p := &leaseDeviceEvents{c: c}
			p.SetID(dev)
			c.Context().Register(p)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			for _, fd := range p.fds {
				defer unix.Close(fd)
			}
			req := c.AllocateID()
			registerProtocol(t, c, req)
			requestProtocol(t, c, dev, dl.WpDrmLeaseDeviceV1RequestCreateLeaseRequest, req)
			if tc.duplicate {
				conn := p.connectors[0]
				requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestRequestConnector, conn)
				requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestRequestConnector, conn)
			} else {
				lease := c.AllocateID()
				registerProtocol(t, c, lease)
				requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestSubmit, lease)
			}
			expectProtocolError(t, c, req, tc.code)
		})
	}
}
func TestDRMLeaseFinished(t *testing.T) {
	c, requests, events, _ := leaseServer(t)
	dev := bindProtocol(t, c, "wp_drm_lease_device_v1")
	p := &leaseDeviceEvents{c: c}
	p.SetID(dev)
	c.Context().Register(p)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p.fds[0])
	req := c.AllocateID()
	registerProtocol(t, c, req)
	requestProtocol(t, c, dev, dl.WpDrmLeaseDeviceV1RequestCreateLeaseRequest, req)
	requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestRequestConnector, p.connectors[0])
	lease := c.AllocateID()
	l := &leaseResultEvents{}
	l.SetID(lease)
	c.Context().Register(l)
	requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestSubmit, lease)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	got := (<-requests).(ports.LeaseRequest)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events <- ports.LeaseReply{ID: got.ID, FD: w, LeaseID: 8}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); len(l.fds) == 0 && time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.fds) != 1 {
		t.Fatal("missing lease fd")
	}
	defer unix.Close(l.fds[0])
	events <- ports.LeaseFinished{Card: got.Card, LeaseID: 8}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); l.finished == 0 && time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if l.finished != 1 {
		t.Fatalf("finished %d", l.finished)
	}
}
