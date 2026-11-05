package core

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestControlWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		control := make(chan ports.ControlRequest)
		client := make(chan ports.ClientEvent)
		output := make(chan ports.OutputEvent)
		frames := make(chan domain.OutputFrame)
		commands := make(chan ports.ClientCommand, 128)
		c.ch.Control, c.ch.Client, c.ch.Output, c.ch.Frames, c.ch.Commands = control, client, output, frames, commands
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 80}}
		synctest.Wait()
		reply := make(chan ports.ControlReply, 1)
		noReply := func() {
			t.Helper()
			synctest.Wait()
			select {
			case r := <-reply:
				t.Fatalf("early reply: %+v", r)
			default:
			}
		}
		receive := func() ports.ControlReply {
			t.Helper()
			synctest.Wait()
			select {
			case r := <-reply:
				return r
			default:
				t.Fatal("missing reply")
				return ports.ControlReply{}
			}
		}
		action := func(name string, level domain.WaitLevel, until *domain.Until, timeout time.Duration) {
			control <- ports.ControlDo{Action: name, Wait: level, Until: until, Timeout: timeout, Caps: domain.CapAct, Reply: reply}
		}
		latestConfigure := func(id domain.WindowID) ports.ConfigureWindow {
			t.Helper()
			synctest.Wait()
			var last ports.ConfigureWindow
			for len(commands) > 0 {
				if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == id {
					last = v
				}
			}
			if last.Serial == 0 {
				t.Fatal("no configure")
			}
			return last
		}
		client <- ports.WindowMapped{ID: 1, AppID: "foot"}
		configured := latestConfigure(1)
		action("none", domain.WaitSettled, nil, time.Second)
		noReply()
		client <- ports.WindowCommitted{ID: 1, Serial: configured.Serial}
		if r := receive(); r.Done != domain.WaitSettled || r.TimedOut {
			t.Fatalf("settled: %+v", r)
		}

		action("focus-workspace-next", domain.WaitPresented, nil, time.Second)
		noReply()
		seq := c.screens[0].last.Seq
		frames <- domain.OutputFrame{Output: "A", SceneSeq: seq - 1}
		noReply()
		frames <- domain.OutputFrame{Output: "A", SceneSeq: seq}
		if r := receive(); r.Done != domain.WaitPresented || len(r.Diff.Workspaces) != 1 {
			t.Fatalf("presented: %+v", r)
		}

		action("none", domain.WaitCommitted, &domain.Until{Kind: domain.UntilWindowMapped, AppID: "foot"}, time.Second)
		noReply()
		client <- ports.WindowMapped{ID: 2, AppID: "foot"}
		if r := receive(); len(r.Diff.Mapped) != 1 || r.Diff.Mapped[0] != 2 {
			t.Fatalf("until: %+v", r)
		}
		configured = latestConfigure(2)

		action("none", domain.WaitCommitted, nil, time.Second)
		if r := receive(); r.Done != domain.WaitCommitted {
			t.Fatalf("committed: %+v", r)
		}
		action("none", domain.WaitSettled, &domain.Until{Kind: domain.UntilWindowMapped, AppID: "missing"}, time.Second)
		noReply()
		client <- ports.WindowMapped{ID: 3, AppID: "other"}
		noReply()
		time.Sleep(time.Second)
		r := receive()
		if !r.TimedOut || r.Err != "timeout" || r.Done != domain.WaitCommitted || len(r.Diff.Mapped) != 1 || r.Diff.Mapped[0] != 3 || len(r.Late) != 2 {
			t.Fatalf("timeout: %+v", r)
		}
		client <- ports.WindowCommitted{ID: 2, Serial: configured.Serial}
		configured = latestConfigure(3)
		client <- ports.WindowCommitted{ID: 3, Serial: configured.Serial}
		synctest.Wait()

		// Remove clients first so disappearance tests only presentation, not
		// outstanding configure commits from migrated windows.
		for _, id := range []domain.WindowID{1, 2, 3} {
			client <- ports.WindowUnmapped{ID: id}
		}
		synctest.Wait()
		// Removing a target output satisfies its presentation boundary.
		client <- ports.WindowMapped{ID: 4}
		configured = latestConfigure(4)
		client <- ports.WindowCommitted{ID: 4, Serial: configured.Serial}
		synctest.Wait()
		action("focus-workspace-next", domain.WaitPresented, nil, time.Second)
		noReply()
		output <- ports.OutputRemoved{Name: "A"}
		if r := receive(); r.Done != domain.WaitPresented || r.TimedOut {
			t.Fatalf("removed: %+v", r)
		}
	})
}

func TestControlWaitEarliestDeadlineAndReachedLevel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		replies := make(chan ports.ControlReply, 3)
		predicate := &domain.Until{Kind: domain.UntilWindowMapped, AppID: "missing"}
		// Queue in FIFO order with deadlines deliberately out of order.
		for _, timeout := range []time.Duration{3 * time.Second, time.Second, time.Second} {
			c.waitControl(c.state(), c.sceneSeqs(), domain.WaitSettled, predicate, timeout, replies, nil)
		}
		c.evalWaits()
		time.Sleep(time.Second)
		<-c.controlWaitC
		c.evalWaits()
		if len(replies) != 2 || len(c.controlWaits) != 1 {
			t.Fatalf("replies %d, waits %d", len(replies), len(c.controlWaits))
		}
		for range 2 {
			r := <-replies
			if !r.TimedOut || r.Done != domain.WaitPresented {
				t.Fatalf("highest reached: %+v", r)
			}
		}
		time.Sleep(2 * time.Second)
		<-c.controlWaitC
		c.evalWaits()
		if len(replies) != 1 || c.controlWaitC != nil {
			t.Fatal("earliest timer did not rearm/stop")
		}
		// Simultaneously satisfied waiters emit their per-request diffs in FIFO order.
		first := c.state()
		second := c.state()
		first.Windows = []domain.WindowState{{ID: 11}}
		second.Windows = []domain.WindowState{{ID: 22}}
		c.waitControl(first, c.sceneSeqs(), domain.WaitSettled, nil, time.Second, replies, nil)
		c.waitControl(second, c.sceneSeqs(), domain.WaitSettled, nil, time.Second, replies, nil)
		c.evalWaits()
		<-replies // previous timeout
		if r := <-replies; len(r.Diff.Unmapped) != 1 || r.Diff.Unmapped[0] != 11 {
			t.Fatalf("FIFO first: %+v", r)
		}
		if r := <-replies; len(r.Diff.Unmapped) != 1 || r.Diff.Unmapped[0] != 22 {
			t.Fatalf("FIFO second: %+v", r)
		}
	})
}

func TestControlWaitIdleAllocations(t *testing.T) {
	c := &Core{}
	if n := testing.AllocsPerRun(100, c.evalWaits); n != 0 {
		t.Fatalf("idle evalWaits: %v allocations", n)
	}
}
