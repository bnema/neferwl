// Package xkb provides a cgo-free libxkbcommon keymap and keyboard state.
package xkb

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego"
)

const (
	modsDepressed = 1 << iota
	modsLatched
	modsLocked
	modsEffective
	layoutEffective = 1 << 7
)

type RMLVO struct{ Rules, Model, Layout, Variant, Options string }

// Keymap owns its native context, keymap and state. It belongs to one input goroutine.
type Keymap struct {
	ctx, mapPtr, state uintptr
	lib, libc          uintptr
	contextUnref       func(uintptr)
	keymapUnref        func(uintptr)
	stateUnref         func(uintptr)
	stateNew           func(uintptr) uintptr
	// Secure tracking belongs to the same producer goroutine as native state.
	epoch             ports.SecurityState
	epochSet          bool
	held, quarantined map[uint32]bool
	keymapString      func(uintptr, uint32) unsafe.Pointer
	free              func(uintptr)
	getSym            func(uintptr, uint32) uint32
	// Buffers go to C as typed pointers: they escape to the heap, where a
	// stack growth during the call cannot move them under C.
	symName         func(uint32, *byte, uintptr) int32
	updateKey       func(uintptr, uint32, uint32) uint32
	serializeMods   func(uintptr, uint32) uint32
	serializeLayout func(uintptr, uint32) uint32
	modActive       func(uintptr, *byte, uint32) int32
	minCode         func(uintptr) uint32
	maxCode         func(uintptr) uint32
	symsLevel       func(uintptr, uint32, uint32, uint32, *unsafe.Pointer) int32
	fromName        func(*byte, uint32) uint32
}

type ruleNames struct{ rules, model, layout, variant, options uintptr }

func New(names RMLVO) (_ *Keymap, err error) {
	k := &Keymap{}
	k.lib, err = purego.Dlopen("libxkbcommon.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			k.Close()
		}
	}()
	k.libc, err = purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	var contextNew func(uint32) uintptr
	var keymapNew func(uintptr, *ruleNames, uint32) uintptr
	bind := func(fn any, name string) { purego.RegisterLibFunc(fn, k.lib, "xkb_"+name) }
	bind(&contextNew, "context_new")
	bind(&k.contextUnref, "context_unref")
	bind(&keymapNew, "keymap_new_from_names")
	bind(&k.keymapUnref, "keymap_unref")
	bind(&k.stateNew, "state_new")
	bind(&k.stateUnref, "state_unref")
	bind(&k.keymapString, "keymap_get_as_string")
	bind(&k.getSym, "state_key_get_one_sym")
	bind(&k.symName, "keysym_get_name")
	bind(&k.updateKey, "state_update_key")
	bind(&k.serializeMods, "state_serialize_mods")
	bind(&k.serializeLayout, "state_serialize_layout")
	bind(&k.modActive, "state_mod_name_is_active")
	bind(&k.minCode, "keymap_min_keycode")
	bind(&k.maxCode, "keymap_max_keycode")
	bind(&k.symsLevel, "keymap_key_get_syms_by_level")
	bind(&k.fromName, "keysym_from_name")
	purego.RegisterLibFunc(&k.free, k.libc, "free")
	k.ctx = contextNew(0)
	if k.ctx == 0 {
		return nil, fmt.Errorf("xkb_context_new failed")
	}
	values := [...]string{names.Rules, names.Model, names.Layout, names.Variant, names.Options}
	var buffers [5][]byte
	var pins runtime.Pinner
	defer pins.Unpin()
	var pointers [5]uintptr
	for i, v := range values {
		if v == "" {
			continue
		}
		buffers[i] = append([]byte(v), 0)
		pins.Pin(&buffers[i][0])
		pointers[i] = uintptr(unsafe.Pointer(&buffers[i][0]))
	}
	r := ruleNames{pointers[0], pointers[1], pointers[2], pointers[3], pointers[4]}
	pins.Pin(&r)
	k.mapPtr = keymapNew(k.ctx, &r, 0)
	runtime.KeepAlive(buffers)
	if k.mapPtr == 0 {
		return nil, fmt.Errorf("xkb_keymap_new_from_names failed")
	}
	k.state = k.stateNew(k.mapPtr)
	if k.state == 0 {
		return nil, fmt.Errorf("xkb_state_new failed")
	}
	return k, nil
}

func (k *Keymap) String() string {
	ptr := k.keymapString(k.mapPtr, 1)
	if ptr == nil {
		return ""
	}
	defer k.free(uintptr(ptr))
	n := 0
	for *(*byte)(unsafe.Add(ptr, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr), n))
}

func (k *Keymap) Key(evdevCode uint32, pressed bool, t time.Duration) ports.KeyEvent {
	code := evdevCode + 8
	sym := k.getSym(k.state, code)
	var buf [64]byte
	n := k.symName(sym, &buf[0], uintptr(len(buf)))
	name := ""
	if n > 0 && n < int32(len(buf)) {
		name = string(buf[:n])
	}
	direction := uint32(0)
	if pressed {
		direction = 1
	}
	k.updateKey(k.state, code, direction)
	event := ports.KeyEvent{Keysym: name, Pressed: pressed, Time: t, Keycode: evdevCode}
	if pressed {
		layout := k.serializeLayout(k.state, layoutEffective)
		event.Base = k.levelName(code, layout, 0)
	}
	event.State = ports.ModState{
		Depressed: k.serializeMods(k.state, modsDepressed), Latched: k.serializeMods(k.state, modsLatched),
		Locked: k.serializeMods(k.state, modsLocked), Group: k.serializeLayout(k.state, layoutEffective),
	}
	for _, mod := range []struct {
		name string
		flag ports.Mods
	}{{"Shift", ports.ModShift}, {"Control", ports.ModCtrl}, {"Mod1", ports.ModAlt}, {"Mod4", ports.ModSuper}} {
		b := append([]byte(mod.name), 0)
		if k.modActive(k.state, &b[0], modsEffective) > 0 {
			event.Mods |= mod.flag
		}
		runtime.KeepAlive(b)
	}
	return event
}

// KeySecure resets native modifier/lock/layout state before translating in a
// new security epoch. Keys physically held across a transition are suppressed
// (including repeats and their eventual release), never replayed into XKB.
// On reset failure no event is translated against the old native state.
func (k *Keymap) KeySecure(code uint32, pressed bool, t time.Duration, epoch ports.SecurityState) (ports.KeyEvent, bool, error) {
	if !k.epochSet || k.epoch != epoch {
		next := k.stateNew(k.mapPtr)
		if next == 0 {
			// Even a failed translation must remember physical presses so a
			// later successful reset cannot admit their repeats.
			if k.held == nil {
				k.held = make(map[uint32]bool)
			}
			if pressed {
				k.held[code] = true
			} else {
				delete(k.held, code)
			}
			return ports.KeyEvent{}, false, fmt.Errorf("xkb_state_new failed during security reset")
		}
		old := k.state
		k.state = next
		k.stateUnref(old)
		k.epoch, k.epochSet = epoch, true
		if k.held == nil {
			k.held = make(map[uint32]bool)
		}
		k.quarantined = make(map[uint32]bool, len(k.held))
		for held := range k.held {
			k.quarantined[held] = true
		}
	}
	blocked := k.quarantined[code]
	if pressed {
		k.held[code] = true
	} else {
		delete(k.held, code)
		delete(k.quarantined, code)
	}
	if blocked {
		return ports.KeyEvent{}, false, nil
	}
	return k.Key(code, pressed, t), true, nil
}

// QuarantineFrom transfers physical held-key tracking when a producer replaces
// its keymap. No old native state or key presses are replayed into the new map.
// Call before closing previous; both maps belong to the calling goroutine.
func (k *Keymap) QuarantineFrom(previous *Keymap) {
	k.epoch, k.epochSet = previous.epoch, previous.epochSet
	k.held = make(map[uint32]bool, len(previous.held))
	k.quarantined = make(map[uint32]bool, len(previous.held))
	for code := range previous.held {
		k.held[code], k.quarantined[code] = true, true
	}
}

// levelName is the first keysym name of a key at one shift level, or "".
func (k *Keymap) levelName(code, layout, level uint32) string {
	var ptr unsafe.Pointer
	if k.symsLevel(k.mapPtr, code, layout, level, &ptr) < 1 {
		return ""
	}
	var buf [64]byte
	n := k.symName(*(*uint32)(ptr), &buf[0], uintptr(len(buf)))
	if n <= 0 || n >= int32(len(buf)) {
		return ""
	}
	return string(buf[:n])
}

func (k *Keymap) KeycodeFor(keysymName string) (evdev uint32, shift bool, ok bool) {
	b := append([]byte(keysymName), 0)
	sym := k.fromName(&b[0], 0)
	runtime.KeepAlive(b)
	if sym == 0 {
		return 0, false, false
	}
	for code := k.minCode(k.mapPtr); code <= k.maxCode(k.mapPtr); code++ {
		for level := uint32(0); level < 2; level++ {
			var ptr unsafe.Pointer
			count := k.symsLevel(k.mapPtr, code, 0, level, &ptr)
			for i := int32(0); i < count; i++ {
				if *(*uint32)(unsafe.Add(ptr, uintptr(i)*4)) == sym && code >= 8 {
					return code - 8, level == 1, true
				}
			}
		}
	}
	return 0, false, false
}

func (k *Keymap) Close() {
	if k.state != 0 {
		k.stateUnref(k.state)
		k.state = 0
	}
	if k.mapPtr != 0 {
		k.keymapUnref(k.mapPtr)
		k.mapPtr = 0
	}
	if k.ctx != 0 {
		k.contextUnref(k.ctx)
		k.ctx = 0
	}
	if k.libc != 0 {
		_ = purego.Dlclose(k.libc)
		k.libc = 0
	}
	if k.lib != 0 {
		_ = purego.Dlclose(k.lib)
		k.lib = 0
	}
}
