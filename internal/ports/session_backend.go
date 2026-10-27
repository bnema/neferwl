package ports

import "github.com/bnema/neferwl/internal/domain"

// SecurityBackendEvent carries authoritative backend inventory and affirmative
// evidence to the display owner. Unlike OutputPresented, it is never merged or
// dropped. Output lifetimes register before starting a renderer.
type SecurityBackendEvent interface{ securityBackendEvent() }

type SecurityOutputAdded struct {
	Instance domain.OutputInstance
	Output   string
}

func (SecurityOutputAdded) securityBackendEvent() {}

// SecurityOutputRemoved means the connector is confirmed disconnected, or an
// output lifetime was shut down with a successful inactive KMS transaction.
// Stopping a renderer, failed KMS, timeout or DRM master loss do not qualify.
type SecurityOutputRemoved struct{ Instance domain.OutputInstance }

func (SecurityOutputRemoved) securityBackendEvent() {}

// SecurityOutputInvalidated revokes prior physical proof on master loss,
// resume or recovery, without treating an output error as disconnection.
type SecurityOutputInvalidated struct {
	Generation domain.LockGeneration
	Instance   domain.OutputInstance
}

func (SecurityOutputInvalidated) securityBackendEvent() {}

type SecurityOutputProof struct{ Proof domain.OutputProtection }

func (SecurityOutputProof) securityBackendEvent() {}

// SecurityBackendBarrier acknowledges ordered inventory and actual lease
// revocation for this round. Errors are diagnostics, never readiness evidence.
type SecurityBackendBarrier struct {
	Generation domain.LockGeneration
	Err        error
}

func (SecurityBackendBarrier) securityBackendEvent() {}
