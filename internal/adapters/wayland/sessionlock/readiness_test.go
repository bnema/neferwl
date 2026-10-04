package sessionlock

import (
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func lockProof(generation ports.LockGeneration, instance ports.OutputInstance) ports.OutputProtection {
	return ports.OutputProtection{Generation: generation, Instance: instance, Kind: ports.ProtectionProtectedFrame}
}

func requireLockAccepted(t *testing.T, accepted bool) {
	t.Helper()
	if !accepted {
		t.Fatal("valid operation rejected")
	}
}

func requireLockReady(t *testing.T, r *Readiness, want bool) {
	t.Helper()
	if got := r.Ready(); got != want {
		t.Fatalf("Ready() = %v, want %v; ledger %+v", got, want, r)
	}
}

func TestLockReadinessZeroValue(t *testing.T) {
	var r Readiness
	requireLockReady(t, &r, false)
	if r.Begin(0) || r.End(0) || r.Add(0) || r.Remove(0) || r.Remove(99) ||
		r.Record(ports.OutputProtection{}) || r.Record(lockProof(1, 1)) ||
		r.BackendBarrier(0) || r.BackendBarrier(1) || r.CaptureBarrier(0) || r.CaptureBarrier(1) {
		t.Fatal("zero/disengaged ledger accepted invalid input")
	}
	requireLockReady(t, &r, false)
}

func TestLockReadinessEmptyInventoryNeedsBothBarriers(t *testing.T) {
	for _, backendFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("backend-first=%v", backendFirst), func(t *testing.T) {
			var r Readiness
			requireLockAccepted(t, r.Begin(7))
			requireLockReady(t, &r, false)
			if backendFirst {
				requireLockAccepted(t, r.BackendBarrier(7))
			} else {
				requireLockAccepted(t, r.CaptureBarrier(7))
			}
			requireLockReady(t, &r, false)
			if backendFirst {
				requireLockAccepted(t, r.CaptureBarrier(7))
			} else {
				requireLockAccepted(t, r.BackendBarrier(7))
			}
			requireLockReady(t, &r, true)
		})
	}
}

func TestLockReadinessEveryPrerequisiteInAnyOrder(t *testing.T) {
	// Two output proofs and two independent barriers: all 24 arrival orders.
	for a := range 4 {
		for b := range 4 {
			for c := range 4 {
				for d := range 4 {
					order := [4]int{a, b, c, d}
					if a == b || a == c || a == d || b == c || b == d || c == d {
						continue
					}
					t.Run(fmt.Sprint(order), func(t *testing.T) {
						var r Readiness
						requireLockAccepted(t, r.Add(11))
						requireLockAccepted(t, r.Add(12))
						requireLockAccepted(t, r.Begin(5))
						requireLockReady(t, &r, false)
						for i, step := range order {
							var accepted bool
							switch step {
							case 0:
								accepted = r.Record(lockProof(5, 11))
							case 1:
								accepted = r.Record(ports.OutputProtection{Generation: 5, Instance: 12, Kind: ports.ProtectionInactiveOutput})
							case 2:
								accepted = r.BackendBarrier(5)
							case 3:
								accepted = r.CaptureBarrier(5)
							}
							requireLockAccepted(t, accepted)
							requireLockReady(t, &r, i == 3)
						}
						// Duplicate evidence is idempotent.
						requireLockAccepted(t, r.Record(lockProof(5, 11)))
						requireLockAccepted(t, r.BackendBarrier(5))
						requireLockAccepted(t, r.CaptureBarrier(5))
						requireLockReady(t, &r, true)
					})
				}
			}
		}
	}
}

func TestLockReadinessGenerationFencing(t *testing.T) {
	var r Readiness
	requireLockAccepted(t, r.Add(1))
	requireLockAccepted(t, r.Begin(10))
	requireLockAccepted(t, r.Record(lockProof(10, 1)))
	requireLockAccepted(t, r.BackendBarrier(10))
	requireLockAccepted(t, r.CaptureBarrier(10))
	requireLockReady(t, &r, true)
	for _, generation := range []ports.LockGeneration{0, 9, 10} {
		if r.Begin(generation) {
			t.Fatalf("accepted non-increasing generation %d", generation)
		}
		requireLockReady(t, &r, true)
	}

	// A newer Begin while engaged clears both barriers and every proof.
	requireLockAccepted(t, r.Begin(11))
	requireLockReady(t, &r, false)
	for _, generation := range []ports.LockGeneration{0, 9, 10, 12} {
		if r.Record(lockProof(generation, 1)) || r.BackendBarrier(generation) || r.CaptureBarrier(generation) || r.End(generation) {
			t.Fatalf("accepted wrong-generation evidence/End for %d", generation)
		}
	}
	requireLockAccepted(t, r.BackendBarrier(11))
	requireLockAccepted(t, r.CaptureBarrier(11))
	requireLockReady(t, &r, false) // old proof was cleared
	requireLockAccepted(t, r.Record(lockProof(11, 1)))
	requireLockReady(t, &r, true)
	requireLockAccepted(t, r.End(11))
	requireLockReady(t, &r, false)
	if r.Record(lockProof(11, 1)) || r.BackendBarrier(11) || r.CaptureBarrier(11) || r.End(11) || r.Begin(11) || r.Begin(10) {
		t.Fatal("ended round accepted late evidence or reused generation")
	}

	requireLockAccepted(t, r.Begin(13)) // intervening gate release may consume 12
	if r.End(11) || r.Record(lockProof(11, 1)) || r.BackendBarrier(11) || r.CaptureBarrier(11) {
		t.Fatal("late old round affected newer round")
	}
	requireLockAccepted(t, r.Record(lockProof(13, 1)))
	requireLockReady(t, &r, false) // neither old barrier survived End/Begin
	requireLockAccepted(t, r.BackendBarrier(13))
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.CaptureBarrier(13))
	requireLockReady(t, &r, true)
}

func TestLockReadinessGenerationExhaustionDoesNotWrap(t *testing.T) {
	var r Readiness
	maxGeneration := ports.LockGeneration(^uint64(0))
	requireLockAccepted(t, r.Begin(maxGeneration))
	requireLockAccepted(t, r.End(maxGeneration))
	for _, generation := range []ports.LockGeneration{0, 1, maxGeneration} {
		if r.Begin(generation) {
			t.Fatalf("accepted wrapped/reused generation %d", generation)
		}
	}
	requireLockReady(t, &r, false)
}

func TestLockReadinessInvalidProofsDoNotCountOrEraseEvidence(t *testing.T) {
	invalid := []ports.OutputProtection{
		{},
		{Generation: 1, Instance: 1},
		{Generation: 1, Instance: 1, Kind: "submitted-frame"},
		{Generation: 1, Instance: 1, Kind: "error-gone"},
		{Generation: 1, Instance: 0, Kind: ports.ProtectionProtectedFrame},
		{Generation: 1, Instance: 99, Kind: ports.ProtectionProtectedFrame},
		{Generation: 0, Instance: 1, Kind: ports.ProtectionProtectedFrame},
		{Generation: 2, Instance: 1, Kind: ports.ProtectionProtectedFrame},
	}
	var r Readiness
	requireLockAccepted(t, r.Add(1))
	requireLockAccepted(t, r.Begin(1))
	requireLockAccepted(t, r.BackendBarrier(1))
	requireLockAccepted(t, r.CaptureBarrier(1))
	for _, proof := range invalid {
		if r.Record(proof) {
			t.Fatalf("accepted invalid proof %+v", proof)
		}
		requireLockReady(t, &r, false)
	}
	requireLockAccepted(t, r.Record(lockProof(1, 1)))
	for _, proof := range invalid {
		if r.Record(proof) {
			t.Fatalf("accepted invalid proof %+v", proof)
		}
		requireLockReady(t, &r, true)
	}
}

func TestLockReadinessOutputInventoryChanges(t *testing.T) {
	var r Readiness
	requireLockAccepted(t, r.Add(1))
	requireLockAccepted(t, r.Add(2))
	requireLockAccepted(t, r.Remove(2)) // registration and retirement before Begin
	if r.Add(2) || r.Remove(2) || r.Add(1) {
		t.Fatal("accepted duplicate or retired lifetime")
	}
	requireLockAccepted(t, r.Begin(1))
	requireLockAccepted(t, r.Add(3)) // new output before Ready
	requireLockAccepted(t, r.Record(lockProof(1, 1)))
	requireLockAccepted(t, r.BackendBarrier(1))
	requireLockAccepted(t, r.CaptureBarrier(1))
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.Record(lockProof(1, 3)))
	requireLockReady(t, &r, true)
	if r.Add(0) || r.Add(3) || r.Remove(99) {
		t.Fatal("accepted invalid inventory operation")
	}
	requireLockReady(t, &r, true)    // duplicate registration did not erase proof
	requireLockAccepted(t, r.Add(4)) // hotplug after Ready must revoke it
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.Remove(4)) // caller-confirmed retired lifetime
	requireLockReady(t, &r, true)

	// The same output name can return, but the new lifetime is instance 5.
	requireLockAccepted(t, r.Remove(3))
	requireLockAccepted(t, r.Add(5))
	if r.Record(lockProof(1, 3)) || r.Add(3) {
		t.Fatal("replacement accepted old-lifetime evidence")
	}
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.Record(lockProof(1, 5)))
	requireLockReady(t, &r, true)

	requireLockAccepted(t, r.End(1))
	requireLockAccepted(t, r.Add(6)) // changes after End join next snapshot
	requireLockAccepted(t, r.Remove(1))
	requireLockAccepted(t, r.Begin(2))
	requireLockAccepted(t, r.BackendBarrier(2))
	requireLockAccepted(t, r.CaptureBarrier(2))
	requireLockAccepted(t, r.Record(lockProof(2, 5)))
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.Record(lockProof(2, 6)))
	requireLockReady(t, &r, true)
	if r.Add(1) || r.Add(2) || r.Add(3) || r.Add(4) {
		t.Fatal("retired lifetime reused across lock rounds")
	}
}

func TestLockReadinessInvalidationRequiresFreshProof(t *testing.T) {
	var r Readiness
	requireLockAccepted(t, r.Add(1))
	requireLockAccepted(t, r.Begin(7))
	requireLockAccepted(t, r.Record(lockProof(7, 1)))
	requireLockAccepted(t, r.BackendBarrier(7))
	requireLockAccepted(t, r.CaptureBarrier(7))
	requireLockReady(t, &r, true)
	if r.Invalidate(6, 1) || r.Invalidate(7, 2) {
		t.Fatal("invalid invalidation accepted")
	}
	requireLockReady(t, &r, true)
	requireLockAccepted(t, r.Invalidate(7, 1))
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.Record(lockProof(7, 1)))
	requireLockReady(t, &r, true)
	requireLockAccepted(t, r.End(7))
	if r.Invalidate(7, 1) {
		t.Fatal("ended invalidation accepted")
	}
}

func TestLockReadinessInvalidationIsolatedAndRetired(t *testing.T) {
	var r Readiness
	requireLockAccepted(t, r.Add(1))
	requireLockAccepted(t, r.Add(2))
	requireLockAccepted(t, r.Add(3))
	requireLockAccepted(t, r.Remove(3))
	requireLockAccepted(t, r.Begin(7))
	requireLockAccepted(t, r.Record(lockProof(7, 1)))
	requireLockAccepted(t, r.Record(lockProof(7, 2)))
	requireLockAccepted(t, r.BackendBarrier(7))
	requireLockAccepted(t, r.CaptureBarrier(7))
	if r.Invalidate(8, 1) || r.Invalidate(7, 3) {
		t.Fatal("future or retired invalidation accepted")
	}
	requireLockReady(t, &r, true)
	requireLockAccepted(t, r.Invalidate(7, 1))
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.Record(lockProof(7, 1)))
	requireLockReady(t, &r, true) // output 2 and both barriers retained
}

func TestLockReadinessRemovingLastOutputCannotReplaceBackendACK(t *testing.T) {
	var r Readiness
	requireLockAccepted(t, r.Add(1))
	requireLockAccepted(t, r.Begin(1))
	requireLockAccepted(t, r.CaptureBarrier(1))
	requireLockAccepted(t, r.Remove(1))
	requireLockReady(t, &r, false)
	requireLockAccepted(t, r.BackendBarrier(1))
	requireLockReady(t, &r, true)
}
