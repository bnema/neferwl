package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestShotDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		captures := make(chan domain.CaptureRequest)
		captured := make(chan domain.CaptureDone)
		shooter := NewShooter(captures, captured, t.TempDir(), func(string) (int, int, bool) { return 1, 1, true })
		s := &Server{ids: workspaceid.WithPrefix("test"), shooter: shooter, injector: newInjector(injectKeymap(t), nil), done: make(chan struct{})}
		defer s.injector.km.Close()
		out := make(chan ports.ControlRequest)
		for _, tc := range []struct {
			wire                  string
			op                    string
			output                string
			inline, fail, timeout bool
		}{
			{`{"op":"shot","outputs":["A"],"inline":true}`, "shot", "A", true, false, false},
			{`{"op":"shot"}`, "shot", "", false, false, false},
			{`{"op":"do","action":"none","shot":true}`, "do", "", false, false, false},
			{`{"op":"inject","kind":"key","combo":"a","shot":["A"]}`, "inject", "A", false, false, false},
			{`{"op":"shot","outputs":["A"]}`, "shot", "A", false, true, false},
			{`{"op":"shot","outputs":["A"]}`, "shot", "A", false, false, true},
		} {
			var req request
			if err := json.Unmarshal([]byte(tc.wire), &req); err != nil {
				t.Fatal(err)
			}
			coreDone := make(chan struct{})
			go func() {
				defer close(coreDone)
				message := <-out
				var shots []domain.ShotRequest
				var wait domain.WaitLevel
				var caps domain.Caps
				var response chan<- ports.ControlReply
				switch r := message.(type) {
				case ports.ControlDo:
					shots, wait, caps, response = r.Shots, r.Wait, r.Caps, r.Reply
					if r.Action != "none" {
						t.Errorf("action %q", r.Action)
					}
				case ports.ControlInject:
					shots, wait, caps, response = r.Shots, r.Wait, r.Caps, r.Reply
				default:
					t.Errorf("message %T", message)
					return
				}
				if len(shots) != 1 || shots[0].Output != tc.output || wait != domain.WaitPresented || caps != domain.CapsAll {
					t.Errorf("forwarded %+v %v %v", shots, wait, caps)
				}
				response <- ports.ControlReply{Done: domain.WaitPresented, Shots: []domain.ShotRequest{{Output: "A", MinSeq: 9}}, TimedOut: tc.timeout}
				q := <-captures
				if q.MinSeq != 9 || q.Output != "A" {
					t.Errorf("capture: %+v", q)
				}
				copy(q.Mapped, []byte{0, 0, 255, 255})
				var err error
				if tc.fail {
					err = errors.New("capture refused")
				}
				captured <- domain.CaptureDone{ID: q.ID, Err: err}
			}()
			announced := false
			got := s.dispatch(context.Background(), req, out, &announced)
			<-coreDone
			if tc.fail {
				if got.OK || got.Error != ports.ControlErrCaptureFailed {
					t.Fatal(got)
				}
				continue
			}
			if tc.timeout {
				if got.OK || got.Error != "timeout" {
					t.Fatal(got)
				}
			} else if !got.OK {
				t.Fatal(got)
			}
			if len(got.Shots) != 1 || got.Shots[0].Seq != 9 || (len(got.Shots[0].PNG) > 0) != tc.inline {
				t.Fatal(got)
			}
		}
	})
}

func TestShotDispatchValidation(t *testing.T) {
	s := &Server{}
	announced := false
	for _, wire := range []string{`{"op":"shot"}`, `{"op":"do","action":"quit","shot":true}`, `{"op":"inject","kind":"key","combo":"a","shot":["A"]}`, `{"op":"shot","raw":true}`, `{"op":"do","shot":"A"}`} {
		var req request
		if err := json.Unmarshal([]byte(wire), &req); err != nil {
			t.Fatal(err)
		}
		got := s.dispatch(context.Background(), req, nil, &announced)
		if got.OK || got.Error != ports.ControlErrBadRequest || got.Detail == "" {
			t.Fatal(got)
		}
		if req.Raw && got.Detail != "raw shots need --every-frame" {
			t.Fatal(got)
		}
	}
}
