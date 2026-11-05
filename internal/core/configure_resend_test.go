package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// A configure whose send fails is not recorded: the next publish sends it
// again, and its commit then clears the pending wait.
func TestFailedConfigureSendIsResent(t *testing.T) {
	var cfg domain.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c := newSizedCore(t, cfg)
	commands := make(chan ports.ClientCommand, 256)
	c.ch.Commands = commands
	c.addScreen(domain.OutputInfo{Name: "A", Width: 200, Height: 100})
	ctx := context.Background()
	c.clientEvent(ctx, ports.WindowMapped{ID: 1})
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	drain := func() (last ports.ConfigureWindow, ok bool) {
		if err := c.out.flush(ctx, commands, 0); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case cmd := <-commands:
				if sc, wrapped := cmd.(ports.SecurityCommand); wrapped {
					cmd = sc.Command
				}
				if v, is := cmd.(ports.ConfigureWindow); is && v.ID == 1 {
					last, ok = v, true
				}
			default:
				return
			}
		}
	}
	first, ok := drain()
	if !ok {
		t.Fatal("no first configure")
	}
	c.clientEvent(ctx, ports.WindowCommitted{ID: 1, Serial: first.Serial})
	// A second window narrows the first; its configure fails to send.
	c.clientEvent(ctx, ports.WindowMapped{ID: 2})
	// The epoch changes during the publish of an input event: the first
	// check passes, the configure's queue sees the new epoch.
	var calls atomic.Int32
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState {
		if calls.Add(1) > 1 {
			return domain.SecurityState{Generation: 1}
		}
		return domain.SecurityState{}
	})
	c.opts.Security = gate
	c.inputActive = true
	if err := c.publish(ctx); !errors.Is(err, errSecurityChanged) {
		t.Fatalf("publish: %v", err)
	}
	c.inputActive = false
	c.syncSecurity()
	if _, sent := drain(); sent {
		t.Fatal("configure delivered despite the failed send")
	}
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	again, ok := drain()
	if !ok || again.Width == first.Width {
		t.Fatalf("configure not resent: %+v (first %+v)", again, first)
	}
	c.clientEvent(ctx, ports.WindowCommitted{ID: 1, Serial: again.Serial})
	for _, id := range c.pendingConfigures() {
		if id == 1 {
			t.Fatal("window 1 still pending")
		}
	}
}
