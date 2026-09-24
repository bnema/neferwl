package wayland

import (
	"os"
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

// A mapped window carries the slot token of its client process.
func TestWindowMappedCarriesSlot(t *testing.T) {
	env := newMockprocEnv(t)
	// The test client runs in this process: its PID is ours.
	env.EXPECT().Lookup(os.Getpid(), ports.SlotEnv).Return("7", true).Once()
	s, events, commands, dir := keyboardServer(t, func(s *Server) { s.env = env })
	mapWindow := toplevelMapper(t, protocolClient(t, s, dir), events)
	// No slot pending: /proc is not read (the mock would fail on a call).
	if w := mapWindow(); w.Slot != "" {
		t.Fatalf("slot %q", w.Slot)
	}
	commands <- ports.SlotsPending{Pending: true}
	if w := mapWindow(); w.Slot != "7" {
		t.Fatalf("slot %q", w.Slot)
	}
}

func TestLinuxProcEnv(t *testing.T) {
	t.Setenv("NEFERTTY_TEST_KEY", "x") // not in /proc: environ is the start env
	if _, ok := (linuxProcEnv{}).Lookup(os.Getpid(), "NEFERTTY_TEST_KEY"); ok {
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
