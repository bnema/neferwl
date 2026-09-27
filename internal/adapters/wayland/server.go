package wayland

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/bnema/purego-libwayland/protocol/relativepointer"
	"os"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/ports"
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
	DMABuf ports.DMABufSupport
	// SyncobjNode is the render node (/dev/dri/renderD*) timeline
	// syncobjs are imported on; empty or without timeline support, no
	// explicit sync is offered.
	SyncobjNode             string
	Keymap                  string
	RepeatRate, RepeatDelay int
	// syncDev replaces the render node's syncobj interface (tests).
	syncDev syncobjDevice
}

// Channels carries client notifications and commands. Events may be unbuffered.
type Channels struct {
	Events   chan<- ports.ClientEvent
	Contents chan<- ports.SurfaceContent
	Commands <-chan ports.ClientCommand
	// Cursors receives the cursor the client under the pointer asks for.
	Cursors chan<- ports.CursorChange
	// Presented paces frame callbacks on the outputs' page flips; outputs
	// that do not flip (idle, headless) are paced at their refresh rate.
	Presented <-chan ports.OutputPresented
	Captures  chan<- ports.CaptureRequest
	Captured  <-chan ports.CaptureDone
	// OutputFormats are the outputs' direct scanout formats, offered in
	// dmabuf feedback to fullscreen surfaces.
	OutputFormats <-chan ports.OutputFormats
	OutputHeads   <-chan ports.OutputHeads
	OutputApply   chan<- ports.OutputApply
	OutputApplied <-chan ports.OutputApplied
}
type Server struct {
	nextCapture     uint64
	captureReplies  map[uint64]func(ports.CaptureDone)
	captureSources  map[*server.Resource]*output
	captureSessions map[*captureSession]struct{}
	display         *server.Display
	env             procEnv
	slotsPending    bool // core waits for a slot window
	name            string
	cleanup         func()
	log             zerowrap.Logger
	channels        Channels
	// awaiting holds frame callbacks by output name, due at its next
	// frame (frameDue, or its page flip).
	awaiting   map[string][]*wayland.Callback
	frameDue   map[string]time.Time
	frameReady chan struct{}
	// held buffers wait until no output reads them (release.go); reports
	// are the outputs' latest ports.OutputPresented.
	held    []heldBuffer
	reports map[string]ports.OutputPresented
	frames  uint64
	started time.Time
	// fifoSurfaces have a fifo barrier or queued commits (fifo.go);
	// lastFlip is each output's latest page flip.
	fifoSurfaces        map[*surface]struct{}
	applyingGraph       bool
	graphFeedback       []graphFeedback
	readinessGeneration uint64
	lastFlip            map[string]time.Time
	// tokens are the issued xdg-activation tokens (activation.go).
	tokens   map[string]activationToken
	surfaces map[*server.Resource]*surface
	buffers  map[*server.Resource]clientBuffer
	dmabuf   *dmabufGlobal
	// hdrOutputs records confirmed DRM modesets; absent outputs are SDR.
	hdrOutputs        map[string]ports.OutputHDR
	colorID           uint64
	colorIdentity     map[string]uint64
	colorDescriptions map[*server.Resource]colorDescription
	colorOutputs      map[*colorOutput]struct{}
	colorFeedbacks    map[*colorFeedback]struct{}
	serial            uint32
	// press is the serial of the last button or key press, sent to
	// pressClient: popup grabs must come from it.
	press       uint32
	pressClient server.Client
	// pressAt and focusAt date the last press and keyboard focus change,
	// for xdg-activation tokens.
	pressAt, focusAt        time.Time
	ctx                     context.Context
	windows                 map[ports.WindowID]*window
	layers                  map[ports.WindowID]*layerSurface
	nextWindow              ports.WindowID
	nextPool                uint64
	nextSurface             uint64
	focused                 ports.WindowID
	pointerFocus            ports.WindowID
	wheelRest               [2]int32   // v120 not yet sent as axis_discrete, per axis
	wheelHeld               [2]float64 // axis value held back with it for pre-v8 clients
	pointerX, pointerY      float64
	pointers                map[server.Client][]*wayland.Pointer
	modState                ports.ModState
	keyboards               map[server.Client][]*wayland.Keyboard
	heldKeys                map[uint32]bool
	keymapFD                int
	keymapSize              uint32
	keymapText              string           // the seat keymap, to compare virtual keymaps with
	keymapOwner             *virtualKeyboard // nil: keyboards carry the seat keymap
	repeatRate, repeatDelay int
	// eventMu protects only the notification queue, not display-owned window state.
	// The event queue is unbounded by design: it grows only if core stops draining
	// Events, which happens only at shutdown (core owns the receiving end). A
	// stalled core is a bug surfaced by ctx cancellation, not a reason to block
	// the display goroutine.
	eventMu    sync.Mutex
	events     []ports.ClientEvent
	eventReady chan struct{}
	contentMu  sync.Mutex
	contents   map[ports.WindowID]ports.SurfaceContent
	contentSeq map[ports.WindowID]uint64
	// damage is each window's recent damage (contentMu).
	damage map[ports.WindowID][]ports.SeqDamage
	// feedbacks wait for the flip that shows their content (presentation.go).
	feedbacks []feedbackWait
	// Explicit sync (syncobj.go): the render node, imported timelines by
	// resource, and the acquire point waiter.
	syncDev   syncobjDevice
	timelines map[*server.Resource]*timeline
	syncWait  *syncWaiter
	// flips is each output's latest flip (pacer, under Do).
	flips map[string]ports.FlipInfo
	// inhibitors are the live inhibitor objects (inhibit.go);
	// shortcutWindows and idleWindows what core was told.
	inhibitors                   []*inhibitor
	shortcutWindows, idleWindows map[ports.WindowID]bool
	// idleNotes and powers are the idle notifications and output power
	// objects (idle.go); outputsOff the outputs core turned off.
	idleNotes        []*idleNotification
	powers           []*outputPower
	outputsOff       map[string]bool
	contentNotify    chan struct{}
	contentReady     chan struct{}
	outputs          []*output
	outputManagers   []*outputManager
	outputHeads      ports.OutputHeads
	outputPlaces     ports.Layout
	managementSerial uint32
	nextOutputApply  uint64
	outputReplies    map[uint64]*outputConfiguration
	focusedOutput    string
	fractions        map[*surface]*fractionalscale.WpFractionalScaleV1
	// cursorSurface is the wl_pointer.set_cursor surface in use.
	cursorSurface *surface
	// cursorMu guards only the latest cursor change for forwardCursors.
	cursorMu     sync.Mutex
	cursorLatest ports.CursorChange
	cursorQueued bool
	cursorReady  chan struct{}
	// Clipboard and primary selection (clipboard.go).
	selections                                  [2]*clipSource
	clipDevices                                 []*clipDevice
	dataSources, primarySources, controlSources map[*server.Resource]*clipSource
	// Pointer constraints (constraints.go).
	regions     map[*server.Resource]*region
	relatives   map[server.Client][]*relativepointer.ZwpRelativePointerV1
	constraints map[*surface]*constraint
	constraint  *constraint // the active one
	positioners map[*server.Resource]*positioner
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
	s := &Server{display: d, awaiting: map[string][]*wayland.Callback{}, frameDue: map[string]time.Time{}, frameReady: make(chan struct{}, 1), reports: map[string]ports.OutputPresented{}, env: linuxProcEnv{}, name: name, cleanup: cleanup, log: log, channels: ch, surfaces: make(map[*server.Resource]*surface), buffers: make(map[*server.Resource]clientBuffer), windows: make(map[ports.WindowID]*window), layers: make(map[ports.WindowID]*layerSurface), nextWindow: 1, eventReady: make(chan struct{}, 1), contents: make(map[ports.WindowID]ports.SurfaceContent), contentSeq: make(map[ports.WindowID]uint64), damage: map[ports.WindowID][]ports.SeqDamage{}, contentReady: make(chan struct{}, 1), cursorReady: make(chan struct{}, 1), dataSources: map[*server.Resource]*clipSource{}, primarySources: map[*server.Resource]*clipSource{}, controlSources: map[*server.Resource]*clipSource{}, contentNotify: make(chan struct{}), keymapFD: -1, keyboards: make(map[server.Client][]*wayland.Keyboard), pointers: make(map[server.Client][]*wayland.Pointer), regions: map[*server.Resource]*region{}, relatives: map[server.Client][]*relativepointer.ZwpRelativePointerV1{}, constraints: map[*surface]*constraint{}, positioners: map[*server.Resource]*positioner{}, repeatRate: opts.RepeatRate, repeatDelay: opts.RepeatDelay}
	s.captureReplies = map[uint64]func(ports.CaptureDone){}
	s.outputReplies = map[uint64]*outputConfiguration{}
	s.managementSerial = 1
	s.captureSources = map[*server.Resource]*output{}
	s.captureSessions = map[*captureSession]struct{}{}
	s.fractions = map[*surface]*fractionalscale.WpFractionalScaleV1{}
	s.fifoSurfaces, s.lastFlip = map[*surface]struct{}{}, map[string]time.Time{}
	s.flips = map[string]ports.FlipInfo{}
	s.timelines = map[*server.Resource]*timeline{}
	var node *renderNode
	if opts.syncDev != nil {
		s.syncDev = opts.syncDev
		if s.syncWait, err = newSyncWaiter(s.wakePacer); err != nil {
			cleanup()
			d.Close()
			return nil, err
		}
	} else if opts.SyncobjNode != "" {
		if node, err = openSyncobj(opts.SyncobjNode); err != nil {
			log.Info().Str("component", "wayland").Err(err).Msg("explicit sync off")
			node = nil
		} else if s.syncWait, err = newSyncWaiter(s.wakePacer); err != nil {
			node.close()
			node = nil
		} else {
			s.syncDev = node
		}
	}
	s.colorIdentity = map[string]uint64{}
	s.colorDescriptions = map[*server.Resource]colorDescription{}
	s.colorOutputs = map[*colorOutput]struct{}{}
	s.colorFeedbacks = map[*colorFeedback]struct{}{}
	s.tokens = map[string]activationToken{}
	if opts.Keymap != "" {
		fd, size, e := keymapFile(opts.Keymap)
		if e != nil {
			cleanup()
			d.Close()
			return nil, e
		}
		s.keymapFD, s.keymapSize, s.keymapText = fd, size, opts.Keymap
	}
	s.cleanup = func() {
		if s.keymapFD >= 0 {
			unix.Close(s.keymapFD)
		}
		s.dmabuf.close()
		if s.syncWait != nil {
			s.syncWait.close()
		}
		if node != nil {
			node.close()
		}
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
	wg.Add(9)
	go func() { defer wg.Done(); s.forward(ctx) }()
	go func() { defer wg.Done(); s.forwardCursors(ctx) }()
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
	go func() { defer wg.Done(); s.pace(ctx) }()
	go func() { defer wg.Done(); s.forwardOutputFormats(ctx) }()
	go func() { defer wg.Done(); s.forwardOutputHeads(ctx) }()
	go func() { defer wg.Done(); s.forwardOutputApplied(ctx) }()
	go func() { defer wg.Done(); s.forwardCaptured(ctx.Done()) }()
	if s.syncWait != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.syncWait.run(ctx) }()
	}
	err := s.display.Run(ctx)
	wg.Wait()
	return err
}

// forwardOutputHeads delivers backend inventory on the display owner goroutine.
func (s *Server) forwardOutputHeads(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case heads, ok := <-s.channels.OutputHeads:
			if !ok {
				return
			}
			if !s.display.Do(func() { s.setOutputHeads(heads) }) {
				return
			}
		}
	}
}

func (s *Server) forwardOutputApplied(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case result, ok := <-s.channels.OutputApplied:
			if !ok {
				return
			}
			if !s.display.Do(func() {
				c := s.outputReplies[result.ID]
				delete(s.outputReplies, result.ID)
				if c == nil || !c.res.Alive() {
					return
				}
				if result.Err != nil {
					c.res.SendFailed()
				} else {
					c.res.SendSucceeded()
				}
			}) {
				return
			}
		}
	}
}

// damageHistory is how many contents a window's damage history covers:
// enough for a renderer target a few frames old.
const damageHistory = 4

// forwardOutputFormats applies the outputs' scanout formats on the
// display goroutine.
func (s *Server) forwardOutputFormats(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case f, ok := <-s.channels.OutputFormats:
			if !ok {
				return
			}
			if !s.display.Do(func() {
				s.setOutputHDR(f)
				if s.dmabuf != nil {
					s.dmabuf.setOutputFormats(f)
				}
			}) {
				return
			}
		}
	}
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

func (s *Server) emitContent(c ports.SurfaceContent, d damage) {
	if s.channels.Contents == nil {
		return
	}
	s.contentMu.Lock()
	s.contentSeq[c.ID]++
	c.Seq = s.contentSeq[c.ID]
	// Copy-on-write: previous publications can still be in an output's hands.
	old := s.damage[c.ID]
	if len(old) >= damageHistory {
		old = old[len(old)-damageHistory+1:]
	}
	h := make([]ports.SeqDamage, len(old)+1)
	copy(h, old)
	h[len(old)] = ports.SeqDamage{Seq: c.Seq, Full: d.full, Rects: d.rects}
	if c.Empty() {
		delete(s.damage, c.ID)
	} else {
		s.damage[c.ID] = h
	}
	c.DamageHistory = h
	s.contents[c.ID] = c
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
// pointer motion for the same window collapsed into the latest one, their
// relative deltas summed. open is
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
					// The position is the latest; relative deltas add up so
					// locked pointers (games) lose no movement.
					m.DX += last.DX
					m.DY += last.DY
					m.UnaccelDX += last.UnaccelDX
					m.UnaccelDY += last.UnaccelDY
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
		if l := s.layers[c.ID]; l != nil && c.ID == s.pointerFocus {
			s.pointerX, s.pointerY = c.X, c.Y
			for _, p := range s.layerPointers(l) {
				p.SendMotion(c.TimeMsec, server.FixedFromFloat(c.X), server.FixedFromFloat(c.Y))
				pointerFrame(p)
			}
			return
		}
		if w := s.windows[c.ID]; w != nil && c.ID == s.pointerFocus {
			if s.relativeMotion(w, c) && s.locked(w) {
				for _, p := range s.windowPointers(w) {
					pointerFrame(p)
				}
			}
			if s.locked(w) {
				// Only relative motion reaches a locked pointer.
				return
			}
			x, y := w.surfacePoint(c.X, c.Y)
			s.pointerX, s.pointerY = c.X, c.Y
			// A constraint waits for the pointer to enter its region.
			if s.constraint == nil {
				s.updateConstraint()
			}
			for _, p := range s.windowPointers(w) {
				p.SendMotion(c.TimeMsec, server.FixedFromFloat(x), server.FixedFromFloat(y))
				pointerFrame(p)
			}
		}
	case ports.PointerButtonTo:
		// Releases may target the implicit-grab window after focus has moved.
		if client, pointers, ok := s.pointerTarget(c.ID); ok {
			s.serial++
			state := uint32(0)
			if c.Pressed {
				state = 1
				s.press, s.pressClient, s.pressAt = s.serial, client, time.Now()
			}
			for _, p := range pointers {
				p.SendButton(s.serial, c.TimeMsec, c.Button, state)
				pointerFrame(p)
			}
		}
	case ports.PointerAxisTo:
		// Scroll applies to the entered surface only.
		if c.ID != s.pointerFocus {
			return
		}
		steps, values := s.wheelSteps(c.Axis)
		_, pointers, _ := s.pointerTarget(c.ID)
		for _, p := range pointers {
			sendAxis(p, c.Axis, steps, values)
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
		s.useKeymap(nil)
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
		if c.Key.Pressed {
			if surf, _ := s.focusTarget(c.ID); surf != nil {
				s.press, s.pressClient, s.pressAt = s.serial, surf.Client(), time.Now()
			}
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
	case ports.ConfigurePopup:
		if w := s.windows[c.ID]; w != nil && w.popup != nil {
			w.popup.configure(c)
		}
	case ports.ClosePopup:
		if w := s.windows[c.ID]; w != nil && w.popup != nil {
			w.popup.dismiss()
		}
	case ports.ConfigureWindow:
		w := s.windows[c.ID]
		if w == nil || w.toplevel == nil || !w.toplevel.Resource.Alive() || !w.xdg.resource.Resource.Alive() {
			s.log.Debug().Uint64("id", uint64(c.ID)).Msg("configure missing window")
			return
		}
		s.log.Info().Uint64("id", uint64(c.ID)).Int("w", c.Width).Int("h", c.Height).Bool("fullscreen", c.Fullscreen).Bool("activated", c.Activated).Msg("configure")
		scanoutChanged := !w.hasLast || w.last.Fullscreen != c.Fullscreen || w.last.Output != c.Output
		w.last, w.hasLast = c, true
		w.sendConfigure()
		if scanoutChanged && w.xdg.surface != nil {
			s.dmabuf.resendSurface(w.xdg.surface)
		}
		if surf := w.xdg.surface; surf != nil && c.Output != "" {
			surf.sendTreeScale()
		}
		if scanoutChanged {
			s.notifyColorFeedback()
		}
	case ports.SetOutputs:
		s.setOutputs(c)
		off := map[string]bool{}
		for _, name := range c.Off {
			off[name] = true
		}
		s.setOutputsOff(off)
	case ports.UserActivity:
		s.userActivity()
	case ports.SlotsPending:
		s.slotsPending = c.Pending
	case ports.ShortcutsInhibitState:
		s.setShortcutsInhibit(c)
	case ports.CloseWindow:
		w := s.windows[c.ID]
		if w == nil || w.toplevel == nil || !w.toplevel.Resource.Alive() {
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
		if surf := l.surfaceResource(); surf != nil {
			return surf, s.clientKeyboards(l.resource.Client())
		}
	}
	return nil, nil
}

// pointerTarget resolves a mapped window or layer ID to its client and
// live pointers.
func (s *Server) pointerTarget(id ports.WindowID) (server.Client, []*wayland.Pointer, bool) {
	if w := s.windows[id]; w != nil && w.mapped && w.xdg.resource.Resource.Alive() {
		return w.xdg.resource.Client(), s.windowPointers(w), true
	}
	if l := s.layers[id]; l != nil && l.mapped && l.resource.Resource.Alive() {
		return l.resource.Client(), s.layerPointers(l), true
	}
	return server.Client{}, nil, false
}

// pointerSurface is the wl_surface of a mapped window or layer and the
// point in its surface coordinates.
func (s *Server) pointerSurface(id ports.WindowID, x, y float64) (*wayland.Surface, []*wayland.Pointer, float64, float64) {
	if w := s.windows[id]; w != nil {
		if surf := w.xdg.surfaceResource(); surf != nil && surf.Resource.Alive() {
			x, y := w.surfacePoint(x, y)
			return surf, s.windowPointers(w), x, y
		}
	}
	if l := s.layers[id]; l != nil && l.mapped {
		if surf := l.surfaceResource(); surf != nil {
			return surf, s.layerPointers(l), x, y
		}
	}
	return nil, nil, 0, 0
}

// keymapFile writes a sealed memfd holding the NUL-terminated keymap.
func keymapFile(keymap string) (int, uint32, error) {
	if uint64(len(keymap))+1 > uint64(^uint32(0)) {
		return -1, 0, fmt.Errorf("keymap too large")
	}
	fd, err := unix.MemfdCreate("neferwl-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
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
	s.keymapFD, s.keymapSize, s.keymapText = fd, size, c.Keymap
	s.keymapOwner = nil
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
	// Enter carries seat held keys and modifiers: they need the seat keymap.
	s.useKeymap(nil)
	old := s.focusClient()
	defer func() {
		// Tokens of a client losing the focus die with it.
		if now := s.focusClient(); now != old {
			s.dropTokens(old)
			s.focusAt = time.Now()
		}
	}()
	if surf, keyboards := s.focusTarget(s.focused); surf != nil {
		for _, k := range keyboards {
			s.serial++
			k.SendLeave(s.serial, surf)
		}
	}
	s.focused = 0
	if surf, keyboards := s.focusTarget(id); surf != nil {
		s.focused = id
		s.focusSelections()
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
	s.updateConstraint()
}

// wheelSteps adds wheel v120 to the rest per axis and returns the whole
// detents for axis_discrete (seat before v8). High-resolution wheels send
// fractions of 120; a direction change drops the rest, as wlroots does.
// wheelHeld keeps the axis value of the frames without a step; values is
// what a pre-v8 client gets with a step: the held value plus this frame's.
func (s *Server) wheelSteps(a ports.PointerAxis) (steps [2]int32, values [2]float64) {
	if a.Source != ports.AxisWheel {
		return steps, values
	}
	for i, ax := range [2]ports.ScrollAxis{a.Vertical, a.Horizontal} {
		if !ax.Set || ax.V120 == 0 {
			continue
		}
		if (s.wheelRest[i] < 0) != (ax.V120 < 0) {
			s.wheelRest[i], s.wheelHeld[i] = 0, 0
		}
		s.wheelRest[i] += ax.V120
		s.wheelHeld[i] += ax.Value
		steps[i] = s.wheelRest[i] / 120
		s.wheelRest[i] -= steps[i] * 120
		if steps[i] != 0 {
			values[i], s.wheelHeld[i] = s.wheelHeld[i], 0
		}
	}
	return steps, values
}

// sendAxis sends one scroll frame, each event gated by the pointer version.
// steps are whole wheel detents for axis_discrete. A pre-v8 client gets a
// wheel axis only with a step, carrying values (all the value since the
// last step), so it never counts smooth and discrete scroll.
func sendAxis(p *wayland.Pointer, a ports.PointerAxis, steps [2]int32, values [2]float64) {
	v := p.Version()
	axes := [2]ports.ScrollAxis{a.Vertical, a.Horizontal}
	for i := range axes {
		if v < 8 && a.Source == ports.AxisWheel && axes[i].V120 != 0 && steps[i] == 0 {
			axes[i].Set = false
		}
	}
	if !axes[0].Set && !axes[1].Set {
		return
	}
	if v >= 5 {
		p.SendAxisSource(uint32(a.Source))
	}
	for axis, s := range axes {
		if !s.Set {
			continue
		}
		if s.Stop {
			if v >= 5 {
				p.SendAxisStop(a.TimeMsec, uint32(axis))
			}
			continue
		}
		value := s.Value
		if a.Source == ports.AxisWheel && s.V120 != 0 {
			switch {
			case v >= 8:
				p.SendAxisValue120(uint32(axis), s.V120)
			default:
				value = values[axis]
				if v >= 5 {
					p.SendAxisDiscrete(uint32(axis), steps[axis])
				}
			}
		}
		p.SendAxis(a.TimeMsec, uint32(axis), server.FixedFromFloat(value))
	}
	pointerFrame(p)
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
	return s.clientPointers(w.xdg.resource.Client())
}

func (s *Server) layerPointers(l *layerSurface) []*wayland.Pointer {
	if l == nil || !l.mapped || !l.resource.Resource.Alive() {
		return nil
	}
	return s.clientPointers(l.resource.Client())
}

func (s *Server) clientPointers(c server.Client) []*wayland.Pointer {
	var result []*wayland.Pointer
	for _, p := range s.pointers[c] {
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
	s.wheelRest, s.wheelHeld = [2]int32{}, [2]float64{}
	// The new client sets its own cursor on enter; until then, the arrow.
	s.cursorSurface = nil
	s.setCursor(ports.CursorChange{})
	if surface, pointers, _, _ := s.pointerSurface(s.pointerFocus, 0, 0); surface != nil {
		for _, p := range pointers {
			s.serial++
			p.SendLeave(s.serial, surface)
			pointerFrame(p)
		}
	}
	s.pointerFocus = 0
	if surface, pointers, sx, sy := s.pointerSurface(id, x, y); surface != nil {
		s.pointerFocus, s.pointerX, s.pointerY = id, x, y
		for _, p := range pointers {
			s.serial++
			p.SendEnter(s.serial, surface, server.FixedFromFloat(sx), server.FixedFromFloat(sy))
			pointerFrame(p)
		}
	}
	s.updateConstraint()
}
