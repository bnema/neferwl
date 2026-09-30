package drm

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestSecurityFinalCommitSnapshotRejectsEngage(t *testing.T) {
	o, _, commits := testOutput(t)
	var state atomic.Uint64
	securityGate(t, o, &state)
	// The frame was admitted in unlocked generation zero; before the final
	// commit boundary a lock engages. No client fence is handed to KMS.
	state.Store(3)
	err := o.commitWith(70, nil, true, true, pendingFrame{security: ports.SecurityState{}}, overlayWin{})
	if !errors.Is(err, errSecurityScene) || len(*commits) != 0 || !o.protected {
		t.Fatalf("obsolete commit admitted: %v commits=%d", err, len(*commits))
	}
}
