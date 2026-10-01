package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/serverdecoration"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

type kdeModeProxy struct {
	wlturbo.BaseProxy
	modes chan uint32
}

func (p *kdeModeProxy) Dispatch(e *wlturbo.Event) { p.modes <- e.Uint32() }

// KDE decoration defaults to server-side and acknowledges the mode a client
// asks for: answering server to Firefox's client request made it ask again
// in a tight loop.
func TestKDEDecorationAcknowledgesMode(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{})
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	mgr := bindProtocol(t, c, "org_kde_kwin_server_decoration_manager")
	registerProtocol(t, c, mgr)
	surf := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	deco := c.AllocateID()
	proxy := &kdeModeProxy{modes: make(chan uint32, 8)}
	proxy.SetID(deco)
	registerWireProxy(c, proxy)
	requestProtocol(t, c, mgr, serverdecoration.OrgKdeKwinServerDecorationManagerRequestCreate, deco, surf)
	requestProtocol(t, c, deco, serverdecoration.OrgKdeKwinServerDecorationRequestRequestMode, uint32(serverdecoration.OrgKdeKwinServerDecorationModeClient))
	// An undefined mode gets no answer.
	requestProtocol(t, c, deco, serverdecoration.OrgKdeKwinServerDecorationRequestRequestMode, uint32(3))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []serverdecoration.OrgKdeKwinServerDecorationMode{serverdecoration.OrgKdeKwinServerDecorationModeServer, serverdecoration.OrgKdeKwinServerDecorationModeClient} {
		select {
		case got := <-proxy.modes:
			if got != uint32(want) {
				t.Fatalf("mode %d want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no mode %d", want)
		}
	}
	select {
	case got := <-proxy.modes:
		t.Fatalf("extra mode %d", got)
	default:
	}
}
