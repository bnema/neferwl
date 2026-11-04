package wayland

import (
	"github.com/bnema/neferwl/internal/adapters/clock"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/peerexe"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestWindowProcessChainAndRemap(t *testing.T) {
	root := fstest.MapFS{
		"20/comm": {Data: []byte("app\n")}, "10/comm": {Data: []byte("launcher\n")},
		"20/stat": {Data: []byte("20 (app) S 10 0 20 " + strings.Repeat("0 ", 15) + "123")},
		"10/stat": {Data: []byte("10 (launcher) S 1 0 10 " + strings.Repeat("0 ", 15) + "456")},
	}
	s := &Server{clock: clock.System{}, processes: peerexe.NewProcesses(root, 0), knownProc: map[domain.WindowID][]domain.ProcNode{}}
	own := s.windowProc(1, 20, false)
	if len(own) != 2 {
		t.Fatalf("first map must retain ancestor identities: %+v", own)
	}
	s.emit(ports.WindowMapped{ID: 1, Proc: own})
	child := s.windowProc(2, 20, false)
	if len(child) != 2 {
		t.Fatalf("eligible chain: %+v", child)
	}
	delete(root, "20/stat") // remap must use cached identity, without proc I/O.
	if got := s.windowProc(1, 20, true); len(got) != 2 || got[0] != own[0] {
		t.Fatalf("remap: %+v", got)
	}
	s.emit(ports.WindowMapped{ID: 1, Proc: own})
	s.emit(ports.WindowDestroyed{ID: 1})
	if len(s.knownProc) != 0 {
		t.Fatal("destroyed process retained")
	}
}

func TestProcessStartedProjection(t *testing.T) {
	now := time.Unix(1000, 0)
	got := processStarted(now, 20*time.Second, 1500)
	if !got.Equal(now.Add(-5 * time.Second)) {
		t.Fatal(got)
	}
}
