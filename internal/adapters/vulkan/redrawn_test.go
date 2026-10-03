package vulkan

import "testing"

func TestTakeRedrawnResets(t *testing.T) {
	r := &Renderer{redrawn: 4096}
	if got := r.TakeRedrawn(); got != 4096 {
		t.Fatalf("taken %d", got)
	}
	if got := r.TakeRedrawn(); got != 0 {
		t.Fatalf("after reset %d", got)
	}
}
