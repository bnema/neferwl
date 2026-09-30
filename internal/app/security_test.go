package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestSecurityRegistrationBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan ports.SecurityBackendEvent)
	set := newOutputSet(ctx, nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	set.setSecurity(ports.SecurityState{Generation: 7, Protected: true})
	started := make(chan ports.OutputInstance, 1)
	result := make(chan bool, 1)
	go func() {
		result <- set.start(ctx, "DP-1", func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, security <-chan ports.SecurityState, instance ports.OutputInstance) error {
			state := <-security
			if state != (ports.SecurityState{Generation: 7, Protected: true}) {
				return errors.New("missing startup protection")
			}
			started <- instance
			<-ctx.Done()
			return nil
		})
	}()
	select {
	case <-started:
		t.Fatal("owner ran before registration")
	default:
	}
	var added ports.SecurityOutputAdded
	select {
	case event := <-events:
		added = event.(ports.SecurityOutputAdded)
	case <-time.After(2 * time.Second):
		t.Fatal("registration missing")
	}
	if added.Instance == 0 || added.Output != "DP-1" {
		t.Fatalf("registration: %+v", added)
	}
	if !<-result {
		t.Fatal("start failed")
	}
	select {
	case instance := <-started:
		if instance != added.Instance {
			t.Fatal("owner lifetime differs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner missing startup protection")
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityReplacementAndStaleState(t *testing.T) {
	ctx := context.Background()
	events := make(chan ports.SecurityBackendEvent, 4)
	set := newOutputSet(ctx, nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	run := func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
		<-ctx.Done()
		return nil
	}
	set.start(ctx, "DP-1", run)
	old := (<-events).(ports.SecurityOutputAdded)
	set.outs["DP-1"].stop()
	if err := set.finish("DP-1"); err != nil {
		t.Fatal(err)
	}
	set.start(ctx, "DP-1", run)
	replacement := (<-events).(ports.SecurityOutputAdded)
	if replacement.Instance <= old.Instance {
		t.Fatal("reused lifetime")
	}
	if set.stoppedCurrent(outputStopped{name: "DP-1", instance: old.Instance}) {
		t.Fatal("old stop matches replacement")
	}
	set.setSecurity(ports.SecurityState{Generation: 3, Protected: true})
	set.setSecurity(ports.SecurityState{Generation: 2})
	if state := <-set.outs["DP-1"].security; state.Generation != 3 || !state.Protected {
		t.Fatalf("stale transition: %+v", state)
	}
	set.setSecurity(ports.SecurityState{Generation: 4})
	if state := <-set.outs["DP-1"].security; state.Generation != 4 || state.Protected {
		t.Fatalf("unlock not forwarded: %+v", state)
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("stop emitted false removal: %+v", ev)
	default:
	}
}

func TestGuardedLeaseRejectsProtected(t *testing.T) {
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true}).Once()
	card := newMocksecurityLeaseCard(t)
	fd, id, err := guardedLease(card, gate, []string{"DP-1"})
	if fd != nil || id != 0 || err == nil {
		t.Fatalf("protected lease: %v %d %v", fd, id, err)
	}
}

func TestGuardedLeaseCrossingTransition(t *testing.T) {
	for _, revokeErr := range []error{nil, errors.New("revoke failed")} {
		t.Run(map[bool]string{false: "revoked", true: "failed"}[revokeErr != nil], func(t *testing.T) {
			fd, err := os.CreateTemp(t.TempDir(), "lease")
			if err != nil {
				t.Fatal(err)
			}
			gate := portsmocks.NewMockSessionSecurity(t)
			gate.EXPECT().Snapshot().Return(ports.SecurityState{}).Once()
			gate.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true}).Once()
			card := newMocksecurityLeaseCard(t)
			card.EXPECT().Lease([]string{"DP-1"}).Return(fd, uint32(12), nil).Once()
			card.EXPECT().Revoke(uint32(12)).Return(revokeErr).Once()
			got, id, err := guardedLease(card, gate, []string{"DP-1"})
			if got != nil || id != 0 || err == nil {
				t.Fatalf("late grant escaped: %v %d %v", got, id, err)
			}
			if _, statErr := fd.Stat(); !errors.Is(statErr, os.ErrClosed) {
				t.Fatalf("FD not closed: %v", statErr)
			}
			if revokeErr != nil && !errors.Is(err, revokeErr) {
				t.Fatal("revoke failure lost")
			}
		})
	}
}

func TestSecurityRevokesAllLeasesAndRetainsFailures(t *testing.T) {
	card := newMocksecurityLeaseCard(t)
	card.EXPECT().LeaseIDs().Return([]uint32{1, 2}).Once()
	card.EXPECT().LeaseIDs().Return([]uint32{2}).Once()
	card.EXPECT().Revoke(uint32(1)).Return(nil).Once()
	failed := errors.New("native revoke failure")
	card.EXPECT().Revoke(uint32(2)).Return(failed).Once()
	card.EXPECT().Path().Return("card0")
	var finished []uint32
	err := revokeSecurityLeases(card, func(id uint32) { finished = append(finished, id) })
	if !errors.Is(err, failed) || len(finished) != 1 || finished[0] != 1 {
		t.Fatalf("revocation: %v finished %v", err, finished)
	}
	card.EXPECT().LeaseIDs().Return([]uint32{2}).Once()
	card.EXPECT().LeaseIDs().Return([]uint32{}).Once()
	card.EXPECT().Revoke(uint32(2)).Return(nil).Once()
	if err := revokeSecurityLeases(card, func(id uint32) {}); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityInstanceExhaustion(t *testing.T) {
	set := newOutputSet(context.Background(), nil)
	events := make(chan ports.SecurityBackendEvent, 2)
	set.wireSecurity(outputChannels{securityEvents: events})
	set.nextInstance = ^ports.OutputInstance(0) - 1
	run := func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
		<-ctx.Done()
		return nil
	}
	if !set.start(context.Background(), "last", run) {
		t.Fatal("last valid lifetime refused")
	}
	if added := (<-events).(ports.SecurityOutputAdded); added.Instance != ^ports.OutputInstance(0) {
		t.Fatalf("last instance: %+v", added)
	}
	for range 2 {
		if set.start(context.Background(), "overflow", run) {
			t.Fatal("exhausted counter launched owner")
		}
	}
	if set.nextInstance != ^ports.OutputInstance(0) || set.startErr == nil {
		t.Fatal("counter wrapped or error missing")
	}
	select {
	case ev := <-events:
		t.Fatalf("registered overflow: %+v", ev)
	default:
	}
	if err := set.wait(); err == nil {
		t.Fatal("exhaustion not returned")
	}
}

// The shared display event channel and an output content queue can both be
// saturated. Routing must retain the next content without pinning the backend
// outside its select; a lease barrier can then finish before either is drained.
func TestSecurityFullEventsAndContentsAllowLeaseBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set := newOutputSet(ctx, nil)
	events := make(chan ports.SecurityBackendEvent, 1)
	set.wireSecurity(outputChannels{securityEvents: events})
	var ownerEvents chan<- ports.SecurityBackendEvent
	set.start(ctx, "DP-1", func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
		<-ctx.Done()
		return nil
	}, &ownerEvents)
	<-events                                               // ordered registration
	events <- ports.SecurityBackendBarrier{Generation: 99} // blocked display
	r := set.outs["DP-1"]
	for i := range cap(r.contents) {
		r.contents <- ports.SurfaceContent{ID: ports.WindowID(i + 1)}
	}
	first := ports.SecurityOutputInvalidated{Generation: 1, Instance: r.instance}
	second := ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: 1, Instance: r.instance, Kind: ports.ProtectionProtectedFrame}}
	ownerEvents <- first
	ownerEvents <- second
	card := newMocksecurityLeaseCard(t)
	card.EXPECT().LeaseIDs().Return([]uint32{9}).Once()
	card.EXPECT().LeaseIDs().Return([]uint32{}).Once()
	card.EXPECT().Revoke(uint32(9)).Return(nil).Once()
	progressed := make(chan error, 1)
	go func() {
		set.content(ports.SurfaceContent{ID: 70})
		set.setSecurity(ports.SecurityState{Generation: 1, Protected: true})
		err := revokeSecurityLeases(card, func(uint32) {})
		set.securityEvent(ports.SecurityBackendBarrier{Generation: 1, Err: err})
		progressed <- err
	}()
	select {
	case err := <-progressed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("content/event backpressure blocked lease transition")
	}
	incoming := make(chan ports.SurfaceContent)
	contentIn, output, next, owner := set.contentOut(incoming)
	if contentIn != nil || output != r.contents || owner != r || next.ID != 70 || len(r.pending) != 1 {
		t.Fatal("pending content not bounded/preserved")
	}
	for range cap(r.contents) {
		<-r.contents
	}
	output <- next
	owner.contentSent()
	if got := <-r.contents; got.ID != 70 {
		t.Fatal("lost pending content")
	}
	if contentIn, _, _, _ := set.contentOut(incoming); contentIn != incoming {
		t.Fatal("content intake stayed disabled")
	}
	<-events // unblock display
	var ownerSequence []ports.SecurityBackendEvent
	for range 3 {
		select {
		case ev := <-events:
			switch ev.(type) {
			case ports.SecurityOutputInvalidated, ports.SecurityOutputProof:
				ownerSequence = append(ownerSequence, ev)
			}
		case <-time.After(time.Second):
			t.Fatal("lossless forwarder lost event")
		}
	}
	if len(ownerSequence) != 2 || ownerSequence[0] != first || ownerSequence[1] != second {
		t.Fatalf("owner order lost: %+v", ownerSequence)
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityLargeReplayDoesNotBlockStart(t *testing.T) {
	set := newOutputSet(context.Background(), nil)
	for i := range 100 {
		set.content(ports.SurfaceContent{ID: ports.WindowID(i + 1), SHM: &ports.SHMBuffer{}})
	}
	if !set.start(context.Background(), "A", func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
		<-ctx.Done()
		return nil
	}) {
		t.Fatal("start failed")
	}
	r := set.outs["A"]
	if len(r.contents) != cap(r.contents) || len(r.pending) != 100-cap(r.contents) {
		t.Fatal("replay not preserved")
	}
	seen := map[ports.WindowID]bool{}
	for len(seen) != 100 {
		select {
		case c := <-r.contents:
			if seen[c.ID] {
				t.Fatal("duplicate replay")
			}
			seen[c.ID] = true
		default:
			_, out, c, owner := set.contentOut(nil)
			if out == nil {
				t.Fatal("replay lost")
			}
			out <- c
			owner.contentSent()
		}
	}
	if len(r.pending) != 0 {
		t.Fatal("replay remains")
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}
