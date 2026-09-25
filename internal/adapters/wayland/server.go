package wayland

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/fractionalscale"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Options configures the server. Outputs is the initial layout; core
// replaces it with ports.SetOutputs.
type Options struct {
	RuntimeDir string
	Outputs    ports.Layout
	// DMABuf is what the renderer imports; empty disables linux-dmabuf.
	DMABuf                  ports.DMABufSupport
	Keymap                  string
	RepeatRate, RepeatDelay int
}

// Channels carries client notifications and commands. Events may be unbuffered.
type Channels struct {
	Events   chan<- ports.ClientEvent
	Contents chan<- ports.SurfaceContent
	Commands <-chan ports.ClientCommand
}
type Server struct {
	display                 *server.Display
	env                     procEnv
	slotsPending            bool // core waits for a slot window
	name                    string
	cleanup                 func()
	log                     zerowrap.Logger
	channels                Channels
	awaiting                []*wayland.Callback
	frames                  uint64
	started                 time.Time
	surfaces                map[*server.Resource]*surface
	buffers                 map[*server.Resource]clientBuffer
	dmabuf                  *dmabufGlobal
	serial                  uint32
	ctx                     context.Context
	windows                 map[ports.WindowID]*window
	layers                  map[ports.WindowID]*layerSurface
	nextWindow              ports.WindowID
	nextPool                uint64
	focused                 ports.WindowID
	pointerFocus            ports.WindowID
	pointerX, pointerY      float64
	pointers                map[server.Client][]*wayland.Pointer
	modState                ports.ModState
	keyboards               map[server.Client][]*wayland.Keyboard
	heldKeys                map[uint32]bool
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
	outputs       []*output
	focusedOutput string
	fractions     map[*surface]*fractionalscale.WpFractionalScaleV1
}

func removeItem[T comparable](list []T, v T) []T {
	for i, x := range list {
		if x == v {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
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
	s := &Server{display: d, env: linuxProcEnv{}, name: name, cleanup: cleanup, log: log, channels: ch, surfaces: make(map[*server.Resource]*surface), buffers: make(map[*server.Resource]clientBuffer), windows: make(map[ports.WindowID]*window), layers: make(map[ports.WindowID]*layerSurface), nextWindow: 1, eventReady: make(chan struct{}, 1), contents: make(map[ports.WindowID]ports.SurfaceContent), contentSeq: make(map[ports.WindowID]uint64), contentReady: make(chan struct{}, 1), contentNotify: make(chan struct{}), keymapFD: -1, keyboards: make(map[server.Client][]*wayland.Keyboard), pointers: make(map[server.Client][]*wayland.Pointer), repeatRate: opts.RepeatRate, repeatDelay: opts.RepeatDelay}
	s.fractions = map[*surface]*fractionalscale.WpFractionalScaleV1{}
	if opts.Keymap != "" {
		fd, size, e := keymapFile(opts.Keymap)
		if e != nil {
			cleanup()
			d.Close()
			return nil, e
		}
		s.keymapFD, s.keymapSize = fd, size
	}
	s.cleanup = func() {
		if s.keymapFD >= 0 {
			unix.Close(s.keymapFD)
		}
		s.dmabuf.close()
		cleanup()
	}
	if err = registerGlobals(d, opts, s); err != nil {
		s.cleanup()
		d.Close()
		return nil, err
	}
	for _, p := range opts.Outputs {
		s.addOutput(p)
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
				// One display round trip per batch: Do waits for the event
				// loop, so a round trip per motion capped pointer events at
				// ~150/s and let a backlog of stale motions build up.
				batch, open := drainCommands(cmd, s.channels.Commands)
				if !s.display.Do(func() {
					for _, c := range batch {
						if ctx.Err() != nil {
							return
						}
						s.apply(c)
					}
				}) || !open {
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

// maxCommandBatch bounds how many queued commands one display round trip applies.
const maxCommandBatch = 256

// drainCommands returns first plus the commands already queued, with runs of
// pointer motion for the same window collapsed into the latest one. open is
// false once the channel is closed.
func drainCommands(first ports.ClientCommand, cmds <-chan ports.ClientCommand) (batch []ports.ClientCommand, open bool) {
	batch = append(batch, first)
	for len(batch) < maxCommandBatch {
		select {
		case c, ok := <-cmds:
			if !ok {
				return batch, false
			}
			if m, isMotion := c.(ports.PointerMotionTo); isMotion {
				if last, lastMotion := batch[len(batch)-1].(ports.PointerMotionTo); lastMotion && last.ID == m.ID {
					batch[len(batch)-1] = m
					continue
				}
			}
			batch = append(batch, c)
		default:
			return batch, true
		}
	}
	return batch, true
}

func (s *Server) apply(cmd ports.ClientCommand) {
	switch c := cmd.(type) {
	case ports.PointerFocus:
		s.changePointerFocus(c.ID, c.X, c.Y)
	case ports.PointerMotionTo:
		if w := s.windows[c.ID]; w != nil && c.ID == s.pointerFocus {
			x, y := w.surfacePoint(c.X, c.Y)
			for _, p := range s.windowPointers(w) {
				p.SendMotion(c.TimeMsec, server.FixedFromFloat(x), server.FixedFromFloat(y))
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
	case ports.SetKeymap:
		s.setKeymap(c)
	case ports.FocusWindow:
		if s.focused != c.ID {
			s.changeFocus(c.ID)
		}
	case ports.ForwardKey:
		_, keyboards := s.focusTarget(c.ID)
		if c.ID != s.focused || len(keyboards) == 0 {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("ignored forward key")
			return
		}
		if s.heldKeys == nil {
			s.heldKeys = make(map[uint32]bool)
		}
		if c.Key.Pressed {
			s.heldKeys[c.Key.Keycode] = true
		} else {
			delete(s.heldKeys, c.Key.Keycode)
		}
		s.serial++
		state := uint32(0)
		if c.Key.Pressed {
			state = 1
		}
		for _, k := range keyboards {
			k.SendKey(s.serial, c.Key.TimeMsec, c.Key.Keycode, state)
		}
		if c.Key.State != s.modState {
			s.modState = c.Key.State
			s.serial++
			for _, k := range keyboards {
				s.sendModifiers(k)
			}
		}
	case ports.ConfigureWindow:
		w := s.windows[c.ID]
		if w == nil || !w.toplevel.Resource.Alive() || !w.xdg.resource.Resource.Alive() {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("configure missing window")
			return
		}
		s.log.Info().Uint64("id", uint64(c.ID)).Int("w", c.Width).Int("h", c.Height).Bool("fullscreen", c.Fullscreen).Bool("activated", c.Activated).Msg("configure")
		w.last, w.hasLast = c, true
		w.sendConfigure()
		if surf := w.xdg.surface; surf != nil && c.Output != "" {
			surf.sendTreeScale()
		}
	case ports.SetOutputs:
		s.setOutputs(c)
	case ports.SlotsPending:
		s.slotsPending = c.Pending
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
	return s.clientKeyboards(w.xdg.resource.Client())
}

func (s *Server) clientKeyboards(c server.Client) []*wayland.Keyboard {
	var alive []*wayland.Keyboard
	for _, k := range s.keyboards[c] {
		if k.Resource.Alive() {
			alive = append(alive, k)
		}
	}
	return alive
}

// focusTarget resolves a window or layer ID to its wl_surface and keyboards.
func (s *Server) focusTarget(id ports.WindowID) (*wayland.Surface, []*wayland.Keyboard) {
	if w := s.windows[id]; w != nil && w.mapped {
		if surf := w.xdg.surfaceResource(); surf != nil {
			return surf, s.windowKeyboards(w)
		}
	}
	if l := s.layers[id]; l != nil && l.mapped && l.resource.Resource.Alive() {
		for resource, state := range s.surfaces {
			if state == l.surface && resource.Alive() {
				return wayland.WrapSurface(resource), s.clientKeyboards(l.resource.Client())
			}
		}
	}
	return nil, nil
}

// keymapFile writes a sealed memfd holding the NUL-terminated keymap.
func keymapFile(keymap string) (int, uint32, error) {
	if uint64(len(keymap))+1 > uint64(^uint32(0)) {
		return -1, 0, fmt.Errorf("keymap too large")
	}
	fd, err := unix.MemfdCreate("nefertty-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, 0, err
	}
	n, err := unix.Write(fd, append([]byte(keymap), 0))
	if err == nil && n != len(keymap)+1 {
		err = fmt.Errorf("short keymap write")
	}
	if err == nil {
		_, err = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL)
	}
	if err != nil {
		unix.Close(fd)
		return -1, 0, err
	}
	return fd, uint32(n), nil
}

// setKeymap sends repeat info, and a new keymap when one is given, to every keyboard.
// A new keymap resets held keys and modifiers: the focused client gets leave, then
// enter with no keys.
func (s *Server) setKeymap(c ports.SetKeymap) {
	s.repeatRate, s.repeatDelay = c.RepeatRate, c.RepeatDelay
	if c.Keymap == "" {
		for _, list := range s.keyboards {
			for _, k := range list {
				if k.Version() >= 4 {
					k.SendRepeatInfo(int32(s.repeatRate), int32(s.repeatDelay))
				}
			}
		}
		s.log.Info().Int("rate", s.repeatRate).Int("delay", s.repeatDelay).Msg("repeat updated")
		return
	}
	fd, size, err := keymapFile(c.Keymap)
	if err != nil {
		s.log.Warn().Err(err).Msg("keymap update failed")
		return
	}
	if s.keymapFD >= 0 {
		unix.Close(s.keymapFD)
	}
	s.keymapFD, s.keymapSize = fd, size
	focused := s.focused
	s.changeFocus(0)
	s.heldKeys = nil
	s.modState = ports.ModState{}
	for _, list := range s.keyboards {
		for _, k := range list {
			k.SendKeymap(uint32(wayland.KeyboardKeymapFormatXkbV1), s.keymapFD, s.keymapSize)
			if k.Version() >= 4 {
				k.SendRepeatInfo(int32(s.repeatRate), int32(s.repeatDelay))
			}
		}
	}
	s.changeFocus(focused)
	s.log.Info().Uint32("size", size).Int("rate", s.repeatRate).Int("delay", s.repeatDelay).Msg("keymap updated")
}

func (s *Server) sendModifiers(k *wayland.Keyboard) {
	m := s.modState
	k.SendModifiers(s.serial, m.Depressed, m.Latched, m.Locked, m.Group)
}

func (s *Server) changeFocus(id ports.WindowID) {
	if surf, keyboards := s.focusTarget(s.focused); surf != nil {
		for _, k := range keyboards {
			s.serial++
			k.SendLeave(s.serial, surf)
		}
	}
	s.focused = 0
	if surf, keyboards := s.focusTarget(id); surf != nil {
		s.focused = id
		for _, k := range keyboards {
			s.serial++
			var keys []byte
			for code := range s.heldKeys {
				keys = binary.LittleEndian.AppendUint32(keys, code)
			}
			k.SendEnter(s.serial, surf, keys)
			s.sendModifiers(k)
		}
	}
	s.log.Debug().Uint64("id", uint64(s.focused)).Msg("keyboard focus")
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
			x, y := w.surfacePoint(x, y)
			for _, p := range s.windowPointers(w) {
				s.serial++
				p.SendEnter(s.serial, surface, server.FixedFromFloat(x), server.FixedFromFloat(y))
				pointerFrame(p)
			}
		}
	}
}
