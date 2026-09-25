package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/kdedecoration"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgdecoration"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
)

// Server-side decorations: nefertty draws the border, so clients must not draw
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
	return kdedecoration.NewOrgKdeKwinServerDecorationManagerGlobal(d, 1, func(c server.Client, v, id uint32) {
		m, err := kdedecoration.NewOrgKdeKwinServerDecorationManager(c, int32(v), id, kdeManager{})
		if err == nil {
			m.SendDefaultMode(uint32(kdedecoration.OrgKdeKwinServerDecorationManagerModeServer))
		}
	})
}

type kdeManager struct{}

func (kdeManager) Create(r *kdedecoration.OrgKdeKwinServerDecorationManager, id uint32, _ *wayland.Surface) {
	if d, err := kdedecoration.NewOrgKdeKwinServerDecoration(r.Client(), r.Version(), id, kdeDecoration{}); err == nil {
		d.SendMode(uint32(kdedecoration.OrgKdeKwinServerDecorationModeServer))
	}
}

type kdeDecoration struct{}

func (kdeDecoration) Release(*kdedecoration.OrgKdeKwinServerDecoration) {}

// Any requested mode is answered with server-side, like xdg-decoration.
func (kdeDecoration) RequestMode(r *kdedecoration.OrgKdeKwinServerDecoration, _ uint32) {
	r.SendMode(uint32(kdedecoration.OrgKdeKwinServerDecorationModeServer))
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
