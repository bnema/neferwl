package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/bnema/nefertty/internal/ports"
	"slices"
	"strconv"
	"strings"
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
	Layouts      chan ports.Layout
	ConfigErrors chan<- error
	// Terminal, when set, keeps a window on every workspace on screen: an
	// empty one gets the configured terminal (the terminal is the desktop).
	Terminal bool
}
type binding struct {
	mods ports.Mods
	key  string
}
type Core struct {
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
	seq              uint64
	// layerChanged is set once layer state arrives from wayland.
	layerChanged bool
	sentOutputs  ports.SetOutputs
	specs        []NamedWorkspace
	presets      []Width
	overflow     Overflow
	slots        map[slotKey]*slotState
	// toSpawn holds slots to start; Run sends them (apply has no context).
	toSpawn     []slotKey
	sentPending bool
	// terms are the terminals spawned for empty workspaces, by SlotEnv
	// token, until their window maps.
	terms map[string]*termSpawn
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
		case "none", ActionSpawnTerminal, ActionFocusColumnLeft, ActionFocusColumnRight, ActionFocusWindowUp, ActionFocusWindowDown, ActionMoveColumnLeft, ActionMoveColumnRight, ActionCycleColumnWidth, ActionToggleFullscreen, ActionCloseWindow, ActionQuit, ActionFocusWorkspaceUp, ActionFocusWorkspaceDown, ActionMoveToWorkspaceUp, ActionMoveToWorkspaceDown, ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight:
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
	defOverflow, err := overflow(cfg.Layout.Overflow)
	if err != nil {
		return err
	}
	if defOverflow == "" {
		defOverflow = OverflowScroll
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
		named = append(named, NamedWorkspace{Name: ws.Name, Monitor: ws.Monitor, Hidden: ws.Hidden, MaxColumns: ws.MaxColumns, Overflow: o})
	}
	specs, err := parseSlots(cfg.Workspaces)
	if err != nil {
		return err
	}
	c.cfg = cfg
	c.binds = binds
	c.specs, c.presets, c.overflow = named, presets, defOverflow
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
	if p := c.anyPending(); p != c.sentPending {
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
	if cap(ch.Scenes) != 1 || (ch.Layouts != nil && cap(ch.Layouts) != 1) {
		return nil, fmt.Errorf("scenes and layouts must have capacity 1")
	}
	// A placeholder screen holds windows until the first output arrives.
	c := &Core{screens: []*screen{{mon: NewMonitor(), scale: 1, cfgScale: 1}}, slots: map[slotKey]*slotState{}, terms: map[string]*termSpawn{}, ch: ch, pressed: map[string]bool{}, buttons: map[uint32]bool{}, sent: map[WindowID]ports.ConfigureWindow{}}
	if err := c.apply(cfg); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Core) command(ctx context.Context, v ports.ClientCommand) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.ch.Commands <- v:
		return nil
	}
}

// clientRect is the placement minus the border drawn around the client.
// The renderer applies the same inset (ports.Border).
func (c *Core) clientRect(p Placement) Rect {
	r := p.Rect
	if p.Fullscreen || p.Borderless {
		return r
	}
	b := min(max(c.cfg.Border.Width, 0), r.W/2, r.H/2)
	return Rect{X: r.X + b, Y: r.Y + b, W: r.W - 2*b, H: r.H - 2*b}
}

// visible reports whether the window is on screen on any output.
func (c *Core) visible(id WindowID) bool {
	for _, sc := range c.screens {
		for _, p := range sc.mon.Layout() {
			if p.ID == id {
				return !p.Hidden
			}
		}
	}
	return false
}

// keyboardFocus is the mapped top/overlay layer with exclusive keyboard
// interactivity and the highest ID on any output, else the focused window of
// the focused output. On-demand layers never take focus automatically. When
// the layer unmaps, focus returns to the window.
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
	id, _ := c.cur().mon.Focused()
	return id
}

// setLayers splits the mapped layer surfaces by output. A surface without an
// output, or on an unknown one, goes to the focused output.
func (c *Core) setLayers(all []ports.LayerSurface) {
	for _, sc := range c.screens {
		sc.layers = nil
	}
	for _, l := range all {
		sc := c.cur()
		if i := c.screenIndex(l.Output); i >= 0 {
			sc = c.screens[i]
		}
		sc.layers = append(sc.layers, l)
	}
	for _, sc := range c.screens {
		sc.arrange()
	}
}

func (c *Core) publish(ctx context.Context) error {
	// Clients learn outputs and scales before the configures sized for them.
	if v := (ports.SetOutputs{Outputs: c.layout(), Focused: c.cur().name()}); !sameOutputs(v, c.sentOutputs) {
		if err := c.command(ctx, v); err != nil {
			return err
		}
		if !slices.Equal(v.Outputs, c.sentOutputs.Outputs) && c.ch.Layouts != nil {
			latest(c.ch.Layouts, v.Outputs)
		}
		c.sentOutputs = v
	}
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
	alive := map[WindowID]bool{}
	focus := c.keyboardFocus()
	scenes := make([]ports.Scene, 0, len(c.screens))
	// Before the first output (and after the last is unplugged) the
	// placeholder's scene has no output name: no renderer draws it.
	for i, sc := range c.screens {
		c.seq++
		o := sc.mon.Output()
		scene := ports.Scene{Output: sc.name(), Seq: c.seq, OutputWidth: o.W, OutputHeight: o.H, Scale: sc.scale, Background: c.cfg.Background.Color, Border: ports.Border{Width: c.cfg.Border.Width, Active: c.cfg.Border.Active, Inactive: c.cfg.Border.Inactive}, Windows: make([]ports.SceneWindow, 0), Layers: append([]ports.SceneLayer(nil), sc.placed...)}
		for _, p := range sc.mon.Layout() {
			alive[p.ID] = true
			// Only the focused output shows the focused border.
			focused := p.Focused && i == c.focusScreen
			scene.Windows = append(scene.Windows, ports.SceneWindow{ID: p.ID, Rect: p.Rect, Focused: focused, Fullscreen: p.Fullscreen, Hidden: p.Hidden, Borderless: p.Borderless})
			if p.Hidden {
				if old, ok := c.sent[p.ID]; ok && (old.Activated || old.Output != "") {
					old.Activated, old.Output = false, ""
					if err := c.command(ctx, old); err != nil {
						return err
					}
					c.sent[p.ID] = old
				}
				continue
			}
			r := c.clientRect(p)
			v := ports.ConfigureWindow{ID: p.ID, Width: r.W, Height: r.H, Fullscreen: p.Fullscreen, Activated: focused, Output: sc.name()}
			if old, ok := c.sent[p.ID]; !ok || old != v {
				if err := c.command(ctx, v); err != nil {
					return err
				}
				c.sent[p.ID] = v
			}
		}
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
	latest(c.ch.Scenes, scenes)
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
	return a.Focused == b.Focused && slices.Equal(a.Outputs, b.Outputs)
}

// allLayers lists the layer surfaces of every output.
func (c *Core) allLayers() []ports.LayerSurface {
	var all []ports.LayerSurface
	for _, sc := range c.screens {
		all = append(all, sc.layers...)
	}
	return all
}

// hit returns the window under the global logical point and the point in
// its surface coordinates. Fullscreen wins; otherwise the last visible
// placement is topmost.
func (c *Core) hit(x, y float64) (WindowID, float64, float64) {
	o, ok := c.layout().At(x, y)
	if !ok {
		return 0, 0, 0
	}
	lx, ly := x-float64(o.X), y-float64(o.Y)
	var id WindowID
	var sx, sy float64
	for _, p := range c.screens[c.screenIndex(o.Info.Name)].mon.Layout() {
		r := c.clientRect(p)
		if !p.Hidden && r.W > 0 && r.H > 0 && lx >= float64(r.X) && lx < float64(r.X+r.W) && ly >= float64(r.Y) && ly < float64(r.Y+r.H) {
			if id == 0 || p.Fullscreen {
				id, sx, sy = p.ID, lx-float64(r.X), ly-float64(r.Y)
			}
			if p.Fullscreen {
				break
			}
		}
	}
	return id, sx, sy
}

func (c *Core) Run(ctx context.Context) error {
	if c.spawnSlots(ctx, false) != nil || c.publishPending(ctx) != nil {
		return nil
	}
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
			case ports.WindowMapped:
				if v.Slot == "" || (!c.placeSlotWindow(v.ID, v.Slot) && !c.placeTerminal(v.ID, v.Slot)) {
					if s, _ := c.screenOf(v.ID); s == nil {
						c.cur().mon.AddWindow(v.ID)
					}
				}
			case ports.WindowUnmapped:
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
			case ports.WindowFullscreenRequest:
				if s, _ := c.screenOf(v.ID); s != nil {
					s.mon.SetFullscreen(v.ID, v.Fullscreen)
				}
			}
			if err := c.publish(ctx); err != nil {
				return nil
			}
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
			if c.spawnSlots(ctx, false) != nil {
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
			switch v := ev.(type) {
			case ports.PointerMotion:
				c.cursorX, c.cursorY = c.layout().Clamp(c.cursorX, c.cursorY, v.X, v.Y)
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
					if err := c.command(ctx, ports.PointerMotionTo{ID: id, X: x, Y: y, TimeMsec: v.TimeMsec}); err != nil {
						return nil
					}
				}
				continue
			case ports.PointerButton:
				id := c.pointer
				if c.grab != 0 {
					id = c.grab
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
					// A click focuses the window and its output.
					if s, w := c.screenOf(id); v.Pressed && s != nil && w == s.mon.Current() && (c.focus != id || s != c.cur()) {
						w.FocusID(id)
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
			}
			key, ok := ev.(ports.KeyEvent)
			if !ok {
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
			held := name
			if key.Keycode != 0 {
				held = "#" + strconv.FormatUint(uint64(key.Keycode), 10)
			}
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
						sc.setScale(StepScale(sc.info.Width, sc.info.Height, sc.scale, dir))
						c.order()
						if err := c.publish(ctx); err != nil {
							return nil
						}
						continue
					}
					before := c.cur().mon.Current()
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
					// Showing a declared workspace refills its empty slots.
					c.releaseSlots()
					c.settleGuests()
					if c.spawnSlots(ctx, c.cur().mon.Current() != before) != nil {
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
