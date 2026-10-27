package core

import (
	"errors"

	"github.com/bnema/neferwl/internal/domain"
)

// LeaseGuard checks the security snapshots surrounding native lease creation.
// Callers must also check the initial snapshot before creating a lease. If this
// check fails after creation, the adapter closes the FD and revokes the lease
// before proceeding with a backend barrier. Any generation change invalidates
// the lease, including a complete protect/unprotect cycle during acquisition.
func LeaseGuard(before, after domain.SecurityState) error {
	if before.Protected {
		return errors.New("session protected")
	}
	if after.Protected || after.Generation != before.Generation {
		return errors.New("session transition during lease")
	}
	return nil
}
