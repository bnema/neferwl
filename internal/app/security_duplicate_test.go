package app

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/adapters/wayland/sessionlock"
	"github.com/bnema/neferwl/internal/ports"
)

// Native Close validation is exercised by DRM's generated-KMS tests. This
// boundary test proves that even a name-discarded physical lifetime enters the
// authoritative ledger, and only affirmative inactivity can retire it.
func TestSecurityDuplicatePhysicalLifetimeRequiresConfirmedInactive(t *testing.T) {
	for _, inactive := range []bool{false, true} {
		name := "unconfirmed"
		if inactive {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			gate := &sessionsecurity.Gate{}
			state, err := gate.Engage()
			if err != nil {
				t.Fatal(err)
			}
			events := make(chan ports.SecurityBackendEvent, 4)
			set := newOutputSet(context.Background(), nil)
			set.wireSecurity(outputChannels{security: gate, securityEvents: events})
			run := func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
				<-ctx.Done()
				return nil
			}
			if !set.start(context.Background(), "DP-1", run) {
				t.Fatal("primary output failed to start")
			}
			primary := (<-events).(ports.SecurityOutputAdded)
			duplicate, err := registerDiscardedOutput(set, "DP-1")
			if err != nil {
				t.Fatal(err)
			}
			added := (<-events).(ports.SecurityOutputAdded)
			if added.Instance != duplicate || added.Output != primary.Output || duplicate <= primary.Instance {
				t.Fatalf("duplicate lifetime registration: primary=%+v duplicate=%+v", primary, added)
			}
			if len(set.outs) != 1 || set.outs["DP-1"].instance != primary.Instance {
				t.Fatal("discarded lifetime replaced routed output")
			}
			var ledger sessionlock.Readiness
			if !ledger.Add(primary.Instance) || !ledger.Add(duplicate) || !ledger.Begin(state.Generation) {
				t.Fatal("ledger setup failed")
			}
			ledger.BackendBarrier(state.Generation)
			ledger.CaptureBarrier(state.Generation)
			ledger.Record(ports.OutputProtection{Generation: state.Generation, Instance: primary.Instance, Kind: ports.ProtectionProtectedFrame})
			if ledger.Ready() {
				t.Fatal("primary proof hid duplicate physical output")
			}
			err = retireDiscardedOutput(set, duplicate, inactive)
			if inactive {
				if err != nil {
					t.Fatal(err)
				}
				removed := (<-events).(ports.SecurityOutputRemoved)
				if removed.Instance != duplicate {
					t.Fatal("removed primary instead of duplicate")
				}
				if !ledger.Remove(removed.Instance) || !ledger.Ready() {
					t.Fatal("confirmed inactive duplicate did not retire")
				}
			} else {
				if err == nil {
					t.Fatal("unconfirmed close accepted")
				}
				select {
				case event := <-events:
					t.Fatalf("false retirement/proof: %+v", event)
				default:
				}
				if ledger.Ready() {
					t.Fatal("unproved duplicate permitted locked")
				}
			}
			if !gate.Snapshot().Protected {
				t.Fatal("discarding duplicate released gate")
			}
			if err := set.wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSecurityDiscardedLifetimeCounterExhaustion(t *testing.T) {
	events := make(chan ports.SecurityBackendEvent, 2)
	set := newOutputSet(context.Background(), nil)
	set.wireSecurity(outputChannels{securityEvents: events})
	set.nextInstance = ^ports.OutputInstance(0) - 1
	instance, err := registerDiscardedOutput(set, "DP-1")
	if err != nil || instance != ^ports.OutputInstance(0) {
		t.Fatalf("last lifetime: %d %v", instance, err)
	}
	if added := (<-events).(ports.SecurityOutputAdded); added.Instance != instance {
		t.Fatal("registration differs")
	}
	for range 2 {
		if instance, err := registerDiscardedOutput(set, "DP-1"); instance != 0 || err == nil {
			t.Fatalf("exhaustion: %d %v", instance, err)
		}
	}
	if set.nextInstance != ^ports.OutputInstance(0) {
		t.Fatal("counter wrapped")
	}
	select {
	case event := <-events:
		t.Fatalf("exhaustion registered: %+v", event)
	default:
	}
	if err := set.wait(); err == nil {
		t.Fatal("exhaustion not retained")
	}
}
