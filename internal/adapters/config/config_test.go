package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/nefertty/internal/ports"
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
	d := Defaults()
	if d.Keyboard.RepeatRate != 25 || d.Keyboard.CmdKey != "super" || !d.Render.DirectScanout || len(d.Binds) != 34 || d.Layout.MaxColumns != 2 {
		t.Fatalf("defaults: %+v", d)
	}
	if d.Binds["Cmd+Ctrl+space"] != "spawn fuzzel" || d.Binds["Alt+Ctrl+BackSpace"] != "quit" {
		t.Fatal(d.Binds)
	}
	if _, _, err := Load(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("explicit missing: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if DefaultPath() != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nefertty", "config") {
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
output.DP-2 = 5120x2160@165.058
output.HDMI-A-1 = off
output.DP-1 = preferred
render.direct-scanout = off
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	if c.Keyboard.Layout != "fr" || c.Keyboard.RepeatRate != 40 || !reflect.DeepEqual(c.Terminal.Command, []string{"foot", "--server"}) || c.Background.Color != "#000000" || c.Render.DirectScanout {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(c.Layout.Presets, []string{"1/3", "1/2", "1"}) || !reflect.DeepEqual(c.Log.Debug, []string{"core", "input"}) {
		t.Fatalf("%+v", c)
	}
	if len(c.Outputs) != 3 || c.Outputs[0].Mode != "5120x2160@165.058" || !c.Outputs[1].Off || c.Outputs[2].Mode != "" || c.Outputs[2].Off {
		t.Fatalf("%+v", c.Outputs)
	}
}

func TestWarningsKeepDefaults(t *testing.T) {
	cases := []struct{ line, want string }{
		{"keyboard.repeat-rate = 0", "keyboard.repeat-rate"},
		{"keyboard.cmd = meta", "keyboard.cmd"},
		{"terminal =", "terminal"},
		{"background = red", "background"},
		{"layout.gaps = 201", "layout.gaps"},
		{"layout.max-columns = 0", "layout.max-columns"},
		{"bind.cmd+x = focus-workspace 0", "focus-workspace"},
		{"layout.presets = 0px", "layout.presets"},
		{"log.level = trace", "log.level"},
		{"log.debug = nope", "log.debug"},
		{"output.DP-2 = big", "output.DP-2"},
		{"output.DP-2 = 1920x1080@0", "output.DP-2"},
		{"nope = 1", "nope"},
		{"just text", "expected key = value"},
		{"bind.cmd+ = quit", "missing key"},
		{"bind.hyper+x = quit", "unknown modifier"},
		{"bind.cmd+x = bogus", "unknown action"},
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
