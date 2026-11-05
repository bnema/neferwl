package control

import (
	"context"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
)

func TestContinuousDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		changes := make(chan ports.ContinuousCaptureRequest)
		s := &Server{continuous: changes, framesDir: "/session/shots/frames"}
		for _, stop := range []bool{false, true} {
			go func() {
				r := <-changes
				if r.Dir != s.framesDir || r.Stop != stop {
					t.Error(r)
				}
				r.Reply <- ports.ContinuousCaptureResult{Dir: r.Dir}
			}()
			announced := false
			got := s.dispatch(context.Background(), request{Op: "shot", EveryFrame: !stop, Stop: stop}, nil, &announced)
			if !got.OK || got.Dir != s.framesDir {
				t.Fatal(got)
			}
		}
	})
}
