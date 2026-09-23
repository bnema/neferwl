package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/bnema/nefertty/internal/ports"
	"strings"
	"unicode"
)

var ErrQuit = errors.New("quit requested")

// Channels connects the owner to adapters. scenes must have capacity 1: core is
// its only sender and may drain a stale scene; consumers only receive.
type Channels struct {
	Client       <-chan ports.ClientEvent
	Input        <-chan ports.InputEvent
	Output       <-chan ports.OutputEvent
	Config       <-chan ports.ConfigChanged
	Commands     chan<- ports.ClientCommand
	Spawn        chan<- ports.SpawnRequest
	Scenes       chan ports.Scene
	ConfigErrors chan<- error
}
type binding struct {
	mods ports.Mods
	key  string
}
type Core struct {
	ch      Channels
	cfg     ports.Config
	ws      Workspace
	binds   map[binding]Action
	pressed map[string]bool
	sent    map[WindowID]ports.ConfigureWindow
	focus   WindowID
	seq     uint64
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
	width, err := ParseWidth(cfg.Layout.DefaultColumnWidth)
	if err != nil {
		return err
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
		switch Action(a) {
		case "none", ActionSpawnTerminal, ActionFocusColumnLeft, ActionFocusColumnRight, ActionFocusWindowUp, ActionFocusWindowDown, ActionMoveColumnLeft, ActionMoveColumnRight, ActionCycleColumnWidth, ActionToggleFullscreen, ActionCloseWindow, ActionQuit:
		default:
			return fmt.Errorf("invalid action %q", a)
		}
		binds[b] = Action(a)
	}
	c.cfg = cfg
	c.binds = binds
	c.ws.SetDefaultWidth(width)
	c.ws.SetPresets(presets)
	c.ws.SetGaps(cfg.Layout.Gaps)
	return nil
}
func New(cfg ports.Config, ch Channels) (*Core, error) {
	if cap(ch.Scenes) != 1 {
		return nil, fmt.Errorf("scenes must have capacity 1")
	}
	c := &Core{ch: ch, pressed: map[string]bool{}, sent: map[WindowID]ports.ConfigureWindow{}}
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
func (c *Core) publish(ctx context.Context) error {
	c.seq++
	scene := ports.Scene{Seq: c.seq, OutputWidth: c.ws.Output.W, OutputHeight: c.ws.Output.H, Background: c.cfg.Background.Color, Windows: make([]ports.SceneWindow, 0)}
	alive := map[WindowID]bool{}
	focus, _ := c.ws.Focused()
	for _, p := range c.ws.Layout() {
		alive[p.ID] = true
		scene.Windows = append(scene.Windows, ports.SceneWindow{ID: p.ID, Rect: p.Rect, Focused: p.Focused, Fullscreen: p.Fullscreen, Hidden: p.Hidden})
		if p.Hidden {
			if old, ok := c.sent[p.ID]; ok && old.Activated {
				old.Activated = false
				if err := c.command(ctx, old); err != nil {
					return err
				}
				c.sent[p.ID] = old
			}
			continue
		}
		v := ports.ConfigureWindow{ID: p.ID, Width: p.Rect.W, Height: p.Rect.H, Fullscreen: p.Fullscreen, Activated: p.Focused}
		if old, ok := c.sent[p.ID]; !ok || old != v {
			if err := c.command(ctx, v); err != nil {
				return err
			}
			c.sent[p.ID] = v
		}
	}
	for id := range c.sent {
		if !alive[id] {
			delete(c.sent, id)
		}
	}
	if focus != c.focus {
		if err := c.command(ctx, ports.FocusWindow{ID: focus}); err != nil {
			return err
		}
		c.focus = focus
	}
	// Only the owner sends and drains; a renderer only receives.
	select {
	case c.ch.Scenes <- scene:
	default:
		select {
		case <-c.ch.Scenes:
		default:
		}
		select {
		case c.ch.Scenes <- scene:
		default:
		}
	}
	return nil
}
func (c *Core) Run(ctx context.Context) error {
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
			case ports.WindowMapped:
				c.ws.AddWindow(v.ID)
			case ports.WindowUnmapped:
				c.ws.RemoveWindow(v.ID)
			case ports.WindowFullscreenRequest:
				c.ws.SetFullscreen(v.ID, v.Fullscreen)
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
			case ports.OutputMode:
				c.ws.SetOutput(v.Width, v.Height)
			case ports.OutputUsable:
				c.ws.SetUsable(v.Rect)
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
			if err := c.publish(ctx); err != nil {
				return nil
			}
		case ev, ok := <-c.ch.Input:
			if !ok {
				c.ch.Input = nil
				continue
			}
			key, ok := ev.(ports.KeyEvent)
			if !ok {
				continue
			}
			name := keyName(key.Keysym)
			if !key.Pressed {
				consumed := c.pressed[name]
				delete(c.pressed, name)
				if consumed {
					continue
				}
			}
			if key.Pressed {
				if action, ok := c.binds[binding{key: name, mods: key.Mods}]; ok {
					c.pressed[name] = true
					effect := c.ws.Apply(action)
					if effect.Quit {
						return ErrQuit
					}
					if effect.Spawn {
						select {
						case <-ctx.Done():
							return nil
						case c.ch.Spawn <- ports.SpawnRequest{Argv: append([]string(nil), c.cfg.Terminal.Command...)}:
						}
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
				c.pressed[name] = false
			}
			if id, ok := c.ws.Focused(); ok {
				if err := c.command(ctx, ports.ForwardKey{ID: id, Key: key}); err != nil {
					return nil
				}
			}
		}
	}
}
