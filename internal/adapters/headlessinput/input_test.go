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
			if err := Run(context.Background(), km, script, input, logging.For(context.Background(), "input")); err != nil {
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
				if tt.codes != nil && i == 1 && ev.Mods&ports.ModSuper == 0 {
					t.Error("Return missing Super")
				}
				if tt.codes != nil && i == 3 && ev.Mods&ports.ModSuper != 0 {
					t.Error("Super not released")
				}
			}
		})
	}
}
