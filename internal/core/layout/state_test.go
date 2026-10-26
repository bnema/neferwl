package layout

import (
	"testing"
	"testing/synctest"
)

func TestStatelessLayoutInitialization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
			w := Lanes{Overflow: o}
			if w.state() != nil || !w.lstateReady {
				t.Fatal("stateless layout not initialized")
			}
			w.ResetLayoutState()
			if w.lstateReady {
				t.Fatal("state initialization not reset")
			}
			w.Snapshot()
			if !w.lstateReady {
				t.Fatal("snapshot did not initialize state")
			}
		}

	})
}
