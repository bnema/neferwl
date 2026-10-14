// Package screencast serves the GNOME screen cast and screenshot D-Bus
// interfaces (org.gnome.Mutter.ScreenCast, org.gnome.Mutter.DisplayConfig,
// org.gnome.Shell.Introspect, org.gnome.Shell.Screenshot) so that
// xdg-desktop-portal-gnome can offer monitor and window sharing and
// screenshots. A cast is a PipeWire Video/Source node whose frames the
// wayland server renders into the node's buffers (ports.CastCommand), and a
// screenshot is taken by running nefercap.
//
// One owner goroutine, locked to its OS thread, owns all state: the D-Bus
// connection, the PipeWire loop, the sessions and the streams. It waits for
// the bus, the PipeWire loop and wake-ups from other goroutines (the wayland
// sink and screenshot jobs) in a single poll. Callers are checked against
// the capture allowlist through a pidfd before anything is dispatched.
package screencast

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/busretry"
	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/fdwait"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerobus"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// The bus names served, and the bounds of the fixed tables.
const (
	nameScreenCast    = "org.gnome.Mutter.ScreenCast"
	nameDisplayConfig = "org.gnome.Mutter.DisplayConfig"
	nameIntrospect    = "org.gnome.Shell.Introspect"
	nameScreenshot    = "org.gnome.Shell.Screenshot"
	busDaemon         = "org.freedesktop.DBus"
	busDaemonPath     = "/org/freedesktop/DBus"

	maxSessions = 8  // live sessions
	maxStreams  = 4  // streams per session
	maxPending  = 16 // requests waiting for a caller check
	maxCallers  = 32 // checked D-Bus peers
	maxArg      = 128

	// sinkCap bounds the wayland events waiting for the owner. Damage past
	// it is merged into a flag; nothing else is dropped.
	sinkCap = 256

	// shotTimeout bounds a nefercap run.
	shotTimeout = 60 * time.Second

	// shutdownWait bounds the wait for frames still in flight at shutdown.
	shutdownWait = 2 * time.Second
)

var _ ports.CastSink = (*Service)(nil)

// Service is the screencast adapter. Create it with New and run it once
// with Run; Push and Inventory are the only methods other goroutines call.
type Service struct {
	policy func() *captureallow.Policy
	cmds   chan<- ports.CastCommand
	log    zerowrap.Logger

	poller  *fdwait.Poller
	initErr error

	// Seams; tests replace them before Run.
	clock   ports.Clock
	dial    dialer
	pw      pipewireOpener
	peers   peerResolver
	runner  runner
	address string // session bus when empty
	env     []string

	// Transport: filled by other goroutines, drained by the owner.
	mu      sync.Mutex
	queue   []ports.CastEvent
	spare   []ports.CastEvent
	lost    bool // damage was merged away
	woken   bool
	invNew  *ports.CastInventory
	shotRes shotResult

	// Owner state.
	ctx      context.Context
	conn     bus
	pwc      videoCore
	owns     [4]bool // nameScreenCast, nameDisplayConfig, nameIntrospect, nameScreenshot
	inv      ports.CastInventory
	haveInv  bool
	callers  [maxCallers]caller
	pending  [maxPending]pending
	sessions [maxSessions]session
	nextSess uint32
	nextStrm uint32
	nextCast uint64
	waitFor  time.Duration // time to the next paced frame, 0 when none
	shot     shotState
	fatal    error
	jobs     sync.WaitGroup
}

// New creates the service. policy returns the capture allowlist in force;
// cmds carries the commands for the wayland server.
func New(policy func() *captureallow.Policy, cmds chan<- ports.CastCommand, log zerowrap.Logger) *Service {
	s := &Service{
		policy: policy,
		cmds:   cmds,
		log:    log,
		clock:  clock.System{},
		dial:   zerobusDialer{},
		pw:     realPipeWire{},
		peers:  pidfdPeers{},
		queue:  make([]ports.CastEvent, 0, sinkCap),
		spare:  make([]ports.CastEvent, 0, sinkCap),
	}
	s.poller, s.initErr = fdwait.New()
	return s
}

// SetEnv sets the environment nefercap runs in (WAYLAND_DISPLAY names this
// compositor). Call it before Run.
func (s *Service) SetEnv(env []string) { s.env = env }

// Push queues an event of the wayland server for the owner. It never blocks.
func (s *Service) Push(ev ports.CastEvent) {
	s.mu.Lock()
	if ev.Kind == ports.CastDamage && len(s.queue) >= sinkCap {
		s.lost = true
	} else {
		s.queue = append(s.queue, ev)
	}
	wake := !s.woken
	s.woken = true
	s.mu.Unlock()
	if wake {
		s.wake()
	}
}

// Inventory takes the new outputs and windows; the owner publishes them.
func (s *Service) Inventory(inv ports.CastInventory) {
	s.mu.Lock()
	s.invNew = &inv
	wake := !s.woken
	s.woken = true
	s.mu.Unlock()
	if wake {
		s.wake()
	}
}

func (s *Service) wake() {
	if s.poller != nil {
		s.poller.Wake()
	}
}

// take swaps out what other goroutines queued. The events stay valid until
// the next take.
func (s *Service) take() (evs []ports.CastEvent, lost bool, inv *ports.CastInventory, shot *shotResult) {
	s.mu.Lock()
	s.spare, s.queue = s.queue, s.spare[:0]
	evs, lost, inv = s.spare, s.lost, s.invNew
	s.lost, s.invNew, s.woken = false, nil, false
	if s.shotRes.ready {
		r := s.shotRes
		s.shotRes = shotResult{}
		shot = &r
	}
	s.mu.Unlock()
	return evs, lost, inv, shot
}

// Run serves until ctx ends. A lost bus or a name that others own is tried
// again after retry, growing to a minute. It returns once every job and
// stream is released.
func (s *Service) Run(ctx context.Context, retry time.Duration) {
	if s.initErr != nil {
		s.log.Warn().Err(s.initErr).Msg("screencast disabled")
		return
	}
	defer s.poller.Close()
	s.poller.StopOn(ctx)
	if s.runner == nil {
		s.runner = execRunner{env: s.env}
	}
	busretry.Run(ctx, retry, "D-Bus screencast", s.log, s.attempt)
	s.jobs.Wait()
}

// attempt is one connection of the service to the buses.
func (s *Service) attempt(ctx context.Context) (err error) {
	// The PipeWire loop belongs to this OS thread for its whole life.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	conn, err := s.dial.Dial(s.address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	s.reset(ctx, conn)
	if s.pwc, err = s.pw.Open(); err != nil {
		s.pwc = nil
		s.log.Warn().Err(err).Msg("PipeWire unavailable: screen casting is not offered")
	}
	defer s.shutdown()
	if err = s.acquire(); err != nil {
		return err
	}
	return s.loop(ctx)
}

// reset clears the owner state of a previous attempt.
func (s *Service) reset(ctx context.Context, conn bus) {
	s.ctx, s.conn, s.fatal = ctx, conn, nil
	s.owns = [4]bool{}
	s.haveInv = false
	s.callers = [maxCallers]caller{}
	s.pending = [maxPending]pending{}
	s.sessions = [maxSessions]session{}
	s.shot = shotState{}
}

// acquire watches departures and takes the names.
func (s *Service) acquire() error {
	rule := "type='signal',sender='" + busDaemon + "',interface='" + busDaemon + "',member='NameOwnerChanged',arg2=''"
	if err := s.conn.AddMatch(rule); err != nil {
		return err
	}
	names := [4]string{nameScreenCast, nameDisplayConfig, nameIntrospect, nameScreenshot}
	got := 0
	for i, name := range names {
		if i == 0 && s.pwc == nil {
			continue
		}
		code, err := s.conn.RequestName(name, zerobus.NameFlagDoNotQueue)
		if err != nil {
			return err
		}
		if code != zerobus.NamePrimaryOwner && code != zerobus.NameAlreadyOwner {
			s.log.Warn().Str("name", name).Msg("name owned by another program; not served")
			continue
		}
		s.owns[i] = true
		got++
		s.log.Info().Str("name", name).Msg("serving")
	}
	if got == 0 {
		return errors.New("no D-Bus name acquired")
	}
	return nil
}

// loop waits for the bus, PipeWire and other goroutines and serves them.
func (s *Service) loop(ctx context.Context) error {
	rc, err := s.conn.SyscallConn()
	if err != nil {
		return err
	}
	busFD := int32(-1)
	if err := rc.Control(func(fd uintptr) { busFD = int32(fd) }); err != nil {
		return err
	}
	var fds [2]unix.PollFd
	for ctx.Err() == nil {
		for s.fatal == nil && s.conn.Buffered() {
			s.readOne()
		}
		if s.pwc != nil {
			s.pwc.Iterate()
		}
		s.service()
		if s.fatal != nil {
			return s.fatal
		}
		fds[0] = unix.PollFd{Fd: busFD, Events: unix.POLLIN}
		n := 1
		if s.pwc != nil {
			fds[1] = unix.PollFd{Fd: int32(s.pwc.Fd()), Events: unix.POLLIN}
			n = 2
		}
		if _, err := s.poller.Poll(fds[:n], s.timeout()); err != nil {
			return err
		}
		if fds[0].Revents != 0 {
			s.readOne()
		}
	}
	return nil
}

// readOne reads and handles one message; the read may wait for the rest of
// a message that arrived in part.
func (s *Service) readOne() {
	m, err := s.conn.ReadMessage()
	if err != nil {
		s.fatal = err
		return
	}
	s.onMessage(m)
}

// service applies everything the other goroutines queued, then produces the
// frames that are due.
func (s *Service) service() {
	evs, lost, inv, shot := s.take()
	if inv != nil {
		s.applyInventory(*inv)
	}
	if lost {
		s.forEachStream(func(st *stream) { st.dirty = true })
	}
	for i := range evs {
		s.onCastEvent(&evs[i])
	}
	if shot != nil {
		s.onShotDone(shot)
	}
	s.pumpAll(s.clock.Now())
	s.reap()
}

// shutdown ends every session and releases PipeWire. A frame still in
// flight is waited for: its buffer is lent to the wayland server.
func (s *Service) shutdown() {
	s.forEachSession(func(sess *session) { s.closeSession(sess, true) })
	deadline := time.Now().Add(shutdownWait)
	for s.streamsLive() && time.Now().Before(deadline) {
		evs, _, _, _ := s.take()
		for i := range evs {
			s.onCastEvent(&evs[i])
		}
		s.reap()
		if s.streamsLive() {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if s.streamsLive() {
		s.log.Warn().Msg("frames still in flight at shutdown; PipeWire resources are left to the process exit")
		s.pwc = nil
	}
	if s.pwc != nil {
		s.pwc.Close()
		s.pwc = nil
	}
}

// timeout is the poll timeout in milliseconds: the time to the next frame
// that only waits for its interval, or -1.
func (s *Service) timeout() int {
	wait := s.waitFor
	s.waitFor = 0
	if wait <= 0 {
		return -1
	}
	return int((wait + time.Millisecond - 1) / time.Millisecond)
}
