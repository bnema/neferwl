package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// Wayland feeds a started output its contents: Attach comes after the
// output registered and got its security state, the output runs on what
// Attach returned, Detach of that same handle follows its end, and a
// restarted connector attaches again.
func TestOutputSetAttachesAndDetachesContents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set := newOutputSet(ctx, nil)
	events := make(chan ports.SecurityBackendEvent, 4)
	set.wireSecurity(outputChannels{securityEvents: events})
	attacher := newMockcontentAttacher(t)
	set.attach = attacher
	rets := []*portsmocks.MockContentReturn{portsmocks.NewMockContentReturn(t), portsmocks.NewMockContentReturn(t)}
	feed := make(chan *ports.ContentBatch, 1)
	var order []string
	type seen struct {
		contents <-chan *ports.ContentBatch
		returns  ports.ContentReturn
		security ports.SecurityState
	}
	started := make(chan seen, 2)
	run := func(ctx context.Context, _ <-chan ports.Scene, contents <-chan *ports.ContentBatch, returns ports.ContentReturn, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, security <-chan ports.SecurityState, _ ports.OutputInstance) error {
		started <- seen{contents, returns, <-security}
		<-ctx.Done()
		return nil
	}
	attaches := 0
	attacher.EXPECT().AttachOutput("DP-1").RunAndReturn(func(string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
		order = append(order, "attach")
		ret := rets[attaches]
		attaches++
		// The output is registered with the security backend first.
		if len(events) != 1 {
			t.Errorf("attached with %d registrations queued, want 1", len(events))
		}
		return feed, ret
	}).Times(2)
	var detached []ports.ContentReturn
	attacher.EXPECT().DetachOutput(mock.Anything).Run(func(feed ports.ContentReturn) {
		order = append(order, "detach")
		detached = append(detached, feed)
	}).Times(2)
	for round := range 2 {
		if !set.start(ctx, "DP-1", run) {
			t.Fatal("start failed")
		}
		got := <-started
		if got.contents != (<-chan *ports.ContentBatch)(feed) || got.returns != ports.ContentReturn(rets[round]) {
			t.Fatalf("round %d: output got another feed", round)
		}
		<-events
		set.outs["DP-1"].stop()
		if err := set.finish("DP-1"); err != nil {
			t.Fatal(err)
		}
	}
	for round := range detached {
		if detached[round] != ports.ContentReturn(rets[round]) {
			t.Fatalf("round %d: detached another feed than the one attached", round)
		}
	}
	want := []string{"attach", "detach", "attach", "detach"}
	if len(order) != len(want) {
		t.Fatalf("order %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
}

// wait detaches the outputs it stops.
func TestOutputSetWaitDetachesContents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set := newOutputSet(ctx, nil)
	attacher := newMockcontentAttacher(t)
	set.attach = attacher
	ret := portsmocks.NewMockContentReturn(t)
	attacher.EXPECT().AttachOutput("A").Return(nil, ret).Once()
	attacher.EXPECT().DetachOutput(ports.ContentReturn(ret)).Return().Once()
	set.start(ctx, "A", func(ctx context.Context, _ <-chan ports.Scene, _ <-chan *ports.ContentBatch, _ ports.ContentReturn, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
		<-ctx.Done()
		return nil
	})
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}
