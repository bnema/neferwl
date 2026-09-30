package xkb

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func secureKey(t *testing.T, k *Keymap, code uint32, down bool, state ports.SecurityState) ports.KeyEvent {
	t.Helper()
	ev, deliver, err := k.KeySecure(code, down, 123, state)
	if err != nil || !deliver {
		t.Fatalf("key %d down=%v: deliver=%v err=%v", code, down, deliver, err)
	}
	return ev
}

func suppressedKey(t *testing.T, k *Keymap, code uint32, down bool, state ports.SecurityState) {
	t.Helper()
	ev, deliver, err := k.KeySecure(code, down, 123, state)
	if err != nil || deliver || ev != (ports.KeyEvent{}) {
		t.Fatalf("inherited key %d down=%v leaked: %+v deliver=%v err=%v", code, down, ev, deliver, err)
	}
}

func TestSecurityEpochQuarantinesHeldKeysAcrossAcquireAndRelease(t *testing.T) {
	k := newTest(t, "us")
	initial := ports.SecurityState{}
	locked := ports.SecurityState{Generation: 1, Protected: true}
	unlocked := ports.SecurityState{Generation: 2}
	for _, code := range []uint32{125, 42, 29, 56, 30} {
		secureKey(t, k, code, true, initial)
	}
	for _, code := range []uint32{125, 42, 29, 56, 30} {
		suppressedKey(t, k, code, true, locked) // repeats cannot reintroduce held modifiers
	}
	if ev := secureKey(t, k, 16, true, locked); ev.Keysym != "q" || ev.Mods != 0 || ev.State != (ports.ModState{}) {
		t.Fatalf("password inherited native state: %+v", ev)
	}
	secureKey(t, k, 16, false, locked)
	secureKey(t, k, 54, true, locked) // password-era right Shift
	if ev := secureKey(t, k, 16, true, unlocked); ev.Keysym != "q" || ev.Mods != 0 || ev.State != (ports.ModState{}) {
		t.Fatalf("desktop inherited password/native state: %+v", ev)
	}
	secureKey(t, k, 16, false, unlocked)
	for _, code := range []uint32{30, 56, 29, 42, 125, 54} {
		suppressedKey(t, k, code, false, unlocked)
	}
	secureKey(t, k, 125, true, unlocked)
	if ev := secureKey(t, k, 16, true, unlocked); ev.Mods&ports.ModSuper == 0 || ev.State.Depressed == 0 {
		t.Fatalf("fresh desktop Super broken: %+v", ev)
	}
}

func TestSecurityEpochResetsLocksButFreshCapsAndNumLockWork(t *testing.T) {
	k := newTest(t, "us")
	initial := ports.SecurityState{}
	locked := ports.SecurityState{Generation: 1, Protected: true}
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
	initial, locked := ports.SecurityState{}, ports.SecurityState{Generation: 1, Protected: true}
	secureKey(t, k, 58, true, initial) // physical Caps is Control in this map
	if ev := secureKey(t, k, 16, true, locked); ev.Mods != 0 || ev.State != (ports.ModState{}) {
		t.Fatalf("remapped inherited Control leaked: %+v", ev)
	}
	secureKey(t, k, 16, false, locked)
	suppressedKey(t, k, 58, false, locked)
	secureKey(t, k, 58, true, locked)
	if ev := secureKey(t, k, 16, true, locked); ev.Mods&ports.ModCtrl == 0 || ev.State.Depressed == 0 {
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
		locked := ports.SecurityState{Generation: 1, Protected: true}
		secureKey(t, k, 100, true, locked) // ISO_Level3_Shift, not guessed Alt
		secureKey(t, k, 56, true, locked)
		if ev := secureKey(t, k, 11, true, locked); ev.Keysym != "at" || ev.Mods&ports.ModAlt == 0 || ev.State.Depressed == 0 {
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
	locked := ports.SecurityState{Generation: 1, Protected: true}
	secureKey(t, previous, 125, true, locked)
	next := newTest(t, "fr")
	next.QuarantineFrom(previous)
	suppressedKey(t, next, 125, true, locked)
	if ev := secureKey(t, next, 16, true, locked); ev.Keysym != "a" || ev.Mods != 0 || ev.State.Depressed != 0 {
		t.Fatalf("replacement replayed old held state: %+v", ev)
	}
	suppressedKey(t, next, 125, false, locked)
	secureKey(t, next, 125, true, locked)
	if ev := secureKey(t, next, 30, true, locked); ev.Mods&ports.ModSuper == 0 {
		t.Fatalf("fresh key after replacement broken: %+v", ev)
	}
}

func TestSecurityStateChangeResetsAltGrAndAltWithoutMaskInference(t *testing.T) {
	k := newTest(t, "fr")
	initial := ports.SecurityState{}
	locked := ports.SecurityState{Generation: 1, Protected: true}
	secureKey(t, k, 100, true, initial)
	secureKey(t, k, 56, true, initial)
	if ev := secureKey(t, k, 11, true, locked); ev.Keysym != "agrave" || ev.Mods != 0 || ev.State != (ports.ModState{}) {
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
	if ev := secureKey(t, k, 11, true, changed); ev.Keysym != "agrave" || ev.Mods != 0 || ev.State != (ports.ModState{}) {
		t.Fatalf("state change missed native reset: %+v", ev)
	}
	suppressedKey(t, k, 100, false, changed)
}
