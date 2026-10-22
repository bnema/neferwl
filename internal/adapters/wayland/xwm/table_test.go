package xwm

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTableAgainstMap(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	tab := newTable(initialSlots)
	model := map[uint32]Slot{}
	var xids []uint32
	for i := 0; i < 10000; i++ {
		switch op := rng.IntN(3); {
		case op < 2 || len(xids) == 0:
			xid := rng.Uint32()&0x1fffff | 0x200000
			if _, ok := model[xid]; ok {
				continue
			}
			s := tab.insert(xid)
			require.NotZero(t, s)
			model[xid] = s
			xids = append(xids, xid)
		default:
			k := rng.IntN(len(xids))
			xid := xids[k]
			xids[k] = xids[len(xids)-1]
			xids = xids[:len(xids)-1]
			tab.remove(model[xid])
			delete(model, xid)
		}
		if i%500 == 0 {
			for xid, s := range model {
				require.Equal(t, s, tab.lookup(xid))
			}
			require.Zero(t, tab.lookup(rng.Uint32()|1<<31))
		}
	}
	require.Equal(t, len(model), tab.n)
	for xid, s := range model {
		require.Equal(t, s, tab.lookup(xid))
		require.Equal(t, xid, tab.rec(s).xid)
	}
}

func TestTableGrowth(t *testing.T) {
	tab := newTable(initialSlots)
	slots := make([]Slot, 0, initialSlots+1)
	for i := 0; i < initialSlots; i++ {
		slots = append(slots, tab.insert(uint32(0x200000+i)))
	}
	require.Len(t, tab.recs, initialSlots)
	tab.rec(slots[7]).title = "kept"
	slots = append(slots, tab.insert(0x300000))
	require.Len(t, tab.recs, 2*initialSlots)
	require.Len(t, tab.index, 4*initialSlots)
	for i, s := range slots[:initialSlots] {
		require.Equal(t, s, tab.lookup(uint32(0x200000+i)))
	}
	require.Equal(t, "kept", tab.rec(slots[7]).title, "growth keeps records")
	require.Equal(t, slots[initialSlots], tab.lookup(0x300000))
}

func TestTableTombstoneReuse(t *testing.T) {
	tab := newTable(initialSlots)
	for i := 0; i < 5000; i++ { // far more churn than slots: tombstones must be recycled
		s := tab.insert(uint32(0x200000 + i))
		require.NotZero(t, s)
		require.Equal(t, s, tab.lookup(uint32(0x200000+i)))
		tab.remove(s)
		require.Zero(t, tab.lookup(uint32(0x200000+i)))
	}
	require.Len(t, tab.recs, initialSlots)
	require.Zero(t, tab.n)
}

func TestTableGenerationAndTag(t *testing.T) {
	tab := newTable(initialSlots)
	s := tab.insert(0x200001)
	tag := tab.tag(s)
	got, rec := tab.live(tag)
	require.Equal(t, s, got)
	require.NotNil(t, rec)
	tab.remove(s)
	_, rec = tab.live(tag)
	require.Nil(t, rec, "freed slot")
	s2 := tab.insert(0x200002)
	require.Equal(t, s, s2, "LIFO free list reuses the slot")
	_, rec = tab.live(tag)
	require.Nil(t, rec, "stale tag after slot reuse")
	_, rec = tab.live(tab.tag(s2))
	require.NotNil(t, rec)
}

func TestTableFull(t *testing.T) {
	tab := newTable(initialSlots)
	for i := 0; i < maxSlots; i++ {
		require.NotZero(t, tab.insert(uint32(i+1)), i)
	}
	require.Zero(t, tab.insert(0xFFFFFF), "table is bounded by the 16-bit slot in Expect tags")
	require.Equal(t, Slot(maxSlots), tab.lookup(maxSlots))
}

func TestTableInsertExisting(t *testing.T) {
	tab := newTable(initialSlots)
	s := tab.insert(5)
	require.Equal(t, s, tab.insert(5))
	require.Equal(t, 1, tab.n)
}

func TestTableLookupAllocations(t *testing.T) {
	tab := newTable(initialSlots)
	for i := 0; i < 200; i++ {
		tab.insert(uint32(0x200000 + i))
	}
	var sink Slot
	n := testing.AllocsPerRun(1000, func() {
		sink += tab.lookup(0x200010)
		sink += tab.lookup(0x7fffffff) // absent
		_, r := tab.live(tab.tag(3))
		if r != nil {
			sink++
		}
	})
	require.Zero(t, n)
	require.NotZero(t, sink)
}

func TestTableInsertRemoveAllocations(t *testing.T) {
	tab := newTable(initialSlots)
	n := testing.AllocsPerRun(1000, func() {
		s := tab.insert(0x200001)
		tab.remove(s)
	})
	require.Zero(t, n)
}
