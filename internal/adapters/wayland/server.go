package wayland

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

type Options struct {
	RuntimeDir                string
	OutputWidth, OutputHeight int
}

// Channels carries client notifications and commands. Events must be buffered.
type Channels struct {
	Events   chan<- ports.ClientEvent
	Commands <-chan ports.ClientCommand
}
type Server struct {
	display    *server.Display
	name       string
	cleanup    func()
	log        zerowrap.Logger
	channels   Channels
	awaiting   []*wayland.Callback
	frames     uint64
	started    time.Time
	surfaces   map[*server.Resource]*surface
	serial     uint32
	ctx        context.Context
	windows    map[ports.WindowID]*window
	nextWindow ports.WindowID
	focused    ports.WindowID
}

func New(opts Options, ch Channels, log zerowrap.Logger) (*Server, error) {
	if opts.RuntimeDir == "" {
		opts.RuntimeDir = os.Getenv("XDG_RUNTIME_DIR")
	}
	if opts.RuntimeDir == "" {
		return nil, fmt.Errorf("XDG_RUNTIME_DIR is empty")
	}
	d, err := server.NewDisplay()
	if err != nil {
		return nil, err
	}
	name, fd, cleanup, err := listen(opts.RuntimeDir)
	if err != nil {
		return nil, err
	}
	if err = d.AddSocketFD(fd); err != nil {
		unix.Close(fd)
		cleanup()
		return nil, err
	}
	s := &Server{display: d, name: name, cleanup: cleanup, log: log, channels: ch, surfaces: make(map[*server.Resource]*surface), windows: make(map[ports.WindowID]*window), nextWindow: 1}
	if err = registerGlobals(d, opts, s); err != nil {
		cleanup()
		return nil, err
	}
	return s, nil
}
func (s *Server) SocketName() string { return s.name }
func (s *Server) Run(ctx context.Context) error {
	defer s.cleanup()
	s.log.Info().Str("socket", s.name).Msg("starting wayland")
	s.started = time.Now()
	s.ctx = ctx
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.display.Stopped():
				return
			case cmd, ok := <-s.channels.Commands:
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}
				if !s.display.Do(func() {
					if ctx.Err() == nil {
						s.apply(cmd)
					}
				}) {
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(16 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.display.Stopped():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				if !s.display.Do(func() {
					if ctx.Err() != nil {
						return
					}
					callbacks := s.awaiting
					s.awaiting = nil
					for _, cb := range callbacks {
						if !cb.Resource.Alive() {
							continue
						}
						cb.SendDone(uint32(time.Since(s.started).Milliseconds()))
						cb.Destroy()
						s.frames++
					}
				}) {
					return
				}
			}
		}
	}()
	err := s.display.Run(ctx)
	wg.Wait()
	return err
}

func (s *Server) emit(ev ports.ClientEvent) {
	if s.channels.Events == nil {
		return
	}
	select {
	case s.channels.Events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *Server) apply(cmd ports.ClientCommand) {
	switch c := cmd.(type) {
	case ports.FocusWindow:
		s.focused = c.ID
	case ports.ForwardKey:
		s.log.Debug().Uint64("id", uint64(c.ID)).Msg("forward key")
	case ports.ConfigureWindow:
		w := s.windows[c.ID]
		if w == nil || !w.toplevel.Resource.Alive() || !w.xdg.resource.Resource.Alive() {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("configure missing window")
			return
		}
		var states []byte
		if c.Fullscreen {
			states = binary.LittleEndian.AppendUint32(states, uint32(xdgshell.ToplevelStateFullscreen))
		}
		if c.Activated {
			states = binary.LittleEndian.AppendUint32(states, uint32(xdgshell.ToplevelStateActivated))
		}
		w.toplevel.SendConfigure(int32(c.Width), int32(c.Height), states)
		s.serial++
		w.xdg.resource.SendConfigure(s.serial)
	case ports.CloseWindow:
		w := s.windows[c.ID]
		if w == nil || !w.toplevel.Resource.Alive() {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("close missing window")
			return
		}
		w.toplevel.SendClose()
	}
}
