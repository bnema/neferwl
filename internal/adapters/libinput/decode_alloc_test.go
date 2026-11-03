package libinput

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
)

var decodedSink ports.InputEvent

func BenchmarkDecodedMotion(b *testing.B) {
	p := newPointer(domain.Layout{{Width: 800, Height: 600}})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		decodedSink = secureInput(decodeMotion(p, 1, 0.1, 0, 0.1, 0, 0), nil, domain.SecurityState{})
	}
}

func TestDecodedMotionAllocations(t *testing.T) {
	p := newPointer(domain.Layout{{Width: 800, Height: 600}})
	// origin/main's escaping decoded value + secureInput boundary: 1 boxing allocation.
	if got := testing.AllocsPerRun(1000, func() { decodedSink = secureInput(decodeMotion(p, 1, 0.1, 0, 0.1, 0, 0), nil, domain.SecurityState{}) }); got != 1 {
		t.Fatalf("allocations=%v want main baseline 1", got)
	}
	if decodedSink.(ports.PointerMotion).Device != 1 {
		t.Fatal("device not stamped")
	}
}
