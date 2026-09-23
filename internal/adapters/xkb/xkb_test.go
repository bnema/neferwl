package xkb

import (
	"strings"
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

func newTest(t *testing.T, layout string) *Keymap {
	t.Helper()
	k, err := New(RMLVO{Layout: layout})
	if err != nil {
		if strings.Contains(err.Error(), "libxkbcommon.so.0") {
			t.Skipf("xkbcommon unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(k.Close)
	return k
}

func TestKeymap(t *testing.T) {
	k := newTest(t, "us")
	if !strings.Contains(k.String(), "xkb_keymap") {
		t.Fatal("missing keymap text")
	}
	if ev := k.Key(30, true, 123); ev.Keysym != "a" || ev.Keycode != 30 || !ev.Pressed || ev.TimeMsec != 123 {
		t.Fatalf("A: %+v", ev)
	}
	k.Key(30, false, 0)
	k.Key(42, true, 0)
	if ev := k.Key(30, true, 0); ev.Keysym != "A" || ev.Mods&ports.ModShift == 0 || ev.State.Depressed == 0 {
		t.Fatalf("shift A: %+v", ev)
	}
	k.Key(42, false, 0)
	if ev := k.Key(30, false, 0); ev.Mods&ports.ModShift != 0 {
		t.Fatalf("shift release: %+v", ev)
	}
	for _, tt := range []struct {
		name      string
		code      uint32
		shift, ok bool
	}{{"a", 30, false, true}, {"A", 30, true, true}, {"Return", 28, false, true}, {"nope", 0, false, false}} {
		code, shift, ok := k.KeycodeFor(tt.name)
		if code != tt.code || shift != tt.shift || ok != tt.ok {
			t.Errorf("%s: %d %v %v", tt.name, code, shift, ok)
		}
	}
	fr := newTest(t, "fr")
	if code, _, ok := fr.KeycodeFor("a"); !ok || code != 16 {
		t.Errorf("fr a: %d %v", code, ok)
	}
}
