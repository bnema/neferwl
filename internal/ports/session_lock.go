package ports

// OutputInstance identifies one output lifetime, not its connector or name.
// Instances are nonzero and unique for the compositor's lifetime; reconnecting
// or replacing an output requires a new instance even if its name is unchanged.
type OutputInstance uint64

// ProtectionKind describes affirmative evidence of output protection.
// The zero value and all values other than the named kinds are invalid.
type ProtectionKind string

const (
	// ProtectionProtectedFrame confirms a protected frame is presented, not
	// merely queued or submitted for rendering.
	ProtectionProtectedFrame ProtectionKind = "protected-frame"
	// ProtectionInactiveOutput confirms the output is actually inactive, not
	// that rendering failed or that its disappearance was inferred from an error.
	ProtectionInactiveOutput ProtectionKind = "inactive-output"
)

// OutputProtection is affirmative protection evidence for one output lifetime
// in exactly one security generation. It does not acknowledge backend lease
// drainage/security handoff or completion of admitted prelock capture copies;
// those are separate barriers.
type OutputProtection struct {
	Generation LockGeneration
	Instance   OutputInstance
	Kind       ProtectionKind
}
