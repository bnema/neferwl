package xkb

import (
	"errors"
	"maps"

	"github.com/bnema/neferwl/internal/domain"
)

// Transaction retains the original native state while its Keymap translates
// tentatively. Finish it before closing/replacing the map. Like Keymap, it is
// owned by one producer; concurrent access and nested transactions are invalid.
type Transaction struct {
	k                       *Keymap
	state                   uintptr
	epoch                   domain.SecurityState
	epochSet                bool
	held, quarantined, down map[uint32]bool
}

// Begin starts a tentative translation on an independent native state. Replaying
// currently depressed keys preserves XKB's per-key modifier accounting; masks
// then restore all depressed, latched and locked modifier/layout components.
// The original state is untouched, so rollback is exact even across epoch resets.
func (k *Keymap) Begin() (*Transaction, error) {
	if k.transaction {
		return nil, errors.New("xkb transaction already active")
	}
	next := k.stateNew(k.mapPtr)
	if next == 0 {
		return nil, errors.New("xkb_state_new failed")
	}
	for code := range k.down {
		k.updateKey(next, code+8, 1)
	}
	k.updateMask(next,
		k.serializeMods(k.state, modsDepressed), k.serializeMods(k.state, modsLatched), k.serializeMods(k.state, modsLocked),
		k.serializeLayout(k.state, layoutDepressed), k.serializeLayout(k.state, layoutLatched), k.serializeLayout(k.state, layoutLocked))
	tx := &Transaction{k: k, state: k.state, epoch: k.epoch, epochSet: k.epochSet, held: k.held, quarantined: k.quarantined, down: k.down}
	k.state = next
	k.held, k.quarantined, k.down = maps.Clone(k.held), maps.Clone(k.quarantined), maps.Clone(k.down)
	k.transaction = true
	return tx, nil
}

// Commit keeps the tentative state. Calling either finish method again is safe.
func (tx *Transaction) Commit() {
	if tx.k == nil {
		return
	}
	tx.k.stateUnref(tx.state)
	tx.k.transaction = false
	tx.k = nil
}

// Rollback discards tentative native and security tracking changes.
func (tx *Transaction) Rollback() {
	if tx.k == nil {
		return
	}
	k := tx.k
	k.stateUnref(k.state)
	k.state = tx.state
	k.epoch, k.epochSet = tx.epoch, tx.epochSet
	k.held, k.quarantined, k.down = tx.held, tx.quarantined, tx.down
	k.transaction = false
	tx.k = nil
}
