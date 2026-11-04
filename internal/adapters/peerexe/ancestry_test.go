package peerexe

import (
	"os"
	"testing"
)

func TestParentPID(t *testing.T) {
	for _, tc := range []struct {
		stat string
		want int
		ok   bool
	}{
		{"123 (steam (game) launcher) S 42 0 0", 42, true},
		{"123 (a) R 1", 1, true},
		{"123 (a) R 0", 0, true},
		{"123 (a) R -2", -2, false},
		{"123 (a) R nope", 0, false},
		{"123 (a) R", 0, false},
		{"broken", 0, false},
	} {
		got, ok := parentPID(tc.stat)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parentPID(%q) = %d, %v", tc.stat, got, ok)
		}
	}
}

func TestAncestorPIDs(t *testing.T) {
	chain := AncestorPIDs(os.Getpid())
	if parent := os.Getppid(); parent > 1 && (len(chain) == 0 || chain[0] != parent) {
		t.Fatalf("chain %v does not start at parent %d", chain, parent)
	}
	if len(chain) > 32 {
		t.Fatalf("unbounded chain: %v", chain)
	}
	seen := map[int]bool{os.Getpid(): true}
	for _, pid := range chain {
		if pid <= 1 || seen[pid] {
			t.Fatalf("invalid chain: %v", chain)
		}
		seen[pid] = true
	}
	for _, pid := range []int{0, 1, -1} {
		if got := AncestorPIDs(pid); len(got) != 0 {
			t.Fatalf("pid %d: %v", pid, got)
		}
	}
}
