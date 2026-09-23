package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

func registerGlobals(d *server.Display, o Options) error {
	for _, register := range []func() error{
		func() error {
			return wayland.NewCompositorGlobal(d, 6, func(c server.Client, v, id uint32) { wayland.NewCompositor(c, int32(v), id, compositor{}) })
		},
		func() error {
			return wayland.NewShmGlobal(d, 1, func(c server.Client, v, id uint32) {
				r, e := wayland.NewShm(c, int32(v), id, shm{})
				if e == nil {
					r.SendFormat(0)
					r.SendFormat(1)
				}
			})
		},
		func() error {
			return wayland.NewOutputGlobal(d, 4, func(c server.Client, v, id uint32) {
				r, e := wayland.NewOutput(c, int32(v), id, output{})
				if e != nil {
					return
				}
				r.SendGeometry(0, 0, 0, 0, 0, "nefertty", "headless", 0)
				r.SendMode(3, int32(o.OutputWidth), int32(o.OutputHeight), 60000)
				if v >= 2 {
					r.SendScale(1)
				}
				if v >= 4 {
					r.SendName("HEADLESS-1")
					r.SendDescription("NeferTTY headless output")
				}
				if v >= 2 {
					r.SendDone()
				}
			})
		},
		func() error {
			return wayland.NewSeatGlobal(d, 7, func(c server.Client, v, id uint32) {
				r, e := wayland.NewSeat(c, int32(v), id, seat{})
				if e == nil {
					r.SendCapabilities(0)
					if v >= 2 {
						r.SendName("seat0")
					}
				}
			})
		},
		func() error {
			return wayland.NewDataDeviceManagerGlobal(d, 3, func(c server.Client, v, id uint32) { wayland.NewDataDeviceManager(c, int32(v), id, dataManager{}) })
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

type compositor struct{}

func (compositor) CreateSurface(r *wayland.Compositor, id uint32) {
	wayland.NewSurface(r.Client(), r.Version(), id, surface{})
}
func (compositor) CreateRegion(r *wayland.Compositor, id uint32) {
	wayland.NewRegion(r.Client(), r.Version(), id, region{})
}
func (compositor) Release(*wayland.Compositor) {}

type surface struct{}

func (surface) Destroy(*wayland.Surface)                                  {}
func (surface) Attach(*wayland.Surface, *wayland.Buffer, int32, int32)    {}
func (surface) Damage(*wayland.Surface, int32, int32, int32, int32)       {}
func (surface) Frame(*wayland.Surface, uint32)                            {}
func (surface) SetOpaqueRegion(*wayland.Surface, *wayland.Region)         {}
func (surface) SetInputRegion(*wayland.Surface, *wayland.Region)          {}
func (surface) Commit(*wayland.Surface)                                   {}
func (surface) SetBufferTransform(*wayland.Surface, int32)                {}
func (surface) SetBufferScale(*wayland.Surface, int32)                    {}
func (surface) DamageBuffer(*wayland.Surface, int32, int32, int32, int32) {}
func (surface) Offset(*wayland.Surface, int32, int32)                     {}
func (surface) GetRelease(*wayland.Surface, uint32)                       {}

type region struct{}

func (region) Destroy(*wayland.Region)                              {}
func (region) Add(*wayland.Region, int32, int32, int32, int32)      {}
func (region) Subtract(*wayland.Region, int32, int32, int32, int32) {}

type shm struct{}

func (shm) CreatePool(r *wayland.Shm, id uint32, fd int, size int32) {
	p, e := wayland.NewShmPool(r.Client(), 1, id, &pool{fd: fd, size: size})
	if e != nil {
		unix.Close(fd)
	} else {
		p.OnDestroy = func() { unix.Close(fd) }
	}
}
func (shm) Release(*wayland.Shm) {}

type pool struct {
	fd   int
	size int32
}

func (p *pool) CreateBuffer(r *wayland.ShmPool, id uint32, _ int32, _ int32, _ int32, _ int32, _ uint32) {
	wayland.NewBuffer(r.Client(), 1, id, buffer{})
}
func (p *pool) Destroy(*wayland.ShmPool)              {}
func (p *pool) Resize(_ *wayland.ShmPool, size int32) { p.size = size }

type buffer struct{}

func (buffer) Destroy(*wayland.Buffer) {}

type output struct{}

func (output) Release(*wayland.Output) {}

type seat struct{}

func (seat) GetPointer(r *wayland.Seat, id uint32) {
	wayland.NewPointer(r.Client(), r.Version(), id, pointer{})
}
func (seat) GetKeyboard(r *wayland.Seat, id uint32) {
	wayland.NewKeyboard(r.Client(), r.Version(), id, keyboard{})
}
func (seat) GetTouch(r *wayland.Seat, id uint32) {
	wayland.NewTouch(r.Client(), r.Version(), id, touch{})
}
func (seat) Release(*wayland.Seat) {}

type pointer struct{}

func (pointer) SetCursor(*wayland.Pointer, uint32, *wayland.Surface, int32, int32) {}
func (pointer) Release(*wayland.Pointer)                                           {}

type keyboard struct{}

func (keyboard) Release(*wayland.Keyboard) {}

type touch struct{}

func (touch) Release(*wayland.Touch) {}

type dataManager struct{}

func (dataManager) CreateDataSource(r *wayland.DataDeviceManager, id uint32) {
	wayland.NewDataSource(r.Client(), r.Version(), id, dataSource{})
}
func (dataManager) GetDataDevice(r *wayland.DataDeviceManager, id uint32, _ *wayland.Seat) {
	wayland.NewDataDevice(r.Client(), r.Version(), id, dataDevice{})
}
func (dataManager) Release(*wayland.DataDeviceManager) {}

type dataSource struct{}

func (dataSource) Offer(*wayland.DataSource, string)      {}
func (dataSource) Destroy(*wayland.DataSource)            {}
func (dataSource) SetActions(*wayland.DataSource, uint32) {}

type dataDevice struct{}

func (dataDevice) StartDrag(*wayland.DataDevice, *wayland.DataSource, *wayland.Surface, *wayland.Surface, uint32) {
}
func (dataDevice) SetSelection(*wayland.DataDevice, *wayland.DataSource, uint32) {}
func (dataDevice) Release(*wayland.DataDevice)                                   {}
