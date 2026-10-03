package drm

import (
	"context"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func applyPipelines(o *Output) {
	for _, p := range []*plane{o.primary, o.overlay} {
		p.colorKnown, p.colorApplied = true, colorSDRToPQ
	}
}

// Before the seat is disabled a plane that applies the pipeline is put back
// on Bypass, in a blocking commit of the planes alone.
func TestSeatDisableBypassesAppliedPipeline(t *testing.T) {
	o, _, commits := colorOutput(t, true, nil)
	applyPipelines(o)
	o.bypassForSeatDisable()
	if len(*commits) != 1 {
		t.Fatalf("%d commits", len(*commits))
	}
	c := (*commits)[0]
	if c.flags != 0 || c.user != 0 {
		t.Fatalf("not a blocking commit: flags %#x user %d", c.flags, c.user)
	}
	for _, p := range []struct{ plane, prop uint32 }{{tPrimary, pColorPipe}, {tOverlay, pColorPipeOverlay}} {
		if v, ok := c.req.value(p.plane, p.prop); !ok || v != 0 {
			t.Errorf("plane %d COLOR_PIPELINE %d %v, want Bypass", p.plane, v, ok)
		}
	}
	if len(c.req.objs) != 2 || o.pipelineApplied() {
		t.Fatalf("objects %v, applied %v", c.req.objs, o.pipelineApplied())
	}
	// Done: a second request has nothing to do.
	o.bypassForSeatDisable()
	if len(*commits) != 1 {
		t.Fatal("commit with Bypass applied")
	}
}

// With nothing applied (or nothing known) no commit is made.
func TestSeatDisableWithoutPipelineDoesNotCommit(t *testing.T) {
	for name, setup := range map[string]func(*Output){
		"bypass":  func(o *Output) { o.colorBypassed() },
		"unknown": func(o *Output) { o.forgetColor() },
		"no pipeline plane": func(o *Output) {
			o.primary.props = maps0(planeProps)
			o.overlay.props = maps0(planeProps)
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, _, commits := colorOutput(t, true, nil)
			setup(o)
			o.bypassForSeatDisable()
			if len(*commits) != 0 {
				t.Fatalf("%d commits", len(*commits))
			}
		})
	}
}

// Run answers the request: the commit is made by the output's goroutine and
// the caller is acked after it. The output stops drawing until the next enable.
func TestRunPreparesSeatDisable(t *testing.T) {
	o, k, commits, mu := testOutputMu(t)
	o.cursor, o.tearing, o.fbs = nil, false, [2]uint32{}
	props := maps0(planeProps)
	props["COLOR_PIPELINE"] = pColorPipe
	o.primary.props = props
	o.primary.pipeline = &colorPipeline{}
	o.primary.colorKnown, o.primary.colorApplied = true, colorSDRToPQ
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	active := make(chan bool, 1)
	active <- false // switched away: no modeset resets the state
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, nil, nil, nil, make(chan ports.OutputPresented, 1), nil, nil)
	}()
	if !o.PrepareSeatDisable(5 * time.Second) {
		t.Fatal("no ack")
	}
	mu.Lock()
	n := len(*commits)
	var v uint64
	var ok bool
	if n > 0 {
		v, ok = (*commits)[0].req.value(tPrimary, pColorPipe)
	}
	mu.Unlock()
	if n != 1 || !ok || v != 0 {
		t.Fatalf("commits %d, COLOR_PIPELINE %d %v", n, v, ok)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// An output that is not running cannot answer: the caller is not held.
func TestPrepareSeatDisableTimesOut(t *testing.T) {
	o, _, _ := testOutput(t)
	if o.PrepareSeatDisable(5 * time.Millisecond) {
		t.Fatal("acked without a running output")
	}
}
