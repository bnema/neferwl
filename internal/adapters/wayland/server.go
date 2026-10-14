package wayland

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/go-wayland-bindings/server/relativepointer"

	"github.com/bnema/go-wayland-bindings/server/fractionalscale"
	"github.com/bnema/go-wayland-bindings/server/inputtimestamps"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/wayland/sessionlock"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/ports"
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
	// CaptureAllow is the executable allowlist for screen capture (captureauth.go);
	// nil lets every client capture, unless the session is protected. The
	// caller keeps it current (captureallow.Store.Run).
	CaptureAllow *captureallow.Store
	// LogDamage logs the damage of every fresh client commit at debug level
	// (--debug=wayland-damage).
	LogDamage bool
	// syncDev replaces the render node's syncobj interface (tests).
	syncDev syncobjDevice
	// peer replaces the peer executable lookup (tests).
	peer peerExe
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
	Commands        <-chan ports.ClientCommand
	Workspaces      <-chan ports.Workspaces
	// Cursors receives the cursor the client under the pointer asks for.
	Cursors chan<- ports.CursorChange
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
	// IdleInhibited reports whether D-Bus clients (org.freedesktop.ScreenSaver)
	// inhibit idle; while true, it holds idle notifications like an inhibitor
	// surface does.
	IdleInhibited <-chan bool
	// IdleActivity carries D-Bus SimulateUserActivity calls: idle
	// notifications resume and restart as on input.
	IdleActivity <-chan struct{}
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
	// capturedRing hands backend replies to the display goroutine
	// (forwardCaptured); the display goroutine applies them in order.
	capturedRing *postRing[ports.CaptureDone]
	// captureInflight counts accepted captures until the backend completes
	// them, independent of client resources; only the display loop touches it.
	captureInflight map[uint64]struct{}
	captureSources  map[*server.Resource]*captureSource
	captureSessions map[*captureSession]struct{}
	// sessionsByID are the sessions registered with core (imagecopy.go);
	// excl is the one live exclusion (exclusion.go); ownUID the compositor's.
	sessionsByID map[uint64]*captureSession
	nextSession  uint64
	excl         *exclusion
	ownUID       uint32
	// captureAllow, peer and peers decide mayCapture (captureauth.go):
	// peers keeps each client's pidfd until it is destroyed.
	captureAllow *captureallow.Store
	peer         peerExe
	peers        map[server.Client]*peerID
	// noPidfdWarned: the missing SO_PEERPIDFD is logged once.
	noPidfdWarned bool
	// sandboxed are the clients that connected through a security context
	// (securitycontext.go); display goroutine only. securityStop is
	// written once at shutdown to end the listeners, securityWG waits for
	// them, securityLive counts them.
	sandboxed    map[server.Client]*sandbox
	securityStop int
	securityWG   sync.WaitGroup
	securityLive atomic.Int32
	display      *server.Display
	env          procEnv
	slotsPending bool // core waits for a slot window
	name         string
	cleanup      func()
	log          zerowrap.Logger
	logDamage    bool
	channels     Channels
	// awaiting holds frame callbacks by output name, due at its next
	// frame (frameDue, or its page flip).
	awaiting    map[string][]*wayland.Callback
	frameOwners map[*wayland.Callback]*surface
	frameDue    map[string]time.Time
	// paceFD is the pace deadline timerfd, armed by ticks on the display
	// goroutine (paceReady: registered with the display loop);
	// paceDepth/paceDirty defer wakePacer inside a commit or tick (display
	// goroutine only). paceSignal coalesces posted ticks (paceSignalFn).
	paceFD     int
	paceReady  bool
	paceWarned bool // a pace timer failure was logged (once)
	// paceArmed: the timerfd is armed for paceDeadline (display goroutine
	// only); armPace and stopPace skip a syscall that would change nothing.
	paceArmed    bool
	paceDeadline time.Time
	paceSignal   atomic.Bool
	paceSignalFn func()
	paceDepth    int
	paceDirty    bool
	// sink receives the outputs' reports (presented.go).
	sink *presentSink
	// framePeriods is the tick's scratch (display goroutine only).
	framePeriods map[string]time.Duration
	// held buffers wait until no output reads them (release.go); reports
	// are the outputs' latest ports.OutputPresented.
	held []heldBuffer
	// heldLive is releaseHeld's scratch (display goroutine only).
	heldLive []ports.OutputPresented
	reports  map[string]ports.OutputPresented
	frames   uint64
	started  time.Time
	// fifoSurfaces have a fifo barrier or queued commits (fifo.go);
	// lastFlip is each output's latest page flip.
	fifoSurfaces        map[*surface]struct{}
	applyingGraph       bool
	graphDrawn          bool
	graphFeedback       []graphFeedback
	readinessGeneration uint64
	lastFlip            map[string]time.Time
	// tokens are the issued xdg-activation tokens (activation.go).
	tokens map[string]activationToken
	// foreignExports are the live xdg-foreign handles (foreign.go).
	foreignExports map[string]*foreignExport
	surfaces       map[*server.Resource]*surface
	buffers        map[*server.Resource]clientBuffer
	dmabuf         *dmabufGlobal
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
	// contentSeq counts each window's contents and damage is its recent
	// damage; latest is its current content. All three belong to the display
	// goroutine, as do the outputs' content links (contentlink.go).
	contentSeq map[ports.WindowID]uint64
	damage     map[ports.WindowID][]ports.SeqDamage
	latest     map[ports.WindowID]ports.SurfaceContent
	links      []*contentLink
	// flushPosted: a flushLinks is posted and has not started (Return).
	flushPosted  atomic.Bool
	flushLinksFn func()
	// feedbacks wait for the flip that shows their content (presentation.go).
	feedbacks []feedbackWait
	// Explicit sync (syncobj.go): the render node, imported timelines by
	// resource, and the acquire point waiter.
	syncDev   syncobjDevice
	timelines map[*server.Resource]*timeline
	syncWait  *syncWaiter
	// flips is each output's latest flip (display goroutine).
	flips map[string]ports.FlipInfo
	// inhibitors are the live inhibitor objects (inhibit.go);
	// shortcutWindows and idleWindows what core was told.
	inhibitors                   []*inhibitor
	shortcutWindows, idleWindows map[ports.WindowID]bool
	// busInhibited is the last IdleInhibited value (idle.go).
	busInhibited bool
	// idleNotes and powers are the idle notifications and output power
	// objects (idle.go); outputsOff the outputs core turned off.
	idleNotes         []*idleNotification
	clock             ports.Clock
	powers            []*outputPower
	outputsOff        map[string]bool
	outputs           []*output
	outputManagers    []*outputManager
	workspaceManagers []*workspaceManager
	toplevelManagers  []*toplevelManager
	extToplevelLists  []*extToplevelList
	workspaceSnapshot ports.Workspaces
	workspaceFrames   []*workspaceFrame
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
	regions   map[*server.Resource]*region
	relatives map[server.Client][]*relativepointer.ZwpRelativePointerV1
	// gestures are each client's zwp_pointer_gestures_v1 objects (gestures.go).
	gestures    map[server.Client][]*gestureObject
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
	s := &Server{
		securityWake:       make(chan struct{}, 1),
		security:           opts.Security,
		securityController: opts.Security,
		display:            d,
		awaiting:           map[string][]*wayland.Callback{},
		frameDue:           map[string]time.Time{},
		reports:            map[string]ports.OutputPresented{},
		env:                linuxProcEnv{},
		name:               name,
		cleanup:            cleanup,
		log:                log,
		logDamage:          opts.LogDamage,
		channels:           ch,
		surfaces:           make(map[*server.Resource]*surface),
		buffers:            make(map[*server.Resource]clientBuffer),
		windows:            make(map[ports.WindowID]*window),
		layers:             make(map[ports.WindowID]*layerSurface),
		nextWindow:         1,
		eventReady:         make(chan struct{}, 1),
		contentSeq:         make(map[ports.WindowID]uint64),
		damage:             map[ports.WindowID][]ports.SeqDamage{},
		latest:             make(map[ports.WindowID]ports.SurfaceContent),
		cursorReady:        make(chan struct{}, 1),
		dataSources:        map[*server.Resource]*clipSource{},
		primarySources:     map[*server.Resource]*clipSource{},
		controlSources:     map[*server.Resource]*clipSource{},
		regions:            map[*server.Resource]*region{},
		relatives:          map[server.Client][]*relativepointer.ZwpRelativePointerV1{},
		gestures:           map[server.Client][]*gestureObject{},
		constraints:        map[*surface]*constraint{},
		positioners:        map[*server.Resource]*positioner{},
		seat:               seatState{keymapFD: -1, keyboards: make(map[server.Client][]*wayland.Keyboard), pointers: make(map[server.Client][]*wayland.Pointer), keyStamps: make(map[*server.Resource][]*inputtimestamps.ZwpInputTimestampsV1), pointerStamps: make(map[*server.Resource][]*inputtimestamps.ZwpInputTimestampsV1), enters: make(map[*server.Resource]uint32), repeatRate: opts.RepeatRate, repeatDelay: opts.RepeatDelay},
	}
	// Bound once, before anything can post them (the sync waiter calls
	// signalPacer from its own goroutine as soon as it exists).
	s.paceSignalFn = s.paceSignalled
	s.flushLinksFn = s.flushLinks
	s.sink = newPresentSink(s)
	s.capturedRing = newPostRing(d, capturedRingSize, s.applyCaptured)
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
	s.captureAllow, s.peer, s.peers = opts.CaptureAllow, opts.peer, map[server.Client]*peerID{}
	if s.peer == nil {
		s.peer = linuxPeerExe{}
	}
	s.sandboxed, s.securityStop = map[server.Client]*sandbox{}, -1
	s.captureReplies = map[uint64]func(ports.CaptureDone){}
	s.captureInflight = map[uint64]struct{}{}
	s.outputReplies = map[uint64]*outputConfiguration{}
	s.managementSerial = 1
	s.captureSources = map[*server.Resource]*captureSource{}
	s.sessionsByID = map[uint64]*captureSession{}
	s.captureSessions = map[*captureSession]struct{}{}
	s.fractions = map[*surface]*fractionalscale.WpFractionalScaleV1{}
	s.fifoSurfaces, s.lastFlip = map[*surface]struct{}{}, map[string]time.Time{}
	s.flips = map[string]ports.FlipInfo{}
	s.timelines = map[*server.Resource]*timeline{}
	var node *renderNode
	if opts.syncDev != nil {
		s.syncDev = opts.syncDev
		if s.syncWait, err = newSyncWaiter(s.signalPacer); err != nil {
			cleanup()
			d.Close()
			return nil, err
		}
	} else if opts.SyncobjNode != "" {
		if node, err = openSyncobj(opts.SyncobjNode); err != nil {
			log.Info().Err(err).Msg("explicit sync off")
			node = nil
		} else if s.syncWait, err = newSyncWaiter(s.signalPacer); err != nil {
			node.close()
			node = nil
		} else {
			s.syncDev = node
		}
	}
	// Implicit dma-buf fences also need this waiter when explicit sync is off.
	if s.syncWait == nil {
		if s.syncWait, err = newSyncWaiter(s.signalPacer); err != nil {
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
	s.foreignExports = map[string]*foreignExport{}
	s.syncWait.log = log
	s.cleanup = func() {
		if s.paceReady {
			unix.Close(s.paceFD)
		}
		if s.securityStop >= 0 {
			unix.Close(s.securityStop)
		}
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
	// Run never starts after a failure here: release what it would close.
	abort := func(err error) (*Server, error) {
		s.cleanup()
		s.syncWait.discard()
		d.Close()
		return nil, err
	}
	if opts.Keymap != "" {
		fd, size, e := keymapFile(opts.Keymap)
		if e != nil {
			return abort(e)
		}
		s.seat.keymapFD, s.seat.keymapSize, s.seat.keymapText = fd, size, opts.Keymap
	}
	if s.securityStop, err = unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK); err != nil {
		return abort(err)
	}
	if err = s.openPaceTimer(); err != nil {
		return abort(err)
	}
	if err = registerGlobals(d, opts, s); err != nil {
		return abort(err)
	}
	for _, p := range opts.Outputs {
		s.addOutput(p)
	}
	return s, nil
}

// openPaceTimer creates the pace timerfd and registers it with the display
// loop (before Run, as AddFD requires).
func (s *Server) openPaceTimer() error {
	fd, err := unix.TimerfdCreate(unix.CLOCK_MONOTONIC, unix.TFD_NONBLOCK|unix.TFD_CLOEXEC)
	if err != nil {
		return err
	}
	// libwayland duplicates fd: ours stays open for TimerfdSettime and is
	// closed by cleanup; the source goes with the display.
	if _, err = s.display.AddFD(fd, s.onPaceTimer); err != nil {
		unix.Close(fd)
		return err
	}
	s.paceFD, s.paceReady = fd, true
	return nil
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
	wg.Add(13)
	go func() { defer wg.Done(); s.forwardBackendSecurity(ctx) }()
	go func() { defer wg.Done(); s.forwardSecurityEvents(ctx) }()
	go func() { defer wg.Done(); s.forwardLeases(ctx) }()
	go func() { defer wg.Done(); s.forwardLeaseRequests(ctx) }()
	go func() { defer wg.Done(); s.forwardWorkspaces(ctx) }()
	go func() { defer wg.Done(); s.forward(ctx) }()
	go func() { defer wg.Done(); s.forwardCursors(ctx) }()
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
	go func() { defer wg.Done(); s.forwardOutputFormats(ctx) }()
	go func() { defer wg.Done(); s.forwardOutputHeads(ctx) }()
	go func() { defer wg.Done(); s.forwardOutputApplied(ctx) }()
	go func() { defer wg.Done(); s.forwardBusIdle(ctx) }()
	go func() { defer wg.Done(); s.forwardCaptured(ctx.Done()) }()
	if s.syncWait != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.syncWait.run(ctx) }()
	}
	err := s.display.Run(ctx)
	s.stopSecurityContexts()
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

// damageHistoryCap is the capacity of a history's backing array: one
// allocation serves damageHistoryCap-damageHistory+1 publications.
const damageHistoryCap = 8 * damageHistory

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

// emitContent publishes a window's content on the display goroutine: it
// becomes the window's latest content and joins the batch of every attached
// output (contentlink.go).
func (s *Server) emitContent(c ports.SurfaceContent, d damage) {
	s.contentSeq[c.ID]++
	c.Seq = s.contentSeq[c.ID]
	// Published histories are immutable: an output may hold one for as long
	// as it likes. The history grows inside one backing array, appended past
	// the end of every published slice (never over an element a reader can
	// see), and moves to a fresh array only when that one is full. Readers
	// get a capacity-clipped slice, so they cannot append into the spare.
	old := s.damage[c.ID]
	if len(old) >= damageHistory {
		old = old[len(old)-damageHistory+1:]
	}
	if len(old) == cap(old) {
		grown := make([]ports.SeqDamage, len(old), damageHistoryCap)
		copy(grown, old)
		old = grown
	}
	h := append(old, ports.SeqDamage{Seq: c.Seq, Full: d.full, Rects: d.rects})
	c.DamageHistory = h[:len(h):len(h)]
	if c.Empty() {
		delete(s.damage, c.ID)
		delete(s.latest, c.ID)
	} else {
		s.damage[c.ID] = h
		s.latest[c.ID] = c
	}
	for _, l := range s.links {
		l.add(c)
		s.flushLink(l)
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
		case ports.PointerFocus, ports.PointerMotionTo, ports.PointerButtonTo, ports.PointerAxisTo, ports.GestureBeginTo, ports.GestureUpdateTo, ports.GestureEndTo, ports.FocusWindow, ports.ForwardKey:
			return
		}
	}
	switch c := cmd.(type) {
	case ports.PointerFocus, ports.PointerMotionTo, ports.PointerButtonTo, ports.PointerAxisTo, ports.GestureBeginTo, ports.GestureUpdateTo, ports.GestureEndTo, ports.SetKeymap, ports.FocusWindow, ports.ForwardKey:
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
