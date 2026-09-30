package wayland

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bnema/purego-libwayland/protocol/relativepointer"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/neferwl/internal/sessionlock"
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
	// Clock runs idle notification timers; nil is the system clock.
	Clock ports.Clock
	// Security is display-owned protection control. Nil leaves session-lock
	// unadvertised; independent protocol tests remain unlocked.
	Security ports.SessionSecurityController
	// WorkspaceIDs names workspaces (ext-workspace id); nil draws a fresh
	// launch prefix. The state file must share the one of the session.
	WorkspaceIDs *workspaceid.IDs
	// syncDev replaces the render node's syncobj interface (tests).
	syncDev syncobjDevice
}

// Channels carries client notifications and commands. Events may be unbuffered.
type Channels struct {
	// SecurityChanges requests ordered backend protection/release. The
	// backend echoes actual inventory/proofs/barriers on SecurityEvents.
	SecurityChanges chan<- ports.SecurityState
	SecurityEvents  <-chan ports.SecurityBackendEvent
	LeaseRequests   chan<- ports.LeaseMessage
	LeaseEvents     <-chan ports.LeaseMessage
	Events          chan<- ports.ClientEvent
	Contents        chan<- ports.SurfaceContent
	Commands        <-chan ports.ClientCommand
	Workspaces      <-chan ports.Workspaces
	// Cursors receives the cursor the client under the pointer asks for.
	Cursors chan<- ports.CursorChange
	// Presented paces frame callbacks on the outputs' page flips; outputs
	// that do not flip (idle, headless) are paced at their refresh rate.
	Presented <-chan ports.OutputPresented
	// Captures carries accepted capture requests, never blocking. Each
	// accepted request holds one of ports.MaxCaptureInflight credits until its
	// CaptureDone arrives on Captured, whether or not the client is still
	// there; the backend must complete every request it takes. In production
	// both channels have capacity >= ports.MaxCaptureInflight, so replies
	// never block the backend; standalone tests may use smaller channels.
	Captures chan<- ports.CaptureRequest
	Captured <-chan ports.CaptureDone
	// OutputFormats are the outputs' direct scanout formats, offered in
	// dmabuf feedback to fullscreen surfaces.
	OutputFormats <-chan ports.OutputFormats
	OutputHeads   <-chan ports.OutputHeads
	OutputApply   chan<- ports.OutputApply
	OutputApplied <-chan ports.OutputApplied
}

type Server struct {
	security           ports.SessionSecurity
	securityController ports.SessionSecurityController
	securityPending    []ports.SecurityState // display-owned ordered backend outbox
	securityWake       chan struct{}
	sessionLock        *sessionLock
	lockReadiness      sessionlock.Readiness
	lockSurfaces       map[ports.WindowID]*lockSurface
	leaseDevices       map[string]*leaseDevice
	pendingLeases      map[uint64]*leaseObject
	pendingLeaseCards  map[uint64]string
	activeLeases       map[leaseKey]*leaseObject
	leaseMu            sync.Mutex // only the outbound lease queue; never window state
	leaseOut           []ports.LeaseMessage
	leaseReady         chan struct{}
	nextLeaseID        uint64
	nextCapture        uint64
	captureReplies     map[uint64]func(ports.CaptureDone)
	// captureInflight counts accepted captures until the backend completes
	// them, independent of client resources; only the display loop touches it.
	captureInflight map[uint64]struct{}
	captureSources  map[*server.Resource]*output
	captureSessions map[*captureSession]struct{}
	// private is the private capture session state (capture_session.go).
	private      privateCapture
	display      *server.Display
	env          procEnv
	slotsPending bool // core waits for a slot window
	name         string
	cleanup      func()
	log          zerowrap.Logger
	channels     Channels
	// awaiting holds frame callbacks by output name, due at its next
	// frame (frameDue, or its page flip).
	awaiting    map[string][]*wayland.Callback
	frameOwners map[*wayland.Callback]*surface
	frameDue    map[string]time.Time
	frameReady  chan struct{}
	// Pacer scratch: periods is used on the display goroutine, while
	// flipped and frameReports belong to pace and its synchronous Do call.
	framePeriods map[string]time.Duration
	flipped      map[string]bool
	frameReports []ports.OutputPresented
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
	graphDrawn          bool
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
	ctx               context.Context
	windows           map[ports.WindowID]*window
	layers            map[ports.WindowID]*layerSurface
	nextWindow        ports.WindowID
	nextPool          uint64
	nextSurface       uint64
	// seat is the keyboard and pointer state (seat.go).
	seat seatState
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
	idleNotes         []*idleNotification
	clock             ports.Clock
	powers            []*outputPower
	outputsOff        map[string]bool
	contentNotify     chan struct{}
	contentReady      chan struct{}
	outputs           []*output
	outputManagers    []*outputManager
	workspaceManagers []*workspaceManager
	toplevelManagers  []*toplevelManager
	workspaceSnapshot ports.Workspaces
	workspaceIDs      *workspaceid.IDs
	outputHeads       ports.OutputHeads
	outputPlaces      ports.Layout
	managementSerial  uint32
	nextOutputApply   uint64
	outputReplies     map[uint64]*outputConfiguration
	focusedOutput     string
	fractions         map[*surface]*fractionalscale.WpFractionalScaleV1
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
	// Text input and input method (textinput.go): activeText is the
	// enabled text input the input method serves.
	textInputs  []*textInput
	activeText  *textInput
	inputMethod *inputMethod
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
	s := &Server{securityWake: make(chan struct{}, 1), security: opts.Security, securityController: opts.Security, display: d, awaiting: map[string][]*wayland.Callback{}, frameDue: map[string]time.Time{}, frameReady: make(chan struct{}, 1), reports: map[string]ports.OutputPresented{}, env: linuxProcEnv{}, name: name, cleanup: cleanup, log: log, channels: ch, surfaces: make(map[*server.Resource]*surface), buffers: make(map[*server.Resource]clientBuffer), windows: make(map[ports.WindowID]*window), layers: make(map[ports.WindowID]*layerSurface), nextWindow: 1, eventReady: make(chan struct{}, 1), contents: make(map[ports.WindowID]ports.SurfaceContent), contentSeq: make(map[ports.WindowID]uint64), damage: map[ports.WindowID][]ports.SeqDamage{}, contentReady: make(chan struct{}, 1), cursorReady: make(chan struct{}, 1), dataSources: map[*server.Resource]*clipSource{}, primarySources: map[*server.Resource]*clipSource{}, controlSources: map[*server.Resource]*clipSource{}, contentNotify: make(chan struct{}), regions: map[*server.Resource]*region{}, relatives: map[server.Client][]*relativepointer.ZwpRelativePointerV1{}, constraints: map[*surface]*constraint{}, positioners: map[*server.Resource]*positioner{}, seat: seatState{keymapFD: -1, keyboards: make(map[server.Client][]*wayland.Keyboard), pointers: make(map[server.Client][]*wayland.Pointer), enters: make(map[*server.Resource]uint32), repeatRate: opts.RepeatRate, repeatDelay: opts.RepeatDelay}}
	s.leaseDevices = map[string]*leaseDevice{}
	s.pendingLeases = map[uint64]*leaseObject{}
	s.pendingLeaseCards = map[uint64]string{}
	s.activeLeases = map[leaseKey]*leaseObject{}
	s.leaseReady = make(chan struct{}, 1)
	s.workspaceIDs = opts.WorkspaceIDs
	if s.workspaceIDs == nil {
		s.workspaceIDs = workspaceid.New()
	}
	s.clock = opts.Clock
	if s.clock == nil {
		s.clock = clock.System{}
	}
	s.captureReplies = map[uint64]func(ports.CaptureDone){}
	s.captureInflight = map[uint64]struct{}{}
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
	// Implicit dma-buf fences also need this waiter when explicit sync is off.
	if s.syncWait == nil {
		if s.syncWait, err = newSyncWaiter(s.wakePacer); err != nil {
			cleanup()
			d.Close()
			return nil, err
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
		s.seat.keymapFD, s.seat.keymapSize, s.seat.keymapText = fd, size, opts.Keymap
	}
	s.syncWait.log = log
	s.cleanup = func() {
		if s.seat.keymapFD >= 0 {
			unix.Close(s.seat.keymapFD)
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
	defer func() {
		for _, device := range s.leaseDevices {
			if device.fd != nil {
				device.fd.Close()
			}
		}
	}()
	s.log.Info().Str("socket", s.name).Msg("starting wayland")
	s.started = time.Now()
	s.ctx = ctx
	var wg sync.WaitGroup
	wg.Add(14)
	go func() { defer wg.Done(); s.forwardBackendSecurity(ctx) }()
	go func() { defer wg.Done(); s.forwardSecurityEvents(ctx) }()
	go func() { defer wg.Done(); s.forwardLeases(ctx) }()
	go func() { defer wg.Done(); s.forwardLeaseRequests(ctx) }()
	go func() { defer wg.Done(); s.forwardWorkspaces(ctx) }()
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
	if envelope, ok := cmd.(ports.SecurityCommand); ok {
		if s.security == nil || envelope.State != s.security.Snapshot() {
			return
		}
		cmd = envelope.Command
	} else if s.security != nil && s.security.Snapshot().Generation != 0 {
		// Only input/focus commands require an owner envelope. Config relay
		// also produces SetKeymap and other epoch-independent commands.
		switch cmd.(type) {
		case ports.PointerFocus, ports.PointerMotionTo, ports.PointerButtonTo, ports.PointerAxisTo, ports.FocusWindow, ports.ForwardKey:
			return
		}
	}
	switch c := cmd.(type) {
	case ports.PointerFocus, ports.PointerMotionTo, ports.PointerButtonTo, ports.PointerAxisTo, ports.SetKeymap, ports.FocusWindow, ports.ForwardKey:
		s.applyInput(c)
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
		// Callbacks move when the tree stops or starts being throttled: on
		// Visible or on Captured.
		visibilityChanged := w.hasLast && (w.last.Visible != c.Visible || w.last.Captured != c.Captured)
		scanoutChanged := !w.hasLast || w.last.Fullscreen != c.Fullscreen || w.last.Output != c.Output || w.last.Visible != c.Visible
		w.last, w.hasLast = c, true
		if visibilityChanged {
			s.relocateCallbacks(w.xdg.surface)
			s.dropFeedbacks(time.Now())
			s.wakePacer() // reconsider FIFO barriers at the new period
		}
		w.sendConfigure()
		s.toplevelChanged(w)
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
	case ports.CaptureSessionState:
		s.captureState(c)
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
