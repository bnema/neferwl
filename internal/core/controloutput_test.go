package core

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
)

func TestControlOutputCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		control := make(chan ports.ControlRequest)
		runtime := make(chan ports.OutputRuntimeRequest, 1)
		output := make(chan ports.OutputEvent)
		c.ch.Control, c.ch.RuntimeOutputs, c.ch.Output = control, runtime, output
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() { cancel(); <-done }()
		reply := make(chan ports.ControlReply, 1)
		control <- ports.ControlOutput{Caps: domain.CapRead, Reply: reply}
		synctest.Wait()
		if r := <-reply; r.Err != ports.ControlErrForbidden {
			t.Fatal(r)
		}
		control <- ports.ControlOutput{Caps: domain.CapAdmin, Reply: reply}
		synctest.Wait()
		req := <-runtime
		req.Reply <- ports.OutputRuntimeResult{Name: "HEADLESS-1"}
		req.Wake <- struct{}{}
		synctest.Wait()
		if len(reply) != 0 {
			t.Fatal("replied before announcement")
		}
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "HEADLESS-1", Width: 100, Height: 80, Virtual: true}}
		synctest.Wait()
		if r := <-reply; len(r.State.Outputs) != 1 {
			t.Fatal(r)
		}
		control <- ports.ControlOutput{Name: "HEADLESS-1", Remove: true, Caps: domain.CapAdmin, Reply: reply}
		synctest.Wait()
		req = <-runtime
		output <- ports.OutputRemoved{Name: "HEADLESS-1"}
		req.Reply <- ports.OutputRuntimeResult{Name: "HEADLESS-1"}
		req.Wake <- struct{}{}
		synctest.Wait()
		if r := <-reply; len(r.State.Outputs) != 0 {
			t.Fatal(r)
		}
	})
}
