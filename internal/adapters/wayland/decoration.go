package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/xdgdecoration"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
)

// Server-side decorations: nefertty draws the border, so clients must not draw
// their own title bar or shadow (which would shrink the visible content).
type decorationManager struct{}

func registerDecoration(d *server.Display) error {
	return xdgdecoration.NewZxdgDecorationManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xdgdecoration.NewZxdgDecorationManagerV1(c, int32(v), id, decorationManager{})
	})
}

func (decorationManager) Destroy(*xdgdecoration.ZxdgDecorationManagerV1) {}

func (decorationManager) GetToplevelDecoration(r *xdgdecoration.ZxdgDecorationManagerV1, id uint32, _ *xdgshell.Toplevel) {
	d, err := xdgdecoration.NewZxdgToplevelDecorationV1(r.Client(), r.Version(), id, decoration{})
	if err != nil {
		return
	}
	// Sent before the toplevel's configure, which the client waits for.
	d.SendConfigure(uint32(xdgdecoration.ZxdgToplevelDecorationV1ModeServerSide))
}

type decoration struct{}

func (decoration) Destroy(*xdgdecoration.ZxdgToplevelDecorationV1) {}

// Any requested mode is answered with server-side.
func (decoration) SetMode(r *xdgdecoration.ZxdgToplevelDecorationV1, _ uint32) {
	r.SendConfigure(uint32(xdgdecoration.ZxdgToplevelDecorationV1ModeServerSide))
}

func (decoration) UnsetMode(r *xdgdecoration.ZxdgToplevelDecorationV1) {
	r.SendConfigure(uint32(xdgdecoration.ZxdgToplevelDecorationV1ModeServerSide))
}
