package wayland

import (
	"context"
	"fmt"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"os"
		"time"
 "sync"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

type Options struct {
	RuntimeDir                string
	OutputWidth, OutputHeight int
}
type Channels struct {
	Events   chan<- ports.ClientEvent
	Commands <-chan ports.ClientCommand
}
type Server struct {
	display  *server.Display
	name     string
	cleanup  func()
	log      zerowrap.Logger
	channels Channels
	awaiting []*wayland.Callback
	frames uint64
	started  time.Time
	surfaces map[*server.Resource]*surface
	serial   uint32
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
	s := &Server{display: d, name: name, cleanup: cleanup, log: log, channels: ch, surfaces: make(map[*server.Resource]*surface)}
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
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(16 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				s.display.Do(func() {
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
				})
			}
		}
	}()
	err := s.display.Run(ctx)
	wg.Wait()
	return err
}
