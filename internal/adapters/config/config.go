// Package config reads the flat `key = value` config file (ADR 012).
package config

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bnema/nefertty/internal/ports"
)

// defaultBinds use the config syntax and go through the same parser as user binds.
var defaultBinds = []struct{ combo, action string }{
	{"cmd+return", "spawn-terminal"},
	{"cmd+left", "focus-column-left"},
	{"cmd+right", "focus-column-right"},
	{"cmd+up", "focus-window-up"},
	{"cmd+down", "focus-window-down"},
	{"cmd+shift+left", "move-column-left"},
	{"cmd+shift+right", "move-column-right"},
	{"cmd+r", "cycle-column-width"},
	{"cmd+shift+f", "toggle-fullscreen"},
	{"cmd+q", "close-window"},
	{"ctrl+alt+backspace", "quit"},
	{"ctrl+cmd+space", "spawn fuzzel"},
}
var actions = map[string]bool{"none": true, "spawn-terminal": true, "focus-column-left": true, "focus-column-right": true, "focus-window-up": true, "focus-window-down": true, "move-column-left": true, "move-column-right": true, "cycle-column-width": true, "toggle-fullscreen": true, "close-window": true, "quit": true}
var components = map[string]bool{"core": true, "wayland": true, "input": true, "drm": true, "seat": true, "render": true, "sync": true, "config": true, "app": true}
var color = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// namedKeys maps lowercase names to xkb keysym names so binds are case-insensitive.
var namedKeys = map[string]string{
	"return": "Return", "enter": "Return", "space": "space", "tab": "Tab", "escape": "Escape", "esc": "Escape",
	"backspace": "BackSpace", "delete": "Delete", "insert": "Insert", "home": "Home", "end": "End",
	"pageup": "Prior", "prior": "Prior", "pagedown": "Next", "next": "Next", "print": "Print",
	"left": "Left", "right": "Right", "up": "Up", "down": "Down",
}

// Warning is a non-fatal problem at one line; the key keeps its default.
type Warning struct {
	Line int
	Msg  string
}

func (w Warning) String() string { return fmt.Sprintf("line %d: %s", w.Line, w.Msg) }

func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "nefertty", "config")
}

func Defaults() ports.Config {
	var c ports.Config
	c.Keyboard.RepeatRate = 25
	c.Keyboard.RepeatDelay = 600
	c.Keyboard.CmdKey = "super"
	c.Terminal.Command = []string{"foot"}
	c.Background.Color = "#1e1e2e"
	c.Border.Width = 2
	c.Border.Active = "#b4befe"
	c.Layout.DefaultColumnWidth = "1/2"
	c.Layout.Presets = []string{"1/3", "1/2", "2/3", "1"}
	c.Binds = map[string]string{}
	for _, b := range defaultBinds {
		combo, err := parseCombo(b.combo)
		if err != nil {
			panic(err)
		}
		c.Binds[combo] = b.action
	}
	c.Render.DirectScanout = true
	c.Log.Level = "info"
	c.Log.Debug = []string{}
	return c
}

// LoadDefault loads DefaultPath; a missing file means defaults.
func LoadDefault() (ports.Config, []Warning, error) {
	c, w, err := Load(DefaultPath())
	if os.IsNotExist(err) {
		return Defaults(), nil, nil
	}
	return c, w, err
}

// Load reads a config file. The error is only for I/O; bad lines become warnings.
func Load(path string) (ports.Config, []Warning, error) {
	f, err := os.Open(path)
	if err != nil {
		return Defaults(), nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse reads `key = value` lines on top of the defaults.
func Parse(r io.Reader) (ports.Config, []Warning, error) {
	c, _, w, err := parse(r)
	return c, w, err
}

// parse also returns the effective key/value pairs, used to log what changed on reload.
func parse(r io.Reader) (ports.Config, map[string]string, []Warning, error) {
	c := Defaults()
	var warnings []Warning
	raw := map[string]string{}
	seen := map[string]int{}
	outputs := map[string]int{}
	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		warn := func(format string, a ...any) {
			warnings = append(warnings, Warning{Line: n, Msg: fmt.Sprintf(format, a...)})
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), stripComment(strings.TrimSpace(value))
		if !ok || key == "" {
			warn("expected key = value")
			continue
		}
		if combo, ok := strings.CutPrefix(key, "bind."); ok {
			if strings.HasSuffix(combo, "+") && strings.HasPrefix(value, "=") {
				warn("%s: write %sequal instead of %s=", key, combo, combo)
				continue
			}
			canon, err := parseCombo(combo)
			if err != nil {
				warn("%s: %v", key, err)
				continue
			}
			if err := checkAction(value); err != nil {
				warn("%s: %v", key, err)
				continue
			}
			key = "bind." + canon
			if prev, dup := seen[key]; dup {
				warn("%s: overrides line %d", key, prev)
			}
			seen[key] = n
			raw[key] = value
			c.Binds[canon] = value
			continue
		}
		if prev, dup := seen[key]; dup {
			warn("%s: overrides line %d", key, prev)
		}
		seen[key] = n
		if name, ok := strings.CutPrefix(key, "output."); ok {
			o := ports.OutputConfig{Name: name}
			if value == "off" {
				o.Off = true
			} else if _, _, _, err := ParseMode(value); err != nil {
				warn("%s: %v", key, err)
				continue
			} else if value != "preferred" {
				o.Mode = value
			}
			raw[key] = value
			if i, ok := outputs[name]; ok {
				c.Outputs[i] = o
			} else {
				outputs[name] = len(c.Outputs)
				c.Outputs = append(c.Outputs, o)
			}
			continue
		}
		if err := set(&c, key, value); err != nil {
			warn("%s: %v", key, err)
			continue
		}
		raw[key] = value
	}
	if err := scanner.Err(); err != nil {
		return c, raw, warnings, err
	}
	// "none" only removes a default bind.
	for combo, a := range c.Binds {
		if a == "none" {
			delete(c.Binds, combo)
		}
	}
	// Cmd resolves to a real modifier; drop binds that then collide. User lines win over
	// defaults (line 0), then the earliest line wins.
	combos := make([]string, 0, len(c.Binds))
	for combo := range c.Binds {
		combos = append(combos, combo)
	}
	order := func(combo string) int {
		if n := seen["bind."+combo]; n > 0 {
			return n
		}
		return math.MaxInt
	}
	sort.Slice(combos, func(i, j int) bool {
		a, b := order(combos[i]), order(combos[j])
		return a < b || a == b && combos[i] < combos[j]
	})
	resolved := map[string]string{}
	for _, combo := range combos {
		r, err := expandCmd(combo, c.Keyboard.CmdKey)
		if prev, dup := resolved[r]; err == nil && dup {
			err = fmt.Errorf("same keys as %s with keyboard.cmd = %s", prev, c.Keyboard.CmdKey)
		}
		if err != nil {
			if n := seen["bind."+combo]; n > 0 {
				warnings = append(warnings, Warning{Line: n, Msg: fmt.Sprintf("bind.%s: %v", combo, err)})
			}
			delete(c.Binds, combo)
			delete(raw, "bind."+combo)
			continue
		}
		resolved[r] = combo
	}
	return c, raw, warnings, nil
}

// expandCmd replaces Cmd with the configured modifier and re-sorts.
func expandCmd(combo, cmd string) (string, error) {
	parts := strings.Split(combo, "+")
	key := parts[len(parts)-1]
	// cmd is super, alt or ctrl; its modifier name is the capitalized form.
	cmdName := strings.ToUpper(cmd[:1]) + cmd[1:]
	seen := map[string]bool{}
	for _, m := range parts[:len(parts)-1] {
		if m == "Cmd" {
			m = cmdName
		}
		if seen[m] {
			return "", fmt.Errorf("%s is already cmd (keyboard.cmd = %s)", strings.ToLower(m), cmd)
		}
		seen[m] = true
	}
	mods := make([]string, 0, len(seen))
	for m := range seen {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	return strings.Join(append(mods, key), "+"), nil
}

// set applies one scalar key. Unknown keys and invalid values are errors.
func set(c *ports.Config, key, v string) error {
	switch key {
	case "keyboard.layout":
		c.Keyboard.Layout = v
	case "keyboard.variant":
		c.Keyboard.Variant = v
	case "keyboard.options":
		c.Keyboard.Options = v
	case "keyboard.repeat-rate":
		return positive(&c.Keyboard.RepeatRate, v, 1000)
	case "keyboard.repeat-delay":
		return positive(&c.Keyboard.RepeatDelay, v, 10000)
	case "keyboard.cmd":
		if v != "super" && v != "alt" && v != "ctrl" {
			return fmt.Errorf("must be super, alt or ctrl")
		}
		c.Keyboard.CmdKey = v
	case "terminal":
		argv := strings.Fields(v)
		if len(argv) == 0 {
			return fmt.Errorf("must not be empty")
		}
		c.Terminal.Command = argv
	case "background":
		if !color.MatchString(v) {
			return fmt.Errorf("must be #rrggbb")
		}
		c.Background.Color = v
	case "border.width":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 32 {
			return fmt.Errorf("must be between 0 and 32")
		}
		c.Border.Width = n
	case "border.active":
		if !color.MatchString(v) {
			return fmt.Errorf("must be #rrggbb")
		}
		c.Border.Active = v
	case "border.inactive":
		if v != "" && !color.MatchString(v) {
			return fmt.Errorf("must be #rrggbb or empty (none)")
		}
		c.Border.Inactive = v
	case "layout.gaps":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 200 {
			return fmt.Errorf("must be between 0 and 200")
		}
		c.Layout.Gaps = n
	case "layout.default-width":
		if !width(v) {
			return fmt.Errorf("must be a fraction like 1/2, 1, or pixels like 800px")
		}
		c.Layout.DefaultColumnWidth = v
	case "layout.presets":
		list := splitList(v)
		if len(list) == 0 {
			return fmt.Errorf("must not be empty")
		}
		for _, s := range list {
			if !width(s) {
				return fmt.Errorf("%q: must be a fraction like 1/2, 1, or pixels like 800px", s)
			}
		}
		c.Layout.Presets = list
	case "render.direct-scanout":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Render.DirectScanout = b
	case "log.level":
		if v != "debug" && v != "info" && v != "warn" && v != "error" {
			return fmt.Errorf("must be debug, info, warn or error")
		}
		c.Log.Level = v
	case "log.debug":
		list := splitList(v)
		for _, s := range list {
			if !components[s] && s != "all" {
				return fmt.Errorf("unknown component %q", s)
			}
		}
		c.Log.Debug = list
	default:
		return fmt.Errorf("unknown key")
	}
	return nil
}

func positive(dst *int, v string, max int) error {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > max {
		return fmt.Errorf("must be between 1 and %d", max)
	}
	*dst = n
	return nil
}

func onOff(v string) (bool, error) {
	switch v {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("must be on or off")
}

func splitList(v string) []string {
	list := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, s)
		}
	}
	return list
}

// stripComment drops a trailing ` # comment` from a value. A # that starts the
// value is kept, so colors like #1e1e2e need no quotes.
func stripComment(value string) string {
	for i := 1; i < len(value); i++ {
		if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
			return strings.TrimSpace(value[:i])
		}
	}
	return value
}

func checkAction(v string) error {
	if rest, ok := strings.CutPrefix(v, "spawn "); ok {
		if len(strings.Fields(rest)) == 0 {
			return fmt.Errorf("spawn needs a command")
		}
		return nil
	}
	if !actions[v] {
		return fmt.Errorf(`unknown action %q (or "spawn <command>")`, v)
	}
	return nil
}

// parseCombo turns `ctrl+cmd+é` into the canonical `Cmd+Ctrl+eacute`:
// modifiers sorted, the key as an xkb keysym name.
func parseCombo(s string) (string, error) {
	parts := strings.Split(s, "+")
	key := parts[len(parts)-1]
	if len(parts) > 1 && key == "" {
		// "cmd++" means the + key.
		if len(parts) > 2 && parts[len(parts)-2] == "" {
			parts, key = parts[:len(parts)-1], "+"
		} else {
			return "", fmt.Errorf("missing key")
		}
	}
	if key == "" {
		return "", fmt.Errorf("missing key")
	}
	seen := map[string]bool{}
	for _, m := range parts[:len(parts)-1] {
		var name string
		switch strings.ToLower(m) {
		case "cmd":
			name = "Cmd"
		case "shift":
			name = "Shift"
		case "ctrl":
			name = "Ctrl"
		case "alt":
			name = "Alt"
		case "super":
			name = "Super"
		default:
			return "", fmt.Errorf("unknown modifier %q", m)
		}
		if seen[name] {
			return "", fmt.Errorf("duplicate modifier %q", m)
		}
		seen[name] = true
	}
	mods := make([]string, 0, len(seen))
	for m := range seen {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	return strings.Join(append(mods, keysym(key)), "+"), nil
}

// keysym maps a written key to its xkb keysym name.
func keysym(k string) string {
	if r := []rune(k); len(r) == 1 {
		if name, ok := runeKeysyms[r[0]]; ok {
			return name
		}
		return strings.ToLower(k)
	}
	if name, ok := namedKeys[strings.ToLower(k)]; ok {
		return name
	}
	if len(k) <= 3 && (k[0] == 'f' || k[0] == 'F') {
		if n, err := strconv.Atoi(k[1:]); err == nil && n >= 1 && n <= 24 {
			return "F" + k[1:]
		}
	}
	return k
}

// ParseMode parses "WxH" or "WxH@Hz" ("preferred" is accepted); hz is 0 when omitted.
func ParseMode(s string) (w, h int, hz float64, err error) {
	if s == "preferred" {
		return 0, 0, 0, nil
	}
	size, rate, hasRate := strings.Cut(s, "@")
	ws, hs, ok := strings.Cut(size, "x")
	w, e1 := strconv.Atoi(ws)
	h, e2 := strconv.Atoi(hs)
	if !ok || e1 != nil || e2 != nil || w <= 0 || h <= 0 {
		return 0, 0, 0, fmt.Errorf("must be off, preferred, WxH or WxH@Hz, got %q", s)
	}
	if hasRate {
		if hz, err = strconv.ParseFloat(rate, 64); err != nil || hz <= 0 {
			return 0, 0, 0, fmt.Errorf("invalid refresh rate in %q", s)
		}
	}
	return w, h, hz, nil
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
