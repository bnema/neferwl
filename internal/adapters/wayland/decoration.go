package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/serverdecoration"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgdecoration"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/purego-libwayland/server"
)

// Server-side decorations: neferwl draws the border, so clients must not draw
// their own title bar or shadow (which would shrink the visible content).
type decorationManager struct{ s *Server }

func registerDecoration(d *server.Display, s *Server) error {
	err := xdgdecoration.NewZxdgDecorationManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xdgdecoration.NewZxdgDecorationManagerV1(c, int32(v), id, decorationManager{s})
	})
	if err != nil {
		return err
	}
	// GTK only speaks KDE's older protocol: without it GTK4 draws its own
	// title bar.
	return serverdecoration.NewOrgKdeKwinServerDecorationManagerGlobal(d, 1, func(c server.Client, v, id uint32) {
		m, err := serverdecoration.NewOrgKdeKwinServerDecorationManager(c, int32(v), id, kdeManager{})
		if err == nil {
			m.SendDefaultMode(uint32(serverdecoration.OrgKdeKwinServerDecorationManagerModeServer))
		}
	})
}

type kdeManager struct{}

func (kdeManager) Create(r *serverdecoration.OrgKdeKwinServerDecorationManager, id uint32, _ *wayland.Surface) {
	if d, err := serverdecoration.NewOrgKdeKwinServerDecoration(r.Client(), r.Version(), id, kdeDecoration{}); err == nil {
		d.SendMode(uint32(serverdecoration.OrgKdeKwinServerDecorationModeServer))
	}
}

type kdeDecoration struct{}

func (kdeDecoration) Release(*serverdecoration.OrgKdeKwinServerDecoration) {}

// A requested mode is acknowledged as is: the protocol has no way to
// refuse, and answering server to a client that insists on client-side
// (Firefox) makes it ask again forever. The server default already makes
// GTK drop its title bar.
// Unknown modes are ignored: replacing them would restart the loop.
func (kdeDecoration) RequestMode(r *serverdecoration.OrgKdeKwinServerDecoration, mode uint32) {
	if mode > uint32(serverdecoration.OrgKdeKwinServerDecorationModeServer) {
		return
	}
	r.SendMode(mode)
}

func (decorationManager) Destroy(*xdgdecoration.ZxdgDecorationManagerV1) {}

func (m decorationManager) GetToplevelDecoration(r *xdgdecoration.ZxdgDecorationManagerV1, id uint32, t *xdgshell.Toplevel) {
	d := &decoration{s: m.s, toplevel: t}
	res, err := xdgdecoration.NewZxdgToplevelDecorationV1(r.Client(), r.Version(), id, d)
	if err != nil {
		return
	}
	d.send(res)
}

type decoration struct {
	s        *Server
	toplevel *xdgshell.Toplevel
}

func (*decoration) Destroy(*xdgdecoration.ZxdgToplevelDecorationV1) {}

// Any requested mode is answered with server-side.
func (d *decoration) SetMode(r *xdgdecoration.ZxdgToplevelDecorationV1, _ uint32) { d.send(r) }
func (d *decoration) UnsetMode(r *xdgdecoration.ZxdgToplevelDecorationV1)         { d.send(r) }

// send answers server-side. The mode applies on the next xdg_surface.configure,
// so a window already configured is configured again with its last size.
func (d *decoration) send(r *xdgdecoration.ZxdgToplevelDecorationV1) {
	r.SendConfigure(uint32(xdgdecoration.ZxdgToplevelDecorationV1ModeServerSide))
	for _, w := range d.s.windows {
		if w.toplevel == d.toplevel && w.hasLast && w.toplevel.Resource.Alive() && w.xdg.resource.Resource.Alive() {
			w.sendConfigure()
			return
		}
	}
}
