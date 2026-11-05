package core

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// Core decides continuous capture: capability, protection, output
// selection and directories; the backend receives only resolved plans.
func TestControlCapturePlan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var epoch atomic.Value
		epoch.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return epoch.Load().(domain.SecurityState) })
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		c.opts.Security = gate
		plans := make(chan ports.ContinuousCaptureRequest, 1)
		c.ch.Continuous = plans
		c.addScreen(domain.OutputInfo{Name: "A", Width: 100, Height: 80})
		c.addScreen(domain.OutputInfo{Name: "B", Width: 100, Height: 80})
		dir := t.TempDir()
		ask := func(r ports.ControlCapture) (ports.ContinuousCaptureRequest, string) {
			t.Helper()
			reply := make(chan ports.ContinuousCaptureResult, 1)
			r.Reply = reply
			if err := c.control(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			select {
			case plan := <-plans:
				return plan, ""
			case res := <-reply:
				return ports.ContinuousCaptureRequest{}, res.Err
			}
		}
		for _, tc := range []struct {
			name string
			req  ports.ControlCapture
			code string
		}{
			{"no capability", ports.ControlCapture{Dir: dir}, ports.ControlErrForbidden},
			{"relative dir", ports.ControlCapture{Dir: "frames", Caps: domain.CapCapture}, ports.ControlErrBadRequest},
			{"unknown output", ports.ControlCapture{Outputs: []string{"Z"}, Dir: dir, Caps: domain.CapCapture}, ports.ControlErrBadRequest},
		} {
			if _, code := ask(tc.req); code != tc.code {
				t.Errorf("%s: %q want %q", tc.name, code, tc.code)
			}
		}
		// All outputs: one subdirectory each, in screen order.
		plan, code := ask(ports.ControlCapture{Dir: dir + "/", Raw: true, Caps: domain.CapCapture})
		want := []ports.ContinuousTarget{{Output: "A", Dir: filepath.Join(dir, "A")}, {Output: "B", Dir: filepath.Join(dir, "B")}}
		if code != "" || !plan.Raw || len(plan.Targets) != 2 || plan.Targets[0] != want[0] || plan.Targets[1] != want[1] || cap(plan.Reply) != 1 {
			t.Fatalf("all: %+v %q", plan, code)
		}
		// One output writes into the directory itself; stop needs no dir.
		if plan, _ := ask(ports.ControlCapture{Outputs: []string{"B"}, Dir: dir, Caps: domain.CapCapture}); len(plan.Targets) != 1 || plan.Targets[0].Dir != dir {
			t.Fatalf("one: %+v", plan)
		}
		if plan, _ := ask(ports.ControlCapture{Stop: true, Caps: domain.CapCapture}); !plan.Stop || len(plan.Targets) != 2 || plan.Targets[0].Dir != "" {
			t.Fatalf("stop: %+v", plan)
		}
		// A busy backend refuses rather than stalling the owner.
		plans <- ports.ContinuousCaptureRequest{}
		if _, code := func() (ports.ContinuousCaptureRequest, string) {
			reply := make(chan ports.ContinuousCaptureResult, 1)
			_ = c.control(context.Background(), ports.ControlCapture{Dir: dir, Caps: domain.CapCapture, Reply: reply})
			return ports.ContinuousCaptureRequest{}, (<-reply).Err
		}(); code != ports.ControlErrBusy {
			t.Fatalf("busy: %q", code)
		}
		<-plans
		// A pending security change refuses before anything reaches the backend.
		epoch.Store(domain.SecurityState{Generation: 1, Protected: true})
		if _, code := ask(ports.ControlCapture{Dir: dir, Caps: domain.CapCapture}); code != ports.ControlErrLocked {
			t.Fatalf("locked: %q", code)
		}
	})
}
