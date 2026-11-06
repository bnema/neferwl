package xkb

import (
	"maps"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestTransactionRefusedModifier(t *testing.T) {
	k := newTest(t, "us")
	tx, err := k.Begin()
	if err != nil {
		t.Fatal(err)
	}
	k.Key(125, true, 0)
	tx.Rollback()
	if ev := k.Key(16, true, 0); ev.Mods&domain.ModSuper != 0 {
		t.Fatalf("refused Super leaked: %+v", ev)
	}
}

func TestTransactionCommitAndReleaseAccounting(t *testing.T) {
	k := newTest(t, "us")
	k.Key(42, true, 0)
	k.Key(54, true, 0)
	k.Key(58, true, 0)
	k.Key(58, false, 0)
	tx, err := k.Begin()
	if err != nil {
		t.Fatal(err)
	}
	k.Key(42, false, 0)
	tx.Commit()
	if ev := k.Key(16, true, 0); ev.Mods&domain.ModShift == 0 {
		t.Fatalf("other Shift lost: %+v", ev)
	}
	k.Key(16, false, 0)
	k.Key(54, false, 0)
	if ev := k.Key(16, true, 0); ev.Mods != 0 || ev.Keysym != "Q" {
		t.Fatalf("release accounting or CapsLock lost: %+v", ev)
	}
}

func TestTransactionRollbackSecurityEpoch(t *testing.T) {
	k := newTest(t, "us")
	old := domain.SecurityState{Generation: 1}
	secureKey(t, k, 125, true, old)
	held, quarantined := maps.Clone(k.held), maps.Clone(k.quarantined)
	tx, err := k.Begin()
	if err != nil {
		t.Fatal(err)
	}
	secureKey(t, k, 16, true, domain.SecurityState{Generation: 2, Protected: true})
	tx.Rollback()
	if k.epoch != old || !k.epochSet || !maps.Equal(k.held, held) || !maps.Equal(k.quarantined, quarantined) {
		t.Fatal("security tracking not restored")
	}
	if ev := secureKey(t, k, 16, true, old); ev.Mods&domain.ModSuper == 0 {
		t.Fatalf("original state lost: %+v", ev)
	}
}

func TestTransactionPreservesLayout(t *testing.T) {
	k, err := New(RMLVO{Layout: "us,fr", Options: "grp:alt_shift_toggle"})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Key(56, true, 0)
	k.Key(42, true, 0)
	k.Key(42, false, 0)
	k.Key(56, false, 0)
	before := k.Key(16, true, 0)
	k.Key(16, false, 0)
	if before.State.Group == 0 {
		t.Fatal("layout did not switch")
	}
	tx, err := k.Begin()
	if err != nil {
		t.Fatal(err)
	}
	after := k.Key(16, true, 0)
	tx.Commit()
	if after.State != before.State || after.Keysym != before.Keysym {
		t.Fatalf("layout changed: %+v -> %+v", before, after)
	}
}

func TestTransactionRollbackQuarantine(t *testing.T) {
	k := newTest(t, "us")
	secureKey(t, k, 125, true, domain.SecurityState{Generation: 1})
	epoch := domain.SecurityState{Generation: 2}
	secureKey(t, k, 30, true, epoch)
	tx, err := k.Begin()
	if err != nil {
		t.Fatal(err)
	}
	suppressedKey(t, k, 125, false, epoch)
	tx.Rollback()
	suppressedKey(t, k, 125, true, epoch)
	suppressedKey(t, k, 125, false, epoch)
	if ev := secureKey(t, k, 16, true, epoch); ev.Mods != 0 {
		t.Fatalf("quarantined modifier leaked: %+v", ev)
	}
}

func TestTransactionPreservesLatchedMasks(t *testing.T) {
	k := newTest(t, "us,fr")
	k.updateMask(k.state, 0, 1, 2, 0, 1, 0)
	tx, err := k.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if k.serializeMods(k.state, modsLatched) != 1 || k.serializeMods(k.state, modsLocked) != 2 || k.serializeLayout(k.state, layoutLatched) != 1 {
		t.Fatal("latched or locked components lost")
	}
	tx.Commit()
}
