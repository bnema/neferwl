package control

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Every-frame capture goes to core as a ControlCapture: the adapter only
// supplies the default directory and relays core's (or the backend's) reply.
func TestContinuousDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{framesDir: "/session/shots/frames"}
		out := make(chan ports.ControlRequest)
		for _, tc := range []struct {
			stop bool
			code string
		}{{false, ""}, {true, ""}, {false, ports.ControlErrLocked}} {
			go func() {
				r := (<-out).(ports.ControlCapture)
				if r.Dir != s.framesDir || r.Stop != tc.stop || r.Caps != domain.CapsAll || cap(r.Reply) != 1 {
					t.Error(r)
				}
				r.Reply <- ports.ContinuousCaptureResult{Dir: r.Dir, Err: tc.code}
			}()
			announced := false
			got := s.dispatch(context.Background(), request{Op: "shot", EveryFrame: !tc.stop, Stop: tc.stop}, out, &announced)
			if got.OK != (tc.code == "") || got.Error != tc.code || got.Dir != s.framesDir {
				t.Fatal(got)
			}
		}
	})
}
