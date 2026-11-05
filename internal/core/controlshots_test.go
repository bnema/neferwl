package core

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestControlShots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		control := make(chan ports.ControlRequest)
		output := make(chan ports.OutputEvent)
		frames := make(chan domain.OutputFrame)
		c.ch.Control, c.ch.Output, c.ch.Frames = control, output, frames
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
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "B", Width: 100, Height: 80}}
		synctest.Wait()
		reply := make(chan ports.ControlReply, 1)
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
		noReply := func() {
			t.Helper()
			synctest.Wait()
			select {
			case r := <-reply:
				t.Fatalf("early reply: %+v", r)
			default:
			}
		}
		seqs := c.sceneSeqs()
		// Empty output expands to both outputs; unchanged scenes still need presentation.
		control <- ports.ControlDo{Action: "none", Wait: domain.WaitCommitted, Shots: []domain.ShotRequest{{Raw: true, MinSeq: 999}}, Caps: domain.CapAct | domain.CapCapture, Reply: reply}
		noReply()
		frames <- domain.OutputFrame{Output: "A", SceneSeq: seqs["A"]}
		noReply()
		frames <- domain.OutputFrame{Output: "B", SceneSeq: seqs["B"]}
		r := receive()
		if r.Done != domain.WaitPresented || len(r.Shots) != 2 {
			t.Fatalf("shots: %+v", r)
		}
		for _, shot := range r.Shots {
			if shot.MinSeq != seqs[shot.Output] || !shot.Raw {
				t.Fatalf("sequence/raw: %+v", shot)
			}
		}
		for _, tc := range []struct {
			caps         domain.Caps
			output, code string
		}{
			{domain.CapAct, "A", ports.ControlErrForbidden},
			{domain.CapAct | domain.CapCapture, "missing", ports.ControlErrBadRequest},
		} {
			control <- ports.ControlDo{Action: "quit", Shots: []domain.ShotRequest{{Output: tc.output}}, Caps: tc.caps, Reply: reply}
			if r := receive(); r.Err != tc.code {
				t.Fatalf("rejection: %+v", r)
			}
		}
		// Injection uses the same shot completion path without capture I/O.
		control <- ports.ControlInject{Wait: domain.WaitCommitted, Shots: []domain.ShotRequest{{Output: "A"}}, Caps: domain.CapInput | domain.CapCapture, Reply: reply}
		if r := receive(); r.Done != domain.WaitPresented || len(r.Shots) != 1 || r.Shots[0].MinSeq != seqs["A"] {
			t.Fatalf("inject shots: %+v", r)
		}
		// Timeout includes only the output whose current scene has presented.
		c.presented["B"] = 0 // owner is quiescent inside the bubble.
		control <- ports.ControlDo{Action: "none", Shots: []domain.ShotRequest{{}}, Caps: domain.CapAct | domain.CapCapture, Timeout: time.Second, Reply: reply}
		noReply()
		time.Sleep(time.Second)
		r = receive()
		if r.Err != ports.ControlErrTimeout || len(r.Shots) != 1 || r.Shots[0].Output != "A" || r.Shots[0].MinSeq != seqs["A"] {
			t.Fatalf("partial: %+v", r)
		}
		// Reject the whole action up front; quit must never execute.
		c.security.Protected = true
		control <- ports.ControlDo{Action: "quit", Shots: []domain.ShotRequest{{Output: "A"}}, Caps: domain.CapAct | domain.CapCapture, Reply: reply}
		if r := receive(); r.Err != ports.ControlErrLocked {
			t.Fatalf("locked: %+v", r)
		}
	})
}
