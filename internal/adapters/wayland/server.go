package wayland

import (
	"context"
	"fmt"
	"os"

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
	if err = registerGlobals(d, opts); err != nil {
		cleanup()
		return nil, err
	}
	return &Server{display: d, name: name, cleanup: cleanup, log: log, channels: ch}, nil
}
func (s *Server) SocketName() string { return s.name }
func (s *Server) Run(ctx context.Context) error {
	defer s.cleanup()
	s.log.Info().Str("socket", s.name).Msg("starting wayland")
	return s.display.Run(ctx)
}
