//lint:file-ignore U1000 pend, sent and stateSent are used by the configure flow

package xwm

import (
	"math/bits"

	"github.com/bnema/neferwl/internal/ports"
)

// Record kinds.
const (
	kindFree uint8 = iota
	kindManaged
	kindUnmanaged
)

const (
	initialSlots = 256
	// maxSlots bounds the table: a Slot travels in the low 16 bits of an
	// Expect argument.
	maxSlots  = 1<<16 - 1
	indexGone = 0xFFFFFFFF // tombstone in table.index
)

// pending is the configure state not yet (or last) sent to the X server.
type pending struct {
	x, y                         int32
	w, h                         uint32
	fullscreen, activated, dirty bool
}

// record is one X window known to the XWM. Strings are the only per-record
// heap data.
type record struct {
	xid  uint32
	id   ports.WindowID
	kind uint8
	gen  uint16 // bumped on insert; stale Expect replies are told apart by it

	// X state
	override, mapped, mapping, ready bool
	x, y                             int32
	w, h                             uint32

	// properties
	pending                        uint16 // propMask bits still awaited
	input, deleteWindow, takeFocus bool
	wtype                          uint8
	modal, fullscreen              bool
	transient                      uint32 // xid
	pid                            int32
	minW, minH, maxW, maxH         int32
	title, class                   string // owned, replaced only on change
	hasNetName                     bool
	floating                       bool // dialog-like type; set with the properties

	// pairing
	serial  uint64
	surface uint64

	// configure
	pend, sent pending
	stateSent  uint8 // _NET_WM_STATE bits last written
}

// table is a slab of records with an open-addressing index by X id. No Go map.
type table struct {
	recs  []record // slab, len == cap, power of two
	free  []uint32 // free slot indices (LIFO)
	index []uint32 // slot+1, 0 empty, indexGone tombstone; len == 2*len(recs)
	shift uint8
	n     int
	tombs int
}

func newTable(size int) *table {
	t := &table{}
	t.resize(size)
	return t
}

// resize reallocates the slab, free list and index for size records and
// reindexes the live ones.
func (t *table) resize(size int) {
	old := t.recs
	t.recs = make([]record, size)
	copy(t.recs, old)
	t.free = make([]uint32, 0, size)
	for i := min(size, maxSlots) - 1; i >= 0; i-- {
		if i >= len(old) || old[i].kind == kindFree {
			t.free = append(t.free, uint32(i))
		}
	}
	t.index = make([]uint32, 2*size)
	t.shift = uint8(32 - bits.TrailingZeros(uint(len(t.index))))
	t.reindex()
}

// reindex rebuilds the index from the live records, dropping tombstones.
// It does not allocate.
func (t *table) reindex() {
	clear(t.index)
	t.tombs = 0
	for i := range t.recs {
		if t.recs[i].kind != kindFree {
			t.indexAdd(t.recs[i].xid, uint32(i)+1)
		}
	}
}

func (t *table) hash(xid uint32) uint32 { return (xid * 0x9E3779B1) >> t.shift }

// indexAdd places slot for xid, which must be absent, in the first empty or
// tombstone position of its probe sequence.
func (t *table) indexAdd(xid, slot uint32) {
	mask := uint32(len(t.index) - 1)
	for i := t.hash(xid); ; i = (i + 1) & mask {
		switch t.index[i] {
		case 0:
			t.index[i] = slot
			return
		case indexGone:
			t.index[i] = slot
			t.tombs--
			return
		}
	}
}

// grow doubles the table. It reports false at maxSlots.
func (t *table) grow() bool {
	if len(t.recs) >= 1<<16 {
		return false
	}
	t.resize(2 * len(t.recs))
	return len(t.free) > 0
}

// insert returns the slot of xid, creating a managed record (generation
// bumped) when absent. It returns 0 when the table is full. Growth is the
// only allocation.
func (t *table) insert(xid uint32) Slot {
	if s := t.lookup(xid); s != 0 {
		return s
	}
	if len(t.free) == 0 && !t.grow() {
		return 0
	}
	i := t.free[len(t.free)-1]
	t.free = t.free[:len(t.free)-1]
	if t.n+t.tombs+1 > len(t.index)/4*3 { // keep an empty index cell for lookup to stop at
		t.reindex()
	}
	r := &t.recs[i]
	gen := r.gen + 1
	if gen == 0 {
		gen = 1
	}
	*r = record{xid: xid, kind: kindManaged, gen: gen, input: true}
	t.indexAdd(xid, i+1)
	t.n++
	return Slot(i + 1)
}

// remove frees s. The record is zeroed except its generation.
func (t *table) remove(s Slot) {
	r := t.rec(s)
	if r == nil || r.kind == kindFree {
		return
	}
	mask := uint32(len(t.index) - 1)
	for i := t.hash(r.xid); ; i = (i + 1) & mask {
		v := t.index[i]
		if v == 0 {
			break
		}
		if v == uint32(s) {
			t.index[i] = indexGone
			t.tombs++
			break
		}
	}
	*r = record{gen: r.gen}
	t.free = append(t.free, uint32(s)-1)
	t.n--
	if t.n+t.tombs > len(t.index)/4*3 {
		t.reindex()
	}
}

// lookup returns the slot of xid, or 0.
func (t *table) lookup(xid uint32) Slot {
	mask := uint32(len(t.index) - 1)
	for i := t.hash(xid); ; i = (i + 1) & mask {
		v := t.index[i]
		if v == 0 {
			return 0
		}
		if v != indexGone && t.recs[v-1].xid == xid {
			return Slot(v)
		}
	}
}

// rec returns the record of s, or nil when s is out of range. The pointer is
// valid only until the next insert, host call or anything else that can grow
// the table or free s: never hold it across those, fetch it again with rec(s).
func (t *table) rec(s Slot) *record {
	if s == 0 || int(s) > len(t.recs) {
		return nil
	}
	return &t.recs[s-1]
}

// tag is the Expect argument naming s: slot in the low 16 bits, generation
// in the high 16.
func (t *table) tag(s Slot) uint32 { return uint32(s) | uint32(t.recs[s-1].gen)<<16 }

// live resolves an Expect tag. It returns nil when the slot was freed or
// reused since the request was sent.
func (t *table) live(tag uint32) (Slot, *record) {
	s := Slot(tag & 0xFFFF)
	r := t.rec(s)
	if r == nil || r.kind == kindFree || r.gen != uint16(tag>>16) {
		return 0, nil
	}
	return s, r
}
