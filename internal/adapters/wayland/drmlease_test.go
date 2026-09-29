package wayland

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	dl "github.com/bnema/purego-libwayland/protocol/drmlease"
	"github.com/bnema/purego-libwayland/server"
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
	c, requests, events, f, _ := leaseServerWithState(t)
	return c, requests, events, f
}
func leaseServerWithState(t *testing.T) (*wlturbo.Display, chan ports.LeaseMessage, chan ports.LeaseMessage, *os.File, *Server) {
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
			return c, requests, events, f, s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no lease global")
	return nil, nil, nil, nil, nil
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

func TestLeaseMessagesRemainOrderedWhenChannelFull(t *testing.T) {
	requests := make(chan ports.LeaseMessage, 1)
	ready := make(chan struct{}, 1)
	display, err := server.NewDisplay()
	if err != nil {
		t.Fatal(err)
	}
	defer display.Close()
	s := &Server{display: display, channels: Channels{LeaseRequests: requests}, leaseReady: ready}
	requests <- ports.LeaseRequest{ID: 99}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.forwardLeaseRequests(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	s.sendLeaseRequest(ports.LeaseRequest{ID: 1})
	s.sendLeaseRequest(ports.LeaseRevoke{Card: "card", LeaseID: 1})
	if got := <-requests; got.(ports.LeaseRequest).ID != 99 {
		t.Fatal(got)
	}
	select {
	case got := <-requests:
		if v, ok := got.(ports.LeaseRequest); !ok || v.ID != 1 {
			t.Fatalf("first %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("request timeout")
	}
	select {
	case got := <-requests:
		if v, ok := got.(ports.LeaseRevoke); !ok || v.LeaseID != 1 {
			t.Fatalf("second %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("revoke timeout")
	}
}

func TestLeaseIDsAreScopedToCard(t *testing.T) {
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
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c := protocolClient(t, s, dir)
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() }) // registered before server cleanup; closed afterwards
	for i, card := range []string{"card-one", "card-two"} {
		events <- ports.LeaseConnectors{Card: card, Device: f, Connectors: []ports.LeaseConnector{{Card: card, Name: fmt.Sprintf("DP-%d", i+1), ConnectorID: uint32(i + 40)}}}
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, g := range c.Registry().GetGlobals() {
			if g.Interface == "wp_drm_lease_device_v1" {
				count++
			}
		}
		if count == 2 {
			break
		}
	}
	var devices []uint32
	for _, g := range c.Registry().GetGlobals() {
		if g.Interface == "wp_drm_lease_device_v1" {
			id, err := c.Registry().BindID(g.Name, g.Interface, 1)
			if err != nil {
				t.Fatal(err)
			}
			devices = append(devices, id)
		}
	}
	if len(devices) != 2 {
		t.Fatalf("device globals: %d", len(devices))
	}
	var bound []*leaseDeviceEvents
	for _, dev := range devices {
		p := &leaseDeviceEvents{c: c}
		p.SetID(dev)
		c.Context().Register(p)
		bound = append(bound, p)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for i, dev := range devices {
		p := bound[i]
		if len(p.connectors) != 1 || len(p.fds) != 1 {
			t.Fatalf("device %d: %+v", dev, p)
		}
		defer unix.Close(p.fds[0])
		req := c.AllocateID()
		registerProtocol(t, c, req)
		requestProtocol(t, c, dev, dl.WpDrmLeaseDeviceV1RequestCreateLeaseRequest, req)
		requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestRequestConnector, p.connectors[0])
		id := c.AllocateID()
		l := &leaseResultEvents{}
		l.SetID(id)
		c.Context().Register(l)
		requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestSubmit, id)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	first := (<-requests).(ports.LeaseRequest)
	second := (<-requests).(ports.LeaseRequest)
	if first.Card == second.Card {
		t.Fatalf("same card: %v %v", first, second)
	}
	for _, req := range []ports.LeaseRequest{first, second} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		events <- ports.LeaseReply{ID: req.ID, FD: w, LeaseID: 7}
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		count := 0
		s.display.Do(func() { count = len(s.activeLeases) })
		if count == 2 {
			break
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if !s.display.Do(func() {
		if len(s.activeLeases) != 2 {
			t.Errorf("active leases %v", s.activeLeases)
		}
	}) {
		t.Fatal("display stopped")
	}
	events <- ports.LeaseFinished{Card: first.Card, LeaseID: 7}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		count := 0
		s.display.Do(func() { count = len(s.activeLeases) })
		if count == 1 {
			break
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if !s.display.Do(func() {
		if len(s.activeLeases) != 1 || s.activeLeases[leaseKey{second.Card, 7}] == nil {
			t.Errorf("wrong card finished: %v", s.activeLeases)
		}
	}) {
		t.Fatal("display stopped")
	}
}

func TestLeaseDestroyPendingRevokesLateReply(t *testing.T) {
	c, requests, events, _, s := leaseServerWithState(t)
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
	registerProtocol(t, c, lease)
	requestProtocol(t, c, req, dl.WpDrmLeaseRequestV1RequestSubmit, lease)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var sent ports.LeaseRequest
	select {
	case msg := <-requests:
		sent = msg.(ports.LeaseRequest)
	case <-time.After(time.Second):
		t.Fatal("no lease request")
	}
	requestProtocol(t, c, lease, dl.WpDrmLeaseV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() {
		if len(s.pendingLeases) != 0 || s.pendingLeaseCards[sent.ID] != sent.Card {
			t.Errorf("pending tombstone: %v %v", s.pendingLeases, s.pendingLeaseCards)
		}
	}) {
		t.Fatal("display stopped")
	}
	if !safelyLeaseMaps(t, c, events, requests, sent) {
		t.Fatal("pending reply not revoked")
	}
	if !s.display.Do(func() {
		if len(s.pendingLeases) != 0 || len(s.pendingLeaseCards) != 0 {
			t.Errorf("pending leaked: %v %v", s.pendingLeases, s.pendingLeaseCards)
		}
	}) {
		t.Fatal("display stopped")
	}
}

// A destroyed lease keeps only the card tombstone until the DRM reply arrives.
func safelyLeaseMaps(t *testing.T, c *wlturbo.Display, events chan ports.LeaseMessage, requests chan ports.LeaseMessage, sent ports.LeaseRequest) bool {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	events <- ports.LeaseReply{ID: sent.ID, FD: w, LeaseID: 51}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-requests:
			revoked, ok := event.(ports.LeaseRevoke)
			return ok && revoked.Card == sent.Card && revoked.LeaseID == 51
		default:
		}
	}
	return false
}

func TestLeaseGlobalRemoveAndReadd(t *testing.T) {
	c, _, events, f := leaseServer(t)
	count := func() int {
		n := 0
		for _, g := range c.Registry().GetGlobals() {
			if g.Interface == "wp_drm_lease_device_v1" {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("initial globals %d", count())
	}
	events <- ports.LeaseConnectors{Card: "/dev/dri/card-test"}
	for deadline := time.Now().Add(2 * time.Second); count() != 0 && time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 0 {
		t.Fatal("lease global not removed")
	}
	events <- ports.LeaseConnectors{Card: "/dev/dri/card-test", Device: f, Connectors: []ports.LeaseConnector{{Card: "/dev/dri/card-test", Name: "DP-1", ConnectorID: 42}}}
	for deadline := time.Now().Add(2 * time.Second); count() != 1 && time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatal("lease global not re-added")
	}
}
