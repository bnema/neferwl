package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrQuit = errors.New("quit requested")

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
	Scenes   chan []ports.Scene
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
	// Terminal enables automatic terminal opening according to terminal.auto-open.
	Terminal bool
	// Clock tells the time for fullscreenGrace and slides; nil is the
	// system clock.
	Clock ports.Clock
	// Frames, when set, reports outputs' page flips: a running slide moves
	// one step per flip. Without flips it moves on a timer.
	Frames <-chan ports.OutputFrame
}

// fullscreenGrace is how long after mapping a window's fullscreen request
// is ignored. Wine asks for fullscreen whenever a window has the monitor's
// size, so a launcher that remembered a fullscreen size would open
// fullscreen and hide the windows it opens next. The user can still
// fullscreen it (bind or taskbar), and an app can ask again later.
const fullscreenGrace = time.Second

type binding struct {
	mods ports.Mods
	key  string
}
type Core struct {
	nextWorkspaceID  uint64
	ch               Channels
	cfg              ports.Config
	screens          []*screen
	focusScreen      int
	binds            map[binding]Action
	pressed          map[string]bool
	sent             map[WindowID]ports.ConfigureWindow
	focus            WindowID
	pointer          WindowID
	grab             WindowID
	buttons          map[uint32]bool
	cursorX, cursorY float64 // global, logical
	pointerOutput    string  // output under the pointer at the last motion
	// constrained is the constraint wayland activated; constraint is it
	// resolved to global coordinates, as last sent to input.
	constrained ports.PointerConstrained
	constraint  ports.PointerConstraint
	seq         uint64
	// layerChanged is set once layer state arrives from wayland.
	layerChanged bool
	sentOutputs  ports.SetOutputs
	specs        []NamedWorkspace
	presets      []Width
	slots        map[slotKey]*slotState
	placement    spawnPlacement
	// toSpawn holds slots to start; Run sends them (apply has no context).
	toSpawn               []slotKey
	sentPending           bool
	firstTerminalResolved bool
	// clients holds the app ID and PID of mapped windows, for State.
	clients        map[WindowID]ports.WindowMapped
	mappedAt       map[WindowID]time.Time
	inputRegions   map[WindowID]ports.InputRegionChanged
	sentState      ports.State
	sentWorkspaces ports.Workspaces
	// popups are the placed xdg_popups; popupOrder stacks them, oldest first.
	popups     map[WindowID]*popupState
	popupOrder []WindowID
	// layerFocus is the on-demand layer surface the user clicked; it keeps
	// the keyboard while it stays mapped on-demand and no window focus
	// changes on any output (layerOver, taken at the click): any click
	// elsewhere, bind, activation or new window takes the keyboard back. The
	// pointer moving to another output does not.
	layerFocus WindowID
	layerOver  map[*screen]WindowID
	// inhibitors are the windows asking to keep compositor binds while
	// focused, inhibiting the one active now (0: none); idle are those
	// keeping the session awake.
	inhibitors map[WindowID]bool
	inhibiting WindowID
	idle       map[WindowID]bool
	// activity is when core last told wayland about user input
	// (ports.UserActivity), sent at most once per ActivityInterval.
	activity time.Time
	// swipe is the touchpad swipe in progress. frameC fires when no page
	// flip came in time to move a running slide (frameStop stops it).
	swipe     *swipeGesture
	frameC    <-chan time.Time
	frameStop func() bool
	// motionMsec is the time of the last pointer motion.
	motionMsec uint32
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
		var m ports.Mods
		switch p {
		case "Cmd":
			switch cmd {
			case "super":
				m = ports.ModSuper
			case "alt":
				m = ports.ModAlt
			case "ctrl":
				m = ports.ModCtrl
			}
		case "Shift":
			m = ports.ModShift
		case "Ctrl":
			m = ports.ModCtrl
		case "Alt":
			m = ports.ModAlt
		case "Super":
			m = ports.ModSuper
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
	presets := make([]Width, 0, len(cfg.Layout.Presets))
	for _, s := range cfg.Layout.Presets {
		v, e := ParseWidth(s)
		if e != nil {
			return e
		}
		presets = append(presets, v)
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
		if _, ok := SpawnArgv(Action(a)); ok {
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
		if _, ok := NamedArg(Action(a)); ok {
			binds[b] = Action(a)
			continue
		}
		switch Action(a) {
		case "none", ActionSpawnTerminal, ActionFocusColumnLeft, ActionFocusColumnRight, ActionFocusWindowUp, ActionFocusWindowDown, ActionMoveColumnLeft, ActionMoveColumnRight, ActionCycleColumnWidth, ActionMaximizeColumn, ActionToggleFullscreen, ActionToggleWindowStash, ActionToggleStashVisible, ActionCloseWindow, ActionQuit, ActionFocusWorkspaceUp, ActionFocusWorkspaceDown, ActionMoveColumnToWorkspaceUp, ActionMoveColumnToWorkspaceDown, ActionMoveWindowToWorkspaceUp, ActionMoveWindowToWorkspaceDown, ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionConsumeOrExpelLeft, ActionConsumeOrExpelRight:
		default:
			return fmt.Errorf("invalid action %q", a)
		}
		binds[b] = Action(a)
	}
	overflow := func(v string) (Overflow, error) {
		switch o := Overflow(v); o {
		case "", OverflowScroll, OverflowFixed:
			return o, nil
		}
		return "", fmt.Errorf("invalid overflow %q", v)
	}
	if _, err := overflow(cfg.Layout.Overflow); err != nil {
		return err
	}
	if cfg.Layout.Overflow == "" {
		cfg.Layout.Overflow = string(OverflowScroll)
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
		if ws.Name == "" || seen[ws.Name] || ws.MaxColumns < 0 {
			return fmt.Errorf("invalid workspace %q", ws.Name)
		}
		seen[ws.Name] = true
		named = append(named, NamedWorkspace{Name: ws.Name, Monitor: ws.Monitor, MaxColumns: ws.MaxColumns, Overflow: o})
	}
	specs, err := parseSlots(cfg.Workspaces)
	if err != nil {
		return err
	}
	c.cfg = cfg
	c.binds = binds
	c.specs, c.presets = named, presets
	c.toSpawn = append(c.toSpawn, c.updateSlots(specs)...)
	c.named()
	for _, s := range c.screens {
		c.settings(s.mon)
	}
	c.applyConfigScales()
	return nil
}

// publishPending tells wayland at once when slots start waiting, before
// their windows can map.
func (c *Core) publishPending(ctx context.Context) error {
	if p := c.placement.anyPending(); p != c.sentPending {
		if err := c.command(ctx, ports.SlotsPending{Pending: p}); err != nil {
			return err
		}
		c.sentPending = p
	}
	return nil
}

// spawnSlots starts the slots queued by config. With shown set (the user
// just switched to another workspace), it also refills the empty slots of
// the workspace now on screen.
func (c *Core) spawnSlots(ctx context.Context, shown bool) error {
	keys := c.toSpawn
	c.toSpawn = nil
	if shown {
		keys = append(keys, c.refill()...)
	}
	started := map[slotKey]bool{}
	for _, key := range keys {
		st, ok := c.slots[key]
		// A key can be queued twice (new slot on the workspace on screen).
		if !ok || started[key] || st.window != 0 {
			continue
		}
		started[key] = true
		req := c.spawnSlot(key)
		// Wayland must read slot tokens before this window can map.
		if err := c.publishPending(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c.ch.Spawn <- req:
		}
	}
	return nil
}
func New(cfg ports.Config, ch Channels) (*Core, error) {
	if cap(ch.Scenes) != 1 || (ch.Layouts != nil && cap(ch.Layouts) != 1) || (ch.Constraints != nil && cap(ch.Constraints) != 1) || (ch.State != nil && cap(ch.State) != 1) || (ch.Workspaces != nil && cap(ch.Workspaces) != 1) {
		return nil, fmt.Errorf("scenes, layouts, constraints, state and workspaces must have capacity 1")
	}
	// A placeholder screen holds windows until the first output arrives.
	c := &Core{slots: map[slotKey]*slotState{}, placement: newSpawnPlacement(), clients: map[WindowID]ports.WindowMapped{}, mappedAt: map[WindowID]time.Time{}, inputRegions: map[WindowID]ports.InputRegionChanged{}, popups: map[WindowID]*popupState{}, ch: ch, pressed: map[string]bool{}, buttons: map[uint32]bool{}, sent: map[WindowID]ports.ConfigureWindow{}, inhibitors: map[WindowID]bool{}, idle: map[WindowID]bool{}}
	c.screens = []*screen{{mon: newMonitorWithIDs("", "", &c.nextWorkspaceID), scale: 1, cfgScale: 1}}
	if err := c.apply(cfg); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Core) now() time.Time {
	if c.ch.Clock != nil {
		return c.ch.Clock.Now()
	}
	return time.Now()
}

func (c *Core) command(ctx context.Context, v ports.ClientCommand) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.ch.Commands <- v:
		return nil
	}
}

// clientRect is the placement minus the border on its inset sides.
// The renderer applies the same inset (ports.Border).
func (c *Core) clientRect(p Placement) Rect {
	if p.Fullscreen {
		return p.Rect
	}
	return p.Rect.Inset(p.Inset, c.cfg.Border.Width)
}

// onScreen reports whether a placement is drawn on its output o: not
// hidden and not scrolled off.
func onScreen(p Placement, o Rect) bool {
	return !p.Hidden && p.Rect.Overlaps(Rect{W: o.W, H: o.H})
}

// floatDim is the veil opacity of a layout: dim while a float is drawn
// over the tiles, none under a fullscreen float (nothing is seen below).
func floatDim(layout []Placement, o Rect, dim float64) float64 {
	shown := false
	for _, p := range layout {
		if p.Floating && onScreen(p, o) {
			if p.Fullscreen {
				return 0
			}
			shown = true
		}
	}
	if !shown {
		return 0
	}
	return dim
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

// keyboardFocus is the mapped top/overlay layer with exclusive keyboard
// interactivity and the highest ID on any output, else a grabbing popup,
// else a clicked on-demand layer (see layerFocus), else the focused window
// of the focused output.
func (c *Core) keyboardFocus() WindowID {
	var layer WindowID
	for _, sc := range c.screens {
		for _, l := range sc.layers {
			if l.Keyboard == 1 && (l.Layer == ports.LayerTop || l.Layer == ports.LayerOverlay) && l.ID > layer {
				layer = l.ID
			}
		}
	}
	if layer != 0 {
		return layer
	}
	// A menu with a grab takes the keyboard until it closes.
	if g := c.grabFocus(); g != 0 {
		return g
	}
	// A layer hidden by a fullscreen window loses the keyboard.
	if c.layerFocus != 0 && (!c.onDemand(c.layerFocus) || !c.visible(c.layerFocus) || !maps.Equal(c.layerOver, c.windowFocus())) {
		c.layerFocus, c.layerOver = 0, nil
	}
	if c.layerFocus != 0 {
		return c.layerFocus
	}
	id, _ := c.cur().mon.Focused()
	return id
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
		delete(c.inputRegions, id)
	}
	for _, sc := range c.screens {
		sc.arrange()
	}
}

func (c *Core) publish(ctx context.Context) error {
	if err := c.closeHiddenPopups(ctx); err != nil {
		return err
	}
	// Clients learn outputs and scales before the configures sized for them.
	if v := (ports.SetOutputs{Outputs: c.layout(), Focused: c.cur().name(), Off: c.offOutputs()}); !sameOutputs(v, c.sentOutputs) {
		if err := c.command(ctx, v); err != nil {
			return err
		}
		if !slices.Equal(v.Outputs, c.sentOutputs.Outputs) && c.ch.Layouts != nil {
			latest(c.ch.Layouts, v.Outputs)
		}
		c.sentOutputs = v
	}
	// Output layout must be known before an automatic terminal can map.
	if err := c.spawnEmpty(ctx); err != nil {
		return err
	}
	if err := c.publishPending(ctx); err != nil {
		return err
	}
	alive := map[WindowID]bool{}
	focus := c.keyboardFocus()
	scenes := make([]ports.Scene, 0, len(c.screens))
	// Before the first output (and after the last is unplugged) the
	// placeholder's scene has no output name: no renderer draws it.
	for i, sc := range c.screens {
		c.seq++
		o := sc.mon.Output()
		scene := ports.Scene{Output: sc.name(), Seq: c.seq, OutputWidth: o.W, OutputHeight: o.H, Scale: sc.scale, Off: sc.off, Background: c.cfg.Background.Color, Border: ports.Border{Width: c.cfg.Border.Width, Active: c.cfg.Border.Active, Inactive: c.cfg.Border.Inactive}, Windows: make([]ports.SceneWindow, 0), Layers: shownLayers(sc)}
		layout := sc.mon.Layout()
		scene.Dim = floatDim(layout, o, c.cfg.Floating.Dim)
		// Only the focused output lights the focused window's lines.
		scene.Separators = separators(layout, c.cfg.Border.Width, sc.mon.Current().gap(), Rect{W: o.W, H: o.H}, i == c.focusScreen)
		for _, p := range layout {
			alive[p.ID] = true
			// Only the focused output has an activated window.
			focused := p.Focused && i == c.focusScreen
			sw := ports.SceneWindow{ID: p.ID, Rect: p.Rect, Focused: focused, Fullscreen: p.Fullscreen, Hidden: p.Hidden, Floating: p.Floating, Inset: p.Inset}
			if p.Peek {
				sw.Dim = c.cfg.Stash.Dim
			}
			scene.Windows = append(scene.Windows, sw)
			floating := p.Floating
			if p.Hidden {
				// A hidden window keeps its size, state and output; it is
				// only deactivated and marked invisible. One mapped hidden
				// is told so at once, sized by the client.
				old, ok := c.sent[p.ID]
				v := old
				if !ok {
					v = ports.ConfigureWindow{ID: p.ID, Floating: floating}
				}
				v.Activated, v.Visible, v.Output = false, false, sc.name()
				if ok && v == old {
					continue
				}
				if err := c.command(ctx, v); err != nil {
					return err
				}
				c.sent[p.ID] = v
				continue
			}
			r := c.clientRect(p)
			v := ports.ConfigureWindow{ID: p.ID, Width: r.W, Height: r.H, Fullscreen: p.Fullscreen, Activated: focused, Floating: floating && !p.Fullscreen, Output: sc.name(), Visible: onScreen(p, o)}
			if v.Floating && !sc.mon.Current().imposedFloat(p.ID) {
				// Native floating windows pick their own size.
				v.Width, v.Height = 0, 0
			}
			if old, ok := c.sent[p.ID]; !ok || old != v {
				if err := c.command(ctx, v); err != nil {
					return err
				}
				c.sent[p.ID] = v
			}
		}
		scene.Windows = append(scene.Windows, c.scenePopups(sc)...)
		scenes = append(scenes, scene)
	}
	for id := range c.sent {
		if !alive[id] {
			delete(c.sent, id)
		}
	}
	// A workspace switch can hide the window under the pointer; it must not get
	// clicks. The next motion re-runs hit-testing.
	if c.pointer != 0 && !c.visible(c.pointer) {
		c.pointer = 0
		if c.grab == 0 {
			if err := c.command(ctx, ports.PointerFocus{}); err != nil {
				return err
			}
		}
	}
	if focus != c.focus {
		if err := c.command(ctx, ports.FocusWindow{ID: focus}); err != nil {
			return err
		}
		c.focus = focus
	}
	if err := c.updateInhibit(ctx); err != nil {
		return err
	}
	c.resolveConstraint()
	latest(c.ch.Scenes, scenes)
	c.publishState()
	c.publishWorkspaces()
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

// step moves the running slides to now and publishes the frame.
func (c *Core) step(ctx context.Context) error {
	c.stopFrame()
	c.animate(c.now())
	return c.slid(ctx, false)
}

// slid publishes a moved view; shown reports another workspace came on
// screen.
func (c *Core) slid(ctx context.Context, shown bool) error {
	if err := c.workspaceVisible(ctx, shown); err != nil {
		return err
	}
	if err := c.publish(ctx); err != nil {
		return err
	}
	return c.rehit(ctx)
}

// rehit points the pointer at what now lies under a still cursor, as
// windows slide under it. A held button keeps its grab.
func (c *Core) rehit(ctx context.Context) error {
	if c.grab != 0 {
		return nil
	}
	id, x, y := c.hit(c.cursorX, c.cursorY)
	if id != c.pointer {
		c.pointer = id
		return c.command(ctx, ports.PointerFocus{ID: id, X: x, Y: y})
	}
	if id == 0 {
		return nil
	}
	// The same window moved under the cursor: it moves in the window. No
	// relative motion: the device did not move. The input clock is the
	// device's; the last motion's time is the closest core knows.
	return c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, TimeMsec: c.motionMsec})
}

// updateInhibit makes the shortcuts inhibitor of the keyboard focus the
// active one and tells wayland when it changes.
func (c *Core) updateInhibit(ctx context.Context) error {
	want := WindowID(0)
	if f := c.keyboardFocus(); f != 0 && c.inhibitors[f] {
		want = f
	}
	if want == c.inhibiting {
		return nil
	}
	if c.inhibiting != 0 {
		if err := c.command(ctx, ports.ShortcutsInhibitState{Window: c.inhibiting}); err != nil {
			return err
		}
	}
	c.inhibiting = want
	if want != 0 {
		return c.command(ctx, ports.ShortcutsInhibitState{Window: want, Active: true})
	}
	return nil
}

// resolveConstraint turns the active constraint into global coordinates
// and sends it to input when it changed. A hidden window holds nothing.
func (c *Core) resolveConstraint() {
	var g ports.PointerConstraint
	if id := c.constrained.ID; id != 0 {
		if s, _ := c.screenOf(id); s != nil {
			for _, p := range s.mon.Layout() {
				if p.ID != id || p.Hidden {
					continue
				}
				r := c.clientRect(p)
				r.X += s.x
				r.Y += s.y
				if w := c.constrained.Rect; w.W > 0 && w.H > 0 {
					// The region is clipped to the window.
					x0, y0 := max(r.X, r.X+w.X), max(r.Y, r.Y+w.Y)
					x1, y1 := min(r.X+r.W, r.X+w.X+w.W), min(r.Y+r.H, r.Y+w.Y+w.H)
					if x1 > x0 && y1 > y0 {
						r = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
					}
				}
				g = ports.PointerConstraint{Mode: c.constrained.Mode, Rect: r}
			}
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
	r, ok := c.inputRegions[id]
	if !ok || r.All {
		return true
	}
	for _, box := range r.Rects {
		if x >= float64(box.X) && x < float64(box.X+box.W) && y >= float64(box.Y) && y < float64(box.Y+box.H) {
			return true
		}
	}
	return false
}

// hit returns the surface under the global logical point and the point in
// its surface coordinates: popups, then overlay and top layers, then
// windows (fullscreen wins; otherwise the last visible placement is
// topmost), then bottom and background layers.
func (c *Core) hit(x, y float64) (WindowID, float64, float64) {
	o, ok := c.layout().At(x, y)
	if !ok {
		return 0, 0, 0
	}
	lx, ly := x-float64(o.X), y-float64(o.Y)
	sc := c.screens[c.screenIndex(o.Info.Name)]
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
	var id WindowID
	var sx, sy float64
	full := false
	for _, p := range sc.mon.Layout() {
		r := c.clientRect(p)
		// A peek is clickable wherever it shows, border included: it may
		// be narrower than its border. The point is clamped to its client.
		box, lx, ly := r, lx, ly
		if p.Peek {
			box = p.Rect
		}
		if !p.Hidden && r.W > 0 && r.H > 0 && lx >= float64(box.X) && lx < float64(box.X+box.W) && ly >= float64(box.Y) && ly < float64(box.Y+box.H) {
			if p.Peek {
				lx, ly = min(max(lx, float64(r.X)), float64(r.X+r.W-1)), min(max(ly, float64(r.Y)), float64(r.Y+r.H-1))
			}
			// Floating windows come last in the layout and are on top,
			// even of a fullscreen window; nothing else is.
			if full && !p.Floating {
				continue
			}
			if !c.acceptsInput(p.ID, lx-float64(r.X), ly-float64(r.Y)) {
				continue
			}
			if id == 0 || p.Fullscreen || p.Floating {
				id, sx, sy = p.ID, lx-float64(r.X), ly-float64(r.Y)
			}
			full = full || p.Fullscreen
		}
	}
	if id == 0 {
		return c.layerAt(sc, lx, ly, false)
	}
	return id, sx, sy
}

func (c *Core) Run(ctx context.Context) error {
	defer c.stopFrame()
	// Startup commands run once per session; a config reload does not
	// run them again.
	for _, argv := range c.cfg.Startup {
		select {
		case <-ctx.Done():
			return nil
		case c.ch.Spawn <- ports.SpawnRequest{Argv: slices.Clone(argv)}:
		}
	}
	if c.spawnSlots(ctx, false) != nil || c.publishPending(ctx) != nil {
		return nil
	}
	c.publishState()
	c.publishWorkspaces()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-c.ch.Client:
			if !ok {
				c.ch.Client = nil
				continue
			}
			switch v := ev.(type) {
			case ports.LayerChanged:
				c.layerChanged = true
				c.setLayers(v.Layers)
			case ports.InputRegionChanged:
				c.inputRegions[v.ID] = v
			case ports.WindowMapped:
				c.clients[v.ID] = v
				c.mappedAt[v.ID] = c.now()
				c.placement.place(c, v)
			case ports.WindowResized:
				if _, w := c.screenOf(v.ID); w != nil {
					w.ResizeFloating(v.ID, v.Width, v.Height)
				}
			case ports.PopupRequest:
				if err := c.placePopup(ctx, v); err != nil {
					return nil
				}
			case ports.PopupMapped:
				if p := c.popups[v.ID]; p != nil {
					p.mapped = true
				}
			case ports.ShortcutsInhibit:
				if v.Active {
					c.inhibitors[v.Window] = true
				} else {
					delete(c.inhibitors, v.Window)
				}
				if err := c.updateInhibit(ctx); err != nil {
					return nil
				}
				continue
			case ports.OutputPower:
				if i := c.screenIndex(v.Output); i >= 0 && c.screens[i].off == v.On {
					c.screens[i].off = !v.On
				}
			case ports.IdleInhibit:
				if v.Active {
					c.idle[v.Window] = true
				} else {
					delete(c.idle, v.Window)
				}
				c.publishState()
				continue
			case ports.WindowAppID:
				if info, ok := c.clients[v.ID]; ok {
					info.AppID = v.AppID
					c.clients[v.ID] = info
				}
			case ports.WindowUnmapped:
				if c.popups[v.ID] != nil {
					if err := c.dropPopup(ctx, v.ID); err != nil {
						return nil
					}
				}
				if err := c.closePopupsOf(ctx, v.ID); err != nil {
					return nil
				}
				delete(c.clients, v.ID)
				delete(c.mappedAt, v.ID)
				delete(c.inputRegions, v.ID)
				delete(c.inhibitors, v.ID)
				delete(c.idle, v.ID)
				if s, _ := c.screenOf(v.ID); s != nil {
					s.mon.RemoveWindow(v.ID)
				}
				c.releaseSlots()
				if c.pointer == v.ID {
					c.pointer = 0
					if err := c.command(ctx, ports.PointerFocus{}); err != nil {
						return nil
					}
				}
			case ports.PointerConstrained:
				c.constrained = v
			case ports.WindowFullscreenRequest:
				if v.Fullscreen && !v.External && c.now().Sub(c.mappedAt[v.ID]) < fullscreenGrace {
					continue
				}
				if s, _ := c.screenOf(v.ID); s != nil {
					s.mon.SetFullscreen(v.ID, v.Fullscreen)
				}
			case ports.WorkspaceActivate:
				before := c.cur().mon.Current()
				for _, id := range v.IDs {
					for i, sc := range c.screens {
						if sc.name() == "" {
							continue
						}
						for _, w := range sc.mon.all() {
							if w.ID == id {
								c.focusScreen = i
								sc.mon.show(w)
								break
							}
						}
					}
				}
				if c.workspaceVisible(ctx, c.cur().mon.Current() != before) != nil {
					return nil
				}
			case ports.WindowActivate:
				if c.activate(ctx, v.ID) != nil {
					return nil
				}
			}
			if err := c.publish(ctx); err != nil {
				return nil
			}
		case f, ok := <-c.ch.Frames:
			if !ok {
				c.ch.Frames = nil
				continue
			}
			if c.sliding(f.Output) && c.step(ctx) != nil {
				return nil
			}
			continue
		case <-c.frameC:
			c.frameC, c.frameStop = nil, nil
			if c.step(ctx) != nil {
				return nil
			}
			continue
		case ev, ok := <-c.ch.Output:
			if !ok {
				c.ch.Output = nil
				continue
			}
			switch v := ev.(type) {
			case ports.OutputAdded:
				c.addScreen(v.Info)
				if c.layerChanged {
					c.setLayers(c.allLayers())
				}
			case ports.OutputRemoved:
				c.removeScreen(v.Name)
				if c.layerChanged {
					c.setLayers(c.allLayers())
				}
				c.cursorX, c.cursorY = c.layout().Clamp(c.cursorX, c.cursorY, c.cursorX, c.cursorY)
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
			if err := c.apply(ev.Config); err != nil {
				select {
				case c.ch.ConfigErrors <- err:
				default:
				}
				continue
			}
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
			if err := c.userActivity(ctx); err != nil {
				return nil
			}
			switch v := ev.(type) {
			case ports.PointerMotion:
				c.motionMsec = v.TimeMsec
				// A locked pointer stays still; relative motion still flows.
				if c.constraint.Mode != ports.ConstraintLock {
					c.cursorX, c.cursorY = c.constraint.Clamp(c.layout().Clamp(c.cursorX, c.cursorY, v.X, v.Y))
				}
				// The focused screen follows the pointer, so new windows
				// and launchers open where the user is.
				// Only a pointer entering another output switches: keyboard
				// moves to another screen stick until then. Not mid-drag.
				if o, ok := c.layout().At(c.cursorX, c.cursorY); ok && o.Info.Name != c.pointerOutput && c.grab == 0 {
					c.pointerOutput = o.Info.Name
					if o.Info.Name != c.cur().name() {
						c.focusScreen = c.screenIndex(o.Info.Name)
						if err := c.publish(ctx); err != nil {
							return nil
						}
					}
				}
				id, x, y := c.hit(c.cursorX, c.cursorY)
				// Layout changes are intentionally re-hit-tested only on motion.
				if id != c.pointer {
					c.pointer = id
					if err := c.command(ctx, ports.PointerFocus{ID: id, X: x, Y: y}); err != nil {
						return nil
					}
				}
				if id != 0 {
					if err := c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, DX: v.DX, DY: v.DY, UnaccelDX: v.UnaccelDX, UnaccelDY: v.UnaccelDY, TimeMsec: v.TimeMsec, TimeUsec: v.TimeUsec}); err != nil {
						return nil
					}
				}
				continue
			case ports.PointerButton:
				id := c.pointer
				if c.grab != 0 {
					id = c.grab
				}
				if v.Pressed && len(c.buttons) == 0 {
					// A click outside an open menu closes it.
					if err := c.dismissGrabs(ctx, id); err != nil {
						return nil
					}
				}
				if v.Pressed {
					if len(c.buttons) == 0 {
						c.grab = id
					}
					c.buttons[v.Button] = true
				} else {
					delete(c.buttons, v.Button)
				}
				if id != 0 {
					if err := c.command(ctx, ports.PointerButtonTo{ID: id, Button: v.Button, Pressed: v.Pressed, TimeMsec: v.TimeMsec}); err != nil {
						return nil
					}
					// A click on an on-demand layer gives it the keyboard.
					if v.Pressed && c.onDemand(id) && c.layerFocus != id {
						c.layerFocus = id
						c.layerOver = c.windowFocus()
						if err := c.publish(ctx); err != nil {
							return nil
						}
					} else if v.Pressed && c.layerFocus != 0 && id != c.layerFocus && c.popupRoot(id) != c.layerFocus {
						// A click anywhere else takes the keyboard back.
						c.layerFocus = 0
						if err := c.publish(ctx); err != nil {
							return nil
						}
					}
					// A click focuses the window and its output.
					s, w := c.screenOf(id)
					if v.Pressed && s != nil && w == s.mon.Current() && (c.focus != id || s != c.cur()) {
						w.Click(id)
						c.focusScreen = c.screenIndex(s.name())
						if err := c.publish(ctx); err != nil {
							return nil
						}
					}
				}
				if len(c.buttons) == 0 {
					c.grab = 0
				}
				continue
			case ports.PointerAxis:
				// Scroll goes to the window under the pointer, which has the
				// pointer focus even mid-drag.
				if c.pointer != 0 {
					if err := c.command(ctx, ports.PointerAxisTo{ID: c.pointer, Axis: v}); err != nil {
						return nil
					}
				}
				continue
			case ports.SwipeBegin:
				if c.swipeBegin(v.Time) && c.slid(ctx, false) != nil {
					return nil
				}
				continue
			case ports.SwipeUpdate:
				if c.swipeUpdate(v) && c.slid(ctx, false) != nil {
					return nil
				}
				continue
			case ports.SwipeEnd:
				if c.slid(ctx, c.swipeEnd(v)) != nil {
					return nil
				}
				continue
			}
			key, ok := ev.(ports.KeyEvent)
			if !ok {
				continue
			}
			// A focused window inhibiting shortcuts gets every key:
			// no bind runs (emergency quit and VT switch are handled by
			// input before core).
			if c.inhibiting != 0 && c.inhibiting == c.keyboardFocus() {
				if held := heldKey(key); !key.Pressed && c.pressed[held] {
					// Its press ran a bind before inhibiting began: the
					// window never saw it.
					delete(c.pressed, held)
					continue
				}
				if err := c.command(ctx, ports.ForwardKey{ID: c.inhibiting, Key: key}); err != nil {
					return nil
				}
				continue
			}
			name := keyName(key.Keysym)
			// A bind on the keysym wins; then the unshifted keysym (Cmd+Shift+1
			// prints exclam on US); then the physical key (code:N).
			action, bound := c.binds[binding{key: name, mods: key.Mods}]
			if !bound && key.Base != "" {
				action, bound = c.binds[binding{key: keyName(key.Base), mods: key.Mods}]
			}
			if !bound && key.Keycode != 0 {
				action, bound = c.binds[binding{key: "code:" + strconv.FormatUint(uint64(key.Keycode), 10), mods: key.Mods}]
			}
			// Track presses by physical key: Shift may be released before the key.
			held := heldKey(key)
			if !key.Pressed {
				consumed := c.pressed[held]
				delete(c.pressed, held)
				if consumed {
					continue
				}
			}
			if key.Pressed {
				if bound {
					c.pressed[held] = true
					if action == ActionScaleUp || action == ActionScaleDown {
						dir := 1
						if action == ActionScaleDown {
							dir = -1
						}
						sc := c.cur()
						prev := sc.scale
						sc.setScale(StepScale(sc.info.Width, sc.info.Height, sc.scale, dir))
						c.order()
						if c.ch.Scales != nil && sc.scale != prev && sc.name() != "" {
							// Never wait on persistence: a stalled save drops the change.
							select {
							case c.ch.Scales <- ports.ScaleChanged{Output: sc.name(), Scale: sc.scale}:
							default:
							}
						}
						if err := c.publish(ctx); err != nil {
							return nil
						}
						continue
					}
					before := c.cur().mon.Current()
					c.layerFocus = 0 // a bind acts on the windows
					effect := c.applyAction(action)
					if effect.Quit {
						return ErrQuit
					}
					if effect.Spawn {
						argv := effect.Argv
						if argv == nil {
							argv = append([]string(nil), c.cfg.Terminal.Command...)
						}
						select {
						case <-ctx.Done():
							return nil
						case c.ch.Spawn <- ports.SpawnRequest{Argv: argv}:
						}
					}
					if c.workspaceVisible(ctx, c.cur().mon.Current() != before) != nil {
						return nil
					}
					if effect.Close != 0 {
						if err := c.command(ctx, ports.CloseWindow{ID: effect.Close}); err != nil {
							return nil
						}
					}
					if err := c.publish(ctx); err != nil {
						return nil
					}
					continue
				}
				c.pressed[held] = false
			}
			if id := c.keyboardFocus(); id != 0 {
				if err := c.command(ctx, ports.ForwardKey{ID: id, Key: key}); err != nil {
					return nil
				}
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
	terms := c.fillEmpty()
	if err := c.publishPending(ctx); err != nil {
		return err
	}
	for _, req := range terms {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c.ch.Spawn <- req:
		}
	}
	return nil
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
	if full := w.cover(); full != 0 && full != id && w.origin != nil {
		// The window hides under a fullscreen workspace: it goes home first.
		s.mon.leaveFullscreen(w, false)
		_, w = c.screenOf(id)
	}
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
