package sessionlock

import "github.com/bnema/neferwl/internal/ports"

// Readiness is pure readiness bookkeeping owned by one goroutine. Its zero
// value is an empty, disengaged ledger. It must not be copied after first use;
// it owns its inventory and never exposes mutable state.
//
// Readiness is not authentication or protocol policy. All acknowledgements
// must be affirmative evidence from the respective owner, never an inference
// from a failed operation.
type Readiness struct {
	generation ports.LockGeneration // high-water mark, retained after End
	active     bool
	backend    bool
	capture    bool
	outputs    map[ports.OutputInstance]lockOutputReadiness
}

type lockOutputReadiness struct {
	registered bool
	proof      ports.ProtectionKind
}

// Add registers a new output lifetime before or during a lock. Zero, duplicate
// and retired IDs are rejected without changing state. A new output has no
// proof, so adding it while engaged revokes Ready until it is protected. IDs
// remain as tombstones after Remove to prevent accepting a replaced lifetime.
func (r *Readiness) Add(instance ports.OutputInstance) bool {
	if instance == 0 {
		return false
	}
	if _, seen := r.outputs[instance]; seen {
		return false
	}
	if r.outputs == nil {
		r.outputs = make(map[ports.OutputInstance]lockOutputReadiness)
	}
	r.outputs[instance] = lockOutputReadiness{registered: true}
	return true
}

// Remove retires a lifetime only when the caller has confirmed it is actually
// disconnected or inactive. An error (including "gone") is not confirmation.
// An inactive lifetime that may resume should instead remain registered with
// an inactive-output proof. Unknown or already retired instances are rejected.
func (r *Readiness) Remove(instance ports.OutputInstance) bool {
	output, ok := r.outputs[instance]
	if !ok || !output.registered {
		return false
	}
	r.outputs[instance] = lockOutputReadiness{}
	return true
}

// Begin snapshots the registered inventory into a new readiness round by
// clearing every proof and both barriers. Registrations may subsequently
// change. Generations must increase strictly, even across End; an invalid
// Begin leaves the current round untouched. Exhaustion must not wrap to zero.
func (r *Readiness) Begin(generation ports.LockGeneration) bool {
	if generation == 0 || generation <= r.generation {
		return false
	}
	r.generation, r.active = generation, true
	r.backend, r.capture = false, false
	for instance, output := range r.outputs {
		output.proof = ""
		r.outputs[instance] = output
	}
	return true
}

// End clears only the exact active round, retaining inventory and the
// generation high-water mark. Late End calls cannot cancel a newer round, and
// late proofs or barriers cannot reactivate an ended round.
func (r *Readiness) End(generation ports.LockGeneration) bool {
	if !r.accepts(generation) {
		return false
	}
	r.active, r.backend, r.capture = false, false, false
	for instance, output := range r.outputs {
		output.proof = ""
		r.outputs[instance] = output
	}
	return true
}

// Record accepts only affirmative protection evidence for a registered
// lifetime in the exact active generation. Repeated valid proofs are harmless;
// invalid evidence never overwrites an existing proof.
func (r *Readiness) Record(proof ports.OutputProtection) bool {
	if !r.accepts(proof.Generation) {
		return false
	}
	switch proof.Kind {
	case ports.ProtectionProtectedFrame, ports.ProtectionInactiveOutput:
	default:
		return false
	}
	output, ok := r.outputs[proof.Instance]
	if !ok || !output.registered {
		return false
	}
	output.proof = proof.Kind
	r.outputs[proof.Instance] = output
	return true
}

// Invalidate revokes a prior proof within the active epoch. A recovered or
// resumed lifetime must provide fresh affirmative protection, never reuse its
// earlier presentation. Unknown, retired and stale epochs have no effect.
func (r *Readiness) Invalidate(generation ports.LockGeneration, instance ports.OutputInstance) bool {
	if !r.accepts(generation) {
		return false
	}
	output, ok := r.outputs[instance]
	if !ok || !output.registered {
		return false
	}
	output.proof = ""
	r.outputs[instance] = output
	return true
}

// BackendBarrier acknowledges authoritative output inventory, drainage of all
// backend leases, and completed security handoff for the active generation.
// This is required even with zero outputs; an empty local inventory is not an
// acknowledgement. It does not acknowledge capture memory-copy completion.
func (r *Readiness) BackendBarrier(generation ports.LockGeneration) bool {
	if !r.accepts(generation) {
		return false
	}
	r.backend = true
	return true
}

// CaptureBarrier acknowledges that all admitted prelock memory-copy work,
// including writes to capture FDs, has completed for the active generation.
// Output protection and backend drainage cannot substitute for this barrier.
func (r *Readiness) CaptureBarrier(generation ports.LockGeneration) bool {
	if !r.accepts(generation) {
		return false
	}
	r.capture = true
	return true
}

// Ready requires an active round, both independent barriers, and affirmative
// protection evidence for every currently registered output lifetime.
func (r *Readiness) Ready() bool {
	if !r.active || !r.backend || !r.capture {
		return false
	}
	for _, output := range r.outputs {
		if output.registered && output.proof == "" {
			return false
		}
	}
	return true
}

func (r *Readiness) accepts(generation ports.LockGeneration) bool {
	return r.active && generation != 0 && generation == r.generation
}
