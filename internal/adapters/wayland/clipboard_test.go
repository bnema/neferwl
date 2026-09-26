package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/extdatacontrol"
	"github.com/bnema/purego-libwayland/protocol/primaryselection"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// clipEvents records the selection offers sent to a data device.
type clipEvents struct {
	wlturbo.BaseProxy
	c      *wlturbo.Display
	offers chan uint32 // selection offer ids; 0 clears
}

func (p *clipEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.DataDeviceEventDataOffer):
		// The new offer: register it so its offer events are accepted.
		registerOffer(p.c, e.Uint32())
	case uint16(wayland.DataDeviceEventSelection):
		p.offers <- e.Uint32()
	}
}

type sourceEvents struct {
	wlturbo.BaseProxy
	sends     chan int
	cancelled chan struct{}
}

func (p *sourceEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(wayland.DataSourceEventSend):
		_ = e.String()
		fd := int(e.Fd())
		_, _ = unix.Write(fd, []byte("pasted"))
		_ = unix.Close(fd)
		p.sends <- fd
	case uint16(wayland.DataSourceEventCancelled):
		p.cancelled <- struct{}{}
	}
}

func registerOffer(c *wlturbo.Display, id uint32) {
	p := &protocolProxy{}
	p.SetID(id)
	c.Context().Register(p)
}

// clipClient is a client with a toplevel, a data device and a pointer to
// its clipboard globals.
type clipClient struct {
	c       *wlturbo.Display
	manager uint32
	device  *clipEvents
	window  ports.WindowID
}

func newClipClient(t *testing.T, s *Server, dir string, events chan ports.ClientEvent) *clipClient {
	t.Helper()
	c := protocolClient(t, s, dir)
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	g, _ := c.Registry().FindGlobal("wl_data_device_manager")
	manager, err := c.Registry().BindID(g.Name, g.Interface, 3)
	if err != nil {
		t.Fatal(err)
	}
	device := &clipEvents{c: c, offers: make(chan uint32, 8)}
	device.SetID(c.AllocateID())
	c.Context().Register(device)
	requestProtocol(t, c, manager, wayland.DataDeviceManagerRequestGetDataDevice, device.ID(), seat)
	w := toplevelMapper(t, c, events)()
	return &clipClient{c: c, manager: manager, device: device, window: w.ID}
}

// selection returns the next selection offer id sent to the client.
func (cc *clipClient) selection(t *testing.T) (uint32, bool) {
	t.Helper()
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-cc.device.offers:
		return id, true
	case <-time.After(100 * time.Millisecond):
		return 0, false
	}
}

func (cc *clipClient) copy(t *testing.T) *sourceEvents {
	t.Helper()
	src := &sourceEvents{sends: make(chan int, 4), cancelled: make(chan struct{}, 4)}
	src.SetID(cc.c.AllocateID())
	cc.c.Context().Register(src)
	requestProtocol(t, cc.c, cc.manager, wayland.DataDeviceManagerRequestCreateDataSource, src.ID())
	requestProtocol(t, cc.c, src.ID(), wayland.DataSourceRequestOffer, "text/plain")
	requestProtocol(t, cc.c, cc.device.ID(), wayland.DataDeviceRequestSetSelection, src.ID(), uint32(0))
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return src
}

// focus gives cc keyboard focus and waits for the selection it gets then.
func (cc *clipClient) focus(t *testing.T, commands chan<- ports.ClientCommand) uint32 {
	t.Helper()
	commands <- ports.FocusWindow{ID: cc.window}
	deadline := time.After(2 * time.Second)
	for {
		if id, ok := cc.selection(t); ok {
			return id
		}
		select {
		case <-deadline:
			t.Fatal("no selection on focus")
		default:
		}
	}
}

// The clipboard follows keyboard focus: only the focused client may set
// it, and a client gets it when it gains focus; pasting reaches the source.
func TestClipboardFollowsFocus(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	a := newClipClient(t, s, dir, events)
	b := newClipClient(t, s, dir, events)
	a.focus(t, commands)

	// An unfocused client cannot set the selection: its source is cancelled.
	stolen := b.copy(t)
	select {
	case <-stolen.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("unfocused source not cancelled")
	}

	src := a.copy(t)
	offer, ok := a.selection(t)
	if !ok || offer == 0 {
		t.Fatalf("focused client got no offer: %d %v", offer, ok)
	}
	if id, ok := b.selection(t); ok {
		t.Fatalf("unfocused client got selection %d", id)
	}

	// Focus moves to b: b gets the offer and can paste from a.
	if offer = b.focus(t, commands); offer == 0 {
		t.Fatal("no offer on focus")
	}
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	if err := b.c.SendRequestWithFDs(offer, uint16(wayland.DataOfferRequestReceive), []int{fds[1]}, "text/plain"); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fds[1])
	if err := b.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// a answers the send event.
	deadline := time.After(2 * time.Second)
	for len(src.sends) == 0 {
		if err := a.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline:
			t.Fatal("source never asked to send")
		case <-time.After(5 * time.Millisecond):
		}
	}
	buf := make([]byte, 16)
	n, _ := unix.Read(fds[0], buf)
	if string(buf[:n]) != "pasted" {
		t.Fatalf("pasted %q", buf[:n])
	}
}

// A clipboard manager (ext_data_control_v1) sees the selection without
// focus, and a destroyed source clears it.
func TestDataControlSeesSelection(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	a := newClipClient(t, s, dir, events)
	a.focus(t, commands)

	m := protocolClient(t, s, dir)
	seat := bindProtocol(t, m, "wl_seat")
	registerProtocol(t, m, seat)
	manager := bindProtocol(t, m, "ext_data_control_manager_v1")
	offers := make(chan uint32, 8)
	dev := &controlEvents{c: m, offers: offers}
	dev.SetID(m.AllocateID())
	m.Context().Register(dev)
	requestProtocol(t, m, manager, extdatacontrol.ExtDataControlManagerV1RequestGetDataDevice, dev.ID(), seat)
	next := func() uint32 {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			if err := m.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case id := <-offers:
				return id
			case <-deadline:
				t.Fatal("no selection event")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	if id := next(); id != 0 {
		t.Fatalf("initial selection %d, want none", id)
	}
	src := a.copy(t)
	if id := next(); id == 0 {
		t.Fatal("manager did not see the copy")
	}
	requestProtocol(t, a.c, src.ID(), wayland.DataSourceRequestDestroy)
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if id := next(); id != 0 {
		t.Fatalf("selection %d after its source died", id)
	}
}

type controlEvents struct {
	wlturbo.BaseProxy
	c      *wlturbo.Display
	offers chan uint32
}

func (p *controlEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(extdatacontrol.ExtDataControlDeviceV1EventDataOffer):
		registerOffer(p.c, e.Uint32())
	case uint16(extdatacontrol.ExtDataControlDeviceV1EventSelection):
		p.offers <- e.Uint32()
	}
}

// A new copy cancels the previous source; setting the current source
// again changes nothing.
func TestClipboardReplaceCancelsOldSource(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	a := newClipClient(t, s, dir, events)
	a.focus(t, commands)
	first := a.copy(t)
	if id, ok := a.selection(t); !ok || id == 0 {
		t.Fatal("no offer for the first copy")
	}
	requestProtocol(t, a.c, a.device.ID(), wayland.DataDeviceRequestSetSelection, first.ID(), uint32(0))
	if id, ok := a.selection(t); ok {
		t.Fatalf("setting the same source again sent offer %d", id)
	}
	if len(first.cancelled) != 0 {
		t.Fatal("current source cancelled when set again")
	}
	a.copy(t)
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("replaced source not cancelled")
	}
}

// A data-control source can be set once.
func TestDataControlUsedSource(t *testing.T) {
	s, _, _, dir := keyboardServer(t)
	m := protocolClient(t, s, dir)
	seat := bindProtocol(t, m, "wl_seat")
	registerProtocol(t, m, seat)
	manager := bindProtocol(t, m, "ext_data_control_manager_v1")
	dev := &controlEvents{c: m, offers: make(chan uint32, 8)}
	dev.SetID(m.AllocateID())
	m.Context().Register(dev)
	requestProtocol(t, m, manager, extdatacontrol.ExtDataControlManagerV1RequestGetDataDevice, dev.ID(), seat)
	src := m.AllocateID()
	registerProtocol(t, m, src)
	requestProtocol(t, m, manager, extdatacontrol.ExtDataControlManagerV1RequestCreateDataSource, src)
	requestProtocol(t, m, src, extdatacontrol.ExtDataControlSourceV1RequestOffer, "text/plain")
	requestProtocol(t, m, dev.ID(), extdatacontrol.ExtDataControlDeviceV1RequestSetSelection, src)
	requestProtocol(t, m, dev.ID(), extdatacontrol.ExtDataControlDeviceV1RequestSetPrimarySelection, src)
	expectProtocolError(t, m, dev.ID(), uint32(extdatacontrol.ExtDataControlDeviceV1ErrorUsedSource))
}

type primaryEvents struct {
	wlturbo.BaseProxy
	c      *wlturbo.Display
	offers chan uint32
}

func (p *primaryEvents) Dispatch(e *wlturbo.Event) {
	switch e.Opcode {
	case uint16(primaryselection.ZwpPrimarySelectionDeviceV1EventDataOffer):
		registerOffer(p.c, e.Uint32())
	case uint16(primaryselection.ZwpPrimarySelectionDeviceV1EventSelection):
		p.offers <- e.Uint32()
	}
}

// A clipboard manager setting the primary selection reaches the focused
// client's primary selection device.
func TestPrimarySelectionFromManager(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	a := newClipClient(t, s, dir, events)
	pm := bindProtocol(t, a.c, "zwp_primary_selection_device_manager_v1")
	seat := bindProtocol(t, a.c, "wl_seat")
	registerProtocol(t, a.c, seat)
	pdev := &primaryEvents{c: a.c, offers: make(chan uint32, 8)}
	pdev.SetID(a.c.AllocateID())
	a.c.Context().Register(pdev)
	requestProtocol(t, a.c, pm, primaryselection.ZwpPrimarySelectionDeviceManagerV1RequestGetDevice, pdev.ID(), seat)
	a.focus(t, commands)

	m := protocolClient(t, s, dir)
	mseat := bindProtocol(t, m, "wl_seat")
	registerProtocol(t, m, mseat)
	manager := bindProtocol(t, m, "ext_data_control_manager_v1")
	dev := &controlEvents{c: m, offers: make(chan uint32, 8)}
	dev.SetID(m.AllocateID())
	m.Context().Register(dev)
	requestProtocol(t, m, manager, extdatacontrol.ExtDataControlManagerV1RequestGetDataDevice, dev.ID(), mseat)
	src := m.AllocateID()
	registerProtocol(t, m, src)
	requestProtocol(t, m, manager, extdatacontrol.ExtDataControlManagerV1RequestCreateDataSource, src)
	requestProtocol(t, m, src, extdatacontrol.ExtDataControlSourceV1RequestOffer, "text/plain")
	requestProtocol(t, m, dev.ID(), extdatacontrol.ExtDataControlDeviceV1RequestSetPrimarySelection, src)
	if err := m.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if err := a.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case id := <-pdev.offers:
			if id != 0 {
				return
			}
		case <-deadline:
			t.Fatal("focused client got no primary selection")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
