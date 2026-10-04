package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/wayland/sessionlock"
	"github.com/bnema/neferwl/internal/ports"
)

func hotplugEvent(t *testing.T, events <-chan ports.SecurityBackendEvent) ports.SecurityBackendEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("missing ordered security event")
		return nil
	}
}

// This switch represents the single display owner's ledger consumption, not
// an alternate backend implementation. Events come through real outputSet FIFO.
func hotplugApply(ledger *sessionlock.Readiness, event ports.SecurityBackendEvent) bool {
	switch v := event.(type) {
	case ports.SecurityOutputAdded:
		return ledger.Add(v.Instance)
	case ports.SecurityOutputRemoved:
		return ledger.Remove(v.Instance)
	case ports.SecurityOutputInvalidated:
		return ledger.Invalidate(v.Generation, v.Instance)
	case ports.SecurityOutputProof:
		return ledger.Record(v.Proof)
	case ports.SecurityBackendBarrier:
		return v.Err == nil && ledger.BackendBarrier(v.Generation)
	default:
		return false
	}
}

func TestSecurityHotplugLifetimeAndProofOrdering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan ports.SecurityBackendEvent, 8)
	set := newOutputSet(ctx, nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	defer func() {
		if err := set.wait(); err != nil {
			t.Error(err)
		}
	}()
	const generation = ports.LockGeneration(41)
	set.setSecurity(ports.SecurityState{Generation: generation, Protected: true})
	var ledger sessionlock.Readiness
	if !ledger.Begin(generation) || !ledger.BackendBarrier(generation) || !ledger.CaptureBarrier(generation) {
		t.Fatal("could not begin display readiness round")
	}
	// Native Close success is independently tested in DRM. Here the real
	// owner callback synchronizes that affirmative result before the backend
	// sends removal; mere callback exit never emits removal from outputSet.
	start := func(closeSuccess bool) (ports.OutputInstance, chan []ports.SecurityBackendEvent, <-chan bool) {
		t.Helper()
		commands := make(chan []ports.SecurityBackendEvent, 1)
		closed := make(chan bool, 1)
		var ownerEvents chan<- ports.SecurityBackendEvent
		if !set.start(ctx, "DP-1", func(octx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, security <-chan ports.SecurityState, instance ports.OutputInstance) error {
			defer func() { closed <- closeSuccess }()
			state := <-security
			proof := ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: state.Generation, Instance: instance, Kind: ports.ProtectionProtectedFrame}}
			select {
			case ownerEvents <- proof:
			case <-octx.Done():
				return nil
			}
			for {
				select {
				case batch := <-commands:
					for _, event := range batch {
						select {
						case ownerEvents <- event:
						case <-octx.Done():
							return nil
						}
					}
				case <-octx.Done():
					return nil
				}
			}
		}, &ownerEvents) {
			t.Fatal("start refused")
		}
		registered := hotplugEvent(t, events)
		added, ok := registered.(ports.SecurityOutputAdded)
		if !ok || added.Output != "DP-1" || added.Instance == 0 || !hotplugApply(&ledger, registered) {
			t.Fatalf("owner evidence preceded lifetime registration: %+v", registered)
		}
		if ledger.Ready() {
			t.Fatal("new lifetime reused previous physical readiness")
		}
		proof := hotplugEvent(t, events)
		if !hotplugApply(&ledger, proof) || !ledger.Ready() {
			t.Fatalf("missing exact lifetime proof: %+v", proof)
		}
		return added.Instance, commands, closed
	}
	old, commands, closed := start(true)
	// A proof already queued by this owner precedes invalidation, even if the
	// display reads later. No separate forwarder can replay it after revoke.
	oldProof := ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: generation, Instance: old, Kind: ports.ProtectionProtectedFrame}}
	invalid := ports.SecurityOutputInvalidated{Generation: generation, Instance: old}
	commands <- []ports.SecurityBackendEvent{oldProof, invalid}
	if got := hotplugEvent(t, events); got != oldProof || !hotplugApply(&ledger, got) {
		t.Fatalf("queued proof lost: %+v", got)
	}
	if got := hotplugEvent(t, events); got != invalid || !hotplugApply(&ledger, got) || ledger.Ready() {
		t.Fatalf("invalidation reordered or ineffective: %+v", got)
	}
	commands <- []ports.SecurityBackendEvent{oldProof}
	if got := hotplugEvent(t, events); got != oldProof || !hotplugApply(&ledger, got) || !ledger.Ready() {
		t.Fatal("fresh resume proof did not restore readiness")
	}
	set.outs["DP-1"].stop()
	select {
	case stopped := <-set.stopped:
		if stopped.instance != old || !set.stoppedCurrent(stopped) {
			t.Fatalf("wrong stopped lifetime: %+v", stopped)
		}
	case <-ctx.Done():
		t.Fatal("owner did not stop")
	}
	if err := set.finish("DP-1"); err != nil {
		t.Fatal(err)
	}
	if !<-closed {
		t.Fatal("shutdown not affirmative")
	}
	select {
	case got := <-events:
		t.Fatalf("owner exit forged removal: %+v", got)
	default:
	}
	if !set.securityEvent(ports.SecurityOutputRemoved{Instance: old}) {
		t.Fatal("confirmed removal refused")
	}
	removal := hotplugEvent(t, events)
	if removal != (ports.SecurityOutputRemoved{Instance: old}) || !hotplugApply(&ledger, removal) {
		t.Fatalf("wrong removal: %+v", removal)
	}
	replacement, _, replacementClosed := start(false)
	if replacement <= old || set.stoppedCurrent(outputStopped{name: "DP-1", instance: old}) {
		t.Fatal("same-name replacement reused retired lifetime")
	}
	if hotplugApply(&ledger, oldProof) || hotplugApply(&ledger, invalid) {
		t.Fatal("retired lifetime affected replacement")
	}
	stale := ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: generation - 1, Instance: replacement, Kind: ports.ProtectionInactiveOutput}}
	if hotplugApply(&ledger, stale) || !ledger.Ready() {
		t.Fatal("stale generation overwrote valid replacement proof")
	}
	if !ledger.Invalidate(generation, replacement) || ledger.Ready() {
		t.Fatal("replacement invalidation ineffective")
	}
	set.outs["DP-1"].stop()
	select {
	case <-set.stopped:
	case <-ctx.Done():
		t.Fatal("replacement stop blocked")
	}
	if err := set.finish("DP-1"); err != nil {
		t.Fatal(err)
	}
	if <-replacementClosed {
		t.Fatal("expected failed shutdown")
	}
	select {
	case got := <-events:
		t.Fatalf("failed shutdown forged removal: %+v", got)
	default:
	}
	if ledger.Ready() {
		t.Fatal("failed replacement exit erased required unproved lifetime")
	}
}

func TestSecurityOwnerFIFOSaturationDrainsOnNormalStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan ports.SecurityBackendEvent) // block display after registration
	set := newOutputSet(ctx, nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	defer func() {
		if err := set.wait(); err != nil {
			t.Error(err)
		}
	}()
	const generation = ports.LockGeneration(7)
	accepted := make(chan int, 40)
	startDone := make(chan bool, 1)
	var ownerEvents chan<- ports.SecurityBackendEvent
	go func() {
		startDone <- set.start(ctx, "DP-1", func(octx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, instance ports.OutputInstance) error {
			for i := range 40 {
				var event ports.SecurityBackendEvent = ports.SecurityOutputInvalidated{Generation: generation, Instance: instance}
				if i%2 == 0 {
					event = ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: generation, Instance: instance, Kind: ports.ProtectionProtectedFrame}}
				}
				select {
				case ownerEvents <- event:
					accepted <- i
				case <-octx.Done():
					return nil
				}
			}
			return nil
		}, &ownerEvents)
	}()
	registered := hotplugEvent(t, events).(ports.SecurityOutputAdded)
	if !<-startDone {
		t.Fatal("start failed")
	}
	if cap(set.outs["DP-1"].securityEvents) != 16 {
		t.Fatal("owner evidence FIFO is not bounded to 16")
	}
	// Forwarder holds one event plus its 16-slot input. The eighteenth send
	// must remain blocked until the display starts draining.
	for i := range 17 {
		select {
		case got := <-accepted:
			if got != i {
				t.Fatalf("acceptance order %d want %d", got, i)
			}
		case <-ctx.Done():
			t.Fatal("FIFO failed to fill")
		}
	}
	select {
	case got := <-accepted:
		t.Fatalf("full FIFO did not backpressure owner: %d", got)
	case <-time.After(20 * time.Millisecond):
	}
	var ledger sessionlock.Readiness
	ledger.Add(registered.Instance)
	ledger.Begin(generation)
	ledger.BackendBarrier(generation)
	ledger.CaptureBarrier(generation)
	for i := range 40 {
		got := hotplugEvent(t, events)
		var want ports.SecurityBackendEvent = ports.SecurityOutputInvalidated{Generation: generation, Instance: registered.Instance}
		if i%2 == 0 {
			want = ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: generation, Instance: registered.Instance, Kind: ports.ProtectionProtectedFrame}}
		}
		if !reflect.DeepEqual(got, want) || !hotplugApply(&ledger, got) || ledger.Ready() != (i%2 == 0) {
			t.Fatalf("evidence lost/duplicated/reordered at %d: %+v", i, got)
		}
	}
	select {
	case stopped := <-set.stopped:
		if !set.stoppedCurrent(stopped) {
			t.Fatal("stop not current")
		}
	case <-ctx.Done():
		t.Fatal("normal owner stop blocked")
	}
	if err := set.finish("DP-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		t.Fatalf("duplicate evidence or removal after normal drain: %+v", got)
	default:
	}
}

// All producer evidence is already buffered when the owner stops normally.
// finish must not cancel that owner's forwarder or lose its outstanding FIFO.
func TestSecurityOwnerBufferedEvidenceDrainsAfterFinish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan ports.SecurityBackendEvent)
	set := newOutputSet(ctx, nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	defer func() {
		if err := set.wait(); err != nil {
			t.Error(err)
		}
	}()
	startDone := make(chan bool, 1)
	var ownerEvents chan<- ports.SecurityBackendEvent
	go func() {
		startDone <- set.start(ctx, "DP-1", func(octx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, instance ports.OutputInstance) error {
			for i := range 16 {
				event := ports.SecurityOutputInvalidated{Generation: ports.LockGeneration(i + 1), Instance: instance}
				select {
				case ownerEvents <- event:
				case <-octx.Done():
					return nil
				}
			}
			return nil
		}, &ownerEvents)
	}()
	registered := hotplugEvent(t, events).(ports.SecurityOutputAdded)
	if !<-startDone {
		t.Fatal("start refused")
	}
	select {
	case stopped := <-set.stopped:
		if stopped.instance != registered.Instance {
			t.Fatal("wrong stopped lifetime")
		}
	case <-ctx.Done():
		t.Fatal("buffered owner did not finish")
	}
	if err := set.finish("DP-1"); err != nil {
		t.Fatal(err)
	}
	for i := range 16 {
		want := ports.SecurityOutputInvalidated{Generation: ports.LockGeneration(i + 1), Instance: registered.Instance}
		if got := hotplugEvent(t, events); got != want {
			t.Fatalf("buffered stopped-owner evidence %d: %+v", i, got)
		}
	}
	select {
	case got := <-events:
		t.Fatalf("duplicate buffered evidence: %+v", got)
	default:
	}
}

func TestSecurityOwnerFIFOSaturationCancelReleasesOwnerAndForwarders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan ports.SecurityBackendEvent)
	set := newOutputSet(ctx, nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	accepted := make(chan int, 40)
	ownerDone := make(chan struct{})
	startDone := make(chan bool, 1)
	var ownerEvents chan<- ports.SecurityBackendEvent
	go func() {
		startDone <- set.start(ctx, "DP-1", func(octx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, instance ports.OutputInstance) error {
			defer close(ownerDone)
			for i := range 40 {
				select {
				case ownerEvents <- ports.SecurityOutputInvalidated{Generation: 7, Instance: instance}:
					accepted <- i
				case <-octx.Done():
					return nil
				}
			}
			return nil
		}, &ownerEvents)
	}()
	hotplugEvent(t, events) // registered before producer starts
	if !<-startDone {
		t.Fatal("start failed")
	}
	for range 17 {
		select {
		case <-accepted:
		case <-ctx.Done():
			t.Fatal("FIFO never filled")
		}
	}
	select {
	case <-accepted:
		t.Fatal("producer did not block at finite FIFO")
	case <-time.After(20 * time.Millisecond):
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- set.wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("saturated evidence leaked owner/forwarder on cancellation")
	}
	select {
	case <-ownerDone:
	default:
		t.Fatal("wait returned before producer exited")
	}
	if len(set.outs) != 0 {
		t.Fatal("stopped lifetime retained in running set")
	}
	// wait includes forwarders.Wait: termination is directly synchronized,
	// rather than guessed from goroutine counts or arbitrary sleeps.
}
