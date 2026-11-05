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

// maxVirtualFrame bounds the requests one frame may hold: a client that
// never sends frame cannot grow compositor memory. Exceeding it is a
// wl_display implementation error and disconnects the client.
const maxVirtualFrame = 256

// maxVirtualHeld bounds the buttons one virtual pointer holds at once.
const maxVirtualHeld = 16

// virtualButtonValid reports whether b is an evdev button code
// (BTN_MISC 0x100 to BTN_GEAR_UP 0x151).
func virtualButtonValid(b uint32) bool { return b >= 0x100 && b <= 0x151 }

// virtualFrameKeep is the largest pending capacity kept across frames.
const virtualFrameKeep = 32

// virtualPointer is a zwlr_virtual_pointer_v1 (wlrctl, agents).
// Requests accumulate, in order, until frame; only then do they go to core
// as ports.VirtualPointerInput. Consecutive motions of one kind merge, as do
// consecutive axis requests. Core places the pointer and routes the events
// as real input; the adapter only translates.
//
// forwarded are the buttons whose press reached core in this epoch: when
// the resource dies they are released, and pending (unframed) requests are
// discarded.
type virtualPointer struct {
	server     *Server
	res        *wlrvirtualpointer.ZwlrVirtualPointerV1
	device     domain.InputDeviceID
	output     string // create_virtual_pointer_with_output, "" for the layout
	pending    []ports.VirtualPointerInput
	source     domain.AxisSource // axis_source of the frame, sticky
	forwarded  map[uint32]bool
	generation domain.LockGeneration
	dead       bool // protocol error sent: requests are ignored
	warned     bool // a dropped button was logged
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
	p := &virtualPointer{server: s, forwarded: map[uint32]bool{}}
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
	p.res = res
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

// admitted drops the pending frame and forwarded buttons when protection
// began or passed since the last request: nothing from before a lock is
// replayed or released into it.
func (p *virtualPointer) admitted() bool {
	if p.server == nil || p.dead {
		return false
	}
	state := p.server.securityState()
	if state.Protected || state.Generation != p.generation {
		p.reset()
		clear(p.forwarded)
		p.generation = state.Generation
	}
	return !state.Protected
}

// reset drops the pending frame; an oversized buffer is not kept.
func (p *virtualPointer) reset() {
	if cap(p.pending) > virtualFrameKeep {
		p.pending = nil
		return
	}
	clear(p.pending)
	p.pending = p.pending[:0]
}

func msec(t uint32) time.Duration { return time.Duration(t) * time.Millisecond }

// add appends one request to the frame, or reports false after a protocol
// error for a frame over maxVirtualFrame.
func (p *virtualPointer) add(v ports.VirtualPointerInput) bool {
	if len(p.pending) >= maxVirtualFrame {
		p.reset()
		p.pending = nil
		p.dead = true
		// The protocol has no error for it: a compositor limit is an
		// implementation error on wl_display.
		p.res.Client().PostImplementationError("zwlr_virtual_pointer_v1: frame holds more than 256 requests")
		return false
	}
	p.pending = append(p.pending, v)
	return true
}

// last is the newest pending request, or nil.
func (p *virtualPointer) last() *ports.VirtualPointerInput {
	if len(p.pending) == 0 {
		return nil
	}
	return &p.pending[len(p.pending)-1]
}

func (p *virtualPointer) Motion(_ *wlrvirtualpointer.ZwlrVirtualPointerV1, t uint32, dx, dy server.Fixed) {
	if !p.admitted() {
		return
	}
	if l := p.last(); l != nil {
		if m, ok := l.Event.(ports.PointerMotion); ok && !l.Absolute {
			m.DX += dx.Float()
			m.DY += dy.Float()
			m.UnaccelDX, m.UnaccelDY, m.Time = m.DX, m.DY, msec(t)
			l.Event = m
			return
		}
	}
	p.add(ports.VirtualPointerInput{Event: ports.PointerMotion{Device: p.device, DX: dx.Float(), DY: dy.Float(), UnaccelDX: dx.Float(), UnaccelDY: dy.Float(), Time: msec(t)}})
}

// MotionAbsolute maps x/x_extent into [0, 1]; x may equal the extent.
// Core clamps the point into the output or layout.
func (p *virtualPointer) MotionAbsolute(_ *wlrvirtualpointer.ZwlrVirtualPointerV1, t, x, y, xExtent, yExtent uint32) {
	if !p.admitted() || xExtent == 0 || yExtent == 0 {
		return
	}
	v := ports.VirtualPointerInput{
		Absolute: true, Output: p.output,
		FX:    float64(min(x, xExtent)) / float64(xExtent),
		FY:    float64(min(y, yExtent)) / float64(yExtent),
		Event: ports.PointerMotion{Device: p.device, Time: msec(t)},
	}
	if l := p.last(); l != nil && l.Absolute {
		*l = v
		return
	}
	p.add(v)
}

func (p *virtualPointer) Button(_ *wlrvirtualpointer.ZwlrVirtualPointerV1, t, button, state uint32) {
	if !p.admitted() {
		return
	}
	pressed := state == uint32(wayland.PointerButtonStatePressed)
	if !virtualButtonValid(button) {
		p.dropButton(button, "not an evdev button code")
		return
	}
	p.add(ports.VirtualPointerInput{Event: ports.PointerButton{Device: p.device, Button: button, Pressed: pressed, Time: msec(t)}})
}

// editAxis applies edit to the frame's axis request on axis, appending one
// unless the newest request is already a scroll. Bad axes are an
// invalid_axis error.
func (p *virtualPointer) editAxis(r *wlrvirtualpointer.ZwlrVirtualPointerV1, axis, t uint32, edit func(*domain.ScrollAxis)) {
	vertical := axis == uint32(wayland.PointerAxisVerticalScroll)
	if !vertical && axis != uint32(wayland.PointerAxisHorizontalScroll) {
		r.PostError(uint32(wlrvirtualpointer.ZwlrVirtualPointerV1ErrorInvalidAxis), "invalid axis")
		return
	}
	if !p.admitted() {
		return
	}
	l := p.last()
	if l == nil || !isAxis(l.Event) {
		if !p.add(ports.VirtualPointerInput{Event: ports.PointerAxis{Device: p.device}}) {
			return
		}
		l = p.last()
	}
	a := l.Event.(ports.PointerAxis)
	a.Time, a.Source = msec(t), p.source
	if vertical {
		edit(&a.Vertical)
	} else {
		edit(&a.Horizontal)
	}
	l.Event = a
}

func isAxis(ev ports.InputEvent) bool { _, ok := ev.(ports.PointerAxis); return ok }

func (p *virtualPointer) Axis(r *wlrvirtualpointer.ZwlrVirtualPointerV1, t, axis uint32, value server.Fixed) {
	p.editAxis(r, axis, t, func(a *domain.ScrollAxis) {
		a.Set = true
		a.Value += value.Float()
	})
}

func (p *virtualPointer) AxisDiscrete(r *wlrvirtualpointer.ZwlrVirtualPointerV1, t, axis uint32, value server.Fixed, discrete int32) {
	p.editAxis(r, axis, t, func(a *domain.ScrollAxis) {
		a.Set = true
		a.Value += value.Float()
		a.V120 += discrete * 120
	})
}

func (p *virtualPointer) AxisStop(r *wlrvirtualpointer.ZwlrVirtualPointerV1, t, axis uint32) {
	p.editAxis(r, axis, t, func(a *domain.ScrollAxis) { a.Set, a.Stop = true, true })
}

func (p *virtualPointer) AxisSource(r *wlrvirtualpointer.ZwlrVirtualPointerV1, source uint32) {
	switch source {
	case uint32(wayland.PointerAxisSourceWheel), uint32(wayland.PointerAxisSourceWheelTilt):
		p.source = domain.AxisWheel
	case uint32(wayland.PointerAxisSourceFinger):
		p.source = domain.AxisFinger
	case uint32(wayland.PointerAxisSourceContinuous):
		p.source = domain.AxisContinuous
	default:
		r.PostError(uint32(wlrvirtualpointer.ZwlrVirtualPointerV1ErrorInvalidAxisSource), "invalid axis source")
		return
	}
	if l := p.last(); l != nil && isAxis(l.Event) {
		a := l.Event.(ports.PointerAxis)
		a.Source = p.source
		l.Event = a
	}
}

// Frame hands the pending requests to core, in order. A release whose press
// was not forwarded in this epoch is dropped, as is a repeated press.
func (p *virtualPointer) Frame(*wlrvirtualpointer.ZwlrVirtualPointerV1) {
	if !p.admitted() {
		return
	}
	state := p.server.securityState()
	for _, v := range p.pending {
		if b, ok := v.Event.(ports.PointerButton); ok {
			if b.Pressed == p.forwarded[b.Button] {
				continue
			}
			if b.Pressed && len(p.forwarded) >= maxVirtualHeld {
				p.dropButton(b.Button, "too many held buttons")
				continue
			}
			if b.Pressed {
				p.forwarded[b.Button] = true
			} else {
				delete(p.forwarded, b.Button)
			}
		}
		v.State = state
		v.Output = p.output
		p.server.emit(v)
	}
	p.reset()
}

// dropButton ignores a press the pointer may not forward. The protocol has
// no error for it, and dropping is harmless: nothing is held that would
// need a release. Logged once per pointer.
func (p *virtualPointer) dropButton(b uint32, why string) {
	if !p.warned {
		p.warned = true
		p.server.log.Warn().Uint32("button", b).Str("reason", why).Msg("virtual pointer button dropped")
	}
}

func (*virtualPointer) Destroy(*wlrvirtualpointer.ZwlrVirtualPointerV1) {}

// release runs when the resource dies, by request or disconnect: unframed
// requests are discarded, and every button whose press was forwarded in
// this epoch is released so no client keeps a stuck drag.
func (p *virtualPointer) release() {
	p.dead = false // a protocol error still releases what was forwarded
	if !p.admitted() {
		return
	}
	p.reset()
	if len(p.forwarded) == 0 {
		return
	}
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	now := time.Duration(ts.Nano())
	state := p.server.securityState()
	for b := range p.forwarded {
		p.server.emit(ports.VirtualPointerInput{State: state, Output: p.output, Event: ports.PointerButton{Device: p.device, Button: b, Time: now}})
	}
	clear(p.forwarded)
	p.dead = true
}
