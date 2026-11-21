package core_test

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
)

// A config with a bind mode parses without warnings and core applies it.
func TestCoreAppliesConfigModes(t *testing.T) {
	cfg, warnings, err := config.Parse(strings.NewReader("bind.cmd+r = mode resize\nmode.resize.bind.h = set-column-width -1; focus-column-left\nmode.resize.bind.escape = mode default\n"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("parse: warnings %v, err %v", warnings, err)
	}
	if _, err := core.New(cfg, core.Channels{Scenes: make(chan []domain.Scene, 1)}, testOptions(t, core.Options{})); err != nil {
		t.Fatalf("apply: %v", err)
	}
}
