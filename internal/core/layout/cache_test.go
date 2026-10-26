package layout

import (
	"testing"
	"testing/synctest"
)

// A direct write to the exported field, or a literal that never set the cache,
// still resolves from Overflow: the cache is only a shortcut.
func TestLanesLayoutCacheNeverDisagrees(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var zero Lanes
		if zero.lcache != nil || zero.Engine() != layoutTable[0] {
			t.Fatal("the zero workspace must resolve to the default layout")
		}
		cacheAgrees(t, "zero after first use", &zero)
		w := &Lanes{Overflow: OverflowFixed}
		if got, _ := Lookup(OverflowFixed); w.Engine() != got {
			t.Fatal("literal without a cache")
		}
		cacheAgrees(t, "literal after first use", w)
		w.SetOverflow(OverflowCascade)
		cacheAgrees(t, "set", w)
		for _, o := range []Overflow{OverflowScroll, OverflowFixed, "", "unknown", OverflowCascade} {
			w.Overflow = o // a stale cache must not win
			want, ok := Lookup(o)
			if !ok {
				want, _ = Lookup(Default)
			}
			if w.Engine() != want {
				t.Fatalf("direct write %q: layout %v, want %v", o, w.Engine(), want)
			}
		}
		// A test override still wins over both.
		w.Override = scrollLayout{}
		w.SetOverflow(OverflowFixed)
		if w.Engine() != (scrollLayout{}) {
			t.Fatal("lay override lost")
		}
		// Copies carry the cache with the Overflow it was resolved for.
		w.Override = nil
		c := *w
		c.Overflow = OverflowCascade
		if c.Engine() != (cascadeLayout{}) || w.Engine() != (fixedLayout{}) {
			t.Fatal("copy and original disagree with their overflow")
		}

	})
}

func cacheAgrees(t *testing.T, step string, w *Lanes) {
	t.Helper()
	want, ok := Lookup(w.Overflow)
	if !ok {
		want, _ = Lookup(Default)
	}
	if w.lcache == nil || w.lcacheFor != w.Overflow || w.Engine() != want || w.lcache != want {
		t.Fatalf("%s: cache %v for %q, want %v for %q", step, w.lcache, w.lcacheFor, want, w.Overflow)
	}
}
