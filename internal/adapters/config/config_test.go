package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func parseString(t *testing.T, s string) (ports.Config, []Warning) {
	t.Helper()
	c, w, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return c, w
}

func TestDefaultsAndLoad(t *testing.T) {
	t.Setenv("TERMINAL", "")
	d := Defaults()
	if d.Keyboard.RepeatRate != 25 || d.Keyboard.CmdKey != "super" || !d.Render.DirectScanout || len(d.Binds) != 73 || d.Layout.MaxColumns != 2 || d.Floating.Dim != 0.3 {
		t.Fatalf("defaults: %+v", d)
	}
	if d.Binds["Cmd+s"] != "toggle-stash-visible" || d.Binds["Cmd+Shift+s"] != "toggle-window-stash" || d.Binds["Cmd+o"] != "toggle-overview" || d.Binds["Cmd+f"] != "maximize-column" || d.Binds["Cmd+Shift+f"] != "toggle-fullscreen" || d.Binds["Cmd+Shift+h"] != "move-column-left" || d.Binds["Cmd+j"] != "focus-window-down" || d.Binds["Cmd+Shift+code:2"] != "move-column-to-workspace 1" || d.Focus.FollowMove {
		t.Fatal(d.Binds)
	}
	if d.Binds["Cmd+Shift+Up"] != "move-window-up" || d.Binds["Cmd+Shift+j"] != "move-window-down" || d.Binds["Alt+Cmd+Left"] != "set-column-width -10%" || d.Binds["Alt+Cmd+l"] != "set-column-width +10%" || d.Binds["Alt+Cmd+k"] != "set-window-height -10%" || d.Binds["Cmd+Ctrl+Shift+Down"] != "move-workspace-down" || d.Binds["Cmd+Ctrl+Shift+k"] != "move-workspace-up" || d.Binds["Cmd+Shift+space"] != "toggle-floating" {
		t.Fatal(d.Binds)
	}
	if d.Binds["Cmd+Ctrl+space"] != "spawn fuzzel" || d.Binds["Alt+Ctrl+BackSpace"] != "quit" {
		t.Fatal(d.Binds)
	}
	if _, _, err := Load(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("explicit missing: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if DefaultPath() != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "neferwl", "config") {
		t.Fatal(DefaultPath())
	}
	got, w, err := LoadDefault()
	if err != nil || len(w) != 0 || !reflect.DeepEqual(got, d) {
		t.Fatalf("missing: %v %v %+v", err, w, got)
	}
	example, w, err := Load("../../../examples/config")
	if err != nil || len(w) != 0 || !reflect.DeepEqual(example, d) {
		t.Fatalf("example: %v %v\n%+v\n%+v", err, w, example, d)
	}
}

func TestRealtime(t *testing.T) {
	if !Defaults().Performance.Realtime {
		t.Fatal("realtime default disabled")
	}
	c, w := parseString(t, "performance.realtime = off\n")
	if c.Performance.Realtime || len(w) != 0 {
		t.Fatal(c.Performance, w)
	}
	c, w = parseString(t, "performance.realtime = invalid\n")
	if !c.Performance.Realtime || len(w) != 1 {
		t.Fatal(c.Performance, w)
	}
}

func TestFollowMove(t *testing.T) {
	c, w := parseString(t, "focus.follow-move = on\n")
	if !c.Focus.FollowMove || len(w) != 0 {
		t.Fatal(c.Focus, w)
	}
	c, w = parseString(t, "focus.follow-move = yes\n")
	if c.Focus.FollowMove || len(w) != 1 {
		t.Fatal(c.Focus, w)
	}
}

func TestFocusIndicator(t *testing.T) {
	d := Defaults().Focus
	if d.Animation != ports.FocusAnimationPulse || d.Effect != ports.FocusEffectScreen || d.Strength != 0.04 {
		t.Fatal("focus indicator defaults", d)
	}
	c, w := parseString(t, "focus.animation = off\nfocus.effect = screen\nfocus.strength = 0.08\n")
	if c.Focus.Animation != ports.FocusAnimationOff || c.Focus.Effect != ports.FocusEffectScreen || c.Focus.Strength != 0.08 || len(w) != 0 {
		t.Fatal(c.Focus, w)
	}
	// Each rejected value warns and keeps its default.
	for _, line := range []string{"focus.animation = blink", "focus.effect = glow", "focus.strength = 0", "focus.strength = 0.5", "focus.strength = x"} {
		c, w = parseString(t, line+"\n")
		if c.Focus != d || len(w) != 1 {
			t.Fatal(line, c.Focus, w)
		}
	}
}

func TestTouchpadTap(t *testing.T) {
	if !Defaults().Touchpad.Tap {
		t.Fatal("tap to click off by default")
	}
	c, w := parseString(t, "touchpad.tap = off\n")
	if c.Touchpad.Tap || len(w) != 0 {
		t.Fatal(c.Touchpad, w)
	}
	c, w = parseString(t, "touchpad.tap = sometimes\n")
	if !c.Touchpad.Tap || len(w) != 1 {
		t.Fatal(c.Touchpad, w)
	}
}

func TestTouchpadSpeed(t *testing.T) {
	d := Defaults().Touchpad
	if d.AccelSpeed != 0 || d.AccelProfile != ports.AccelAdaptive || d.ScrollFactor != 1 {
		t.Fatal(d)
	}
	c, w := parseString(t, "touchpad.accel-speed = -0.5\ntouchpad.accel-profile = flat\ntouchpad.scroll-factor = 0.5\n")
	if c.Touchpad.AccelSpeed != -0.5 || c.Touchpad.AccelProfile != ports.AccelFlat || c.Touchpad.ScrollFactor != 0.5 || len(w) != 0 {
		t.Fatal(c.Touchpad, w)
	}
	for _, bad := range []string{
		"touchpad.scroll-factor = 0",
		"touchpad.scroll-factor = 11",
	} {
		c, w := parseString(t, bad+"\n")
		if c.Touchpad != d || len(w) != 1 {
			t.Errorf("%s: %+v %v", bad, c.Touchpad, w)
		}
	}
}

func TestPointerKeys(t *testing.T) {
	d := Defaults()
	if d.Mouse != (ports.PointerConfig{AccelProfile: ports.AccelAdaptive}) {
		t.Fatal(d.Mouse)
	}
	if d.Touchpad.LeftHanded || d.Touchpad.NaturalScroll {
		t.Fatal(d.Touchpad)
	}
	for _, prefix := range []string{"touchpad", "mouse"} {
		// pointer returns the shared settings under the prefix.
		pointer := func(c ports.Config) ports.PointerConfig {
			if prefix == "mouse" {
				return c.Mouse
			}
			return c.Touchpad.PointerConfig
		}
		c, w := parseString(t, prefix+".natural-scroll = on\n"+prefix+".left-handed = on\n"+prefix+".accel-speed = -0.5\n"+prefix+".accel-profile = flat\n")
		want := ports.PointerConfig{NaturalScroll: true, LeftHanded: true, AccelSpeed: -0.5, AccelProfile: ports.AccelFlat}
		if pointer(c) != want || len(w) != 0 {
			t.Errorf("%s: %+v %v", prefix, pointer(c), w)
		}
		// The other device keeps its defaults.
		other := c.Mouse
		if prefix == "mouse" {
			other = c.Touchpad.PointerConfig
		}
		if other != (ports.PointerConfig{AccelProfile: ports.AccelAdaptive}) {
			t.Errorf("%s leaked into the other device: %+v", prefix, other)
		}
		for _, bad := range []string{
			".natural-scroll = maybe",
			".left-handed = 1",
			".accel-speed = 2",
			".accel-speed = -1.5",
			".accel-speed = NaN",
			".accel-speed = fast",
			".accel-profile = fast",
			".accel-profile = ",
			".unknown = on",
		} {
			c, w := parseString(t, prefix+bad+"\n")
			if c.Mouse != d.Mouse || c.Touchpad != d.Touchpad || len(w) != 1 {
				t.Errorf("%s%s: %+v %v", prefix, bad, pointer(c), w)
			}
		}
	}
	// Touchpad-only keys do not exist for mice.
	for _, bad := range []string{"mouse.tap = on", "mouse.scroll-factor = 2"} {
		if c, w := parseString(t, bad+"\n"); c.Mouse != d.Mouse || c.Touchpad != d.Touchpad || len(w) != 1 {
			t.Errorf("%s: %+v %v", bad, c.Mouse, w)
		}
	}
}

func TestParse(t *testing.T) {
	c, w := parseString(t, `
# comment
keyboard.layout = fr
keyboard.repeat-rate = 40   # inline comment
terminal = foot --server
background = #000000
border.inactive =
layout.presets = 1/3, 1/2 ,1
log.debug = core, input
output.DP-2.scale = 4/3
output.DP-2 = 5120x2160@165.058
output.HDMI-A-1 = off
output.DP-1 = preferred
output.DP-1.scale = 1.5
output.DP-1.primary = on
bind.cmd+code:30 = quit
render.direct-scanout = off
render.tearing = off
render.vrr = off
startup = wl-paste --watch cliphist store
startup = wl-paste --primary --watch cliphist store
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	if !reflect.DeepEqual(c.Startup, [][]string{{"wl-paste", "--watch", "cliphist", "store"}, {"wl-paste", "--primary", "--watch", "cliphist", "store"}}) {
		t.Fatalf("startup: %q", c.Startup)
	}
	if c.Keyboard.Layout != "fr" || c.Keyboard.RepeatRate != 40 || !reflect.DeepEqual(c.Terminal.Command, []string{"foot", "--server"}) || c.Background.Color != "#000000" || c.Render.DirectScanout || c.Render.Tearing || c.Render.VRR {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(c.Layout.Presets, []string{"1/3", "1/2", "1"}) || !reflect.DeepEqual(c.Log.Debug, []string{"core", "input"}) {
		t.Fatalf("%+v", c)
	}
	// Scale and mode lines combine in either order.
	if len(c.Outputs) != 3 || c.Outputs[0].Mode != "5120x2160@165.058" || c.Outputs[0].Scale != 4.0/3 || !c.Outputs[1].Off || c.Outputs[2].Mode != "" || c.Outputs[2].Off || c.Outputs[2].Scale != 1.5 || !c.Outputs[2].Primary || c.Outputs[0].Primary {
		t.Fatalf("%+v", c.Outputs)
	}
	if c.Binds["Cmd+code:30"] != "quit" {
		t.Fatal(c.Binds)
	}
}

func TestWarningsKeepDefaults(t *testing.T) {
	cases := []struct{ line, want string }{
		{"keyboard.repeat-rate = 0", "keyboard.repeat-rate"},
		{"keyboard.cmd = meta", "keyboard.cmd"},
		{"terminal =", "terminal"},
		{"xwayland = a b", "xwayland"},
		{"background = red", "background"},
		{"layout.gaps = 201", "layout.gaps"},
		{"layout.max-columns = 0", "layout.max-columns"},
		{"bind.cmd+x = focus-workspace 0", "focus-workspace"},
		{"layout.presets = 0px", "layout.presets"},
		{"log.level = trace", "log.level"},
		{"log.debug = nope", "log.debug"},
		{"output.DP-2 = big", "output.DP-2"},
		{"output.DP-2 = 1920x1080@0", "output.DP-2"},
		{"output.DP-2.scale = 0.5", "between 1 and 4"},
		{"output.DP-2.primary = yes", "output.DP-2.primary"},
		{"output.DP-2.scale = 3/0", "between 1 and 4"},
		{"bind.cmd+code:x = quit", "evdev key code"},
		{"nope = 1", "nope"},
		{"just text", "expected key = value"},
		{"bind.cmd+ = quit", "missing key"},
		{"bind.hyper+x = quit", "unknown modifier"},
		{"bind.cmd+x = bogus", "unknown action"},
		{"bind.cmd+x = set-column-width 10%", "set-column-width needs"},
		{"bind.cmd+x = set-column-width +0%", "set-column-width needs"},
		{"bind.cmd+x = set-window-height -101%", "set-window-height needs"},
		{"bind.cmd+x = set-window-height +5", "set-window-height needs"},
		{"bind.cmd+x = spawn   ", "unknown action"},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			c, w := parseString(t, tc.line)
			if len(w) != 1 || w[0].Line != 1 || !strings.Contains(w[0].Msg, tc.want) {
				t.Fatalf("warnings %v, want %q", w, tc.want)
			}
			if !reflect.DeepEqual(c, Defaults()) {
				t.Fatalf("config changed: %+v", c)
			}
		})
	}
}

func TestXwayland(t *testing.T) {
	if Defaults().Xwayland != "xwayland-satellite" {
		t.Fatal(Defaults().Xwayland)
	}
	if c, w := parseString(t, "xwayland = off"); c.Xwayland != "" || len(w) != 0 {
		t.Fatal(c.Xwayland, w)
	}
	if c, w := parseString(t, "xwayland = /opt/xwls"); c.Xwayland != "/opt/xwls" || len(w) != 0 {
		t.Fatal(c.Xwayland, w)
	}
}

func TestDuplicateKeyLastWins(t *testing.T) {
	c, w := parseString(t, "background = #000000\nbackground = #111111\n")
	if c.Background.Color != "#111111" || len(w) != 1 || w[0].Line != 2 {
		t.Fatal(c.Background, w)
	}
}

func TestBinds(t *testing.T) {
	c, w := parseString(t, `
bind.CTRL+Cmd+E = spawn foo --bar
bind.cmd+; = close-window
bind.cmd+é = quit
bind.cmd+semicolon = quit
bind.cmd+equal = spawn-terminal
bind.cmd+# = none
bind.cmd++ = quit
bind.alt+f5 = quit
bind.cmd+return = none
bind.cmd+w = focus-workspace 12
`)
	want := map[string]string{
		"Cmd+Ctrl+e":    "spawn foo --bar",
		"Cmd+semicolon": "quit",
		"Cmd+eacute":    "quit",
		"Cmd+equal":     "spawn-terminal",
		"Cmd+plus":      "quit",
		"Alt+F5":        "quit",
	}
	for combo, action := range want {
		if c.Binds[combo] != action {
			t.Errorf("%s = %q, want %q", combo, c.Binds[combo], action)
		}
	}
	if _, ok := c.Binds["Cmd+Return"]; ok {
		t.Error("none did not remove the default")
	}
	if _, ok := c.Binds["Cmd+numbersign"]; ok {
		t.Error("none kept")
	}
	// cmd+; then cmd+semicolon is the same key: warn and keep the last.
	if len(w) != 1 || w[0].Line != 5 || !strings.Contains(w[0].Msg, "overrides line 3") {
		t.Fatal(w)
	}
}

func TestBindReservedCharacter(t *testing.T) {
	_, w := parseString(t, "bind.cmd+= = quit\n")
	if len(w) != 1 || !strings.Contains(w[0].Msg, "cmd+equal") {
		t.Fatal(w)
	}
}

func TestBindResolvedCollision(t *testing.T) {
	// The later line wins, like any duplicate key.
	c, w := parseString(t, "keyboard.cmd = ctrl\nbind.ctrl+x = quit\nbind.cmd+x = close-window\nbind.cmd+ctrl+y = quit\n")
	if c.Binds["Cmd+x"] != "close-window" {
		t.Fatal(c.Binds)
	}
	if _, ok := c.Binds["Ctrl+x"]; ok {
		t.Fatal(c.Binds)
	}
	if _, ok := c.Binds["Cmd+Ctrl+y"]; ok {
		t.Fatal(c.Binds)
	}
	if len(w) != 2 || w[0].Line != 4 || w[1].Line != 2 || !strings.Contains(w[1].Msg, "overridden by bind.Cmd+x") {
		t.Fatal(w)
	}
	// A user bind replaces a default with the same resolved keys, without a warning.
	c, w = parseString(t, "bind.super+q = quit\n")
	if c.Binds["Super+q"] != "quit" || c.Binds["Cmd+q"] != "" || len(w) != 0 {
		t.Fatal(c.Binds, w)
	}
}

func TestInvalidDuplicateKeepsEarlierValue(t *testing.T) {
	c, w := parseString(t, "background = #000000\nbackground = red\nbackground = #111111\n")
	if c.Background.Color != "#111111" || len(w) != 2 || w[0].Line != 2 || w[1].Line != 3 || !strings.Contains(w[1].Msg, "overrides line 1") {
		t.Fatal(c.Background, w)
	}
}

func TestBindCharacters(t *testing.T) {
	c, w := parseString(t, "bind.cmd+É = quit\nbind.cmd+ж = quit\n")
	if c.Binds["Cmd+eacute"] != "quit" {
		t.Fatal(c.Binds)
	}
	if len(w) != 1 || w[0].Line != 2 || !strings.Contains(w[0].Msg, "keysym name") {
		t.Fatal(w)
	}
}

func TestParseMode(t *testing.T) {
	w, h, hz, err := ParseMode("3440x1440")
	if err != nil || w != 3440 || h != 1440 || hz != 0 {
		t.Fatalf("%d %d %v %v", w, h, hz, err)
	}
	if _, _, hz, err := ParseMode("1920x1080@59.94"); err != nil || hz != 59.94 {
		t.Fatal(hz, err)
	}
}

func TestDigitBindReplacesPhysicalDefault(t *testing.T) {
	c, w := parseString(t, "bind.cmd+1 = none\nbind.cmd+shift+2 = quit\n")
	if len(w) != 0 {
		t.Fatal(w)
	}
	if _, ok := c.Binds["Cmd+code:2"]; ok {
		t.Fatal("default cmd+code:2 kept")
	}
	if _, ok := c.Binds["Cmd+Shift+code:3"]; ok || c.Binds["Cmd+Shift+2"] != "quit" {
		t.Fatal(c.Binds)
	}
	if c.Binds["Cmd+code:4"] != "focus-workspace 3" {
		t.Fatal("other defaults must stay")
	}
}

func TestWorkspaces(t *testing.T) {
	c, w := parseString(t, `layout.overflow = fixed
workspace.dev.hidden = on
workspace.dev.max-columns = 3
workspace.web.overflow = scroll
workspace.dev.monitor = DP-2
workspace.bad name.hidden = on
workspace.dev.color = red
workspace.web.overflow = spiral
bind.cmd+d = workspace dev
bind.cmd+w = workspace
bind.cmd+x = workspace typo
workspace.lost.monitor = DP-1
`)
	want := []ports.WorkspaceConfig{
		{Name: "dev", Monitor: "DP-2", MaxColumns: 3},
		{Name: "web", Overflow: "scroll"},
		{Name: "lost", Monitor: "DP-1"},
	}
	if c.Layout.Overflow != "fixed" || !reflect.DeepEqual(c.Workspaces, want) {
		t.Fatalf("%q %+v", c.Layout.Overflow, c.Workspaces)
	}
	if c.Binds["Cmd+d"] != "workspace dev" {
		t.Fatal(c.Binds["Cmd+d"])
	}
	lines := []int{}
	for _, x := range w {
		lines = append(lines, x.Line)
	}
	if !reflect.DeepEqual(lines, []int{2, 4, 6, 7, 8, 10, 11, 12}) {
		t.Fatal(w)
	}
}

func TestLayoutPerOutput(t *testing.T) {
	c, w := parseString(t, `layout.max-columns = 2
layout.HDMI-A-1.max-columns = 3
layout.HDMI-A-1.overflow = fixed
layout.LG Electronics 27GR95UM 123.overflow = scroll
layout.DP-2.overflow = spiral
layout.DP-2.color = red
`)
	want := []ports.OutputLayout{
		{Output: "HDMI-A-1", MaxColumns: 3, Overflow: "fixed"},
		{Output: "LG Electronics 27GR95UM 123", Overflow: "scroll"},
	}
	if c.Layout.MaxColumns != 2 || !reflect.DeepEqual(c.Layout.Outputs, want) || len(w) != 2 {
		t.Fatalf("%+v %v", c.Layout.Outputs, w)
	}
}

func TestFixedOutputWarnsSlotWidths(t *testing.T) {
	_, w := parseString(t, `layout.DP-2.overflow = fixed
workspace.dev.monitor = DP-2
workspace.dev.column.1 = 50%, foot
workspace.web.column.1 = 50%, foot
`)
	if len(w) != 3 || w[1].Line != 3 {
		t.Fatal(w)
	}
}

func TestWorkspaceSlots(t *testing.T) {
	c, w := parseString(t, `workspace.dev.monitor = DP-2
workspace.dev.column.2 = 33%, foot --title x
workspace.dev.column.1 = 67%, code --new-window
workspace.dev.column.2 = 1/3, kitty
workspace.dev.column.0 = 50%, foot
workspace.dev.column.3 = 50%
workspace.dev.column.4 = huge, foot
bind.cmd+d = workspace dev
`)
	want := []ports.SlotConfig{
		{Index: 1, Width: "67%", Argv: []string{"code", "--new-window"}},
		{Index: 2, Width: "1/3", Argv: []string{"kitty"}},
	}
	if len(c.Workspaces) != 1 || !reflect.DeepEqual(c.Workspaces[0].Slots, want) {
		t.Fatalf("%+v", c.Workspaces)
	}
	lines := []int{}
	for _, x := range w {
		lines = append(lines, x.Line)
	}
	if !reflect.DeepEqual(lines, []int{4, 5, 6, 7}) {
		t.Fatal(w)
	}
}

func TestSlotWidthsIgnoredWithFixedOverflow(t *testing.T) {
	_, w := parseString(t, `workspace.dev.overflow = fixed
workspace.dev.column.1 = 67%, foot
`)
	if len(w) != 2 || w[1].Line != 2 || !strings.Contains(w[1].Msg, "ignored") {
		t.Fatal(w)
	}
}

func TestTerminalResolutionAndAutoOpen(t *testing.T) {
	t.Setenv("TERMINAL", "ghostty --new-window")
	c, w := parseString(t, "terminal.auto-open = all\n")
	if len(w) != 0 || !reflect.DeepEqual(c.Terminal.Command, []string{"ghostty", "--new-window"}) || c.Terminal.AutoOpen != "all" {
		t.Fatal(c.Terminal, w)
	}
	c, w = parseString(t, "terminal = foot --server\nterminal.auto-open = off\n")
	if len(w) != 0 || !reflect.DeepEqual(c.Terminal.Command, []string{"foot", "--server"}) || c.Terminal.AutoOpen != "off" {
		t.Fatal(c.Terminal, w)
	}
	c, w = parseString(t, "terminal.auto-open = invalid\n")
	if len(w) != 1 || c.Terminal.AutoOpen != "first" {
		t.Fatal(c.Terminal, w)
	}
	t.Setenv("TERMINAL", " ")
	if got := Defaults().Terminal.Command; !reflect.DeepEqual(got, []string{"foot"}) {
		t.Fatal(got)
	}
}

func TestStash(t *testing.T) {
	if d := Defaults(); d.Stash.Width != 80 || d.Stash.Gap != 2 || d.Stash.Dim != 0.5 {
		t.Fatalf("defaults %+v", d.Stash)
	}
	for _, tc := range []struct {
		line    string
		width   int
		gap     int
		dim     float64
		warning bool
	}{
		{"stash.width = 10", 10, 2, 0.5, false}, {"stash.width = 70%", 70, 2, 0.5, false}, {"stash.width = 90", 90, 2, 0.5, false},
		{"stash.width = 9", 80, 2, 0.5, true}, {"stash.width = 91", 80, 2, 0.5, true}, {"stash.width = 0", 80, 2, 0.5, true}, {"stash.width = 1/2", 80, 2, 0.5, true},
		{"stash.gap = 0", 80, 0, 0.5, false}, {"stash.gap = 10", 80, 10, 0.5, false},
		{"stash.gap = 11", 80, 2, 0.5, true}, {"stash.gap = -1", 80, 2, 0.5, true}, {"stash.gap = 2.5", 80, 2, 0.5, true},
		{"stash.dim = 0.8", 80, 2, 0.8, false}, {"stash.dim = 1.5", 80, 2, 0.5, true}, {"stash.dim = NaN", 80, 2, 0.5, true},
	} {
		t.Run(tc.line, func(t *testing.T) {
			c, warnings := parseString(t, tc.line)
			if c.Stash.Width != tc.width || c.Stash.Gap != tc.gap || c.Stash.Dim != tc.dim || (len(warnings) != 0) != tc.warning {
				t.Fatalf("stash %+v, warnings %v", c.Stash, warnings)
			}
		})
	}
}

func TestStashCapture(t *testing.T) {
	if c, _ := parseString(t, ""); !c.Stash.Capture {
		t.Fatal("stash.capture is on by default")
	}
	if c, w := parseString(t, "stash.capture = off"); c.Stash.Capture || len(w) != 0 {
		t.Fatalf("off: %v %v", c.Stash.Capture, w)
	}
	if c, w := parseString(t, "stash.capture = yes"); !c.Stash.Capture || len(w) == 0 {
		t.Fatalf("invalid: %v %v", c.Stash.Capture, w)
	}
}

func TestVRRFlipGap(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    time.Duration
		warning bool
	}{
		{"0", 0, false}, {"0ms", 0, false}, {"500us", 500 * time.Microsecond, false}, {"2ms", 2 * time.Millisecond, false}, {"10ms", 10 * time.Millisecond, false},
		{"11ms", time.Millisecond, true}, {"-1ms", time.Millisecond, true}, {"1", time.Millisecond, true}, {"fast", time.Millisecond, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c, warnings := parseString(t, "render.vrr-flip-gap = "+tc.value)
			if c.Render.VRRFlipGap != tc.want || (len(warnings) != 0) != tc.warning {
				t.Fatalf("gap %v, warnings %v", c.Render.VRRFlipGap, warnings)
			}
		})
	}
}

func TestCursorHideAfter(t *testing.T) {
	if d := Defaults().Cursor.HideAfter; d != 5*time.Second {
		t.Fatalf("default %v", d)
	}
	for _, tc := range []struct {
		value   string
		want    time.Duration
		warning bool
	}{
		{"off", 0, false}, {"100ms", 100 * time.Millisecond, false}, {"2s", 2 * time.Second, false}, {"1h", time.Hour, false},
		{"0", 5 * time.Second, true}, {"50ms", 5 * time.Second, true}, {"2h", 5 * time.Second, true}, {"on", 5 * time.Second, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c, warnings := parseString(t, "cursor.hide-after = "+tc.value)
			if c.Cursor.HideAfter != tc.want || (len(warnings) != 0) != tc.warning {
				t.Fatalf("hide-after %v, warnings %v", c.Cursor.HideAfter, warnings)
			}
		})
	}
}

func TestFloatingDim(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    float64
		warning bool
	}{
		{"0", 0, false}, {"0.75", 0.75, false}, {"1", 1, false},
		{"-0.1", 0.3, true}, {"1.1", 0.3, true}, {"NaN", 0.3, true}, {"invalid", 0.3, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c, warnings := parseString(t, "floating.dim = "+tc.value)
			if c.Floating.Dim != tc.want || (len(warnings) != 0) != tc.warning {
				t.Fatalf("dim %v, warnings %v", c.Floating.Dim, warnings)
			}
		})
	}
}
