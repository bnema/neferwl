package xkb

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func secureKey(t *testing.T, k *Keymap, code uint32, down bool, state domain.SecurityState) ports.KeyEvent {
	t.Helper()
	ev, deliver, err := k.KeySecure(code, down, 123, state)
	if err != nil || !deliver {
		t.Fatalf("key %d down=%v: deliver=%v err=%v", code, down, deliver, err)
	}
	return ev
}

func suppressedKey(t *testing.T, k *Keymap, code uint32, down bool, state domain.SecurityState) {
	t.Helper()
	ev, deliver, err := k.KeySecure(code, down, 123, state)
	if err != nil || deliver || ev != (ports.KeyEvent{}) {
		t.Fatalf("inherited key %d down=%v leaked: %+v deliver=%v err=%v", code, down, ev, deliver, err)
	}
}

func TestSecurityEpochQuarantinesHeldKeysAcrossAcquireAndRelease(t *testing.T) {
	k := newTest(t, "us")
	initial := domain.SecurityState{}
	locked := domain.SecurityState{Generation: 1, Protected: true}
	unlocked := domain.SecurityState{Generation: 2}
	for _, code := range []uint32{125, 42, 29, 56, 30} {
		secureKey(t, k, code, true, initial)
	}
	for _, code := range []uint32{125, 42, 29, 56, 30} {
		suppressedKey(t, k, code, true, locked) // repeats cannot reintroduce held modifiers
	}
	if ev := secureKey(t, k, 16, true, locked); ev.Keysym != "q" || ev.Mods != 0 || ev.State != (domain.ModState{}) {
		t.Fatalf("password inherited native state: %+v", ev)
	}
	secureKey(t, k, 16, false, locked)
	secureKey(t, k, 54, true, locked) // password-era right Shift
	if ev := secureKey(t, k, 16, true, unlocked); ev.Keysym != "q" || ev.Mods != 0 || ev.State != (domain.ModState{}) {
		t.Fatalf("desktop inherited password/native state: %+v", ev)
	}
	secureKey(t, k, 16, false, unlocked)
	for _, code := range []uint32{30, 56, 29, 42, 125, 54} {
		suppressedKey(t, k, code, false, unlocked)
	}
	secureKey(t, k, 125, true, unlocked)
	if ev := secureKey(t, k, 16, true, unlocked); ev.Mods&domain.ModSuper == 0 || ev.State.Depressed == 0 {
		t.Fatalf("fresh desktop Super broken: %+v", ev)
	}
}

func TestSecurityEpochResetsLocksButFreshCapsAndNumLockWork(t *testing.T) {
	k := newTest(t, "us")
	initial := domain.SecurityState{}
	locked := domain.SecurityState{Generation: 1, Protected: true}
	for _, code := range []uint32{58, 69} {
		secureKey(t, k, code, true, initial)
		secureKey(t, k, code, false, initial)
	}
	if ev := secureKey(t, k, 30, true, initial); ev.Keysym != "A" || ev.State.Locked == 0 {
		t.Fatalf("baseline caps: %+v", ev)
	}
	secureKey(t, k, 30, false, initial)
	if ev := secureKey(t, k, 30, true, locked); ev.Keysym != "a" || ev.State.Locked != 0 {
		t.Fatalf("old locks crossed epoch: %+v", ev)
	}
	secureKey(t, k, 30, false, locked)
	secureKey(t, k, 58, true, locked)
	secureKey(t, k, 58, false, locked)
	if ev := secureKey(t, k, 30, true, locked); ev.Keysym != "A" || ev.State.Locked == 0 {
		t.Fatalf("fresh protected caps broken: %+v", ev)
	}
	secureKey(t, k, 30, false, locked)
	secureKey(t, k, 69, true, locked)
	secureKey(t, k, 69, false, locked)
	if ev := secureKey(t, k, 79, true, locked); ev.Keysym != "KP_1" || ev.State.Locked == 0 {
		t.Fatalf("fresh protected numlock broken: %+v", ev)
	}
}

func TestSecurityEpochUsesActualRemappedModifiers(t *testing.T) {
	k, err := New(RMLVO{Layout: "us", Options: "ctrl:swapcaps"})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	initial, locked := domain.SecurityState{}, domain.SecurityState{Generation: 1, Protected: true}
	secureKey(t, k, 58, true, initial) // physical Caps is Control in this map
	if ev := secureKey(t, k, 16, true, locked); ev.Mods != 0 || ev.State != (domain.ModState{}) {
		t.Fatalf("remapped inherited Control leaked: %+v", ev)
	}
	secureKey(t, k, 16, false, locked)
	suppressedKey(t, k, 58, false, locked)
	secureKey(t, k, 58, true, locked)
	if ev := secureKey(t, k, 16, true, locked); ev.Mods&domain.ModCtrl == 0 || ev.State.Depressed == 0 {
		t.Fatalf("fresh remapped Control broken: %+v", ev)
	}
	secureKey(t, k, 16, false, locked)
	secureKey(t, k, 58, false, locked)
	secureKey(t, k, 29, true, locked) // physical Control is Caps in this map
	secureKey(t, k, 29, false, locked)
	if ev := secureKey(t, k, 30, true, locked); ev.Keysym != "A" || ev.State.Locked == 0 {
		t.Fatalf("fresh remapped Caps broken: %+v", ev)
	}
}

func TestSecurityEpochAltGrAndAltReleaseOrder(t *testing.T) {
	for _, releases := range [][]uint32{{100, 56}, {56, 100}} {
		k := newTest(t, "fr")
		locked := domain.SecurityState{Generation: 1, Protected: true}
		secureKey(t, k, 100, true, locked) // ISO_Level3_Shift, not guessed Alt
		secureKey(t, k, 56, true, locked)
		if ev := secureKey(t, k, 11, true, locked); ev.Keysym != "at" || ev.Mods&domain.ModAlt == 0 || ev.State.Depressed == 0 {
			t.Fatalf("fresh AltGr+Alt not mapped by XKB: %+v", ev)
		}
		secureKey(t, k, 11, false, locked)
		for _, code := range releases {
			secureKey(t, k, code, false, locked)
		}
		if ev := secureKey(t, k, 16, true, locked); ev.Keysym != "a" || ev.Mods != 0 || ev.State.Depressed != 0 {
			t.Fatalf("release order left modifiers: %+v", ev)
		}
	}
}

func TestKeymapReplacementRetainsQuarantine(t *testing.T) {
	previous := newTest(t, "us")
	locked := domain.SecurityState{Generation: 1, Protected: true}
	secureKey(t, previous, 125, true, locked)
	next := newTest(t, "fr")
	next.QuarantineFrom(previous)
	suppressedKey(t, next, 125, true, locked)
	if ev := secureKey(t, next, 16, true, locked); ev.Keysym != "a" || ev.Mods != 0 || ev.State.Depressed != 0 {
		t.Fatalf("replacement replayed old held state: %+v", ev)
	}
	suppressedKey(t, next, 125, false, locked)
	secureKey(t, next, 125, true, locked)
	if ev := secureKey(t, next, 30, true, locked); ev.Mods&domain.ModSuper == 0 {
		t.Fatalf("fresh key after replacement broken: %+v", ev)
	}
}

func TestSecurityStateChangeResetsAltGrAndAltWithoutMaskInference(t *testing.T) {
	k := newTest(t, "fr")
	initial := domain.SecurityState{}
	locked := domain.SecurityState{Generation: 1, Protected: true}
	secureKey(t, k, 100, true, initial)
	secureKey(t, k, 56, true, initial)
	if ev := secureKey(t, k, 11, true, locked); ev.Keysym != "agrave" || ev.Mods != 0 || ev.State != (domain.ModState{}) {
		t.Fatalf("inherited AltGr/Alt crossed acquisition: %+v", ev)
	}
	secureKey(t, k, 11, false, locked)
	suppressedKey(t, k, 56, false, locked)
	suppressedKey(t, k, 100, false, locked)
	secureKey(t, k, 100, true, locked)
	// Full state comparison is conservative even if a caller changes the
	// protection bit without advancing the generation.
	changed := locked
	changed.Protected = false
	if ev := secureKey(t, k, 11, true, changed); ev.Keysym != "agrave" || ev.Mods != 0 || ev.State != (domain.ModState{}) {
		t.Fatalf("state change missed native reset: %+v", ev)
	}
	suppressedKey(t, k, 100, false, changed)
}

func TestResetSeatDropsModifiersAndSuppressesHeldReleases(t *testing.T) {
	k := newTest(t, "us")
	state := domain.SecurityState{Generation: 3}
	secureKey(t, k, 125, true, state)
	if _, err := k.ResetSeat(); err != nil {
		t.Fatal(err)
	}
	if k.epoch != state || !k.epochSet {
		t.Fatalf("epoch changed by ResetSeat: %+v set=%v", k.epoch, k.epochSet)
	}
	if ev := secureKey(t, k, 106, true, state); ev.Mods != 0 || ev.State != (domain.ModState{}) {
		t.Fatalf("Super survived the reset: %+v", ev)
	}
	secureKey(t, k, 106, false, state)
	suppressedKey(t, k, 125, false, state)
	// A key pressed after the reset is not suppressed on release.
	secureKey(t, k, 125, true, state)
	if ev := secureKey(t, k, 106, true, state); ev.Mods&domain.ModSuper == 0 {
		t.Fatalf("fresh Super lost: %+v", ev)
	}
}

func TestResetSeatKeepsLockedModifiers(t *testing.T) {
	k := newTest(t, "us")
	state := domain.SecurityState{}
	secureKey(t, k, 58, true, state)
	secureKey(t, k, 58, false, state)
	secureKey(t, k, 125, true, state)
	kept, err := k.ResetSeat()
	if err != nil {
		t.Fatal(err)
	}
	if kept.Locked == 0 || kept.Depressed != 0 {
		t.Fatalf("ResetSeat state %+v: want CapsLock locked, nothing depressed", kept)
	}
	ev := secureKey(t, k, 30, true, state)
	if ev.State.Locked == 0 || ev.State.Depressed != 0 || ev.Mods != 0 || ev.Keysym != "A" {
		t.Fatalf("CapsLock lost or Super kept: %+v", ev)
	}
}
