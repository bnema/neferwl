package wayland

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bnema/neferwl/internal/adapters/peerexe"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestWindowProcessGateAndRemap(t *testing.T) {
	root := fstest.MapFS{
		"20/stat": {Data: []byte("20 (app) S 10 0 20 " + strings.Repeat("0 ", 15) + "123")},
		"10/stat": {Data: []byte("10 (launcher) S 1 0 10 " + strings.Repeat("0 ", 15) + "456")},
	}
	s := &Server{processes: peerexe.NewProcesses(root, 0), mappedProc: map[domain.WindowID]bool{}, knownProc: map[domain.WindowID][]domain.ProcNode{}}
	own := s.windowProc(1, 20, false)
	if len(own) != 1 {
		t.Fatalf("first map should read only own identity: %+v", own)
	}
	s.emit(ports.WindowMapped{ID: 1, Proc: own})
	child := s.windowProc(2, 20, false)
	if len(child) != 2 {
		t.Fatalf("eligible chain: %+v", child)
	}
	delete(root, "20/stat") // remap must use cached identity, without proc I/O.
	if got := s.windowProc(1, 20, true); len(got) != 1 || got[0] != own[0] {
		t.Fatalf("remap: %+v", got)
	}
	s.emit(ports.WindowUnmapped{ID: 1})
	if len(s.mappedProc) != 0 {
		t.Fatal("unmapped process still counted")
	}
	root["20/stat"] = &fstest.MapFile{Data: []byte("20 (app) S 10 0 20 " + strings.Repeat("0 ", 15) + "123")}
	if got := s.windowProc(2, 20, false); len(got) != 1 {
		t.Fatalf("gate after unmap: %+v", got)
	}
	s.emit(ports.WindowMapped{ID: 1, Proc: own})
	s.emit(ports.WindowDestroyed{ID: 1})
	if len(s.mappedProc) != 0 || len(s.knownProc) != 0 {
		t.Fatal("destroyed process retained")
	}
}
