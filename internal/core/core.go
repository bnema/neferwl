package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

var ErrQuit = errors.New("quit requested")

var errSecurityChanged = errors.New("security epoch changed during input")

// Channels connects the owner to adapters. Scenes must have capacity 1: core is
// its only sender and may drain a stale set; consumers only receive. Each
// send holds one scene per output.
type Channels struct {
	Client   <-chan ports.ClientEvent
	Input    <-chan ports.InputEvent
	Output   <-chan ports.OutputEvent
	Config   <-chan ports.ConfigChanged
	Commands chan<- ports.ClientCommand
	Spawn    chan<- ports.SpawnRequest
	Scenes   chan []domain.Scene
	// Layouts, when set, receives the output layout whenever it changes,
	// latest first (capacity 1, drained like Scenes). Input and cursors use it.
	Layouts chan ports.Layout
	// Constraints, when set, receives the active pointer constraint in
	// global logical coordinates whenever it changes (capacity 1, drained
	// like Scenes). Input applies it to the pointer.
	Constraints  chan ports.PointerConstraint
	ConfigErrors chan<- error
	// State, when set, receives a snapshot for scripts whenever it changes,
	// latest first (capacity 1, drained like Scenes).
	State      chan ports.State
	Workspaces chan ports.Workspaces
	// Scales, when set, receives each scale a scale bind sets, to persist it.
	// Core does not wait: a full channel drops the change.
	Scales chan<- ports.ScaleChanged
	// Frames, when set, carries the outputs' reports of each flip of an
	// animating scene (Scene.Animating): a running slide moves one step per
	// report. Without them it moves on a timer.
	Frames <-chan ports.OutputFrame
	// HintScans, when set, receives the scan request of each hint action
	// (capacity 1; core never waits: a full channel cancels the hint).
	// Hints carries the results back. With HintScans nil the hint action
	// does nothing.
	HintScans chan<- ports.HintScanRequest
	Hints     <-chan ports.HintScanResult
}

// Options are the services and policy core is constructed with.
type Options struct {
	// Security is the defensive session gate; nil preserves standalone operation.
	Security ports.SessionSecurity
	// Clock supplies every core timestamp and timer; nil uses the system clock.
	Clock ports.Clock
	// Terminal enables automatic terminal opening according to terminal.auto-open.
	Terminal bool
	// HintDisabled, when set, is called the first time the hint action runs
	// without a scanner (hint mode off). It must not block.
	HintDisabled func()
}

// fullscreenGrace is how long after mapping a window's fullscreen request
// is ignored. Wine asks for fullscreen whenever a window has the monitor's
// size, so a launcher that remembered a fullscreen size would open
// fullscreen and hide the windows it opens next. The user can still
// fullscreen it (bind or taskbar), and an app can ask again later.
const fullscreenGrace = time.Second

type binding struct {
	mods domain.Mods
	key  string
}
type Core struct {
	osd osd
	// overviewReal is publish's scratch map of the real layouts under an open overview.
	overviewReal map[WindowID]Placement
	// realBuf backs the layout read through at once by publish (each
	// workspace while overviewReal is filled) and by state(); never retained.
	realBuf         []Placement
	scratch         publishScratch
	security        domain.SecurityState
	lock            lockState
	inputKeys       map[string]bool
	inputActive     bool
	startup         [][]string
	nextWorkspaceID uint64
	ch              Channels
	opts            Options
	cfg             ports.Config
	screens         []*screen
	focusScreen     int
	// offGone names outputs unplugged while turned off: a display that
	// reconnects from deep sleep comes back off, until the next input.
	offGone    map[string]bool
	binds      map[binding]Action
	pressed    map[string]bool
	configures configures
	pointer    WindowID
	grab       WindowID
	buttons    map[uint32]bool
	// drag is the pointer drag in progress (drag.go); swallow holds the
	// buttons whose release no client must see. mods are the modifiers
	// held, cmdMod the one keyboard.cmd names. lastButton is the latest
	// press a client saw, the one xdg_toplevel.move's serial names.
	drag            *dragState
	pgrab           pointerGrab
	lastBorderClick struct {
		at time.Duration
		ws *Workspace
		b  Border
	}
	// dataDrag is the client data drag in progress (datadrag.go).
	dataDrag         *dataDragState
	swallow          map[uint32]bool
	lastButton       uint32
	mods             domain.Mods
	cmdMod           domain.Mods
	cursorX, cursorY float64 // global, logical
	pointerOutput    string  // output under the pointer at the last motion
	// constrained is the constraint wayland activated; constraint is it
	// resolved to global coordinates, as last sent to input.
	constrained ports.PointerConstrained
	constraint  ports.PointerConstraint
	seq         uint64
	// layerChanged is set once layer state arrives from wayland.
	layerChanged bool
	sent         sentCache
	specs        []NamedWorkspace
	slots        map[slotKey]*slotState
	rules        []rule
	placement    spawnPlacement
	// toSpawn holds slots to start; Run sends them (apply has no context).
	toSpawn               []slotKey
	firstTerminalResolved bool
	windows               windowRegistry
	// popups are the placed xdg_popups; popupOrder stacks them, oldest first.
	popups     map[WindowID]*popupState
	popupOrder []WindowID
	keyboard   keyboard
	// activity is when core last told wayland about user input
	// (ports.UserActivity), sent at most once per ActivityInterval.
	activity time.Time
	// swipe is the touchpad swipe in progress. frameC fires when no page
	// flip came in time to move a running slide; nil when not armed. It is
	// the channel of frameTimer, made once and reset per frame: an
	// animation frame allocates no timer.
	swipe *swipeGesture
	// gesture is the touchpad gesture forwarded to a client
	// (clientgesture.go).
	gesture clientGesture
	// ping carries watchdog probes (Ping); Run answers each one.
	ping chan chan struct{}
	// out queues the commands for c.ch.Commands; Run's sender drains it.
	out        outbox
	frameC     <-chan time.Time
	frameTimer ports.Timer
	// shots is the snapshot of what the screens show before an action
	// (transition.go), reused by every action.
	shots []viewShot
	// motionTime is the time of the last pointer motion; pointerAt is the
	// last position sent in the pointer's window.
	motionTime time.Duration
	pointerAt  [2]float64
	// capt is the capture state (capture.go, capindicator.go).
	capt              captureState
	flash             focusFlash
	positionHintTimer positionHintTimer
	// hint is keyboard hint mode (hint.go).
	hint hintState
	// keyState is the real modifier state of the latest key event, swallowed
	// keys included.
	keyState ports.ModState
	// synthetic is set while a hint click runs through the pointer handlers:
	// compositor modifier+button actions (window drag) do not apply to it.
	synthetic bool
	// switcher is the column switcher in progress: the screen it is on and
	// the timer that shows its cards (switcher.go).
	switcher struct {
		sc        *screen
		timerC    <-chan time.Time
		timerStop func() bool
	}
	// repeat is the held key that repeats a move (repeat.go); repeatC its
	// timer's channel, nil when no key repeats.
	repeat  keyRepeat
	repeatC <-chan time.Time
}

// publishScratch is where publishState and publishWorkspaces build their
// snapshots each publish: owner goroutine storage, never sent (a snapshot
// is copied out of it when it changed).
type publishScratch struct {
	state      ports.State
	workspaces ports.Workspaces
	shown      map[WindowID]bool
	ids        []WindowID
	rects      []Rect
	rows       []Rect
	layout     ports.Layout
}

// sentCache is what core last sent to wayland and on its latest-only
// channels; an unchanged value is not sent again.
type sentCache struct {
	outputs    ports.SetOutputs
	outputsSeq uint64 // last successfully queued output layout
	state      ports.State
	workspaces ports.Workspaces
}

func keyName(s string) string {
	if len(s) == 1 && unicode.IsLetter(rune(s[0])) {
		return strings.ToLower(s)
	}
	return s
}
func parseBind(s, cmd string) (binding, error) {
	parts := strings.Split(s, "+")
	b := binding{key: keyName(parts[len(parts)-1])}
	if b.key == "" {
		return b, fmt.Errorf("invalid bind %q", s)
	}
	for _, p := range parts[:len(parts)-1] {
		var m domain.Mods
		switch p {
		case "Cmd":
			switch cmd {
			case "super":
				m = domain.ModSuper
			case "alt":
				m = domain.ModAlt
			case "ctrl":
				m = domain.ModCtrl
			}
		case "Shift":
			m = domain.ModShift
		case "Ctrl":
			m = domain.ModCtrl
		case "Alt":
			m = domain.ModAlt
		case "Super":
			m = domain.ModSuper
		}
		if m == 0 || b.mods&m != 0 {
			return b, fmt.Errorf("invalid bind %q", s)
		}
		b.mods |= m
	}
	return b, nil
}
func (c *Core) apply(cfg ports.Config) error {
	if cfg.Layout.MaxColumns < 1 {
		return fmt.Errorf("invalid max columns")
	}
	if cfg.Layout.Gaps < 0 {
		return fmt.Errorf("invalid gaps")
	}
	if cfg.Keyboard.CmdKey != "super" && cfg.Keyboard.CmdKey != "alt" && cfg.Keyboard.CmdKey != "ctrl" {
		return fmt.Errorf("invalid cmd key")
	}
	binds := map[binding]Action{}
	for combo, a := range cfg.Binds {
		b, e := parseBind(combo, cfg.Keyboard.CmdKey)
		if e != nil {
			return e
		}
		if _, ok := binds[b]; ok {
			return fmt.Errorf("duplicate bind %q", combo)
		}
		if _, ok := SpawnCommands(Action(a)); ok {
			binds[b] = Action(a)
			continue
		}
		if _, ok := StashCommands(Action(a)); ok {
			binds[b] = Action(a)
			continue
		}
		if Action(a) == ActionScaleUp || Action(a) == ActionScaleDown {
			binds[b] = Action(a)
			continue
		}
		if _, _, ok := WorkspaceArg(Action(a)); ok {
			binds[b] = Action(a)
			continue
		}
		if _, _, ok := ResizeArg(Action(a)); ok {
			binds[b] = Action(a)
			continue
		}
		if _, ok := NamedArg(Action(a)); ok {
			binds[b] = Action(a)
			continue
		}
		switch Action(a) {
		case "none", ActionSpawnTerminal, ActionFocusColumnLeft, ActionFocusColumnRight, ActionFocusWindowUp, ActionFocusWindowDown, ActionMoveColumnLeft, ActionMoveColumnRight, ActionCycleColumnWidth, ActionMaximizeColumn, ActionToggleFullscreen, ActionToggleWindowStash, ActionToggleStashVisible, ActionToggleOverview, ActionCloseWindow, ActionQuit, ActionFocusWorkspacePrev, ActionFocusWorkspaceNext, ActionMoveColumnToWorkspacePrev, ActionMoveColumnToWorkspaceNext, ActionMoveWindowToWorkspacePrev, ActionMoveWindowToWorkspaceNext, ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown, ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown, ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight,
			ActionMoveWindowUp, ActionMoveWindowDown, ActionMoveWorkspacePrev, ActionMoveWorkspaceNext, ActionToggleFloating, ActionSwitchColumnNext, ActionSwitchColumnPrev, ActionHint, ActionOSDPin:
		default:
			return fmt.Errorf("invalid action %q", a)
		}
		binds[b] = Action(a)
	}
	overflow := func(v string) (Overflow, error) {
		if _, ok := layout.Lookup(Overflow(v)); ok {
			return Overflow(v), nil
		}
		return "", fmt.Errorf("invalid overflow %q", v)
	}
	if _, err := overflow(cfg.Layout.Overflow); err != nil {
		return err
	}
	// Normalize the global config value; empty output/workspace values still inherit.
	if len(cfg.Layout.Overflow) == 0 {
		cfg.Layout.Overflow = string(layout.Default)
	}
	for _, o := range cfg.Layout.Outputs {
		if _, err := overflow(o.Overflow); err != nil {
			return err
		}
		if o.Output == "" || o.MaxColumns < 0 {
			return fmt.Errorf("invalid layout for output %q", o.Output)
		}
	}
	named := make([]NamedWorkspace, 0, len(cfg.Workspaces))
	seen := map[string]bool{}
	for _, ws := range cfg.Workspaces {
		o, err := overflow(ws.Overflow)
		if err != nil {
			return err
		}
		if ws.Name == "" || seen[ws.Name] || ws.MaxColumns < 0 || ws.Size[0] < 0 || ws.Size[1] < 0 {
			return fmt.Errorf("invalid workspace %q", ws.Name)
		}
		seen[ws.Name] = true
		named = append(named, NamedWorkspace{Name: ws.Name, Monitor: ws.Monitor, MaxColumns: ws.MaxColumns, Overflow: o, Size: ws.Size})
	}
	specs, err := parseSlots(cfg.Workspaces)
	if err != nil {
		return err
	}
	rules, err := parseRules(cfg.Rules)
	if err != nil {
		return err
	}
	// Reload is cold-path work; unrelated changes keep a scroll or gesture.
	type layoutAxes struct{ content, workspace layout.Axis }
	axes := make(map[*Monitor]layoutAxes, len(c.screens))
	for _, s := range c.screens {
		content, workspace := s.mon.Current().Engine().Axes()
		axes[s.mon] = layoutAxes{content, workspace}
	}
	if c.cfg.Hint.Size != cfg.Hint.Size {
		c.hintCancel()
	}
	c.cfg = cfg
	c.binds = binds
	c.cmdMod = map[string]domain.Mods{"super": domain.ModSuper, "alt": domain.ModAlt, "ctrl": domain.ModCtrl}[cfg.Keyboard.CmdKey]
	c.specs, c.rules = named, rules
	c.toSpawn = append(c.toSpawn, c.updateSlots(specs)...)
	c.named()
	for _, s := range c.screens {
		c.settings(s.mon)
	}
	c.applyOutputConfig()
	for _, s := range c.screens {
		content, workspace := s.mon.Current().Engine().Axes()
		old, now := axes[s.mon], (layoutAxes{content, workspace})
		if old.content != now.content || old.workspace != now.workspace {
			s.mon.ov.dropScroll()
		}
	}
	return nil
}

// spawnSlots starts the slots queued by config. With shown set (the user
// just switched to another workspace), it also refills the empty slots of
// the workspace now on screen.
func (c *Core) spawnSlots(ctx context.Context, shown bool) error {
	if c.protectionRequested() {
		return nil
	}
	keys := c.toSpawn
	c.toSpawn = nil
	if shown {
		keys = append(keys, c.refill()...)
	}
	started := map[slotKey]bool{}
	for i, key := range keys {
		if c.protectionRequested() {
			c.toSpawn = append(c.toSpawn, keys[i:]...)
			return nil
		}
		st, ok := c.slots[key]
		// A key can be queued twice (new slot on the workspace on screen).
		if !ok || started[key] || st.window != 0 {
			continue
		}
		started[key] = true
		req := c.spawnSlot(key)
		if err := ctx.Err(); err != nil || c.protectionRequested() {
			c.placement.dropSlot(key)
			st.stale = false
			c.toSpawn = append(c.toSpawn, keys[i:]...)
			return err
		}
		if !c.trySpawn(ctx, req) {
			c.placement.dropSlot(key)
			st.stale = false
			c.toSpawn = append(c.toSpawn, keys[i:]...)
			return ctx.Err()
		}
	}
	return nil
}
func New(cfg ports.Config, ch Channels, opts Options) (*Core, error) {
	if cap(ch.Scenes) != 1 || (ch.Layouts != nil && cap(ch.Layouts) != 1) || (ch.Constraints != nil && cap(ch.Constraints) != 1) || (ch.State != nil && cap(ch.State) != 1) || (ch.Workspaces != nil && cap(ch.Workspaces) != 1) {
		return nil, fmt.Errorf("scenes, layouts, constraints, state and workspaces must have capacity 1")
	}
	// A placeholder screen holds windows until the first output arrives.
	c := &Core{inputKeys: map[string]bool{}, slots: map[slotKey]*slotState{}, placement: newSpawnPlacement(), windows: newWindowRegistry(), popups: map[WindowID]*popupState{}, ch: ch, opts: opts, pressed: map[string]bool{}, buttons: map[uint32]bool{}, swallow: map[uint32]bool{}, configures: newConfigures(), ping: make(chan chan struct{}), out: newOutbox()}
	c.screens = []*screen{{mon: newMonitorWithIDs("", "", &c.nextWorkspaceID), scale: 1, cfgScale: 1}}
	if err := c.apply(cfg); err != nil {
		return nil, err
	}
	return c, nil
}

// newTimer makes a timer on clock, or the system timer when clock is nil.
func newTimer(clock ports.Clock, d time.Duration) (<-chan time.Time, func() bool) {
	t := newPortTimer(clock, d)
	return t.C(), t.Stop
}

// newPortTimer makes a ports.Timer on clock, or on the system clock when
// clock is nil.
func newPortTimer(clock ports.Clock, d time.Duration) ports.Timer {
	if clock != nil {
		return clock.NewTimer(d)
	}
	return sysTimer{time.NewTimer(d)}
}

// sysTimer is a time.Timer as a ports.Timer.
type sysTimer struct{ t *time.Timer }

func (t sysTimer) C() <-chan time.Time        { return t.t.C }
func (t sysTimer) Stop() bool                 { return t.t.Stop() }
func (t sysTimer) Reset(d time.Duration) bool { return t.t.Reset(d) }

func (c *Core) now() time.Time {
	if c.opts.Clock != nil {
		return c.opts.Clock.Now()
	}
	return time.Now()
}

// command queues v for the wayland goroutine and never blocks: a busy
// display must not stall keyboard, scenes or frame wakes. The sender
// goroutine of Run delivers the queue in order.
func (c *Core) command(ctx context.Context, v ports.ClientCommand) error {
	_, err := c.queue(ctx, v)
	return err
}

// queue pushes v to the outbox and returns its sequence number (0: not
// queued).
func (c *Core) queue(ctx context.Context, v ports.ClientCommand) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c.inputEpochChanged() {
		return 0, errSecurityChanged
	}
	if c.ch.Commands == nil {
		// Nobody receives: queueing would only grow without bound.
		return 0, nil
	}
	seq := c.out.push(c.ch.Commands, v, c.security, c.opts.Security != nil)
	if c.out.backlog() > outboxLimit {
		if err := c.out.flush(ctx, c.ch.Commands, seq-outboxLimit); err != nil {
			return seq, err
		}
	}
	// The queued command keeps the epoch it was made under, so a display
	// that has moved on drops it. Report the change all the same: the rest
	// of the event was decided under the old epoch and must not go on.
	if c.inputEpochChanged() {
		return seq, errSecurityChanged
	}
	return seq, nil
}

// clientRect is the placement minus the border on its inset sides.
// The renderer applies the same inset (domain.Border).
func (c *Core) clientRect(p Placement) Rect {
	if p.Fullscreen {
		return p.Rect
	}
	return p.Rect.Inset(p.Inset, c.cfg.Border.Width)
}

// viewport is where a workspace is seen: frame is the whole viewport,
// usable the part exclusive layers (panels) leave to the tiles.
type viewport struct{ frame, usable Rect }

// whole is a viewport with no panel: all of r is usable.
func whole(r Rect) viewport { return viewport{frame: r, usable: r} }

// area is where p can be seen, drawn and pointed at. A tile lives in the
// usable area and is cut at its edge (Scene.TileInset), so a cascade band
// or a column scrolled under a panel is not seen; a float, a fullscreen
// window and an overview preview are placed against the frame.
func (v viewport) area(p Placement) Rect {
	if p.Floating || p.Fullscreen || p.Preview > 0 {
		return v.frame
	}
	return v.usable
}

// shows reports whether the user sees p: not hidden, and overlapping its
// area.
func (v viewport) shows(p Placement) bool {
	return !p.Hidden && p.Rect.Overlaps(v.area(p))
}

// floatDim is the veil opacity of a layout: dim only when a float is
// drawn above the tiles, never for a demoted covering float alone nor for
// an overview preview. A leaving float (fading out after it closed or hid)
// drops the veil at once: a veil that fades changes Scene.Dim every frame,
// which redraws the whole output for the length of the fade, where the
// float's own fade redraws only its rect.
func floatDim(layout []Placement, v viewport, dim float64) float64 {
	for _, p := range layout {
		if p.Floating && !p.Below && p.Preview == 0 && v.shows(p) {
			if p.Fullscreen {
				return 0
			}
			return dim
		}
	}
	return 0
}

// visible reports whether the window, layer surface or popup is on screen
// on any output.
func (c *Core) visible(id WindowID) bool {
	_, _, ok := c.windowRect(id)
	return ok
}

// windowFocus is the focused window of each output.
func (c *Core) windowFocus() map[*screen]WindowID {
	m := make(map[*screen]WindowID, len(c.screens))
	for _, sc := range c.screens {
		m[sc], _ = sc.mon.Focused()
	}
	return m
}

// keyboardFocus is the surface holding the keyboard (see keyboard.focus).
func (c *Core) keyboardFocus() WindowID {
	if c.security.Protected {
		return c.lockKeyboardFocus()
	}
	var exclusive WindowID
	for _, sc := range c.screens {
		for _, l := range sc.layers {
			if l.Keyboard == 1 && (l.Layer == domain.LayerTop || l.Layer == domain.LayerOverlay) && l.ID > exclusive {
				exclusive = l.ID
			}
		}
	}
	window, _ := c.cur().mon.Focused()
	return c.keyboard.focus(keyboardCandidates{
		exclusive: exclusive,
		grab:      c.grabFocus(),
		window:    window,
		layerShown: func(id WindowID) bool {
			return c.onDemand(id) && c.visible(id)
		},
		windows: c.windowFocus,
	})
}

// setLayers splits the mapped layer surfaces by output. A surface without an
// output, or on an unknown one, goes to the focused output.
func (c *Core) setLayers(all []ports.LayerSurface) {
	previous := make(map[WindowID]bool)
	for _, sc := range c.screens {
		for _, l := range sc.layers {
			previous[l.ID] = true
		}
		sc.layers = nil
	}
	for _, l := range all {
		delete(previous, l.ID)
		sc := c.cur()
		if i := c.screenIndex(l.Output); i >= 0 {
			sc = c.screens[i]
		}
		sc.layers = append(sc.layers, l)
	}
	for id := range previous {
		c.windows.layerGone(id)
	}
	c.pruneRetained()
	c.clampCaptureKeyboard()
	for _, sc := range c.screens {
		sc.arrange()
	}
}

func (c *Core) publish(ctx context.Context) error { return c.publishFrame(ctx, nil) }

// publishFrame publishes the scenes. only is the screen whose page flip
// moved the springs (nil: any, or none moved): only a flip's own screen gets
// a fresh Seq for its animation, so another output keeps its Seq while it draws the same.
func (c *Core) publishFrame(ctx context.Context, only *screen) error {
	if c.inputEpochChanged() {
		return errSecurityChanged
	}
	c.syncSecurity()
	c.updatePositionHints(only)
	c.hintValidate()
	if c.security.Protected {
		if err := c.osdWatch(ctx); err != nil {
			return err
		}
		return c.publishProtected(ctx)
	}
	if c.switching() && c.switcherStale() {
		// Another workspace came on screen under the switcher: it is over.
		c.cancelSwitcher()
	}
	for _, sc := range c.screens {
		// The overview's selection is provisional: it focuses nothing yet.
		if !sc.mon.ov.open {
			// A switcher's selection is provisional too.
			sc.mon.Current().Settle(!sc.mon.previewing())
		}
	}
	if c.pgrab != nil && !c.pgrab.valid() {
		if err := c.abortPointerGrab(ctx); err != nil {
			return err
		}
	}
	c.refreshShown()
	c.captureExpire()
	capture, err := c.captureEvaluate(ctx)
	if err != nil {
		return err
	}
	// The workspace a session renders off screen keeps its real layout for
	// the configures, popups and CaptureScene of this publish.
	c.captureTrack(capture)
	if err := c.closeHiddenPopups(ctx); err != nil {
		return err
	}
	// Clients learn outputs and scales before the configures sized for them.
	if err := c.syncOutputs(ctx); err != nil {
		return err
	}
	// Output layout must be known before an automatic terminal can map.
	if err := c.spawnEmpty(ctx); err != nil {
		return err
	}
	focus := c.keyboardFocus()
	if err := c.osdWatch(ctx); err != nil {
		return err
	}
	c.advanceFlash(only)
	scenes := make([]domain.Scene, 0, len(c.screens))
	// Before the first output (and after the last is unplugged) the
	// placeholder's scene has no output name: no renderer draws it.
	for i, sc := range c.screens {
		scene, err := c.sceneFor(ctx, i, sc, only, capture)
		if err != nil {
			return err
		}
		scenes = append(scenes, scene)
	}
	c.configures.prune()
	for i, sc := range c.screens {
		sc.last = scenes[i]
	}
	// A workspace switch can hide the window under the pointer; it must not get
	// clicks. The next motion re-runs hit-testing.
	if c.pointer != 0 && !c.visible(c.pointer) {
		c.pointer = 0
		if c.grab == 0 {
			if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
				return err
			}
		}
	}
	if err := c.syncFocus(ctx, focus); err != nil {
		return err
	}
	if err := c.updateInhibit(ctx); err != nil {
		return err
	}
	c.resolveConstraint()
	if c.inputEpochChanged() {
		return errSecurityChanged
	}
	if c.syncSecurity() {
		return c.publishFrame(ctx, only)
	}
	latest(c.ch.Scenes, scenes)
	c.publishState()
	c.publishWorkspaces()
	c.armCaptureTimer()
	// A running slide moves on the next flip, or on the fallback timer.
	if c.animating() {
		if c.frameC == nil {
			c.armFrame()
		}
	} else {
		c.stopFrame()
	}
	return nil
}

// step moves the running slides of only (nil: every screen) to now and
// publishes the frame.
func (c *Core) step(ctx context.Context, only *screen) error {
	// The fallback timer guards every animating output: a flip of one keeps
	// it running while another still animates, or that one could starve.
	if only == nil || !c.animatingOther(only) {
		c.stopFrame()
	}
	c.animate(c.now(), only)
	return c.slidFrame(ctx, false, only)
}

// animatingOther reports whether a spring runs on a screen
// other than only.
func (c *Core) animatingOther(only *screen) bool {
	for _, sc := range c.screens {
		if sc != only && (sc.springing() || c.flashing(sc) || c.positionHintFading(sc)) {
			return true
		}
	}
	return false
}

// slid publishes a moved view; shown reports another workspace came on
// screen.
func (c *Core) slid(ctx context.Context, shown bool) error {
	return c.slidFrame(ctx, shown, nil)
}

// slidFrame is slid for the page flip of only (nil: any screen).
func (c *Core) slidFrame(ctx context.Context, shown bool, only *screen) error {
	if err := c.workspaceVisible(ctx, shown); err != nil {
		return err
	}
	if err := c.publishFrame(ctx, only); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// rehit points the pointer at what now lies under a still cursor, as
// windows slide under it. A held button keeps its grab.
func (c *Core) rehit(ctx context.Context) error {
	if c.dataDrag != nil {
		// The drag target follows the layout too.
		return c.dataDragRetarget(ctx, c.motionTime)
	}
	if c.grab != 0 || c.pointerBusy() {
		return nil
	}
	id, x, y := c.hit(c.cursorX, c.cursorY)
	if id != c.pointer {
		c.pointer, c.pointerAt = id, [2]float64{x, y}
		return c.pointerFocus(ctx, ports.PointerFocus{ID: id, X: x, Y: y})
	}
	if id == 0 || c.pointerAt == [2]float64{x, y} {
		return nil
	}
	c.pointerAt = [2]float64{x, y}
	// The same window moved under the cursor: it moves in the window. No
	// relative motion: the device did not move. The input clock is the
	// device's; the last motion's time is the closest core knows.
	return c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, Time: c.motionTime})
}

// updateInhibit makes the shortcuts inhibitor of the keyboard focus the
// active one and tells wayland when it changes.
func (c *Core) updateInhibit(ctx context.Context) error {
	if c.security.Protected {
		return nil
	}
	f := c.keyboardFocus()
	release, activate, changed := c.keyboard.inhibit(f, c.windows.lookup(f).inhibitShortcuts)
	if !changed {
		return nil
	}
	if release != 0 {
		if err := c.command(ctx, ports.ShortcutsInhibitState{Window: release}); err != nil {
			return err
		}
	}
	if activate != 0 {
		return c.command(ctx, ports.ShortcutsInhibitState{Window: activate, Active: true})
	}
	return nil
}

// resolveConstraint turns the active constraint into global coordinates
// and sends it to input when it changed. A hidden window holds nothing.
func (c *Core) resolveConstraint() {
	var g ports.PointerConstraint
	if full, vis, ok := c.shownClient(c.constrained.ID); ok {
		r := full
		if w := c.constrained.Rect; w.W > 0 && w.H > 0 {
			// The region is window-local: placed from the full client origin,
			// clipped to the window, then to what the viewport shows.
			x0, y0 := max(full.X, full.X+w.X), max(full.Y, full.Y+w.Y)
			x1, y1 := min(full.X+full.W, full.X+w.X+w.W), min(full.Y+full.H, full.Y+w.Y+w.H)
			if x1 > x0 && y1 > y0 {
				r = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
			}
		}
		r = intersect(r, vis)
		if r.W > 0 && r.H > 0 {
			g = ports.PointerConstraint{Mode: c.constrained.Mode, Rect: r}
		} else {
			// The region lies outside the viewport: hold the visible client.
			g = ports.PointerConstraint{Mode: c.constrained.Mode, Rect: vis}
		}
	}
	// The cursor follows core; input resyncs to it only on a change.
	if g.Mode == c.constraint.Mode && g.Rect == c.constraint.Rect {
		return
	}
	// A layout change must not leave a locked cursor off its window.
	c.cursorX, c.cursorY = g.Clamp(c.cursorX, c.cursorY)
	g.X, g.Y = c.cursorX, c.cursorY
	c.constraint = g
	if c.ch.Constraints != nil {
		latest(c.ch.Constraints, g)
	}
}

// intersect is the overlap of a and b, empty (zero size) when disjoint.
func intersect(a, b Rect) Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// shownClient is the global logical client rectangle of a window drawn on
// its output, whole (window-local coordinates start at its origin), and the
// part of it inside the workspace viewport, which alone takes the pointer.
// ok is false when it is hidden, a preview or entirely outside the viewport.
func (c *Core) shownClient(id WindowID) (full, visible Rect, ok bool) {
	if id == 0 {
		return Rect{}, Rect{}, false
	}
	s, _ := c.screenOf(id)
	if s == nil {
		return Rect{}, Rect{}, false
	}
	for _, p := range s.shownLayout() {
		if p.ID != id || p.Hidden || p.Preview > 0 {
			continue
		}
		r := c.clientRect(p)
		full = Rect{X: r.X + s.x, Y: r.Y + s.y, W: r.W, H: r.H}
		visible = intersect(r, s.mon.viewport().area(p))
		visible.X, visible.Y = visible.X+s.x, visible.Y+s.y
		if visible.W <= 0 || visible.H <= 0 {
			return Rect{}, Rect{}, false
		}
		return full, visible, true
	}
	return Rect{}, Rect{}, false
}

// shownClientRect is the visible part of shownClient.
func (c *Core) shownClientRect(id WindowID) (Rect, bool) {
	_, vis, ok := c.shownClient(id)
	return vis, ok
}

// warpPointer moves the cursor to a window-local point of the window under
// it (wp_pointer_warp_v1). Input takes the new position, and the window
// gets the motion; the active constraint still bounds the point.
func (c *Core) warpPointer(ctx context.Context, v ports.PointerWarp) error {
	if v.ID == 0 || v.ID != c.pointer || c.grab != 0 && c.grab != v.ID {
		return nil
	}
	full, vis, ok := c.shownClient(v.ID)
	if !ok || v.X < 0 || v.Y < 0 || v.X >= float64(full.W) || v.Y >= float64(full.H) {
		return nil
	}
	// Local to the whole client, and refused outside the viewport.
	gx, gy := float64(full.X)+v.X, float64(full.Y)+v.Y
	if gx < float64(vis.X) || gy < float64(vis.Y) || gx >= float64(vis.X+vis.W) || gy >= float64(vis.Y+vis.H) {
		return nil
	}
	c.cursorX, c.cursorY = c.constraint.Clamp(gx, gy)
	if c.ch.Constraints != nil {
		g := c.constraint
		g.X, g.Y, g.Warp = c.cursorX, c.cursorY, true
		latest(c.ch.Constraints, g)
	}
	// A locked pointer moved: the client knows where it put it.
	if c.constraint.Mode == domain.ConstraintLock {
		return nil
	}
	return c.rehit(ctx)
}

// syncOutputs sends the output layout when it changed; input learns the
// geometry only when the outputs themselves moved.
func (c *Core) syncOutputs(ctx context.Context) error {
	// Built in the scratch every publish, copied only when sent.
	c.scratch.layout = c.layoutInto(c.scratch.layout)
	v := ports.SetOutputs{Outputs: c.scratch.layout, Focused: c.cur().name(), Off: c.offOutputs()}
	if sameOutputs(v, c.sent.outputs) {
		return nil
	}
	v.Outputs = append(make(ports.Layout, 0, len(v.Outputs)), v.Outputs...)
	seq, err := c.queue(ctx, v)
	if err != nil {
		return err
	}
	c.sent.outputsSeq = seq
	if c.ch.Layouts != nil && !slices.Equal(v.Outputs, c.sent.outputs.Outputs) {
		latest(c.ch.Layouts, v.Outputs)
	}
	c.sent.outputs = v
	return nil
}

// syncFocus tells wayland the keyboard focus when it changed.
func (c *Core) syncFocus(ctx context.Context, focus WindowID) error {
	if focus == c.keyboard.sent {
		return nil
	}
	if err := c.command(ctx, ports.FocusWindow{ID: focus}); err != nil {
		return err
	}
	c.keyboard.sent = focus
	return nil
}

// latest replaces any unread value: only the owner sends and drains;
// consumers only receive.
func latest[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
}

func sameOutputs(a, b ports.SetOutputs) bool {
	return a.Focused == b.Focused && slices.Equal(a.Outputs, b.Outputs) && slices.Equal(a.Off, b.Off)
}

// allLayers lists the layer surfaces of every output.
func (c *Core) allLayers() []ports.LayerSurface {
	var all []ports.LayerSurface
	for _, sc := range c.screens {
		all = append(all, sc.layers...)
	}
	return all
}

func (c *Core) acceptsInput(id WindowID, x, y float64) bool {
	return c.windows.acceptsInput(id, x, y)
}

// hit returns the surface under the global logical point and the point in
// its surface coordinates: popups, then overlay and top layers, then
// windows (last visible placement is topmost), then bottom and
// background layers.
func (c *Core) hit(x, y float64) (WindowID, float64, float64) {
	if c.security.Protected {
		return c.lockHit(x, y)
	}
	i := c.screenAt(x, y)
	if i < 0 {
		return 0, 0, 0
	}
	sc := c.screens[i]
	lx, ly := x-float64(sc.x), y-float64(sc.y)
	// Layer popups are over everything; window popups are over the windows
	// only, under the top and overlay layers (as drawn).
	if id, px, py := c.popupAt(sc, lx, ly, true); id != 0 {
		return id, px, py
	}
	if id, px, py := c.layerAt(sc, lx, ly, true); id != 0 {
		return id, px, py
	}
	if id, px, py := c.popupAt(sc, lx, ly, false); id != 0 {
		return id, px, py
	}
	if sc.mon.previewing() {
		// Previews and the layers under them take no input: a click
		// picks a preview (overviewClick, switcherClick).
		return 0, 0, 0
	}
	if !sc.mon.frameHas(lx, ly) {
		return c.layerAt(sc, lx, ly, false)
	}
	var id WindowID
	var sx, sy float64
	view := sc.mon.viewport()
	for _, p := range sc.shownLayout() {
		r := c.clientRect(p)
		// A peek is clickable wherever it shows, border included: it may
		// be narrower than its border. The point is clamped to its client.
		box, lx, ly := r, lx, ly
		if p.Peek {
			box = p.Rect
		}
		// Only what is drawn takes the pointer: a tile not over a panel.
		box = intersect(box, view.area(p))
		if !p.Hidden && r.W > 0 && r.H > 0 && lx >= float64(box.X) && lx < float64(box.X+box.W) && ly >= float64(box.Y) && ly < float64(box.Y+box.H) {
			if p.Peek {
				lx, ly = min(max(lx, float64(r.X)), float64(r.X+r.W-1)), min(max(ly, float64(r.Y)), float64(r.Y+r.H-1))
			}
			if c.acceptsInput(p.ID, lx-float64(r.X), ly-float64(r.Y)) {
				// Layout is bottom to top, including fullscreen and floats.
				id, sx, sy = p.ID, lx-float64(r.X), ly-float64(r.Y)
			}
		}
	}
	if id == 0 {
		return c.layerAt(sc, lx, ly, false)
	}
	return id, sx, sy
}

// Ping returns once Run handled a probe, or once ctx ends: the watchdog
// uses it to detect a stuck owner loop.
func (c *Core) Ping(ctx context.Context) {
	ack := make(chan struct{})
	select {
	case c.ping <- ack:
	case <-ctx.Done():
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	}
}

// CommandStats returns the command outbox counters since the last call.
// Any goroutine may call it.
func (c *Core) CommandStats() CommandStats { return c.out.takeStats() }

func (c *Core) Run(ctx context.Context) error {
	// The sender stops with Run, not only with ctx: Run also returns on
	// quit and on errors. Run waits for it so nothing sends after Run.
	sendCtx, stopSender := context.WithCancel(ctx)
	senderDone := make(chan struct{})
	if c.ch.Commands != nil {
		c.out.start()
	}
	go func() {
		defer close(senderDone)
		if c.ch.Commands != nil {
			c.out.run(sendCtx, c.ch.Commands)
		}
	}()
	defer func() {
		stopSender()
		<-senderDone
	}()
	c.syncSecurity()
	defer c.stopFrame()
	defer c.stopCaptureTimer()
	defer c.stopFlashTimer()
	defer c.stopPositionHintTimer()
	defer c.cancelSwitcher()
	defer c.stopRepeat()
	// Startup commands run once per session. Full launcher queues retain a
	// bounded remainder, selectable alongside owner events without polling.
	c.startup = make([][]string, len(c.cfg.Startup))
	for i, argv := range c.cfg.Startup {
		c.startup[i] = slices.Clone(argv)
	}
	c.spawnStartup(ctx)
	if c.spawnSlots(ctx, false) != nil {
		return nil
	}
	c.publishState()
	c.publishWorkspaces()
	for {
		if c.securityCheckpoint(ctx) != nil {
			return nil
		}
		startupC, startupRequest := c.pendingStartup()
		select {
		case <-ctx.Done():
			return nil
		case startupC <- startupRequest:
			// Successful transfer consumes startup once. Only the receiver can
			// decide execution admission; a later gate read cannot infer it.
			c.startup = c.startup[1:]
			continue
		case ev, ok := <-c.ch.Client:
			if !ok {
				c.ch.Client = nil
				continue
			}
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if c.blockProtected(ev) {
				continue
			}
			publish, stop := c.clientEvent(ctx, ev)
			if stop {
				return nil
			}
			if publish {
				if c.publish(ctx) != nil {
					return nil
				}
				if c.dataDrag != nil && dataDragHitChanged(ev) && c.dataDragRetarget(ctx, c.motionTime) != nil {
					return nil
				}
			}
		case f, ok := <-c.ch.Frames:
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if !ok {
				c.ch.Frames = nil
				continue
			}
			if i := c.screenIndex(f.Output); i >= 0 && c.sliding(f.Output) && c.step(ctx, c.screens[i]) != nil {
				return nil
			}
			continue
		case ack := <-c.ping:
			close(ack)
			continue
		case <-c.frameC:
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			c.frameC = nil
			if c.step(ctx, nil) != nil {
				return nil
			}
			continue
		case <-c.positionHintTimer.timerC:
			c.positionHintTimer.timerC = nil
			if c.securityCheckpoint(ctx) != nil || c.publish(ctx) != nil {
				return nil
			}
			continue
		case <-c.flash.timerC:
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if c.flashTick() && c.publish(ctx) != nil {
				return nil
			}
			continue
		case <-c.switcher.timerC:
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if c.switcherTick() && (c.publish(ctx) != nil || c.rehit(ctx) != nil) {
				return nil
			}
			continue
		case <-c.osd.key.holdC:
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if !c.cfg.OSD.Enable || c.keyboard.inhibited(c.keyboardFocus()) {
				c.osdReset()
				continue
			}
			if c.osdApply(c.osd.key.holdFired()) && c.publish(ctx) != nil {
				return nil
			}
			continue
		case <-c.repeatC:
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if !c.repeatLive() {
				continue
			}
			// A held key is activity, as its press was: idle timers
			// must not fire while it repeats.
			if err := c.userActivity(ctx); err != nil {
				return nil
			}
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if err := c.repeatTick(ctx); err != nil {
				if errors.Is(err, errSecurityChanged) {
					continue
				}
				return err
			}
			continue
		case <-c.capt.timerC:
			c.capt.timerC, c.capt.timerStop = nil, nil
			if c.captureFlashTick() && c.publish(ctx) != nil {
				return nil
			}
			continue
		case res, ok := <-c.ch.Hints:
			if !ok {
				c.ch.Hints = nil
				continue
			}
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			if c.hintResult(res) && c.publish(ctx) != nil {
				return nil
			}
		case ev, ok := <-c.ch.Output:
			if !ok {
				c.ch.Output = nil
				continue
			}
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			switch v := ev.(type) {
			case ports.OutputAdded:
				c.addScreen(v.Info)
				if c.layerChanged {
					c.setLayers(c.allLayers())
				}
				c.cursorX, c.cursorY = c.clampPointer(c.cursorX, c.cursorY, c.cursorX, c.cursorY)
			case ports.OutputRemoved:
				// A drop target may be on the output going away.
				c.abortDrag()
				c.removeScreen(v.Name)
				if c.layerChanged {
					c.setLayers(c.allLayers())
				}
				c.cursorX, c.cursorY = c.clampPointer(c.cursorX, c.cursorY, c.cursorX, c.cursorY)
			}
			if c.workspaceVisible(ctx, false) != nil {
				return nil
			}
			if err := c.publish(ctx); err != nil {
				return nil
			}
		case ev, ok := <-c.ch.Config:
			if !ok {
				c.ch.Config = nil
				continue
			}
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			// A new keymap quarantines held keys: their release never
			// comes, so no repeat may outlive a reload.
			c.stopRepeat()
			if err := c.apply(ev.Config); err != nil {
				select {
				case c.ch.ConfigErrors <- err:
				default:
				}
				continue
			}
			if !c.animOn() {
				c.stopAnimations()
			}
			// A rotation can shrink the layout under a still pointer.
			c.cursorX, c.cursorY = c.clampPointer(c.cursorX, c.cursorY, c.cursorX, c.cursorY)
			if c.workspaceVisible(ctx, false) != nil {
				return nil
			}
			if err := c.publish(ctx); err != nil {
				return nil
			}
		case ev, ok := <-c.ch.Input:
			if !ok {
				c.ch.Input = nil
				continue
			}
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			// Even stale input is activity, but never becomes client input.
			if err := c.userActivity(ctx); err != nil {
				return nil
			}
			// Commands can backpressure; recheck after activity before admits/binds.
			if c.securityCheckpoint(ctx) != nil {
				return nil
			}
			var admitted bool
			ev, admitted = c.admitInput(ev)
			if !admitted {
				continue
			}
			if err := c.handleInput(ctx, ev); err != nil {
				if errors.Is(err, errSecurityChanged) {
					continue
				}
				return err
			}
		}
	}
}

// workspaceVisible settles guests and refills slots on a show. Publication
// subsequently decides whether visible empty workspaces need terminals.
func (c *Core) workspaceVisible(ctx context.Context, shown bool) error {
	c.releaseSlots()
	c.settleGuests()
	return c.spawnSlots(ctx, shown)
}

// spawnEmpty requests terminals for visible empty workspaces. publish calls
// it after the output layout and before scenes, so a terminal never maps
// before wayland knows its output.
func (c *Core) spawnEmpty(ctx context.Context) error {
	if c.protectionRequested() {
		return nil
	}
	first := c.firstTerminalResolved
	terms := c.fillEmpty()
	if len(terms) > 0 && c.sent.outputsSeq != 0 {
		// flush returns immediately when this layout was already delivered.
		if err := c.out.flush(ctx, c.ch.Commands, c.sent.outputsSeq); err != nil {
			for _, req := range terms {
				c.cancelClaim(req)
			}
			c.firstTerminalResolved = first
			return err
		}
	}
	issued, err := c.dispatch(ctx, terms)
	if len(terms) > 0 && issued == 0 {
		c.firstTerminalResolved = first
	}
	return err
}

// dispatch sends reqs to the launcher and reports how many it issued.
// From the first request that is not issued (context ended, or the gate
// suppressed the send), the rest are not sent and their claims are cancelled.
func (c *Core) dispatch(ctx context.Context, reqs []ports.SpawnRequest) (issued int, err error) {
	for i, req := range reqs {
		err = ctx.Err()
		if err != nil || !c.trySpawn(ctx, req) {
			for _, suppressed := range reqs[i:] {
				c.cancelClaim(suppressed)
			}
			return i, err
		}
	}
	return len(reqs), err
}

// activate brings a window forward for a valid xdg-activation token. The
// token proves a user action in the requesting client (a click on a link,
// a notification), so unlike other client requests it may move the user
// (the exception to the ADR 011 golden rule).
func (c *Core) activate(ctx context.Context, id WindowID) error {
	s, w := c.screenOf(id)
	if s == nil || c.popups[id] != nil {
		return nil
	}
	before := c.cur().mon.Current()
	if w != s.mon.Current() {
		s.mon.show(w)
	}
	w.Activate(id)
	c.focusScreen = c.screenIndex(s.name())
	return c.workspaceVisible(ctx, c.cur().mon.Current() != before)
}

// heldKey names a key for press tracking: by physical key, since Shift
// may be released before the key.
func heldKey(key ports.KeyEvent) string {
	if key.Keycode != 0 {
		return "#" + strconv.FormatUint(uint64(key.Keycode), 10)
	}
	return keyName(key.Keysym)
}
