package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestControlRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var epoch atomic.Value
		epoch.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) })
		control := make(chan ports.ControlRequest)
		output := make(chan ports.OutputEvent)
		client := make(chan ports.ClientEvent)
		binds := make(chan domain.InputTrace, 8)
		c, err := core.New(scrollDefaults(), core.Channels{
			Control: control, Output: output, Client: client,
			Commands: make(chan ports.ClientCommand, 128), Scenes: make(chan []domain.Scene, 1), Binds: binds,
		}, testOptions(t, core.Options{Security: gate}))
		if err != nil {
			t.Fatal(err)
		}
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
		client <- ports.WindowMapped{ID: 1, Title: "private-title"}
		synctest.Wait()
		reply := make(chan ports.ControlReply, 1)
		control <- ports.ControlQuery{What: "state", Caps: domain.CapRead, Reply: reply}
		before := receive(t, reply)
		if before.Err != "" || len(before.State.Windows) != 1 || before.State.Outputs[0].Active != 1 {
			t.Fatalf("query: %+v", before)
		}
		for i, wait := range []domain.WaitLevel{domain.WaitCommitted, domain.WaitCommitted, domain.WaitCommitted} {
			action := "focus-workspace-next"
			if i == 1 {
				action = "focus-workspace-prev"
			}
			control <- ports.ControlDo{Action: action, Caps: domain.CapAct, Wait: wait, Reply: reply}
			got := receive(t, reply)
			if got.Err != "" || got.Done != domain.WaitCommitted || len(got.Diff.Workspaces) != 1 || got.State.Outputs[0].Active == before.State.Outputs[0].Active {
				t.Fatalf("action: %+v", got)
			}
			trace, ok := receive(t, binds).(domain.BindDispatched)
			if !ok || trace.Source != domain.BindSourceControl {
				t.Fatalf("trace: %+v", trace)
			}
			before = got
		}
		tests := []struct {
			request ports.ControlRequest
			want    string
		}{
			{ports.ControlDo{Action: "missing", Caps: domain.CapAct, Reply: reply}, ports.ControlErrUnknownCommand},
			{ports.ControlDo{Action: "state", Caps: domain.CapAct, Reply: reply}, ports.ControlErrUnknownCommand},
			{ports.ControlDo{Action: "focus-workspace-next", Reply: reply}, ports.ControlErrForbidden},
			{ports.ControlQuery{What: "state", Reply: reply}, ports.ControlErrForbidden},
			{ports.ControlQuery{What: "missing", Caps: domain.CapRead, Reply: reply}, ports.ControlErrBadRequest},
			{ports.ControlInject{Reply: reply}, ports.ControlErrForbidden},
		}
		for _, tt := range tests {
			control <- tt.request
			if got := receive(t, reply); got.Err != tt.want {
				t.Fatalf("%T: %+v, want %s", tt.request, got, tt.want)
			}
		}
		// A full or absent reply channel must not stall the owner.
		reply <- ports.ControlReply{}
		control <- ports.ControlQuery{What: "state", Caps: domain.CapRead, Reply: reply}
		synctest.Wait()
		<-reply
		control <- ports.ControlQuery{What: "state", Caps: domain.CapRead}
		control <- ports.ControlQuery{What: "state", Caps: domain.CapRead, Reply: reply}
		receive(t, reply)
		epoch.Store(domain.SecurityState{Generation: 1, Protected: true})
		control <- ports.ControlQuery{What: "state", Caps: domain.CapRead, Reply: reply}
		protected := receive(t, reply)
		data, err := json.Marshal(protected.State)
		if err != nil {
			t.Fatal(err)
		}
		if protected.Err != "" || len(protected.State.Windows) != 1 || strings.Contains(string(data), "private-title") {
			t.Fatalf("protected query: %s (%s)", data, protected.Err)
		}
		close(control)
		synctest.Wait()
	})
}
