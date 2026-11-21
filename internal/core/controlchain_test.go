package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

// A control "do" runs a ; chain of actions, and rejects the whole chain,
// running nothing, when one of its actions is unknown.
func TestControlDoChain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		if got := r.do(t, "focus-column-left; focus-column-left"); got.Err != "" {
			t.Fatalf("chain: %+v", got)
		}
		if got := r.focused(); len(got) != 2 || got[0] != 4 || got[1] != 3 {
			t.Fatalf("chain focus %v, want [4 3]", got)
		}
		for _, action := range []string{"focus-column-left; bogus", "", " ; "} {
			if got := r.do(t, action); got.Err != ports.ControlErrUnknownCommand {
				t.Fatalf("%q: %+v, want unknown-command", action, got)
			}
		}
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("rejected chain changed focus: %v", got)
		}
		if got := r.do(t, "none"); got.Err != "" {
			t.Fatalf("none: %+v", got)
		}
	})
}
