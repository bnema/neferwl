package core

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// An epoch adopted by a publish, outside the run loop's checkpoint, also
// returns to the default binds.
func TestSyncSecurityResetsMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var epoch atomic.Value
		epoch.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) }).Maybe()
		var cfg domain.Config
		cfg.Layout.MaxColumns = 2
		cfg.Keyboard.CmdKey = "super"
		cfg.Modes = map[string]map[string]string{"resize": {}}
		c, err := New(cfg, Channels{Commands: make(chan ports.ClientCommand, 64), Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{Security: gate}))
		if err != nil {
			t.Fatal(err)
		}
		c.addScreen(domain.OutputInfo{Name: "A", Width: 800, Height: 600})
		c.setMode("resize")
		if c.mode != "resize" {
			t.Fatalf("mode %q", c.mode)
		}
		epoch.Store(domain.SecurityState{Generation: 1})
		if err := c.publish(context.Background()); err != nil {
			t.Fatal(err)
		}
		if c.mode != "" {
			t.Fatalf("mode after epoch %q, want default", c.mode)
		}
	})
}
