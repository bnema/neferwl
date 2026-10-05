package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A publish that changes nothing on an output keeps that output's Seq, so
// its owner does not recompose; a real change takes a new one.
func TestSceneSeqKeptWhenNothingChanged(t *testing.T) {
	r := startSwipe(t, nil)
	first := r.mapWindow(t, 1)[0]
	if first.Seq == 0 {
		t.Fatal("scene without Seq")
	}
	// Focus-column-left at the left edge changes nothing but still publishes.
	again := r.key(t, "Left", ports.ModAlt)[0]
	if again.Seq != first.Seq || !again.SameAs(first) {
		t.Fatalf("Seq %d, want the held %d", again.Seq, first.Seq)
	}
	next := r.mapWindow(t, 2)[0]
	if next.Seq <= first.Seq || len(next.Windows) != 2 {
		t.Fatalf("Seq %d after mapping a window (was %d), %d windows", next.Seq, first.Seq, len(next.Windows))
	}
}

// A key that starts a transition can publish a scene that draws what the
// previous one did (the springs start there): it still gets a fresh Seq, or
// the outputs would drop it and the motion would wait for the fallback timer
// to begin.
func TestAnimatingKeyGetsFreshSeq(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ports.Config)
		// setup returns the scene before the key.
		setup func(*testing.T, *swipeRig) ports.Scene
		sym   string
		mods  ports.Mods
		// same: the key's scene draws what the previous one did.
		same bool
	}{
		{"width change", func(c *ports.Config) { c.Binds["Cmd+Ctrl+Right"] = "set-column-width +10%" },
			func(t *testing.T, r *swipeRig) ports.Scene {
				twoColumns(t, r)
				return r.key(t, "Left", ports.ModAlt)[0]
			}, "Right", ports.ModAlt | ports.ModCtrl, true},
		{"focus scroll", nil, func(t *testing.T, r *swipeRig) ports.Scene {
			threeColumns(t, r)
			return r.key(t, "Left", ports.ModAlt)[0] // 2 is on screen: no scroll
		}, "Left", ports.ModAlt, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := startSwipe(t, tt.edit)
			prev := tt.setup(t, r)
			s := r.key(t, tt.sym, tt.mods)[0]
			if s.SameAs(prev) != tt.same {
				t.Fatalf("scene right after the key same as the previous one: %t, want %t", s.SameAs(prev), tt.same)
			}
			if s.Seq <= prev.Seq {
				t.Fatalf("Seq %d right after the key, want a new one (was %d)", s.Seq, prev.Seq)
			}
		})
	}
}

// A key that animates nothing keeps its output's Seq.
func TestNoOpKeyKeepsSeqWithAnimationsOn(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	first := r.key(t, "Right", ports.ModAlt)[0] // 3 is the last column
	for range 2 {
		if s := r.key(t, "Right", ports.ModAlt)[0]; s.Seq != first.Seq || !s.SameAs(first) {
			t.Fatalf("Seq %d on a key at the edge, want the held %d", s.Seq, first.Seq)
		}
	}
}
