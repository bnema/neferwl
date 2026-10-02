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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bnema/neferwl/internal/ports"
)

// maxVRRFlipGap bounds render.vrr-flip-gap below one refresh of a slow
// VRR panel (48 Hz: 20.8 ms).
const maxVRRFlipGap = 10 * time.Millisecond

// defaultBinds use the config syntax and go through the same parser as user binds.
var defaultBinds = []struct{ combo, action string }{
	{"cmd+return", "spawn-terminal"},
	{"cmd+left", "focus-column-left"},
	{"cmd+right", "focus-column-right"},
	{"cmd+up", "focus-window-up"},
	{"cmd+down", "focus-window-down"},
	{"cmd+shift+left", "move-column-left"},
	{"cmd+shift+right", "move-column-right"},
	{"cmd+bracketleft", "consume-or-expel-window-left"},
	{"cmd+bracketright", "consume-or-expel-window-right"},
	{"cmd+r", "cycle-column-width"},
	{"cmd+f", "maximize-column"},
	{"cmd+shift+f", "toggle-fullscreen"},
	{"cmd+s", "toggle-stash-visible"},
	{"cmd+shift+s", "toggle-window-stash"},
	{"cmd+o", "toggle-overview"},
	{"cmd+q", "close-window"},
	{"ctrl+alt+backspace", "quit"},
	{"ctrl+cmd+space", "spawn fuzzel"},
	{"cmd+pageup", "focus-workspace-up"},
	{"cmd+pagedown", "focus-workspace-down"},
	{"cmd+shift+pageup", "move-column-to-workspace-up"},
	{"cmd+shift+pagedown", "move-column-to-workspace-down"},
	{"cmd+ctrl+left", "focus-monitor-left"},
	{"cmd+ctrl+right", "focus-monitor-right"},
	{"cmd+ctrl+shift+left", "move-workspace-to-monitor-left"},
	{"cmd+ctrl+shift+right", "move-workspace-to-monitor-right"},
	{"cmd+shift+up", "move-window-up"},
	{"cmd+shift+down", "move-window-down"},
	{"cmd+alt+left", "set-column-width -10%"},
	{"cmd+alt+right", "set-column-width +10%"},
	{"cmd+alt+up", "set-window-height -10%"},
	{"cmd+alt+down", "set-window-height +10%"},
	{"cmd+ctrl+shift+up", "move-workspace-up"},
	{"cmd+ctrl+shift+down", "move-workspace-down"},
	{"cmd+shift+space", "toggle-floating"},
}
var actions = map[string]bool{"focus-monitor-left": true, "focus-monitor-right": true, "move-workspace-to-monitor-left": true, "move-workspace-to-monitor-right": true, "scale-up": true, "scale-down": true, "focus-workspace-up": true, "focus-workspace-down": true, "move-column-to-workspace-up": true, "move-column-to-workspace-down": true, "move-window-to-workspace-up": true, "move-window-to-workspace-down": true, "none": true, "consume-or-expel-window-left": true, "consume-or-expel-window-right": true, "spawn-terminal": true, "focus-column-left": true, "focus-column-right": true, "focus-window-up": true, "focus-window-down": true, "move-column-left": true, "move-column-right": true, "cycle-column-width": true, "maximize-column": true, "toggle-fullscreen": true, "toggle-window-stash": true, "toggle-stash-visible": true, "toggle-overview": true, "close-window": true, "quit": true, "move-window-up": true, "move-window-down": true, "move-workspace-up": true, "move-workspace-down": true, "toggle-floating": true}
var components = map[string]bool{"core": true, "wayland": true, "input": true, "drm": true, "seat": true, "render": true, "sync": true, "config": true, "app": true}
var color = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// resizeStep is the +N% or -N% step of set-column-width and
// set-window-height, N from 1 to 100.
var resizeStep = regexp.MustCompile(`^[+-]([1-9][0-9]?|100)%$`)

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
	return filepath.Join(base, "neferwl", "config")
}

func Defaults() ports.Config {
	var c ports.Config
	c.Keyboard.RepeatRate = 25
	c.Keyboard.RepeatDelay = 600
	c.Keyboard.CmdKey = "super"
	c.Terminal.Command = strings.Fields(os.Getenv("TERMINAL"))
	if len(c.Terminal.Command) == 0 {
		c.Terminal.Command = []string{"foot"}
	}
	c.Terminal.AutoOpen = "first"
	c.Xwayland = "xwayland-satellite"
	c.Background.Color = "#111111"
	c.Floating.Dim = 0.3
	c.Stash.Width = 80
	c.Stash.Gap = 2
	c.Stash.Dim = 0.5
	c.Stash.Capture = true
	c.Border.Width = 2
	c.Border.Active = "#808080"
	c.Border.Inactive = "#111111"
	c.Layout.MaxColumns = 2
	c.Layout.Presets = []string{"1/3", "1/2", "2/3", "1"}
	c.Layout.Overflow = "scroll"
	c.Touchpad.Tap = true
	c.Touchpad.AccelProfile = ports.AccelAdaptive
	c.Touchpad.ScrollFactor = 1
	c.Focus.Pulse = ports.FocusPulseContrast
	c.Focus.PulseStrength = 0.04
	c.Binds = map[string]string{}
	for _, b := range defaultBinds {
		combos := []string{b.combo}
		// Every arrow bind has a vim twin: h j k l.
		for arrow, vim := range map[string]string{"left": "h", "down": "j", "up": "k", "right": "l"} {
			if base, ok := strings.CutSuffix(b.combo, "+"+arrow); ok {
				combos = append(combos, base+"+"+vim)
			}
		}
		for _, s := range combos {
			combo, err := parseCombo(s)
			if err != nil {
				panic(err)
			}
			c.Binds[combo] = b.action
		}
	}
	// Digits and zoom use physical keys (evdev codes 2-10, 12, 13), so they
	// work the same on AZERTY, QWERTZ or Dvorak.
	for combo, action := range map[string]string{"cmd+code:13": "scale-up", "cmd+code:12": "scale-down"} {
		canon, _ := parseCombo(combo)
		c.Binds[canon] = action
	}
	for n := 1; n <= 9; n++ {
		focus, _ := parseCombo(fmt.Sprintf("cmd+code:%d", n+1))
		move, _ := parseCombo(fmt.Sprintf("cmd+shift+code:%d", n+1))
		c.Binds[focus] = fmt.Sprintf("focus-workspace %d", n)
		c.Binds[move] = fmt.Sprintf("move-column-to-workspace %d", n)
	}
	c.Render.DirectScanout = true
	c.Render.Tearing = true
	c.Render.VRR = true
	c.Render.VRRFlipGap = time.Millisecond
	c.Performance.Realtime = true
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
	workspaces := map[string]int{}
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
		override := func() {
			if prev, dup := seen[key]; dup {
				warn("%s: overrides line %d", key, prev)
			}
			seen[key] = n
			raw[key] = value
		}
		if name, ok := strings.CutPrefix(key, "output."); ok {
			entry := func() *ports.OutputConfig {
				if i, ok := outputs[name]; ok {
					return &c.Outputs[i]
				}
				outputs[name] = len(c.Outputs)
				c.Outputs = append(c.Outputs, ports.OutputConfig{Name: name, ScaleOnly: true, SDRBrightness: ports.DefaultSDRBrightness})
				return &c.Outputs[len(c.Outputs)-1]
			}
			if base, ok := strings.CutSuffix(name, ".primary"); ok {
				name = base
				b, err := onOff(value)
				if err != nil {
					warn("%s: %v", key, err)
					continue
				}
				override()
				entry().Primary = b
				continue
			}
			if base, ok := strings.CutSuffix(name, ".hdr"); ok {
				name = base
				b, err := onOff(value)
				if err != nil {
					warn("%s: %v", key, err)
					continue
				}
				override()
				entry().HDR = b
				continue
			}
			if base, ok := strings.CutSuffix(name, ".sdr-brightness"); ok {
				name = base
				nits, err := strconv.Atoi(value)
				if err != nil || nits < 80 || nits > 1000 {
					warn("%s: must be between 80 and 1000", key)
					continue
				}
				override()
				entry().SDRBrightness = nits
				continue
			}
			if base, ok := strings.CutSuffix(name, ".scale"); ok {
				name = base
				s, err := parseScale(value)
				if err != nil {
					warn("%s: %v", key, err)
					continue
				}
				override()
				entry().Scale = s
				continue
			}
			o := ports.OutputConfig{Name: name}
			if value == "off" {
				o.Off = true
			} else if _, _, _, err := ParseMode(value); err != nil {
				warn("%s: %v", key, err)
				continue
			} else if value != "preferred" {
				o.Mode = value
			}
			override()
			e := entry()
			o.Scale, o.Primary, o.HDR, o.SDRBrightness = e.Scale, e.Primary, e.HDR, e.SDRBrightness
			*e = o
			continue
		}
		if rest, ok := strings.CutPrefix(key, "workspace."); ok {
			name, field, _ := strings.Cut(rest, ".")
			if !workspaceName.MatchString(name) {
				warn("%s: workspace name must be letters, digits, - or _", key)
				continue
			}
			var ws ports.WorkspaceConfig
			if i, ok := workspaces[name]; ok {
				ws = c.Workspaces[i]
			}
			if err := setWorkspace(&ws, field, value); err != nil {
				warn("%s: %v", key, err)
				continue
			}
			override()
			if i, ok := workspaces[name]; ok {
				c.Workspaces[i] = ws
			} else {
				ws.Name = name
				workspaces[name] = len(c.Workspaces)
				c.Workspaces = append(c.Workspaces, ws)
			}
			continue
		}
		if err := set(&c, key, value); err != nil {
			warn("%s: %v", key, err)
			continue
		}
		if key == "startup" {
			// Repeatable: each line adds a command.
			raw[fmt.Sprintf("startup.%d", len(c.Startup))] = value
			continue
		}
		override()
	}
	if err := scanner.Err(); err != nil {
		return c, raw, warnings, err
	}
	warnings = append(warnings, checkWorkspaceBinds(c, seen)...)
	sort.SliceStable(warnings, func(i, j int) bool { return warnings[i].Line < warnings[j].Line })
	// A user bind on a digit (cmd+1, even "none") replaces the default bound
	// to the same physical key (cmd+code:2), so older configs keep working.
	for combo := range c.Binds {
		if seen["bind."+combo] == 0 {
			continue
		}
		parts := strings.Split(combo, "+")
		key := parts[len(parts)-1]
		if len(key) != 1 || key[0] < '0' || key[0] > '9' {
			continue
		}
		code := int(key[0]-'0') + 1
		if key == "0" {
			code = 11
		}
		parts[len(parts)-1] = "code:" + strconv.Itoa(code)
		if physical := strings.Join(parts, "+"); seen["bind."+physical] == 0 {
			delete(c.Binds, physical)
		}
	}
	// "none" only removes a default bind.
	for combo, a := range c.Binds {
		if a == "none" {
			delete(c.Binds, combo)
		}
	}
	// Cmd resolves to a real modifier; drop binds that then collide. As for any key,
	// the later line wins; defaults (no line) lose to every user line.
	combos := make([]string, 0, len(c.Binds))
	for combo := range c.Binds {
		combos = append(combos, combo)
	}
	order := func(combo string) int {
		if n := seen["bind."+combo]; n > 0 {
			return -n
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
			err = fmt.Errorf("overridden by bind.%s (same keys with keyboard.cmd = %s)", prev, c.Keyboard.CmdKey)
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
	case "startup":
		argv := strings.Fields(v)
		if len(argv) == 0 {
			return fmt.Errorf("must not be empty")
		}
		c.Startup = append(c.Startup, argv)
	case "terminal":
		argv := strings.Fields(v)
		if len(argv) == 0 {
			return fmt.Errorf("must not be empty")
		}
		c.Terminal.Command = argv
	case "terminal.auto-open":
		if v != "first" && v != "all" && v != "off" {
			return fmt.Errorf("must be first, all or off")
		}
		c.Terminal.AutoOpen = v
	case "xwayland":
		switch {
		case v == "off":
			c.Xwayland = ""
		case v == "" || strings.ContainsAny(v, " \t"):
			return fmt.Errorf("must be off or the xwayland-satellite program")
		default:
			c.Xwayland = v
		}
	case "background":
		if !color.MatchString(v) {
			return fmt.Errorf("must be #rrggbb")
		}
		c.Background.Color = v
	case "floating.dim":
		dim, err := strconv.ParseFloat(v, 64)
		if err != nil || !(dim >= 0 && dim <= 1) {
			return fmt.Errorf("must be between 0 and 1")
		}
		c.Floating.Dim = dim
	case "stash.width":
		n, err := strconv.Atoi(strings.TrimSuffix(v, "%"))
		if err != nil || n < 10 || n > 90 {
			return fmt.Errorf("must be between 10 and 90")
		}
		c.Stash.Width = n
	case "stash.gap":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 10 {
			return fmt.Errorf("must be between 0 and 10")
		}
		c.Stash.Gap = n
	case "stash.dim":
		dim, err := strconv.ParseFloat(v, 64)
		if err != nil || !(dim >= 0 && dim <= 1) {
			return fmt.Errorf("must be between 0 and 1")
		}
		c.Stash.Dim = dim
	case "stash.capture":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Stash.Capture = b
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
		return fmt.Errorf("replaced by layout.max-columns (columns share the width equally)")
	case "layout.max-columns":
		return setLayoutRule(&c.Layout.LayoutRules, "max-columns", v)
	case "layout.overflow":
		return setLayoutRule(&c.Layout.LayoutRules, "overflow", v)
	case "layout.presets":
		list := splitList(v)
		if len(list) == 0 {
			return fmt.Errorf("must not be empty")
		}
		for _, s := range list {
			if !width(s) {
				return fmt.Errorf("%q: must be a fraction like 1/2, a percentage like 50%%, 1, or pixels like 800px", s)
			}
		}
		c.Layout.Presets = list
	case "touchpad.natural-scroll":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Touchpad.NaturalScroll = b
	case "touchpad.tap":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Touchpad.Tap = b
	case "touchpad.accel-speed":
		s, err := strconv.ParseFloat(v, 64)
		if err != nil || !(s >= -1 && s <= 1) {
			return fmt.Errorf("must be between -1 and 1")
		}
		c.Touchpad.AccelSpeed = s
	case "touchpad.accel-profile":
		if v != ports.AccelAdaptive && v != ports.AccelFlat {
			return fmt.Errorf("must be adaptive or flat")
		}
		c.Touchpad.AccelProfile = v
	case "touchpad.scroll-factor":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !(f > 0 && f <= 10) {
			return fmt.Errorf("must be above 0 and at most 10")
		}
		c.Touchpad.ScrollFactor = f
	case "focus.follow-move":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Focus.FollowMove = b
	case "focus.pulse":
		if v != ports.FocusPulseContrast && v != ports.FocusPulseOff {
			return fmt.Errorf("must be contrast or off")
		}
		c.Focus.Pulse = v
	case "focus.pulse-strength":
		// Capped low: the pulse must never flash.
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !(f >= 0.01 && f <= 0.2) {
			return fmt.Errorf("must be between 0.01 and 0.2")
		}
		c.Focus.PulseStrength = f
	case "render.direct-scanout":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Render.DirectScanout = b
	case "render.tearing":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Render.Tearing = b
	case "render.vrr":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Render.VRR = b
	case "render.vrr-flip-gap":
		d, err := time.ParseDuration(v)
		if v == "0" {
			d, err = 0, nil
		}
		if err != nil || d < 0 || d > maxVRRFlipGap {
			return fmt.Errorf("must be a duration between 0 and %s, e.g. 1ms", maxVRRFlipGap)
		}
		c.Render.VRRFlipGap = d
	case "performance.realtime":
		b, err := onOff(v)
		if err != nil {
			return err
		}
		c.Performance.Realtime = b
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
		if rest, ok := strings.CutPrefix(key, "layout."); ok {
			if i := strings.LastIndex(rest, "."); i > 0 {
				return setOutputLayout(c, rest[:i], rest[i+1:], v)
			}
		}
		return fmt.Errorf("unknown key")
	}
	return nil
}

// setOutputLayout applies one layout.<output>.<field> key.
func setOutputLayout(c *ports.Config, output, field, v string) error {
	i := slices.IndexFunc(c.Layout.Outputs, func(o ports.OutputLayout) bool { return o.Output == output })
	o := ports.OutputLayout{Output: output}
	if i >= 0 {
		o = c.Layout.Outputs[i]
	}
	if err := setLayoutRule(&o.LayoutRules, field, v); err != nil {
		return err
	}
	if i < 0 {
		c.Layout.Outputs = append(c.Layout.Outputs, o)
	} else {
		c.Layout.Outputs[i] = o
	}
	return nil
}

// setLayoutRule applies max-columns or overflow, for layout.*,
// layout.<output>.* and workspace.<name>.*.
func setLayoutRule(r *ports.LayoutRules, field, v string) error {
	switch field {
	case "max-columns":
		return positive(&r.MaxColumns, v, 16)
	case "overflow":
		if v != "scroll" && v != "fixed" {
			return fmt.Errorf("must be scroll or fixed")
		}
		r.Overflow = v
		return nil
	}
	return fmt.Errorf("unknown key (max-columns, overflow)")
}

// checkWorkspaceBinds warns about a `workspace <name>` bind to an undeclared
// workspace (it does nothing) and a named workspace no bind reaches.
func checkWorkspaceBinds(c ports.Config, seen map[string]int) []Warning {
	declared := map[string]bool{}
	for _, w := range c.Workspaces {
		declared[w.Name] = true
	}
	bound := map[string]bool{}
	var warnings []Warning
	for combo, a := range c.Binds {
		name, ok := strings.CutPrefix(a, "workspace ")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		bound[name] = true
		if !declared[name] {
			warnings = append(warnings, Warning{Line: seen["bind."+combo], Msg: fmt.Sprintf("bind.%s: no workspace.%s.* declared", combo, name)})
		}
	}
	for _, w := range c.Workspaces {
		// The config only knows the monitor as written: a connector home
		// does not match a key rule, and the reverse.
		rules := c.Layout.LayoutRules
		for _, o := range c.Layout.Outputs {
			if w.Monitor != "" && o.Output == w.Monitor {
				rules = o.Over(rules)
			}
		}
		if w.Over(rules).Overflow == "fixed" && len(w.Slots) > 0 {
			line := seen["workspace."+w.Name+".column."+strconv.Itoa(w.Slots[0].Index)]
			warnings = append(warnings, Warning{Line: line, Msg: fmt.Sprintf("workspace.%s: column widths are ignored with overflow = fixed (columns share the width)", w.Name)})
		}
		if !bound[w.Name] {
			line := 0
			for key, n := range seen {
				if strings.HasPrefix(key, "workspace."+w.Name+".") && (line == 0 || n < line) {
					line = n
				}
			}
			warnings = append(warnings, Warning{Line: line, Msg: fmt.Sprintf("workspace.%s: no bind shows it (add bind.<keys> = workspace %s)", w.Name, w.Name)})
		}
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].Line < warnings[j].Line })
	return warnings
}

var workspaceName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// setWorkspace applies one workspace.<name>.<field> key.
func setWorkspace(w *ports.WorkspaceConfig, field, v string) error {
	switch field {
	case "monitor":
		if v == "" {
			return fmt.Errorf("needs a connector (DP-2) or a monitor key")
		}
		w.Monitor = v
	case "size":
		if v == "inherit" {
			w.Size = [2]int{}
			return nil
		}
		width, height, _, err := ParseMode(v)
		if err != nil || width == 0 || strings.Contains(v, "@") {
			return fmt.Errorf("must be inherit or WxH in logical pixels")
		}
		w.Size = [2]int{width, height}
	case "max-columns", "overflow":
		return setLayoutRule(&w.LayoutRules, field, v)
	default:
		n, ok := strings.CutPrefix(field, "column.")
		if !ok {
			return fmt.Errorf("unknown key (monitor, size, max-columns, overflow, column.N)")
		}
		index, err := strconv.Atoi(n)
		if err != nil || index < 1 || index > 16 {
			return fmt.Errorf("column number must be between 1 and 16")
		}
		size, cmd, _ := strings.Cut(v, ",")
		size = strings.TrimSpace(size)
		argv := strings.Fields(cmd)
		if !width(size) || len(argv) == 0 {
			return fmt.Errorf("must be <width>, <command> like 67%%, foot")
		}
		slot := ports.SlotConfig{Index: index, Width: size, Argv: argv}
		i := sort.Search(len(w.Slots), func(i int) bool { return w.Slots[i].Index >= index })
		if i < len(w.Slots) && w.Slots[i].Index == index {
			w.Slots[i] = slot
		} else {
			w.Slots = slices.Insert(w.Slots, i, slot)
		}
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
	if rest, ok := strings.CutPrefix(v, "workspace "); ok {
		if !workspaceName.MatchString(strings.TrimSpace(rest)) {
			return fmt.Errorf("workspace needs a name (letters, digits, - or _)")
		}
		return nil
	}
	for _, prefix := range []string{"focus-workspace ", "move-column-to-workspace ", "move-window-to-workspace "} {
		if rest, ok := strings.CutPrefix(v, prefix); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(rest)); err != nil || n < 1 || n > 99 {
				return fmt.Errorf("%snumber must be between 1 and 99", prefix)
			}
			return nil
		}
	}
	for _, prefix := range []string{"set-column-width ", "set-window-height "} {
		if rest, ok := strings.CutPrefix(v, prefix); ok {
			if !resizeStep.MatchString(strings.TrimSpace(rest)) {
				return fmt.Errorf("%sneeds +N%% or -N%% (1-100)", prefix)
			}
			return nil
		}
	}
	if !actions[v] {
		return fmt.Errorf(`unknown action %q (or "spawn <command>", "workspace <name>")`, v)
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
	name, err := keysym(key)
	if err != nil {
		return "", err
	}
	return strings.Join(append(mods, name), "+"), nil
}

// keysym maps a written key to its xkb keysym name.
func keysym(k string) (string, error) {
	// code:N is the physical key (evdev code), the same on every layout.
	if rest, ok := strings.CutPrefix(strings.ToLower(k), "code:"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n > 0 && n < 768 {
			return "code:" + rest, nil
		}
		return "", fmt.Errorf("code must be an evdev key code like code:2")
	}
	if r := []rune(k); len(r) == 1 {
		// Binds match the unshifted keysym, so É means é.
		if name, ok := runeKeysyms[unicode.ToLower(r[0])]; ok {
			return name, nil
		}
		if r[0] > unicode.MaxASCII {
			return "", fmt.Errorf("write %q by its xkb keysym name", k)
		}
		return strings.ToLower(k), nil
	}
	if name, ok := namedKeys[strings.ToLower(k)]; ok {
		return name, nil
	}
	if len(k) <= 3 && (k[0] == 'f' || k[0] == 'F') {
		if n, err := strconv.Atoi(k[1:]); err == nil && n >= 1 && n <= 24 {
			return "F" + k[1:], nil
		}
	}
	return k, nil
}

// parseScale accepts a decimal (1.5) or a fraction (4/3) between 1 and 4.
func parseScale(v string) (float64, error) {
	var s float64
	var err error
	if num, den, ok := strings.Cut(v, "/"); ok {
		var n, d float64
		n, err = strconv.ParseFloat(strings.TrimSpace(num), 64)
		if err == nil {
			d, err = strconv.ParseFloat(strings.TrimSpace(den), 64)
		}
		if err == nil && d != 0 {
			s = n / d
		}
	} else {
		s, err = strconv.ParseFloat(v, 64)
	}
	if err != nil || !(s >= 1 && s <= 4) {
		return 0, fmt.Errorf("must be between 1 and 4, like 1.5 or 4/3")
	}
	return s, nil
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
	if n, ok := strings.CutSuffix(s, "%"); ok {
		v, e := strconv.Atoi(n)
		return e == nil && v > 0 && v <= 100
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
