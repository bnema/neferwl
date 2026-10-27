package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Each whole-window redraw is counted under its reason, so the stats tell
// a client claiming full damage from a history too short.
func TestFullRedrawReasons(t *testing.T) {
	rect := image.Rect(0, 0, 10, 10)
	held := &target{valid: true, windows: map[domain.WindowID]heldWindow{1: {seq: 1, rect: rect}}}
	var why fullRedraws
	d := &damageRegion{held: held, seen: map[domain.WindowID]bool{}, drawn: map[domain.WindowID]heldWindow{}, bound: rect, why: &why}
	pixels := ports.SurfaceContent{Width: 10, Height: 10, SHM: &ports.SHMBuffer{}}

	known := pixels
	known.ID, known.Seq = 1, 2
	known.DamageHistory = []domain.SeqDamage{{Seq: 2, Rects: []domain.Rect{{W: 1, H: 1}}}}
	d.window(1, known, rect)
	if why != (fullRedraws{}) {
		t.Fatalf("partial damage counted as full: %v", why)
	}

	d.seen = map[domain.WindowID]bool{}
	gap := pixels
	gap.ID, gap.Seq = 1, 9
	gap.DamageHistory = []domain.SeqDamage{{Seq: 9, Rects: []domain.Rect{{W: 1, H: 1}}}}
	d.window(1, gap, rect)
	d.window(2, gap, rect)
	if why[fullHistory] != 1 || why[fullNewWindow] != 1 {
		t.Fatalf("reasons %v", why)
	}
	if got := why.take(); got[fullHistory] != 1 || why != (fullRedraws{}) {
		t.Fatalf("take %v left %v", got, why)
	}
}
