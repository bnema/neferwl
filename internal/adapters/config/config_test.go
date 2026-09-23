package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultsAndLoad(t *testing.T) {
	d := Defaults()
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	if d.Keyboard.RepeatRate != 25 || d.Keyboard.RepeatDelay != 600 || d.Keyboard.CmdKey != "super" || d.Render.DirectScanout != true || len(d.Binds) != 11 {
		t.Fatalf("defaults: %+v", d)
	}
	path := filepath.Join(t.TempDir(), "missing")
	_, err := Load(path)
	if !os.IsNotExist(err) {
		t.Fatalf("explicit missing: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := LoadDefault()
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("missing: %v %+v", err, got)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if DefaultPath() != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nefertty", "config.toml") {
		t.Fatal(DefaultPath())
	}
	example, err := Load("../../../examples/config.toml")
	if err != nil || !reflect.DeepEqual(example, d) {
		t.Fatalf("example: %v %+v", err, example)
	}
}
func TestValidation(t *testing.T) {
	cases := []struct{ name, content, want string }{
		{"rate", "[keyboard]\nrepeat_rate=0", "keyboard.repeat_rate"},
		{"delay", "[keyboard]\nrepeat_delay=0", "keyboard.repeat_delay"},
		{"cmd", "[keyboard]\ncmd_key='meta'", "keyboard.cmd_key"},
		{"terminal", "[terminal]\ncommand=[]", "terminal.command"},
		{"color", "[background]\ncolor='red'", "background.color"},
		{"gaps", "[layout]\ngaps=201", "layout.gaps"},
		{"width", "[layout]\ndefault_column_width='2/1'", "layout.default_column_width"},
		{"preset", "[layout]\npresets=['0px']", "layout.presets[0]"},
		{"level", "[log]\nlevel='trace'", "log.level"},
		{"debug", "[log]\ndebug=['nope']", "log.debug[0]"},
		{"combo", "[binds]\n'Cmd+'='quit'", "binds.Cmd+"},
		{"action", "[binds]\n'Cmd+X'='bogus'", "binds.Cmd+X"},
		{"resolved duplicate", "[keyboard]\ncmd_key='ctrl'\n[binds]\n'Cmd+X'='quit'\n'Ctrl+X'='quit'", "duplicates"},
		{"duplicate modifier", "[keyboard]\ncmd_key='ctrl'\n[binds]\n'Cmd+Ctrl+X'='quit'", "duplicate resolved modifier"},
		{"duplicate", "[binds]\n'Cmd+Shift+X'='quit'\n'Shift+Cmd+X'='quit'", "duplicates"},
		{"unknown", "[layout]\nnope=1", "layout.nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}
func TestBindMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[binds]\n'Cmd+Return'='none'\n'Shift+Cmd+Left'='quit'\n'Alt+X'='spawn-terminal'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Binds["Cmd+Return"]; ok {
		t.Fatal(c.Binds)
	}
	if _, ok := c.Binds["Cmd+Shift+Left"]; ok {
		t.Fatal(c.Binds)
	}
	if c.Binds["Shift+Cmd+Left"] != "quit" || c.Binds["Alt+X"] != "spawn-terminal" {
		t.Fatal(c.Binds)
	}
}
