package wayland

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/extidlenotify"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlroutputpower"
	"github.com/bnema/purego-libwayland/server"
)

// Idle and output power. ext_idle_notifier_v1: each notification has its
// own timeout, restarted by user activity (ports.UserActivity from core);
// idle inhibitors hold every notification but input-idle ones.
// zwlr_output_power_v1: a client such as wlopm turns a display off or on;
// core decides and the scene carries it.

// idleNotification is one ext_idle_notification_v1.
type idleNotification struct {
	res     *extidlenotify.ExtIdleNotificationV1
	timeout time.Duration
	input   bool // get_input_idle_notification: ignores idle inhibitors
	idle    bool // idled sent, resumed not yet
	timer   ports.Timer
	gen     uint64 // bumped on stop: a firing timer of an older gen is stale
}

// outputPower is one zwlr_output_power_v1.
type outputPower struct {
	res    *wlroutputpower.ZwlrOutputPowerV1
	output string
}

func registerIdle(d *server.Display, s *Server) error {
	if err := extidlenotify.NewExtIdleNotifierV1Global(d, 2, func(c server.Client, v, id uint32) {
		_, _ = extidlenotify.NewExtIdleNotifierV1(c, int32(v), id, idleNotifier{s})
	}); err != nil {
		return err
	}
	return wlroutputpower.NewZwlrOutputPowerManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = wlroutputpower.NewZwlrOutputPowerManagerV1(c, int32(v), id, powerManager{s})
	})
}

type idleNotifier struct{ server *Server }

func (idleNotifier) Destroy(*extidlenotify.ExtIdleNotifierV1) {}

func (n idleNotifier) GetIdleNotification(r *extidlenotify.ExtIdleNotifierV1, id, timeout uint32, _ *wayland.Seat) {
	n.server.addIdle(r, id, timeout, false)
}

func (n idleNotifier) GetInputIdleNotification(r *extidlenotify.ExtIdleNotifierV1, id, timeout uint32, _ *wayland.Seat) {
	n.server.addIdle(r, id, timeout, true)
}

type idleNotificationHandler struct{}

func (idleNotificationHandler) Destroy(*extidlenotify.ExtIdleNotificationV1) {}

func (s *Server) addIdle(r *extidlenotify.ExtIdleNotifierV1, id, timeout uint32, input bool) {
	res, err := extidlenotify.NewExtIdleNotificationV1(r.Client(), r.Version(), id, idleNotificationHandler{})
	if err != nil {
		return
	}
	n := &idleNotification{res: res, timeout: time.Duration(timeout) * time.Millisecond, input: input}
	s.idleNotes = append(s.idleNotes, n)
	res.OnDestroy = func() {
		n.stop()
		for i, x := range s.idleNotes {
			if x == n {
				s.idleNotes = append(s.idleNotes[:i], s.idleNotes[i+1:]...)
				break
			}
		}
	}
	s.armIdle(n)
}

// idleHeld reports whether an idle inhibitor keeps n from firing.
func (s *Server) idleHeld(n *idleNotification) bool {
	return !n.input && len(s.idleWindows) > 0
}

// armIdle restarts n's timer, unless an inhibitor holds it. The timer
// fires on the display goroutine.
func (s *Server) armIdle(n *idleNotification) {
	n.stop()
	if s.idleHeld(n) {
		return
	}
	gen := n.gen
	// Core reports activity at most once per ActivityInterval: a shorter
	// timeout would fire while the user is still typing.
	n.timer = s.clock.AfterFunc(max(n.timeout, ports.ActivityInterval), func() {
		s.display.Do(func() {
			// A timer stopped or replaced while it waited for Do is stale.
			if n.gen != gen || !n.res.Resource.Alive() || n.idle || s.idleHeld(n) {
				return
			}
			n.idle = true
			n.res.SendIdled()
		})
	})
}

func (n *idleNotification) stop() {
	n.gen++
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
}

// userActivity resumes idle notifications and restarts their timers.
func (s *Server) userActivity() {
	for _, n := range s.idleNotes {
		if n.idle {
			n.idle = false
			n.res.SendResumed()
		}
		s.armIdle(n)
	}
}

// syncIdle follows idle inhibitors: while one exists, inhibitable timers
// stop; once the last one goes, they start again from zero.
func (s *Server) syncIdle(wasHeld bool) {
	if held := len(s.idleWindows) > 0; held != wasHeld {
		for _, n := range s.idleNotes {
			if !n.input {
				s.armIdle(n)
			}
		}
	}
}

type powerManager struct{ server *Server }

func (powerManager) Destroy(*wlroutputpower.ZwlrOutputPowerManagerV1) {}

func (m powerManager) GetOutputPower(r *wlroutputpower.ZwlrOutputPowerManagerV1, id uint32, wl *wayland.Output) {
	s := m.server
	o := s.outputOf(wl)
	p := &outputPower{}
	res, err := wlroutputpower.NewZwlrOutputPowerV1(r.Client(), r.Version(), id, powerHandler{s, p})
	if err != nil {
		return
	}
	p.res = res
	if o == nil {
		// Unplugged or unknown: the object is inert.
		res.SendFailed()
		return
	}
	p.output = o.name()
	s.powers = append(s.powers, p)
	res.OnDestroy = func() {
		for i, x := range s.powers {
			if x == p {
				s.powers = append(s.powers[:i], s.powers[i+1:]...)
				break
			}
		}
	}
	res.SendMode(powerMode(!s.outputsOff[p.output]))
}

type powerHandler struct {
	server *Server
	p      *outputPower
}

func (powerHandler) Destroy(*wlroutputpower.ZwlrOutputPowerV1) {}

func (h powerHandler) SetMode(r *wlroutputpower.ZwlrOutputPowerV1, mode uint32) {
	if mode != uint32(wlroutputpower.ZwlrOutputPowerV1ModeOff) && mode != uint32(wlroutputpower.ZwlrOutputPowerV1ModeOn) {
		r.PostError(uint32(wlroutputpower.ZwlrOutputPowerV1ErrorInvalidMode), "invalid mode")
		return
	}
	if h.p.output == "" {
		return
	}
	h.server.emit(ports.OutputPower{Output: h.p.output, On: mode == uint32(wlroutputpower.ZwlrOutputPowerV1ModeOn)})
}

func powerMode(on bool) uint32 {
	if on {
		return uint32(wlroutputpower.ZwlrOutputPowerV1ModeOn)
	}
	return uint32(wlroutputpower.ZwlrOutputPowerV1ModeOff)
}

// setOutputsOff applies core's power state: each object of an output
// whose mode changed hears it; objects of gone outputs fail.
func (s *Server) setOutputsOff(off map[string]bool) {
	names := map[string]bool{}
	for _, o := range s.outputs {
		names[o.name()] = true
	}
	kept := s.powers[:0]
	for _, p := range s.powers {
		if !names[p.output] {
			// Failed objects are inert, even if an output of that name
			// comes back.
			p.res.SendFailed()
			p.res.OnDestroy = nil
			p.output = ""
			continue
		}
		if off[p.output] != s.outputsOff[p.output] {
			p.res.SendMode(powerMode(!off[p.output]))
		}
		kept = append(kept, p)
	}
	s.powers = kept
	s.outputsOff = off
}
