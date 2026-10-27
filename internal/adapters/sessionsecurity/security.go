// Package sessionsecurity supplies a coherent defensive session gate to
// boundaries outside the Wayland display goroutine. It owns no window state.
package sessionsecurity

import (
	"errors"
	"math"
	"sync/atomic"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

var (
	ErrGenerationExhausted = errors.New("session security generation exhausted")
	ErrStaleRelease        = errors.New("session security release does not own protection")
)

// Gate's zero value is an unlocked session. The packed atomic prevents readers
// from observing a generation from one transition and protection from another.
// Only the display owner calls Engage and Release.
type Gate struct{ state atomic.Uint64 }

func unpack(state uint64) domain.SecurityState {
	return domain.SecurityState{Generation: domain.LockGeneration(state >> 1), Protected: state&1 != 0}
}

func (g *Gate) Snapshot() domain.SecurityState { return unpack(g.state.Load()) }

// Engage creates a new protected generation, including orphan-owner takeover.
func (g *Gate) Engage() (domain.SecurityState, error) {
	for {
		old := g.state.Load()
		if old>>1 == math.MaxUint64>>1 {
			return unpack(old), ErrGenerationExhausted
		}
		next := ((old>>1)+1)<<1 | 1
		if g.state.CompareAndSwap(old, next) {
			return unpack(next), nil
		}
	}
}

func (g *Gate) Release(expected domain.LockGeneration) (domain.SecurityState, error) {
	for {
		old := g.state.Load()
		state := unpack(old)
		if !state.Protected || state.Generation != expected {
			return state, ErrStaleRelease
		}
		// Reserve the final generation for protection. Publishing it unlocked
		// would make every later Engage fail without being able to protect.
		if old>>1 >= (math.MaxUint64>>1)-1 {
			return state, ErrGenerationExhausted
		}
		next := ((old >> 1) + 1) << 1
		if g.state.CompareAndSwap(old, next) {
			return unpack(next), nil
		}
	}
}

var _ ports.SessionSecurityController = (*Gate)(nil)
