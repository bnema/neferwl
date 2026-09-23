package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bnema/nefertty/internal/ports"
)

var defaultBinds = map[string]string{
	"Cmd+Return": "spawn-terminal", "Cmd+Left": "focus-column-left", "Cmd+Right": "focus-column-right", "Cmd+Up": "focus-window-up", "Cmd+Down": "focus-window-down", "Cmd+Shift+Left": "move-column-left", "Cmd+Shift+Right": "move-column-right", "Cmd+R": "cycle-column-width", "Cmd+Shift+F": "toggle-fullscreen", "Cmd+Q": "close-window", "Ctrl+Alt+BackSpace": "quit",
}
var actions = map[string]bool{"none": true, "spawn-terminal": true, "focus-column-left": true, "focus-column-right": true, "focus-window-up": true, "focus-window-down": true, "move-column-left": true, "move-column-right": true, "cycle-column-width": true, "toggle-fullscreen": true, "close-window": true, "quit": true}
var components = map[string]bool{"core": true, "wayland": true, "input": true, "drm": true, "render": true, "sync": true, "config": true, "app": true}
var color = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "nefertty", "config.toml")
}
func Defaults() ports.Config {
	var c ports.Config
	c.Keyboard.RepeatRate = 25
	c.Keyboard.RepeatDelay = 600
	c.Keyboard.CmdKey = "super"
	c.Terminal.Command = []string{"foot"}
	c.Background.Color = "#1e1e2e"
	c.Layout.Gaps = 8
	c.Layout.DefaultColumnWidth = "1/2"
	c.Layout.Presets = []string{"1/3", "1/2", "2/3", "1"}
	c.Binds = make(map[string]string, len(defaultBinds))
	for k, v := range defaultBinds {
		c.Binds[k] = v
	}
	c.Render.DirectScanout = true
	c.Log.Level = "info"
	c.Log.Debug = []string{}
	return c
}
func Load(path string) (ports.Config, error) {
	c := Defaults()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	// Decode binds separately so user entries can remove or override defaults.
	var raw struct {
		Binds map[string]string `toml:"binds"`
	}
	if _, err = toml.Decode(string(data), &raw); err != nil {
		return c, err
	}
	c.Binds = make(map[string]string)
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return c, err
	}
	var errs []error
	for _, key := range md.Undecoded() {
		errs = append(errs, fmt.Errorf("%s: unknown key", key.String()))
	}
	// Validate user bindings before merging to catch normalized collisions.
	errs = append(errs, Validate(c))
	if err = errors.Join(errs...); err != nil {
		return c, err
	}
	c.Binds = Defaults().Binds
	for k, v := range raw.Binds {
		normalized, _ := normalize(k)
		for old := range c.Binds {
			n, _ := normalize(old)
			if n == normalized {
				delete(c.Binds, old)
			}
		}
		if v != "none" {
			c.Binds[k] = v
		}
	}
	return c, nil
}
func width(s string) bool {
	if s == "1" {
		return true
	}
	if strings.HasSuffix(s, "px") {
		n, e := strconv.Atoi(strings.TrimSuffix(s, "px"))
		return e == nil && n > 0
	}
	p := strings.Split(s, "/")
	if len(p) != 2 {
		return false
	}
	a, e1 := strconv.Atoi(p[0])
	b, e2 := strconv.Atoi(p[1])
	return e1 == nil && e2 == nil && a > 0 && a <= b
}
func normalize(s string) (string, error) {
	p := strings.Split(s, "+")
	if len(p) < 2 || p[len(p)-1] == "" {
		return "", fmt.Errorf("invalid key combo")
	}
	seen := map[string]bool{}
	for _, m := range p[:len(p)-1] {
		if m != "Cmd" && m != "Shift" && m != "Ctrl" && m != "Alt" || seen[m] {
			return "", fmt.Errorf("invalid key combo")
		}
		seen[m] = true
	}
	mods := make([]string, 0, len(seen))
	for m := range seen {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	return strings.Join(append(mods, p[len(p)-1]), "+"), nil
}
func Validate(c ports.Config) error {
	var errs []error
	add := func(path, msg string) { errs = append(errs, fmt.Errorf("%s: %s", path, msg)) }
	if c.Keyboard.RepeatRate <= 0 {
		add("keyboard.repeat_rate", "must be positive")
	}
	if c.Keyboard.RepeatDelay <= 0 {
		add("keyboard.repeat_delay", "must be positive")
	}
	if c.Keyboard.CmdKey != "super" && c.Keyboard.CmdKey != "alt" && c.Keyboard.CmdKey != "ctrl" {
		add("keyboard.cmd_key", "must be super, alt or ctrl")
	}
	if len(c.Terminal.Command) == 0 {
		add("terminal.command", "must not be empty")
	} else {
		for _, s := range c.Terminal.Command {
			if s == "" {
				add("terminal.command", "must not contain empty entries")
				break
			}
		}
	}
	if !color.MatchString(c.Background.Color) {
		add("background.color", "must be #rrggbb")
	}
	if c.Layout.Gaps < 0 || c.Layout.Gaps > 200 {
		add("layout.gaps", "must be between 0 and 200")
	}
	if !width(c.Layout.DefaultColumnWidth) {
		add("layout.default_column_width", "must be a positive fraction or pixel width")
	}
	if len(c.Layout.Presets) == 0 {
		add("layout.presets", "must not be empty")
	}
	for i, s := range c.Layout.Presets {
		if !width(s) {
			add(fmt.Sprintf("layout.presets[%d]", i), "must be a positive fraction or pixel width")
		}
	}
	if c.Log.Level != "debug" && c.Log.Level != "info" && c.Log.Level != "warn" && c.Log.Level != "error" {
		add("log.level", "invalid level")
	}
	for i, s := range c.Log.Debug {
		if !components[s] {
			add(fmt.Sprintf("log.debug[%d]", i), "invalid component")
		}
	}
	keys := make([]string, 0, len(c.Binds))
	for k := range c.Binds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	combos := map[string]string{}
	for _, k := range keys {
		v := c.Binds[k]
		n, e := normalize(k)
		if e != nil {
			add("binds."+k, e.Error())
		} else if old, ok := combos[n]; ok {
			add("binds."+k, "duplicates "+old)
		} else {
			combos[n] = k
		}
		if !actions[v] {
			add("binds."+k, "invalid action")
		}
	}
	return errors.Join(errs...)
}
