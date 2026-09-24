package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgoutput"
	"github.com/bnema/purego-libwayland/server"
)

type xdgOutputManager struct {
	server *Server
	info   OutputInfo
}

func registerXDGOutput(d *server.Display, o Options, s *Server) error {
	return xdgoutput.NewZxdgOutputManagerV1Global(d, 3, func(c server.Client, v, id uint32) {
		_, _ = xdgoutput.NewZxdgOutputManagerV1(c, int32(v), id, xdgOutputManager{s, o.output()})
	})
}
func (xdgOutputManager) Destroy(*xdgoutput.ZxdgOutputManagerV1) {}
func (m xdgOutputManager) GetXdgOutput(r *xdgoutput.ZxdgOutputManagerV1, id uint32, output *wayland.Output) {
	x, err := xdgoutput.NewZxdgOutputV1(r.Client(), r.Version(), id, xdgOutput{})
	if err != nil {
		return
	}
	st := &m.server.scale
	st.xdgOutputs = append(st.xdgOutputs, x)
	x.OnDestroy = func() { st.xdgOutputs = removeItem(st.xdgOutputs, x) }
	x.SendLogicalPosition(0, 0)
	x.SendLogicalSize(int32(st.width), int32(st.height))
	if r.Version() >= 2 {
		x.SendName(m.info.Name)
		x.SendDescription(m.info.Description)
	}
	if r.Version() < 3 || output == nil || output.Version() < 2 {
		x.SendDone()
	} else {
		output.SendDone()
	}
}

type xdgOutput struct{}

func (xdgOutput) Destroy(*xdgoutput.ZxdgOutputV1) {}
