// Package xkb provides a cgo-free libxkbcommon keymap and keyboard state.
package xkb

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
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
	keymapString       func(uintptr, uint32) unsafe.Pointer
	free               func(uintptr)
	getSym             func(uintptr, uint32) uint32
	symName            func(uint32, uintptr, uintptr) int32
	updateKey          func(uintptr, uint32, uint32) uint32
	serializeMods      func(uintptr, uint32) uint32
	serializeLayout    func(uintptr, uint32) uint32
	modActive          func(uintptr, uintptr, uint32) int32
	minCode            func(uintptr) uint32
	maxCode            func(uintptr) uint32
	symsLevel          func(uintptr, uint32, uint32, uint32, *unsafe.Pointer) int32
	fromName           func(uintptr, uint32) uint32
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
	var stateNew func(uintptr) uintptr
	bind := func(fn any, name string) { purego.RegisterLibFunc(fn, k.lib, "xkb_"+name) }
	bind(&contextNew, "context_new")
	bind(&k.contextUnref, "context_unref")
	bind(&keymapNew, "keymap_new_from_names")
	bind(&k.keymapUnref, "keymap_unref")
	bind(&stateNew, "state_new")
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
	k.state = stateNew(k.mapPtr)
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

func (k *Keymap) Key(evdevCode uint32, pressed bool, timeMsec uint32) ports.KeyEvent {
	code := evdevCode + 8
	sym := k.getSym(k.state, code)
	var buf [64]byte
	n := k.symName(sym, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	name := ""
	if n > 0 && n < int32(len(buf)) {
		name = string(buf[:n])
	}
	direction := uint32(0)
	if pressed {
		direction = 1
	}
	k.updateKey(k.state, code, direction)
	event := ports.KeyEvent{Keysym: name, Pressed: pressed, TimeMsec: timeMsec, Keycode: evdevCode}
	event.State = ports.ModState{
		Depressed: k.serializeMods(k.state, modsDepressed), Latched: k.serializeMods(k.state, modsLatched),
		Locked: k.serializeMods(k.state, modsLocked), Group: k.serializeLayout(k.state, layoutEffective),
	}
	for _, mod := range []struct {
		name string
		flag ports.Mods
	}{{"Shift", ports.ModShift}, {"Control", ports.ModCtrl}, {"Mod1", ports.ModAlt}, {"Mod4", ports.ModSuper}} {
		b := append([]byte(mod.name), 0)
		if k.modActive(k.state, uintptr(unsafe.Pointer(&b[0])), modsEffective) > 0 {
			event.Mods |= mod.flag
		}
		runtime.KeepAlive(b)
	}
	return event
}

func (k *Keymap) KeycodeFor(keysymName string) (evdev uint32, shift bool, ok bool) {
	b := append([]byte(keysymName), 0)
	sym := k.fromName(uintptr(unsafe.Pointer(&b[0])), 0)
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
