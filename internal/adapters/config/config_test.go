package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

func parseString(t *testing.T, s string) (domain.Config, []Warning) {
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
	if d.Keyboard.RepeatRate != 25 || d.Keyboard.CmdKey != "super" || !d.Render.DirectScanout || len(d.Binds) != 79 || d.Layout.MaxColumns != 2 || d.Floating.Dim != 0.3 {
		t.Fatalf("defaults: %+v", d)
	}
	if d.Binds["Cmd+s"] != "toggle-stash-visible" || d.Binds["Cmd+Shift+s"] != "toggle-window-stash" || d.Binds["Cmd+o"] != "toggle-overview" || d.Binds["Cmd+f"] != "maximize-column" || d.Binds["Cmd+Shift+f"] != "toggle-fullscreen" || d.Binds["Cmd+Shift+h"] != "move-column-left" || d.Binds["Cmd+j"] != "focus-window-down" || d.Binds["Cmd+Shift+code:2"] != "move-column-to-workspace 1" || d.Focus.FollowMove {
		t.Fatal(d.Binds)
	}
	if d.Binds["Cmd+Shift+Up"] != "move-window-up" || d.Binds["Cmd+Shift+j"] != "move-window-down" || d.Binds["Alt+Cmd+Left"] != "set-column-width -1" || d.Binds["Alt+Cmd+l"] != "set-column-width +1" || d.Binds["Alt+Cmd+k"] != "set-window-height -10%" || d.Binds["Cmd+Ctrl+Shift+Down"] != "move-workspace-next" || d.Binds["Cmd+Ctrl+Shift+k"] != "move-workspace-prev" || d.Binds["Cmd+Shift+space"] != "toggle-floating" {
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

func TestAnimations(t *testing.T) {
	d := Defaults().Animations
	if d.On || d.Slowdown != normalSlowdown {
		t.Fatal("animations defaults", d)
	}
	c, w := parseString(t, "animations = on\nanimations.speed = 0.5\n")
	if !c.Animations.On || c.Animations.Slowdown != 2*normalSlowdown || len(w) != 0 {
		t.Fatal(c.Animations, w)
	}
	for v, slowdown := range map[string]float64{"slow": 1, "normal": 0.7, "fast": 0.5, "10": 0.07, "0.1": 7} {
		c, w = parseString(t, "animations.speed = "+v+"\n")
		if math.Abs(c.Animations.Slowdown-slowdown) > 1e-9 || len(w) != 0 {
			t.Fatal(v, c.Animations, w)
		}
	}
	c, w = parseString(t, "animations = off\n")
	if c.Animations.On || len(w) != 0 {
		t.Fatal(c.Animations, w)
	}
	for _, line := range []string{"animations = maybe", "animations.speed = 0", "animations.speed = 11", "animations.speed = turbo", "animations.slowdown = 2"} {
		c, w = parseString(t, line+"\n")
		if c.Animations != d || len(w) != 1 {
			t.Fatal(line, c.Animations, w)
		}
		if strings.Contains(line, "speed") && !strings.Contains(w[0].Msg, "must be slow, normal, fast or a number between 0.1 and 10") {
			t.Fatal(line, w)
		}
	}
}

func TestFocusFlash(t *testing.T) {
	d := Defaults().Focus
	if !d.Flash || d.FlashWidth != 2 || d.FlashOpacity != 0.6 || d.FlashDuration != 200*time.Millisecond || d.FlashColor != "" {
		t.Fatal("focus flash defaults", d)
	}
	c, w := parseString(t, "focus.flash = off\nfocus.flash-width = 8\nfocus.flash-opacity = 1\nfocus.flash-duration = 1s\nfocus.flash-color = #112233\n")
	want := d
	want.Flash, want.FlashWidth, want.FlashOpacity, want.FlashDuration, want.FlashColor = false, 8, 1, time.Second, "#112233"
	if c.Focus != want || len(w) != 0 {
		t.Fatal(c.Focus, w)
	}
	c, w = parseString(t, "focus.flash = on\nfocus.flash-width = 1\nfocus.flash-opacity = 0.1\nfocus.flash-duration = 50ms\nfocus.flash-color =\n")
	want.Flash, want.FlashWidth, want.FlashOpacity, want.FlashDuration, want.FlashColor = true, 1, 0.1, 50*time.Millisecond, ""
	if c.Focus != want || len(w) != 0 {
		t.Fatal(c.Focus, w)
	}
	// Each rejected value warns and keeps its default; removed keys are unknown.
	for _, line := range []string{
		"focus.flash = maybe",
		"focus.flash-width = 0", "focus.flash-width = 9", "focus.flash-width = x", "focus.flash-width = 1.5",
		"focus.flash-opacity = 0.05", "focus.flash-opacity = 1.1", "focus.flash-opacity = x",
		"focus.flash-duration = 10ms", "focus.flash-duration = 2s", "focus.flash-duration = x", "focus.flash-duration = 200",
		"focus.flash-color = red", "focus.flash-color = #12345",
		"focus.animation = pulse", "focus.effect = screen", "focus.strength = 0.04",
	} {
		c, w = parseString(t, line+"\n")
		if c.Focus != d || len(w) != 1 {
			t.Fatal(line, c.Focus, w)
		}
		if strings.HasPrefix(line, "focus.animation") || strings.HasPrefix(line, "focus.effect") || strings.HasPrefix(line, "focus.strength") {
			if !strings.Contains(w[0].Msg, "unknown key") {
				t.Fatal(line, w)
			}
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
	if d.AccelSpeed != 0 || d.AccelProfile != domain.AccelAdaptive || d.ScrollFactor != 1 {
		t.Fatal(d)
	}
	c, w := parseString(t, "touchpad.scroll-factor = 0.5\n")
	if c.Touchpad.ScrollFactor != 0.5 || len(w) != 0 {
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
	if d.Mouse != (domain.PointerConfig{AccelProfile: domain.AccelAdaptive}) {
		t.Fatal(d.Mouse)
	}
	if d.Touchpad.LeftHanded || d.Touchpad.NaturalScroll {
		t.Fatal(d.Touchpad)
	}
	touchpad := func(c domain.Config) domain.PointerConfig { return c.Touchpad.PointerConfig }
	mouse := func(c domain.Config) domain.PointerConfig { return c.Mouse }
	for _, tc := range []struct {
		prefix         string
		pointer, other func(domain.Config) domain.PointerConfig
	}{
		{"touchpad", touchpad, mouse},
		{"mouse", mouse, touchpad},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			prefix := tc.prefix
			c, w := parseString(t, prefix+".natural-scroll = on\n"+prefix+".left-handed = on\n"+prefix+".accel-speed = -0.5\n"+prefix+".accel-profile = flat\n")
			want := domain.PointerConfig{NaturalScroll: true, LeftHanded: true, AccelSpeed: -0.5, AccelProfile: domain.AccelFlat}
			if tc.pointer(c) != want || len(w) != 0 {
				t.Errorf("%+v %v", tc.pointer(c), w)
			}
			// The other device keeps its defaults.
			if other := tc.other(c); other != (domain.PointerConfig{AccelProfile: domain.AccelAdaptive}) {
				t.Errorf("leaked into the other device: %+v", other)
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
					t.Errorf("%s%s: %+v %v", prefix, bad, tc.pointer(c), w)
				}
			}
		})
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
startup = wl-paste --primary --watch cliphist store; waybar ;mako;
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	if !reflect.DeepEqual(c.Startup, [][]string{{"wl-paste", "--watch", "cliphist", "store"}, {"wl-paste", "--primary", "--watch", "cliphist", "store"}, {"waybar"}, {"mako"}}) {
		t.Fatalf("startup: %q", c.Startup)
	}
	if c.Keyboard.Layout != "fr" || c.Keyboard.RepeatRate != 40 || !reflect.DeepEqual(c.Terminal.Command, []string{"foot", "--server"}) || c.Background.Color != "#000000" || c.Render.DirectScanout || c.Render.Tearing || c.Render.VRR {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(c.Log.Debug, []string{"core", "input"}) {
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
		{"layout.presets = 1/2, 1", "layout.presets: unknown key"},
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
		{"bind.cmd+x = focus-workspace-up", `renamed to "focus-workspace-prev"`},
		{"bind.cmd+x = move-workspace-down", `renamed to "move-workspace-next"`},
		{"bind.cmd+x = move-column-to-workspace-up", `renamed to "move-column-to-workspace-prev"`},
		{"bind.cmd+x = set-column-width 1", "set-column-width needs"},
		{"bind.cmd+x = set-column-width +0", "set-column-width needs"},
		{"bind.cmd+x = set-column-width +17", "set-column-width needs"},
		{"bind.cmd+x = set-column-width +10%", "set-column-width needs"},
		{"bind.cmd+x = set-column-width +-1", "set-column-width needs"},
		{"bind.cmd+x = set-window-height +5", "set-window-height needs"},
		{"bind.cmd+x = set-window-height -101%", "set-window-height needs"},
		{"bind.cmd+x = set-window-height +5", "set-window-height needs"},
		{"bind.cmd+x = spawn   ", "unknown action"},
		{"bind.cmd+x = spawn ; ;", "spawn needs a command"},
		{"bind.cmd+x = stash ", "unknown action"},
		{"bind.cmd+x = stash ; ;", "stash needs a command"},
		{"startup = ;", "must not be empty"},
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

func TestWorkspacePrevNextAction(t *testing.T) {
	c, w := parseString(t, "bind.cmd+x = focus-workspace-next")
	if len(w) != 0 || c.Binds["Cmd+x"] != "focus-workspace-next" {
		t.Fatal(c.Binds["Cmd+x"], w)
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

func TestColumnSwitcherBinds(t *testing.T) {
	d := Defaults()
	if d.Binds["Cmd+Tab"] != "switch-column-next" || d.Binds["Cmd+Shift+Tab"] != "switch-column-prev" {
		t.Fatal(d.Binds)
	}
	for _, action := range []string{"switch-column-next", "switch-column-prev"} {
		c, w := parseString(t, "bind.cmd+x = "+action+"\n")
		if c.Binds["Cmd+x"] != action || len(w) != 0 {
			t.Fatalf("%s: binds %v warnings %v", action, c.Binds, w)
		}
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
	want := []domain.WorkspaceConfig{
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
	want := []domain.OutputLayout{
		{Output: "HDMI-A-1", MaxColumns: 3, Overflow: "fixed"},
		{Output: "LG Electronics 27GR95UM 123", Overflow: "scroll"},
	}
	if c.Layout.MaxColumns != 2 || !reflect.DeepEqual(c.Layout.Outputs, want) || len(w) != 2 {
		t.Fatalf("%+v %v", c.Layout.Outputs, w)
	}
}

func TestFixedOutputWarnsSlotWidths(t *testing.T) {
	_, w := parseString(t, `layout.overflow = scroll
layout.DP-2.overflow = fixed
workspace.dev.monitor = DP-2
workspace.dev.column.1 = 50%, foot
workspace.web.column.1 = 50%, foot
`)
	if len(w) != 3 || w[1].Line != 4 {
		t.Fatal(w)
	}
}

func TestWorkspaceSlots(t *testing.T) {
	c, w := parseString(t, `layout.overflow = scroll
workspace.dev.monitor = DP-2
workspace.dev.column.2 = 1, foot --title x
workspace.dev.column.1 = 3, code --new-window
workspace.dev.column.2 = 16, kitty
workspace.dev.column.0 = 2, foot
workspace.dev.column.3 = 2
workspace.dev.column.4 = huge, foot
workspace.dev.column.5 = 67%, foot
workspace.dev.column.6 = 1/3, foot
workspace.dev.column.7 = 800px, foot
workspace.dev.column.8 = 0, foot
workspace.dev.column.9 = 17, foot
bind.cmd+d = workspace dev
`)
	want := []domain.SlotConfig{
		{Index: 1, Cells: 3, Argv: []string{"code", "--new-window"}},
		{Index: 2, Cells: 16, Argv: []string{"kitty"}},
	}
	if len(c.Workspaces) != 1 || !reflect.DeepEqual(c.Workspaces[0].Slots, want) {
		t.Fatalf("%+v", c.Workspaces)
	}
	lines := []int{}
	for _, x := range w {
		lines = append(lines, x.Line)
	}
	if !reflect.DeepEqual(lines, []int{5, 6, 7, 8, 9, 10, 11, 12, 13}) {
		t.Fatal(w)
	}
}

// Slot widths are cells in every overflow: nothing is ignored.
func TestSlotWidthsKeptWithFixedOverflow(t *testing.T) {
	c, w := parseString(t, `workspace.dev.overflow = fixed
workspace.dev.column.1 = 2, foot
bind.cmd+d = workspace dev
`)
	if len(w) != 0 || len(c.Workspaces) != 1 || len(c.Workspaces[0].Slots) != 1 || c.Workspaces[0].Slots[0].Cells != 2 {
		t.Fatal(w, c.Workspaces)
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

func TestStashSpawnTerminal(t *testing.T) {
	if c, _ := parseString(t, ""); !c.Stash.SpawnTerminal {
		t.Fatal("stash.spawn-terminal is on by default")
	}
	if c, w := parseString(t, "stash.spawn-terminal = off"); c.Stash.SpawnTerminal || len(w) != 0 {
		t.Fatalf("off: %v %v", c.Stash.SpawnTerminal, w)
	}
	if c, w := parseString(t, "stash.spawn-terminal = on"); !c.Stash.SpawnTerminal || len(w) != 0 {
		t.Fatalf("on: %v %v", c.Stash.SpawnTerminal, w)
	}
	if c, w := parseString(t, "stash.spawn-terminal = yes"); !c.Stash.SpawnTerminal || len(w) == 0 {
		t.Fatalf("invalid: %v %v", c.Stash.SpawnTerminal, w)
	}
}

func TestStashBind(t *testing.T) {
	c, w := parseString(t, "bind.cmd+shift+f = stash firefox --private-window")
	if len(w) != 0 || c.Binds["Cmd+Shift+f"] != "stash firefox --private-window" {
		t.Fatalf("stash bind: %v %v", c.Binds, w)
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

func TestOutputRelations(t *testing.T) {
	c, w := parseString(t, `
output.DP-2.right-of = DP-1
output.DP-2.offset = 180
output.DP-3.left-of = DP-1
output.DP-3.offset = -40
output.DP-4.above = DP-1
output.DP-5.below = DP-1
output.DP-5.offset = 7
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	want := map[string]domain.OutputAnchor{
		"DP-2": {Relation: domain.RelationRightOf, To: "DP-1", Offset: 180},
		"DP-3": {Relation: domain.RelationLeftOf, To: "DP-1", Offset: -40},
		"DP-4": {Relation: domain.RelationAbove, To: "DP-1"},
		"DP-5": {Relation: domain.RelationBelow, To: "DP-1", Offset: 7},
	}
	if len(c.Outputs) != len(want) {
		t.Fatalf("%+v", c.Outputs)
	}
	for _, o := range c.Outputs {
		if o.Anchor != want[o.Name] || !o.ScaleOnly {
			t.Fatalf("%s: %+v", o.Name, o)
		}
	}
}

func TestOutputTransform(t *testing.T) {
	names := []string{"normal", "90", "180", "270", "flipped", "flipped-90", "flipped-180", "flipped-270"}
	var text strings.Builder
	for i, n := range names {
		fmt.Fprintf(&text, "output.O%d.transform = %s\n", i, n)
	}
	c, w := parseString(t, text.String())
	if len(w) != 0 || len(c.Outputs) != len(names) {
		t.Fatalf("%v %+v", w, c.Outputs)
	}
	for i, o := range c.Outputs {
		if o.Transform != domain.BufferTransform(i) || !o.ScaleOnly {
			t.Fatalf("%s: %+v", names[i], o)
		}
	}

	c, w = parseString(t, "output.DP-1.transform = 45\n")
	if len(w) != 1 || !strings.Contains(w[0].Msg, "invalid transform") {
		t.Fatalf("warnings %v", w)
	}
	if len(c.Outputs) != 0 {
		t.Fatalf("%+v", c.Outputs)
	}

	// A later mode line keeps the transform.
	c, w = parseString(t, "output.DP-1.transform = 90\noutput.DP-1 = 1920x1080\n")
	if len(w) != 0 || len(c.Outputs) != 1 || c.Outputs[0].Transform != 1 || c.Outputs[0].ScaleOnly {
		t.Fatalf("%v %+v", w, c.Outputs)
	}
}

func TestOutputRelationOverride(t *testing.T) {
	c, w := parseString(t, "output.DP-2.right-of = DP-1\noutput.DP-2.below = DP-3\n")
	if len(w) != 1 || w[0].Line != 2 || !strings.Contains(w[0].Msg, "overrides line 1") {
		t.Fatalf("warnings %v", w)
	}
	if got := c.Outputs[0].Anchor; got != (domain.OutputAnchor{Relation: domain.RelationBelow, To: "DP-3"}) {
		t.Fatalf("%+v", got)
	}
}

func TestOutputRelationWarnings(t *testing.T) {
	const rangeMsg = "must be an integer between -65536 and 65536"
	rel := "output.DP-2.right-of = DP-1\n"
	cases := []struct {
		name, text string
		want       []string // expected warning messages (substring), in order
		lines      []int    // expected warning lines, in order
		anchored   []string // outputs that keep a relation
	}{
		{"self", "output.DP-2.right-of = DP-2", []string{"relative to itself"}, []int{1}, nil},
		{"empty", "output.DP-2.above =", []string{"expected an output name"}, []int{1}, nil},
		{"bad offset", rel + "output.DP-2.offset = abc", []string{rangeMsg}, []int{2}, []string{"DP-2"}},
		{"offset too large", rel + "output.DP-2.offset = 65537", []string{rangeMsg}, []int{2}, []string{"DP-2"}},
		{"offset too small", rel + "output.DP-2.offset = -65537", []string{rangeMsg}, []int{2}, []string{"DP-2"}},
		{"offset overflow", rel + "output.DP-2.offset = 99999999999999999999", []string{rangeMsg}, []int{2}, []string{"DP-2"}},
		{"offset alone", "output.DP-2.offset = 10", []string{"without a relation"}, []int{1}, nil},
		{"two cycle", "output.A.right-of = B\noutput.B.below = A", []string{"relation cycle", "relation cycle"}, []int{1, 2}, nil},
		{"three cycle", "output.A.right-of = B\noutput.B.right-of = C\noutput.C.above = A", []string{"relation cycle", "relation cycle", "relation cycle"}, []int{1, 2, 3}, nil},
		{"tail into cycle", "output.A.right-of = B\noutput.B.right-of = A\noutput.C.left-of = A", []string{"relation cycle", "relation cycle"}, []int{1, 2}, []string{"C"}},
		{"relation overridden twice", "output.DP-2.right-of = A\noutput.DP-2.below = B\noutput.DP-2.right-of = C", []string{"overrides line 1", "overrides line 2"}, []int{2, 3}, []string{"DP-2"}},
		{"relation overridden three times", "output.DP-2.right-of = A\noutput.DP-2.below = B\noutput.DP-2.right-of = C\noutput.DP-2.below = D", []string{"overrides line 1", "overrides line 2", "overrides line 3"}, []int{2, 3, 4}, []string{"DP-2"}},
		{"same relation on two outputs", "output.DP-2.right-of = DP-1\noutput.DP-3.right-of = DP-1", []string{"output.DP-3: same relation as output.DP-2; both get the same position"}, []int{2}, []string{"DP-2", "DP-3"}},
		{"same relation and offset on three outputs", "output.A.below = R\noutput.A.offset = 5\noutput.B.below = R\noutput.B.offset = 5\noutput.C.below = R\noutput.C.offset = 5", []string{"output.B: same relation as output.A", "output.C: same relation as output.A"}, []int{3, 5}, []string{"A", "B", "C"}},
		{"same reference, other offset", "output.DP-2.right-of = DP-1\noutput.DP-3.right-of = DP-1\noutput.DP-3.offset = 10", nil, nil, []string{"DP-2", "DP-3"}},
		{"same reference, other relation", "output.DP-2.right-of = DP-1\noutput.DP-3.left-of = DP-1", nil, nil, []string{"DP-2", "DP-3"}},
		{"same relation twice", "output.DP-2.below = A\noutput.DP-2.below = B", []string{"overrides line 1"}, []int{2}, []string{"DP-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := parseString(t, tc.text)
			if len(w) != len(tc.want) {
				t.Fatalf("warnings %v, want %q", w, tc.want)
			}
			for i, x := range w {
				if !strings.Contains(x.Msg, tc.want[i]) || x.Line != tc.lines[i] {
					t.Fatalf("warning %d = %+v, want %q at line %d (all: %v)", i, x, tc.want[i], tc.lines[i], w)
				}
			}
			var got []string
			for _, o := range c.Outputs {
				if o.Anchor.Relation != domain.RelationNone {
					got = append(got, o.Name)
				}
			}
			if !reflect.DeepEqual(got, tc.anchored) {
				t.Fatalf("anchored %v, want %v: %+v", got, tc.anchored, c.Outputs)
			}
		})
	}
}

func TestDefaultMonitorBinds(t *testing.T) {
	d := Defaults()
	for combo, action := range map[string]string{
		"Cmd+Ctrl+Up": "focus-monitor-up", "Cmd+Ctrl+k": "focus-monitor-up",
		"Cmd+Ctrl+Down": "focus-monitor-down", "Cmd+Ctrl+j": "focus-monitor-down",
		"Cmd+Ctrl+Left": "focus-monitor-left", "Cmd+Ctrl+l": "focus-monitor-right",
	} {
		if d.Binds[combo] != action {
			t.Errorf("%s = %q, want %q", combo, d.Binds[combo], action)
		}
	}
	// The up/down move-workspace-to-monitor actions have no default bind but
	// load from a config file.
	c, w := parseString(t, "bind.cmd+ctrl+alt+up = move-workspace-to-monitor-up\nbind.cmd+ctrl+alt+down = move-workspace-to-monitor-down\nbind.cmd+alt+u = focus-monitor-up\n")
	if len(w) != 0 || c.Binds["Alt+Cmd+Ctrl+Up"] != "move-workspace-to-monitor-up" || c.Binds["Alt+Cmd+Ctrl+Down"] != "move-workspace-to-monitor-down" {
		t.Fatalf("%v %v", w, c.Binds)
	}
}

func TestOutputOffsetKeepsEarlierValueWhenInvalid(t *testing.T) {
	c, _ := parseString(t, "output.DP-2.right-of = DP-1\noutput.DP-2.offset = 40\noutput.DP-2.offset = 65537\n")
	if got := c.Outputs[0].Anchor.Offset; got != 40 {
		t.Fatalf("offset %d, want 40", got)
	}
	c, w := parseString(t, "output.DP-2.right-of = DP-1\noutput.DP-2.offset = 65536\noutput.DP-2.offset = -65536\n")
	if len(w) != 1 || c.Outputs[0].Anchor.Offset != -65536 {
		t.Fatalf("%v %+v", w, c.Outputs)
	}
}

func TestOutputModeLineKeepsAnchor(t *testing.T) {
	c, w := parseString(t, "output.DP-2.right-of = DP-1\noutput.DP-2.offset = 50\noutput.DP-2 = 1920x1080\n")
	if len(w) != 0 {
		t.Fatal(w)
	}
	o := c.Outputs[0]
	if o.Mode != "1920x1080" || o.Anchor != (domain.OutputAnchor{Relation: domain.RelationRightOf, To: "DP-1", Offset: 50}) {
		t.Fatalf("%+v", o)
	}
}

func TestFileSyntax(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		check    func(t *testing.T, c domain.Config)
		warnings []string
	}{
		{
			name: "invalid UTF-8 costs a warning, not the file",
			in:   "# caf\xe9\nborder.width = 5\n",
			check: func(t *testing.T, c domain.Config) {
				if c.Border.Width != 5 {
					t.Fatalf("border.width = %d", c.Border.Width)
				}
			},
			warnings: []string{"line 1: not valid UTF-8; invalid bytes replaced"},
		},
		{
			name: "output name with spaces",
			in:   "output.LG Electronics 27GR95UM.scale = 1.5\n",
			check: func(t *testing.T, c domain.Config) {
				if len(c.Outputs) != 1 || c.Outputs[0].Name != "LG Electronics 27GR95UM" || c.Outputs[0].Scale != 1.5 {
					t.Fatalf("%+v", c.Outputs)
				}
			},
		},
		{
			// A '#' inside a quoted word is kept, as a shell would.
			name: "comment after a quoted word",
			in:   "startup = notify-send \"a # b\" # note\n",
			check: func(t *testing.T, c domain.Config) {
				if !reflect.DeepEqual(c.Startup, [][]string{{"notify-send", `"a`, "#", `b"`}}) {
					t.Fatalf("startup: %q", c.Startup)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := parseString(t, tc.in)
			got := make([]string, 0, len(w))
			for _, x := range w {
				got = append(got, x.String())
			}
			if len(got) != len(tc.warnings) || (len(got) > 0 && !reflect.DeepEqual(got, tc.warnings)) {
				t.Fatalf("warnings %q, want %q", got, tc.warnings)
			}
			tc.check(t, c)
		})
	}
}

// A FIFO in place of the config fails at once instead of blocking startup.
//
//testpolicy:realtime opening a real filesystem FIFO must return without blocking
func TestLoadRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := Load(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO loaded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load blocked on a FIFO")
	}
}

func TestHintBind(t *testing.T) {
	c, w := parseString(t, "bind.cmd+g = hint\n")
	if len(w) != 0 || c.Binds["Cmd+g"] != "hint" {
		t.Fatal(c.Binds["Cmd+g"], w)
	}
}

func TestHintRestoredKeys(t *testing.T) {
	for _, line := range []string{"hint.targets = links", "hint.text-click = none"} {
		_, w := parseString(t, line+"\n")
		if len(w) != 0 {
			t.Errorf("%q: %v", line, w)
		}
	}
}

func TestHintDetectorConfig(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_DATA_HOME", "")
	d := Defaults().Hint
	if d.Detector != "nefer-hints" || d.Model != "/home/u/.local/share/nefer-hints/models" || d.Threshold != 0 {
		t.Fatalf("defaults: %+v", d)
	}
	t.Setenv("XDG_DATA_HOME", "/data")
	if m := Defaults().Hint.Model; m != "/data/nefer-hints/models" {
		t.Fatal(m)
	}
	c, w := parseString(t, "hint.detector = ~/bin/nh\nhint.model = ~/m\nhint.threshold = 0.3\n")
	if len(w) != 0 || c.Hint.Detector != "/home/u/bin/nh" || c.Hint.Model != "/home/u/m" || c.Hint.Threshold != 0.3 {
		t.Fatalf("%+v %v", c.Hint, w)
	}
	c, w = parseString(t, "hint.detector = /opt/nh\nhint.model = /opt/models\n")
	if len(w) != 0 || c.Hint.Detector != "/opt/nh" || c.Hint.Model != "/opt/models" {
		t.Fatalf("%+v %v", c.Hint, w)
	}
	for _, input := range []string{"", "hint.threshold =\n"} {
		c, w := parseString(t, input)
		if len(w) != 0 || c.Hint.Threshold != 0 {
			t.Errorf("unset threshold %q: %v %v", input, c.Hint.Threshold, w)
		}
	}
	c, w = parseString(t, "hint.threshold = 0.3\nhint.threshold =\n")
	if len(w) != 1 || c.Hint.Threshold != 0 { // duplicate-key warning only
		t.Fatalf("cleared threshold: %v %v", c.Hint.Threshold, w)
	}
	for _, v := range []string{"0", "0.009", "1", "0.995", "NaN", "Inf", "-Inf", "high", "-1"} {
		c, w := parseString(t, "hint.threshold = "+v+"\n")
		if len(w) != 1 || c.Hint.Threshold != 0 {
			t.Errorf("hint.threshold = %s: %v %v", v, c.Hint.Threshold, w)
		}
	}
	for _, v := range []string{"0.01", "0.99"} {
		if c, w := parseString(t, "hint.threshold = "+v+"\n"); len(w) != 0 || c.Hint.Threshold == 0 {
			t.Errorf("hint.threshold = %s: %v", v, w)
		}
	}
}

func TestHintStyleConfig(t *testing.T) {
	c, warnings := parseString(t, "hint.size = 17.5\nhint.background = #89b4fa\nhint.foreground = #111111\n")
	if len(warnings) != 0 || c.Hint.Size != 17.5 || c.Hint.Background != "#89b4fa" || c.Hint.Foreground != "#111111" {
		t.Fatalf("style: %+v, warnings: %v", c.Hint, warnings)
	}
	c, warnings = parseString(t, "hint.size = auto\nhint.background =\nhint.foreground =\n")
	if len(warnings) != 0 || c.Hint.Size != 0 || c.Hint.Background != "" || c.Hint.Foreground != "" {
		t.Fatalf("automatic: %+v %v", c.Hint, warnings)
	}
	for _, line := range []string{"hint.size = 0", "hint.size = -1", "hint.size = NaN", "hint.size = Inf", "hint.size = huge", "hint.size = 5.9", "hint.size = 72.1", "hint.size = 1e9", "hint.background = red", "hint.foreground = #fff"} {
		_, warnings := parseString(t, line+"\n")
		if len(warnings) == 0 {
			t.Errorf("expected validate-config warning for %s", line)
		}
	}
}

func TestHintCapture(t *testing.T) {
	if Defaults().Hint.Capture {
		t.Fatal("hint.capture defaults to off")
	}
	c, w := parseString(t, "hint.capture = on\n")
	if !c.Hint.Capture || len(w) != 0 {
		t.Fatal(c.Hint, w)
	}
	c, w = parseString(t, "hint.capture = off\n")
	if c.Hint.Capture || len(w) != 0 {
		t.Fatal(c.Hint, w)
	}
	c, w = parseString(t, "hint.capture = maybe\n")
	if c.Hint.Capture || len(w) != 1 {
		t.Fatal(c.Hint, w)
	}
}

func TestHintSizeBounds(t *testing.T) {
	for _, v := range []string{"6", "72", "13"} {
		c, w := parseString(t, "hint.size = "+v+"\n")
		if len(w) != 0 || c.Hint.Size == 0 {
			t.Errorf("hint.size = %s: %+v %v", v, c.Hint, w)
		}
	}
}

func TestHintEnable(t *testing.T) {
	if Defaults().Hint.Enable {
		t.Fatal("hint mode must be off by default")
	}
	c, w := parseString(t, "hint.enable = on\n")
	if !c.Hint.Enable || len(w) != 0 {
		t.Fatal(c.Hint, w)
	}
	c, w = parseString(t, "hint.enable = off\n")
	if c.Hint.Enable || len(w) != 0 {
		t.Fatal(c.Hint, w)
	}
	c, w = parseString(t, "hint.enable = maybe\n")
	if c.Hint.Enable || len(w) != 1 {
		t.Fatal(c.Hint, w)
	}
}

func TestPositionHint(t *testing.T) {
	if !Defaults().Focus.PositionHint {
		t.Fatal("position hint default off")
	}
	for _, value := range []string{"on", "off", "invalid"} {
		c, warnings := parseString(t, "focus.position-hint = "+value+"\n")
		if value == "invalid" {
			if len(warnings) == 0 {
				t.Fatal("accepted invalid value")
			}
			continue
		}
		if len(warnings) != 0 || c.Focus.PositionHint != (value == "on") {
			t.Fatalf("%s: %+v %v", value, c.Focus, warnings)
		}
	}
}

func TestOSDEnable(t *testing.T) {
	if !Defaults().OSD.Enable {
		t.Fatal("osd default off")
	}
	for _, value := range []string{"on", "off", "invalid"} {
		c, warnings := parseString(t, "osd.enable = "+value+"\n")
		if value == "invalid" {
			if len(warnings) == 0 {
				t.Fatal("accepted invalid value")
			}
			continue
		}
		if len(warnings) != 0 || c.OSD.Enable != (value == "on") {
			t.Fatalf("%s: %+v %v", value, c.OSD, warnings)
		}
	}
	c, warnings := parseString(t, "bind.cmd+shift+i = osd-pin\n")
	if len(warnings) != 0 || len(c.Binds) == 0 {
		t.Fatalf("osd-pin bind: %v %v", c.Binds, warnings)
	}
	found := false
	for _, a := range c.Binds {
		found = found || a == "osd-pin"
	}
	if !found {
		t.Fatalf("osd-pin not bound: %v", c.Binds)
	}
}

func TestBorderRadius(t *testing.T) {
	if Defaults().Border.Radius != 0 {
		t.Fatal("nonzero default")
	}
	for _, v := range []string{"0", "10", "64"} {
		c, w := parseString(t, "border.radius = "+v+"\n")
		if len(w) != 0 || fmt.Sprint(c.Border.Radius) != v {
			t.Fatalf("radius %s: %v %v", v, c.Border.Radius, w)
		}
	}
	for _, v := range []string{"-1", "bad", "65"} {
		_, w := parseString(t, "border.radius = "+v+"\n")
		if len(w) == 0 {
			t.Fatalf("accepted %s", v)
		}
	}
}

func TestHintSources(t *testing.T) {
	defaults := Defaults()
	if !reflect.DeepEqual(defaults.Hint.Sources, []domain.HintSource{domain.HintSourceATSPI, domain.HintSourceOCR}) || defaults.Hint.LinksOnly || defaults.Hint.TextClick != domain.ModCtrl|domain.ModShift {
		t.Fatal(defaults.Hint)
	}
	c0, w0 := parseString(t, "hint.targets = clickable\nhint.text-click = none\n")
	if len(w0) != 0 || c0.Hint.LinksOnly || c0.Hint.TextClick != 0 {
		t.Fatal(c0.Hint, w0)
	}

	for _, tc := range []struct {
		value string
		want  []domain.HintSource
		bad   bool
	}{
		{"", []domain.HintSource{domain.HintSourceATSPI, domain.HintSourceOCR}, false},
		{" Detector, ATSPI, ocr ", []domain.HintSource{domain.HintSourceDetector, domain.HintSourceATSPI, domain.HintSourceOCR}, false},
		{"ocr,ocr", nil, true}, {"other", nil, true}, {",", nil, true}, {"ocr,", nil, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c, w := parseString(t, "hint.sources = "+tc.value+"\n")
			if tc.bad {
				if len(w) == 0 {
					t.Fatal("accepted invalid sources")
				}
			} else if len(w) != 0 || !reflect.DeepEqual(c.Hint.Sources, tc.want) {
				t.Fatal(c.Hint.Sources, w)
			}
		})
	}
	c, w := parseString(t, "hint.targets = links\nhint.text-click = ctrl+shift\n")
	if len(w) != 0 || !c.Hint.LinksOnly || c.Hint.TextClick != domain.ModCtrl|domain.ModShift {
		t.Fatal(c.Hint, w)
	}
	for _, line := range []string{"hint.targets = bad", "hint.text-click = ctrl+ctrl", "hint.text-click = ctrl+bad"} {
		_, w := parseString(t, line+"\n")
		if len(w) == 0 {
			t.Fatal(line)
		}
	}
}
