package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestConfigureCommitPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, commands := fadeCore(t)
		c.mapWindow(ports.WindowMapped{ID: 2})
		indicatorScene(t, c)
		var sent ports.ConfigureWindow
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 2 {
				sent = v
			}
		}
		if sent.Serial == 0 || !slices.Equal(c.pendingConfigures(), []WindowID{2}) {
			t.Fatalf("configure %+v; pending %v", sent, c.pendingConfigures())
		}
		c.clientEvent(context.Background(), ports.WindowCommitted{ID: 2, Serial: sent.Serial - 1})
		if !slices.Equal(c.pendingConfigures(), []WindowID{2}) {
			t.Fatal("stale commit cleared pending")
		}
		c.clientEvent(context.Background(), ports.WindowCommitted{ID: 2, Serial: sent.Serial})
		if len(c.pendingConfigures()) != 0 {
			t.Fatal("commit did not clear pending")
		}
		c.configures.answer[2] = true
		indicatorScene(t, c)
		var next ports.ConfigureWindow
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 2 {
				next = v
			}
		}
		if next.Serial <= sent.Serial {
			t.Fatalf("serial did not increase: %d -> %d", sent.Serial, next.Serial)
		}
		c.clientEvent(context.Background(), ports.WindowCommitted{ID: 2, Serial: sent.Serial})
		if len(c.pendingConfigures()) != 1 {
			t.Fatal("previous configure commit cleared newer pending")
		}
		c.configures.forget(2)
		if len(c.pendingConfigures()) != 0 {
			t.Fatal("removed window kept pending")
		}
	})
}

func TestConfigurePendingVisibilityAndOrder(t *testing.T) {
	c := &Core{configures: newConfigures()}
	c.configures.mark(ports.ConfigureWindow{ID: 3, Visible: true})
	c.configures.mark(ports.ConfigureWindow{ID: 1, Captured: true})
	c.configures.mark(ports.ConfigureWindow{ID: 2})
	if !slices.Equal(c.pendingConfigures(), []WindowID{1, 3}) {
		t.Fatalf("pending %v", c.pendingConfigures())
	}
	c.configures.mark(ports.ConfigureWindow{ID: 3})
	if !slices.Equal(c.pendingConfigures(), []WindowID{1}) {
		t.Fatal("hidden uncaptured window kept pending")
	}
	c.clientEvent(context.Background(), ports.WindowCommitted{ID: 1, Serial: 100})
	if len(c.pendingConfigures()) != 0 {
		t.Fatal("newer commit did not clear pending")
	}
}
