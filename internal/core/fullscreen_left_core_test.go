package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Through core, scroll overflow: an app whose fullscreen the user left by
// activating another window cannot cover it again by asking at once; once
// activated again, it may ask and is honoured.
func TestCoreLeftFullscreenLatch(t *testing.T) {
	client := make(chan ports.ClientEvent)
	output := make(chan ports.OutputEvent)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{Client: client, Output: output, Commands: commands, Scenes: scenes}, Options{Clock: laterClock(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	go func() {
		for range commands {
		}
	}()
	full := func() ports.WindowID {
		t.Helper()
		select {
		case s := <-scenes:
			for _, w := range s[0].Windows {
				if w.Fullscreen && !w.Hidden {
					return w.ID
				}
			}
			return 0
		case <-time.After(time.Second):
			t.Fatal("scene timeout")
			return 0
		}
	}
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	full()
	client <- ports.WindowMapped{ID: 1}
	full()
	client <- ports.WindowMapped{ID: 2}
	full()
	client <- ports.WindowFullscreenRequest{ID: 2, Fullscreen: true}
	if id := full(); id != 2 {
		t.Fatalf("fullscreen %d, want 2", id)
	}
	client <- ports.WindowActivate{ID: 1}
	if id := full(); id != 0 {
		t.Fatalf("after activation: fullscreen %d", id)
	}
	client <- ports.WindowFullscreenRequest{ID: 2, Fullscreen: true}
	if id := full(); id != 0 {
		t.Fatalf("re-request covered: fullscreen %d", id)
	}
	client <- ports.WindowActivate{ID: 2}
	full()
	client <- ports.WindowActivate{ID: 1}
	full()
	// The latch settled when 2 had the focus: scroll overflow honours an
	// unfocused window's request again.
	client <- ports.WindowFullscreenRequest{ID: 2, Fullscreen: true}
	if id := full(); id != 2 {
		t.Fatalf("after refocus: fullscreen %d, want 2", id)
	}
	cancel()
	<-done
	close(commands)
}
