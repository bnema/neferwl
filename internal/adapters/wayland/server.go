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
	Keymap                    string
	RepeatRate, RepeatDelay   int
}

// Channels carries client notifications and commands. Events may be unbuffered.
type Channels struct {
	Events   chan<- ports.ClientEvent
	Contents chan<- ports.SurfaceContent
	Commands <-chan ports.ClientCommand
}
type Server struct {
	display                 *server.Display
	name                    string
	cleanup                 func()
	log                     zerowrap.Logger
	channels                Channels
	awaiting                []*wayland.Callback
	frames                  uint64
	started                 time.Time
	surfaces                map[*server.Resource]*surface
	buffers                 map[*server.Resource]*buffer
	serial                  uint32
	ctx                     context.Context
	windows                 map[ports.WindowID]*window
	nextWindow              ports.WindowID
	focused                 ports.WindowID
	pointerFocus            ports.WindowID
	pointerX, pointerY      float64
	pointers                map[server.Client][]*wayland.Pointer
	modState                ports.ModState
	keyboards               map[server.Client][]*wayland.Keyboard
	keymapFD                int
	keymapSize              uint32
	repeatRate, repeatDelay int
	// eventMu protects only the notification queue, not display-owned window state.
	// The event queue is unbounded by design: it grows only if core stops draining
	// Events, which happens only at shutdown (core owns the receiving end). A
	// stalled core is a bug surfaced by ctx cancellation, not a reason to block
	// the display goroutine.
	eventMu       sync.Mutex
	events        []ports.ClientEvent
	eventReady    chan struct{}
	contentMu     sync.Mutex
	contents      map[ports.WindowID]ports.SurfaceContent
	contentSeq    map[ports.WindowID]uint64
	contentNotify chan struct{}
	contentReady  chan struct{}
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
		d.Close()
		return nil, err
	}
	if err = d.AddSocketFD(fd); err != nil {
		unix.Close(fd)
		cleanup()
		d.Close()
		return nil, err
	}
	s := &Server{display: d, name: name, cleanup: cleanup, log: log, channels: ch, surfaces: make(map[*server.Resource]*surface), buffers: make(map[*server.Resource]*buffer), windows: make(map[ports.WindowID]*window), nextWindow: 1, eventReady: make(chan struct{}, 1), contents: make(map[ports.WindowID]ports.SurfaceContent), contentSeq: make(map[ports.WindowID]uint64), contentReady: make(chan struct{}, 1), contentNotify: make(chan struct{}), keymapFD: -1, keyboards: make(map[server.Client][]*wayland.Keyboard), pointers: make(map[server.Client][]*wayland.Pointer), repeatRate: opts.RepeatRate, repeatDelay: opts.RepeatDelay}
	if opts.Keymap != "" {
		if uint64(len(opts.Keymap))+1 > uint64(^uint32(0)) {
			cleanup()
			d.Close()
			return nil, fmt.Errorf("keymap too large")
		}
		fd, e := unix.MemfdCreate("nefertty-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
		if e == nil {
			var n int
			n, e = unix.Write(fd, append([]byte(opts.Keymap), 0))
			if e == nil && n != len(opts.Keymap)+1 {
				e = fmt.Errorf("short keymap write")
			}
			if e == nil {
				_, e = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL)
			}
			if e != nil {
				unix.Close(fd)
			} else {
				s.keymapFD, s.keymapSize = fd, uint32(n)
			}
		}
		if e != nil {
			cleanup()
			d.Close()
			return nil, e
		}
		s.cleanup = func() { unix.Close(fd); cleanup() }
	}
	if err = registerGlobals(d, opts, s); err != nil {
		s.cleanup()
		d.Close()
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
	wg.Add(4)
	go func() { defer wg.Done(); s.forward(ctx) }()
	go func() { defer wg.Done(); s.forwardContents(ctx) }()
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
	s.eventMu.Lock()
	s.events = append(s.events, ev)
	s.eventMu.Unlock()
	select {
	case s.eventReady <- struct{}{}:
	default:
	}
}

func (s *Server) forward(ctx context.Context) {
	for {
		s.eventMu.Lock()
		if len(s.events) == 0 {
			s.eventMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-s.eventReady:
				continue
			}
		}
		ev := s.events[0]
		s.events[0] = nil
		s.events = s.events[1:]
		s.eventMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case s.channels.Events <- ev:
		}
	}
}

func (s *Server) emitContent(c ports.SurfaceContent) {
	if s.channels.Contents == nil {
		return
	}
	s.contentMu.Lock()
	s.contents[c.ID] = c
	s.contentSeq[c.ID]++
	close(s.contentNotify)
	s.contentNotify = make(chan struct{})
	s.contentMu.Unlock()
	select {
	case s.contentReady <- struct{}{}:
	default:
	}
}

func (s *Server) forwardContents(ctx context.Context) {
	for {
		s.contentMu.Lock()
		var id ports.WindowID
		var c ports.SurfaceContent
		found := false
		var seq uint64
		var notify <-chan struct{}
		for id, c = range s.contents {
			found = true
			seq = s.contentSeq[id]
			break
		}
		notify = s.contentNotify
		s.contentMu.Unlock()
		if !found {
			select {
			case <-ctx.Done():
				return
			case <-s.contentReady:
				continue
			}
		}
		// Do not hold a stale frame while the receiver is busy: wake on newer data.
		select {
		case <-ctx.Done():
			return
		case <-notify:
			continue
		case s.channels.Contents <- c:
			s.contentMu.Lock()
			if s.contentSeq[id] == seq {
				delete(s.contents, id)
			}
			s.contentMu.Unlock()
		}
	}
}

func (s *Server) apply(cmd ports.ClientCommand) {
	switch c := cmd.(type) {
	case ports.PointerFocus:
		s.changePointerFocus(c.ID, c.X, c.Y)
	case ports.PointerMotionTo:
		if c.ID == s.pointerFocus {
			for _, p := range s.windowPointers(s.windows[c.ID]) {
				p.SendMotion(c.TimeMsec, server.FixedFromFloat(c.X), server.FixedFromFloat(c.Y))
				pointerFrame(p)
			}
		}
	case ports.PointerButtonTo:
		// Releases may target the implicit-grab window after focus has moved.
		if w := s.windows[c.ID]; w != nil {
			for _, p := range s.windowPointers(w) {
				s.serial++
				state := uint32(0)
				if c.Pressed {
					state = 1
				}
				p.SendButton(s.serial, c.TimeMsec, c.Button, state)
				pointerFrame(p)
			}
		}
	case ports.FocusWindow:
		if s.focused != c.ID {
			s.changeFocus(c.ID)
		}
	case ports.ForwardKey:
		w := s.windows[c.ID]
		if c.ID != s.focused || w == nil || !w.mapped || len(s.windowKeyboards(w)) == 0 {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("ignored forward key")
			return
		}
		s.serial++
		state := uint32(0)
		if c.Key.Pressed {
			state = 1
		}
		for _, k := range s.windowKeyboards(w) {
			k.SendKey(s.serial, c.Key.TimeMsec, c.Key.Keycode, state)
		}
		if c.Key.State != s.modState {
			s.modState = c.Key.State
			s.serial++
			for _, k := range s.windowKeyboards(w) {
				s.sendModifiers(k)
			}
		}
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
		w.xdg.serials = append(w.xdg.serials, s.serial)
	case ports.CloseWindow:
		w := s.windows[c.ID]
		if w == nil || !w.toplevel.Resource.Alive() {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("close missing window")
			return
		}
		w.toplevel.SendClose()
	}
}

func (s *Server) windowKeyboards(w *window) []*wayland.Keyboard {
	if w == nil || !w.mapped || !w.xdg.resource.Resource.Alive() {
		return nil
	}
	var alive []*wayland.Keyboard
	for _, k := range s.keyboards[w.xdg.resource.Client()] {
		if k.Resource.Alive() {
			alive = append(alive, k)
		}
	}
	return alive
}

func (s *Server) sendModifiers(k *wayland.Keyboard) {
	m := s.modState
	k.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
}

func (s *Server) changeFocus(id ports.WindowID) {
	if old := s.windows[s.focused]; old != nil && old.xdg.surfaceResource() != nil {
		for _, k := range s.windowKeyboards(old) {
			s.serial++
			k.SendLeave(s.serial, old.xdg.surfaceResource())
		}
	}
	s.focused = 0
	if w := s.windows[id]; w != nil && w.mapped && w.xdg.surfaceResource() != nil {
		s.focused = id
		for _, k := range s.windowKeyboards(w) {
			s.serial++
			k.SendEnter(s.serial, w.xdg.surfaceResource(), []byte{})
			s.sendModifiers(k)
		}
	}
}

func pointerFrame(p *wayland.Pointer) {
	if p.Version() >= 5 {
		p.SendFrame()
	}
}
func (s *Server) windowPointers(w *window) []*wayland.Pointer {
	if w == nil || !w.mapped || !w.xdg.resource.Resource.Alive() {
		return nil
	}
	var result []*wayland.Pointer
	for _, p := range s.pointers[w.xdg.resource.Client()] {
		if p.Resource.Alive() {
			result = append(result, p)
		}
	}
	return result
}
func (s *Server) changePointerFocus(id ports.WindowID, x, y float64) {
	if id == s.pointerFocus {
		return
	}
	if old := s.windows[s.pointerFocus]; old != nil {
		if surface := old.xdg.surfaceResource(); surface != nil && surface.Resource.Alive() {
			for _, p := range s.windowPointers(old) {
				s.serial++
				p.SendLeave(s.serial, surface)
				pointerFrame(p)
			}
		}
	}
	s.pointerFocus = 0
	if w := s.windows[id]; w != nil {
		if surface := w.xdg.surfaceResource(); surface != nil && surface.Resource.Alive() {
			s.pointerFocus, s.pointerX, s.pointerY = id, x, y
			for _, p := range s.windowPointers(w) {
				s.serial++
				p.SendEnter(s.serial, surface, server.FixedFromFloat(x), server.FixedFromFloat(y))
				pointerFrame(p)
			}
		}
	}
}
