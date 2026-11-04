package wayland

import (
	"time"

	"bnema.dev/purego-libwayland/server"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/wlrvirtualpointer"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// virtualPointerDevices is the first device identity of virtual pointers;
// real producers count up from 1 and never reach it.
const virtualPointerDevices domain.InputDeviceID = 1 << 62

// virtualPointerInterface is the manager global, hidden from sandboxed clients.
const virtualPointerInterface = "zwlr_virtual_pointer_manager_v1"

// virtualPointer is a zwlr_virtual_pointer_v1 (wlrctl, ydotool, agents).
// Requests accumulate until frame, then go to core as one batch of
// ports.VirtualPointerInput: motion first, then buttons, then scroll. Core
// places the pointer and routes the events as real input; the adapter
// only translates. Held buttons are released when the resource dies.
type virtualPointer struct {
	server *Server
	device domain.InputDeviceID
	output string // create_virtual_pointer_with_output, "" for the layout
	// pending frame
	moved      bool
	absolute   bool
	dx, dy     float64
	fx, fy     float64
	motionTime time.Duration
	buttons    []ports.PointerButton
	axis       ports.PointerAxis
	scrolled   bool
	pressed    map[uint32]bool
	generation domain.LockGeneration
}

// registerVirtualPointer exposes the global to unsandboxed clients
// (globalVisible); a sandboxed bind is refused anyway.
func registerVirtualPointer(d *server.Display, s *Server) error {
	return wlrvirtualpointer.NewZwlrVirtualPointerManagerV1Global(d, 2, func(c server.Client, v, id uint32) {
		_, _ = wlrvirtualpointer.NewZwlrVirtualPointerManagerV1(c, int32(v), id, virtualPointerManager{s})
	})
}

type virtualPointerManager struct{ server *Server }

func (virtualPointerManager) Destroy(*wlrvirtualpointer.ZwlrVirtualPointerManagerV1) {}

func (m virtualPointerManager) CreateVirtualPointer(r *wlrvirtualpointer.ZwlrVirtualPointerManagerV1, seat *wayland.Seat, id uint32) {
	m.CreateVirtualPointerWithOutput(r, seat, nil, id)
}

func (m virtualPointerManager) CreateVirtualPointerWithOutput(r *wlrvirtualpointer.ZwlrVirtualPointerManagerV1, _ *wayland.Seat, output *wayland.Output, id uint32) {
	s := m.server
	p := &virtualPointer{server: s, pressed: map[uint32]bool{}}
	if o := s.outputOf(output); o != nil {
		p.output = o.name()
	}
	res, err := wlrvirtualpointer.NewZwlrVirtualPointerV1(r.Client(), r.Version(), id, p)
	if err != nil {
		return
	}
	if s.sandboxed[r.Client()] != nil {
		// Inert: a sandboxed client never drives the seat.
		p.server = nil
		s.log.Info().Int("pid", r.Client().PID()).Msg("virtual pointer refused: sandboxed client")
		return
	}
	s.nextVirtualPointer++
	p.device = virtualPointerDevices + s.nextVirtualPointer
	p.generation = s.securityState().Generation
	res.OnDestroy = p.release
	s.log.Info().Int("pid", r.Client().PID()).Str("output", p.output).Msg("virtual pointer created")
}

func (s *Server) securityState() domain.SecurityState {
	if s.security == nil {
		return domain.SecurityState{}
	}
	return s.security.Snapshot()
}

// admitted drops the pending frame and held buttons when protection began
// or passed since the last request: nothing from before a lock is replayed.
func (p *virtualPointer) admitted() bool {
	if p.server == nil {
		return false
	}
	state := p.server.securityState()
	if state.Protected || state.Generation != p.generation {
		p.reset()
		clear(p.pressed)
		p.generation = state.Generation
	}
	return !state.Protected
}

func (p *virtualPointer) reset() {
	p.moved, p.absolute, p.dx, p.dy, p.fx, p.fy = false, false, 0, 0, 0, 0
	p.buttons = p.buttons[:0]
	p.axis, p.scrolled = ports.PointerAxis{}, false
}

func msec(t uint32) time.Duration { return time.Duration(t) * time.Millisecond }

func (p *virtualPointer) Motion(_ *wlrvirtualpointer.ZwlrVirtualPointerV1, t uint32, dx, dy server.Fixed) {
	if !p.admitted() {
		return
	}
	if p.absolute {
		// Relative after absolute in one frame: the absolute point is
		// flushed first so the delta applies from there.
		p.flush()
	}
	p.moved = true
	p.dx += dx.Float()
	p.dy += dy.Float()
	p.motionTime = msec(t)
}

func (p *virtualPointer) MotionAbsolute(_ *wlrvirtualpointer.ZwlrVirtualPointerV1, t, x, y, xExtent, yExtent uint32) {
	if !p.admitted() || xExtent == 0 || yExtent == 0 {
		return
	}
	if p.moved && !p.absolute {
		p.flush()
	}
	p.moved, p.absolute = true, true
	p.fx = min(float64(x), float64(xExtent-1)) / float64(xExtent)
	p.fy = min(float64(y), float64(yExtent-1)) / float64(yExtent)
	p.motionTime = msec(t)
}

func (p *virtualPointer) Button(_ *wlrvirtualpointer.ZwlrVirtualPointerV1, t, button, state uint32) {
	if !p.admitted() {
		return
	}
	pressed := state == uint32(wayland.PointerButtonStatePressed)
	if !pressed && !p.pressed[button] {
		return
	}
	if pressed {
		p.pressed[button] = true
	} else {
		delete(p.pressed, button)
	}
	p.buttons = append(p.buttons, ports.PointerButton{Device: p.device, Button: button, Pressed: pressed, Time: msec(t)})
}

// scrollAxis is the frame's axis, or nil after an invalid_axis error.
func (p *virtualPointer) scrollAxis(r *wlrvirtualpointer.ZwlrVirtualPointerV1, axis uint32) *domain.ScrollAxis {
	switch axis {
	case uint32(wayland.PointerAxisVerticalScroll):
		return &p.axis.Vertical
	case uint32(wayland.PointerAxisHorizontalScroll):
		return &p.axis.Horizontal
	}
	r.PostError(uint32(wlrvirtualpointer.ZwlrVirtualPointerV1ErrorInvalidAxis), "invalid axis")
	return nil
}

func (p *virtualPointer) Axis(r *wlrvirtualpointer.ZwlrVirtualPointerV1, t, axis uint32, value server.Fixed) {
	a := p.scrollAxis(r, axis)
	if a == nil || !p.admitted() {
		return
	}
	a.Set = true
	a.Value += value.Float()
	p.axis.Time, p.scrolled = msec(t), true
}

func (p *virtualPointer) AxisDiscrete(r *wlrvirtualpointer.ZwlrVirtualPointerV1, t, axis uint32, value server.Fixed, discrete int32) {
	a := p.scrollAxis(r, axis)
	if a == nil || !p.admitted() {
		return
	}
	a.Set = true
	a.Value += value.Float()
	a.V120 += discrete * 120
	p.axis.Time, p.scrolled = msec(t), true
}

func (p *virtualPointer) AxisStop(r *wlrvirtualpointer.ZwlrVirtualPointerV1, t, axis uint32) {
	a := p.scrollAxis(r, axis)
	if a == nil || !p.admitted() {
		return
	}
	a.Set, a.Stop = true, true
	p.axis.Time, p.scrolled = msec(t), true
}

func (p *virtualPointer) AxisSource(r *wlrvirtualpointer.ZwlrVirtualPointerV1, source uint32) {
	switch source {
	case uint32(wayland.PointerAxisSourceWheel), uint32(wayland.PointerAxisSourceWheelTilt):
		p.axis.Source = domain.AxisWheel
	case uint32(wayland.PointerAxisSourceFinger):
		p.axis.Source = domain.AxisFinger
	case uint32(wayland.PointerAxisSourceContinuous):
		p.axis.Source = domain.AxisContinuous
	default:
		r.PostError(uint32(wlrvirtualpointer.ZwlrVirtualPointerV1ErrorInvalidAxisSource), "invalid axis source")
	}
}

func (p *virtualPointer) Frame(*wlrvirtualpointer.ZwlrVirtualPointerV1) {
	if p.admitted() {
		p.flush()
	}
}

// flush hands the pending frame to core and clears it.
func (p *virtualPointer) flush() {
	s := p.server
	state := s.securityState()
	send := func(v ports.VirtualPointerInput) {
		v.State = state
		s.emit(v)
	}
	if p.moved {
		m := ports.PointerMotion{Device: p.device, Time: p.motionTime}
		v := ports.VirtualPointerInput{Absolute: p.absolute, Output: p.output}
		if p.absolute {
			v.FX, v.FY = p.fx, p.fy
		} else {
			m.DX, m.DY, m.UnaccelDX, m.UnaccelDY = p.dx, p.dy, p.dx, p.dy
		}
		v.Event = m
		send(v)
	}
	for _, b := range p.buttons {
		send(ports.VirtualPointerInput{Event: b})
	}
	if p.scrolled {
		a := p.axis
		a.Device = p.device
		send(ports.VirtualPointerInput{Event: a})
	}
	source := p.axis.Source
	p.reset()
	p.axis.Source = source
}

func (*virtualPointer) Destroy(*wlrvirtualpointer.ZwlrVirtualPointerV1) {}

// release runs when the resource dies, by request or disconnect: buttons
// still held in this epoch are released so no client keeps a stuck drag.
func (p *virtualPointer) release() {
	if !p.admitted() || len(p.pressed) == 0 {
		return
	}
	p.reset()
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	now := time.Duration(ts.Nano())
	for b := range p.pressed {
		p.buttons = append(p.buttons, ports.PointerButton{Device: p.device, Button: b, Time: now})
	}
	clear(p.pressed)
	p.flush()
}
