package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgoutput"
	"github.com/bnema/purego-libwayland/server"
)

type xdgOutputManager struct{ width, height int32 }

func registerXDGOutput(d *server.Display, o Options) error {
	return xdgoutput.NewZxdgOutputManagerV1Global(d, 3, func(c server.Client, v, id uint32) {
		_, _ = xdgoutput.NewZxdgOutputManagerV1(c, int32(v), id, xdgOutputManager{int32(o.OutputWidth), int32(o.OutputHeight)})
	})
}
func (xdgOutputManager) Destroy(*xdgoutput.ZxdgOutputManagerV1) {}
func (m xdgOutputManager) GetXdgOutput(r *xdgoutput.ZxdgOutputManagerV1, id uint32, output *wayland.Output) {
	x, err := xdgoutput.NewZxdgOutputV1(r.Client(), r.Version(), id, xdgOutput{})
	if err != nil {
		return
	}
	x.SendLogicalPosition(0, 0)
	x.SendLogicalSize(m.width, m.height)
	if r.Version() >= 2 {
		x.SendName("HEADLESS-1")
		x.SendDescription("NeferTTY headless output")
	}
	if r.Version() < 3 || output == nil || output.Version() < 2 {
		x.SendDone()
	} else {
		output.SendDone()
	}
}

type xdgOutput struct{}

func (xdgOutput) Destroy(*xdgoutput.ZxdgOutputV1) {}
