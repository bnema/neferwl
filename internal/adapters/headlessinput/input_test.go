package headlessinput

import (
	"context"
	"github.com/bnema/nefertty/internal/adapters/xkb"
	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
	"strings"
	"testing"
)

func TestScript(t *testing.T) {
	for _, tt := range []struct {
		line  string
		names []string
		codes []uint32
	}{
		{"type Hi!", []string{"Shift_L", "H", "H", "Shift_L", "i", "i", "Shift_L", "exclam", "exclam", "Shift_L"}, nil},
		{"key Shift+A", []string{"Shift_L", "A", "A", "Shift_L"}, []uint32{42, 30, 30, 42}},
		{"key Super+Return", []string{"Super_L", "Return", "Return", "Super_L"}, []uint32{125, 28, 28, 125}},
	} {
		t.Run(tt.line, func(t *testing.T) {
			km, err := xkb.New(xkb.RMLVO{Layout: "us"})
			if err != nil {
				if strings.Contains(err.Error(), "libxkbcommon.so.0") {
					t.Skip(err)
				}
				t.Fatal(err)
			}
			script := make(chan string, 1)
			script <- tt.line
			close(script)
			input := make(chan ports.InputEvent, 32)
			if err := Run(context.Background(), km, nil, script, input, nil, logging.For(context.Background(), "input")); err != nil {
				t.Fatal(err)
			}
			if len(input) != len(tt.names) {
				t.Fatalf("events=%d want %d", len(input), len(tt.names))
			}
			for i, name := range tt.names {
				ev := (<-input).(ports.KeyEvent)
				if ev.Keysym != name {
					t.Errorf("event %d: %s want %s", i, ev.Keysym, name)
				}
				if tt.codes != nil && ev.Keycode != tt.codes[i] {
					t.Errorf("code %d: %d", i, ev.Keycode)
				}
				if tt.line == "key Super+Return" && i == 1 && ev.Mods&ports.ModSuper == 0 {
					t.Error("Return missing Super")
				}
				if tt.line == "key Super+Return" && i == 3 && ev.Mods&ports.ModSuper != 0 {
					t.Error("Super not released")
				}
			}
		})
	}
}

func TestPointerScript(t *testing.T) {
	km, err := xkb.New(xkb.RMLVO{Layout: "us"})
	if err != nil {
		t.Skip(err)
	}
	script := make(chan string, 4)
	for _, line := range []string{"move 12.5 45", "click", "down right", "up right"} {
		script <- line
	}
	close(script)
	input := make(chan ports.InputEvent, 8)
	if err := Run(context.Background(), km, nil, script, input, nil, logging.For(context.Background(), "input")); err != nil {
		t.Fatal(err)
	}
	if len(input) != 5 {
		t.Fatalf("events: %d", len(input))
	}
	m := (<-input).(ports.PointerMotion)
	if m.X != 12.5 || m.Y != 45 {
		t.Fatal(m)
	}
	for _, want := range []ports.PointerButton{{Button: 0x110, Pressed: true}, {Button: 0x110}, {Button: 0x111, Pressed: true}, {Button: 0x111}} {
		got := (<-input).(ports.PointerButton)
		if got.Button != want.Button || got.Pressed != want.Pressed {
			t.Fatalf("got %+v want %+v", got, want)
		}
	}
}
