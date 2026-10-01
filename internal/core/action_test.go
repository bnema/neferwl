package core_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// altCmdDefaults is the default config with keyboard.cmd = alt. The loader
// drops binds that use both cmd and alt then; so does this helper.
func altCmdDefaults() ports.Config {
	cfg := config.Defaults()
	cfg.Keyboard.CmdKey = "alt"
	for combo := range cfg.Binds {
		parts := strings.Split(combo, "+")
		mods := parts[:len(parts)-1]
		if slices.Contains(mods, "Alt") && slices.Contains(mods, "Cmd") {
			delete(cfg.Binds, combo)
		}
	}
	return cfg
}

func TestDefaultBindsAccepted(t *testing.T) {
	for _, cmd := range []string{"super", "alt"} {
		cfg := config.Defaults()
		if cmd == "alt" {
			cfg = altCmdDefaults()
		}
		if _, err := core.New(cfg, core.Channels{Scenes: make(chan []ports.Scene, 1)}); err != nil {
			t.Fatalf("cmd=%s: %v", cmd, err)
		}
	}
}

func TestResizeArg(t *testing.T) {
	for _, tc := range []struct {
		a         core.Action
		axis, pct int
		ok        bool
	}{
		{"set-column-width +10%", core.ResizeWidth, 10, true},
		{"set-column-width -5%", core.ResizeWidth, -5, true},
		{"set-window-height +100%", core.ResizeHeight, 100, true},
		{"set-window-height -1%", core.ResizeHeight, -1, true},
		{"set-column-width 10%", 0, 0, false},
		{"set-column-width +0%", 0, 0, false},
		{"set-column-width +101%", 0, 0, false},
		{"set-column-width +10", 0, 0, false},
		{"set-column-width +-1%", 0, 0, false},
		{"set-column-width ++1%", 0, 0, false},
		{"set-window-height", 0, 0, false},
		{"cycle-column-width", 0, 0, false},
	} {
		axis, pct, ok := core.ResizeArg(tc.a)
		if axis != tc.axis || pct != tc.pct || ok != tc.ok {
			t.Errorf("%q: %d %d %v", tc.a, axis, pct, ok)
		}
	}
}
