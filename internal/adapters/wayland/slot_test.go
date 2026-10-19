package wayland

import (
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A mapped window carries the slot token of its client process.
func TestWindowMappedCarriesSlot(t *testing.T) {
	env := newMockprocEnv(t)
	// The test client runs in this process: its PID is ours.
	env.EXPECT().Lookup(os.Getpid(), ports.SlotEnv).Return("7", true).Once()
	s, events, _, dir := keyboardServer(t, func(s *Server) { s.env = env })
	mapWindow := toplevelMapper(t, protocolClient(t, s, dir), events)
	// Hold the pending command outside the display until after mapping. The
	// protocol connection must carry the token even when command application
	// loses the race to this fast client's first window.
	pending := ports.SlotsPending{Pending: true}
	if w := mapWindow(); w.Slot != "7" || w.PID != os.Getpid() {
		t.Fatalf("slot %q pid %d", w.Slot, w.PID)
	}
	if !s.display.Do(func() { s.apply(pending) }) {
		t.Fatal("display stopped before applying delayed command")
	}
}

func TestLinuxProcEnv(t *testing.T) {
	t.Setenv("NEFERWL_TEST_KEY", "x") // not in /proc: environ is the start env
	if _, ok := (linuxProcEnv{}).Lookup(os.Getpid(), "NEFERWL_TEST_KEY"); ok {
		t.Fatal("environ reflects later changes")
	}
	path, ok := (linuxProcEnv{}).Lookup(os.Getpid(), "PATH")
	if want, _ := os.LookupEnv("PATH"); ok != (want != "") {
		t.Fatalf("PATH %q %v", path, ok)
	}
	if _, ok := (linuxProcEnv{}).Lookup(0, "PATH"); ok {
		t.Fatal("pid 0")
	}
}
