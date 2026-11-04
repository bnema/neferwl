package peerexe

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func stat(pid, parent, session int, start uint64) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(fmt.Sprintf("%d (steam (game) launcher) S %d 0 %d %s %d", pid, parent, session, strings.Repeat("0 ", 15), start))}
}

func TestParseStat(t *testing.T) {
	for _, leader := range []bool{false, true} {
		session := 42
		if leader {
			session = 123
		}
		node, parent, ok := parseStat(123, string(stat(123, 77, session, 987654321).Data))
		if !ok || parent != 77 || node.Key.PID != 123 || node.Key.Start != 987654321 || node.SessionLeader != leader {
			t.Fatalf("%+v %d %v", node, parent, ok)
		}
	}
	for _, bad := range []string{"broken", "1 (a) S 1", strings.ReplaceAll(string(stat(3, 2, 2, 3).Data), "3", "nope")} {
		if _, _, ok := parseStat(3, bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestProcessChain(t *testing.T) {
	root := fstest.MapFS{"100/stat": stat(100, 90, 90, 1), "90/stat": stat(90, 80, 90, 2), "80/stat": stat(80, 1, 80, 3), "200/stat": stat(200, 90, 200, 4), "300/stat": stat(300, 200, 300, 5)}
	p := NewProcesses(root, 100)
	if got := p.Chain(300, true); len(got) != 2 || got[0].Key.PID != 300 || got[1].Key.PID != 200 {
		t.Fatalf("cut: %+v", got)
	}
	// Cut membership was computed at startup, not re-read for every chain.
	delete(root, "100/stat")
	delete(root, "90/stat")
	if got := p.Chain(200, true); len(got) != 1 {
		t.Fatalf("cached cut: %+v", got)
	}
	if got := p.Chain(300, false); len(got) != 1 {
		t.Fatalf("eligibility: %+v", got)
	}
	for _, pid := range []int{-1, 0, 1, 80, 90, 100} {
		if got := p.Chain(pid, true); len(got) != 0 {
			t.Fatalf("pid %d: %+v", pid, got)
		}
	}
	root["200/stat"] = stat(200, 300, 200, 4)
	if got := p.Chain(300, true); len(got) != 2 {
		t.Fatalf("cycle: %+v", got)
	}
}

func TestProcessDepth(t *testing.T) {
	root := fstest.MapFS{}
	for pid := 2; pid < 100; pid++ {
		root[fmt.Sprintf("%d/stat", pid)] = stat(pid, pid-1, pid, 1)
	}
	p := NewProcesses(root, 0)
	if got := p.Chain(99, true); len(got) != 32 {
		t.Fatalf("depth: %d", len(got))
	}
	if got := p.Chain(3, true); len(got) != 2 {
		t.Fatalf("init: %+v", got)
	}
}
