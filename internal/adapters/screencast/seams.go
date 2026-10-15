package screencast

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/peerexe"
	"github.com/bnema/purego-pipewire/pipewire"
	"github.com/bnema/zerobus"
)

// bus is the part of a zerobus connection the service uses. Only Send and
// ReadMessage are used after startup: a connection has one encoder and one
// receive buffer, so Call and Handle are off limits.
type bus interface {
	NewReply(m *zerobus.Message, sig string) *zerobus.Encoder
	NewError(m *zerobus.Message, name, sig string) *zerobus.Encoder
	NewSignal(path, iface, member, sig string) *zerobus.Encoder
	NewSignalTo(dest, path, iface, member, sig string) *zerobus.Encoder
	NewMethodCall(dest, path, iface, member, sig string) (*zerobus.Encoder, uint32)
	Send() (uint32, error)
	ReadMessage() (*zerobus.Message, error)
	Buffered() bool
	SyscallConn() (syscall.RawConn, error)
	AddMatch(rule string) error
	RequestName(name string, flags uint32) (uint32, error)
	Close() error
}

// videoSource is the part of a pipewire.VideoSource the producer uses. All
// calls run on the loop thread.
type videoSource interface {
	NodeID() uint32
	State() int32
	Dequeue() *pipewire.VideoBuffer
	Queue(b *pipewire.VideoBuffer, seq uint64, pts time.Duration, corrupted bool)
	Release(b *pipewire.VideoBuffer)
	Trigger()
	Resize(w, h int) error
	Destroy()
}

// videoCore is a PipeWire connection driven by the service's own poll loop.
type videoCore interface {
	// Fd is the pollable descriptor of the loop.
	Fd() int
	// Iterate dispatches pending events; handler callbacks run inside it.
	Iterate()
	NewVideoSource(cfg pipewire.VideoConfig, h pipewire.VideoHandler) (videoSource, error)
	Close()
}

// peerResolver proves which executable a D-Bus peer runs: Open pins the
// process behind a pid, Exe reads the executable through that pin.
type peerResolver interface {
	Open(pid int) (*os.File, error)
	Exe(pidfd *os.File) (exe string, pid int, err error)
}

// runner runs nefercap with args.
type runner interface {
	Run(ctx context.Context, args []string) error
}

// dialer connects the session bus (address empty) or the bus at address.
type dialer interface {
	Dial(address string) (bus, error)
}

type zerobusDialer struct{}

func (zerobusDialer) Dial(address string) (bus, error) {
	if address == "" {
		return zerobus.SessionBus()
	}
	return zerobus.Dial(address)
}

// pipewireOpener connects to the PipeWire daemon. Open runs on the goroutine
// that stays locked to its OS thread: the loop belongs to that thread.
type pipewireOpener interface {
	Open() (videoCore, error)
}

type realPipeWire struct{}

func (realPipeWire) Open() (videoCore, error) { return openPipeWire() }

// pidfdPeers is peerResolver over the pidfd proof of package peerexe.
type pidfdPeers struct{}

func (pidfdPeers) Open(pid int) (*os.File, error) { return peerexe.Open(pid) }

func (pidfdPeers) Exe(pidfd *os.File) (string, int, error) {
	exe, pid, err := peerexe.ExeOfPidfd(int(pidfd.Fd()))
	runtime.KeepAlive(pidfd)
	return exe, pid, err
}

// nefercap is the program that takes the screenshots.
const nefercap = "nefercap"

// execRunner runs nefercap in the session's environment (WAYLAND_DISPLAY
// names this compositor).
type execRunner struct{ env []string }

func (r execRunner) Run(ctx context.Context, args []string) error {
	bin, err := exec.LookPath(nefercap)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// A cancelled run is joined by the owner: never wait long for pipes
	// that a grandchild keeps open.
	cmd.WaitDelay = 2 * time.Second
	if len(r.env) > 0 {
		cmd.Env = r.env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 256 {
			out = out[len(out)-256:]
		}
		return fmt.Errorf("%s: %w: %s", nefercap, err, out)
	}
	return nil
}

// pwLoop is videoCore over a real PipeWire loop, context and core.
type pwLoop struct {
	rt   pipewire.Runtime
	loop pipewire.Loop
	ctx  pipewire.Context
	core pipewire.Core
}

// openPipeWire connects to the PipeWire daemon.
func openPipeWire() (videoCore, error) {
	rt, err := pipewire.Init()
	if err != nil {
		return nil, err
	}
	p := &pwLoop{rt: rt}
	if p.loop, err = rt.NewLoop(); err != nil {
		p.Close()
		return nil, err
	}
	if p.ctx, err = rt.NewContext(p.loop); err != nil {
		p.Close()
		return nil, err
	}
	if p.core, err = p.ctx.Connect(); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *pwLoop) Fd() int { return p.loop.Fd() }

func (p *pwLoop) Iterate() { p.loop.Iterate() }

func (p *pwLoop) NewVideoSource(cfg pipewire.VideoConfig, h pipewire.VideoHandler) (videoSource, error) {
	vs, err := p.core.NewVideoSource(cfg, h)
	if err != nil {
		return nil, err
	}
	if vs == nil {
		return nil, errors.New("pipewire returned no video source")
	}
	return vs, nil
}

// Close releases the connection, innermost first. Destroy the sources first.
func (p *pwLoop) Close() {
	if p.core != nil {
		p.core.Disconnect()
	}
	if p.ctx != nil {
		p.ctx.Destroy()
	}
	if p.loop != nil {
		p.loop.Destroy()
	}
	p.rt.Deinit()
}
