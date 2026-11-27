package control

import (
	"context"
	"encoding/json"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
)

func TestOutputDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{ids: workspaceid.WithPrefix("test")}
		out := make(chan ports.ControlRequest)
		for _, tc := range []struct {
			wire, edidID string
			remove       bool
		}{
			{`{"op":"output-add","mode":"1920x1080@60","name":"X","edid_id":"aa01"}`, "aa01", false},
			{`{"op":"output-add","mode":"1920x1080@60","name":"X"}`, "", false},
			{`{"op":"output-rm","name":"X"}`, "", true},
		} {
			var req request
			if err := json.Unmarshal([]byte(tc.wire), &req); err != nil {
				t.Fatal(err)
			}
			go func() {
				r := (<-out).(ports.ControlOutput)
				if r.Name != "X" || r.Caps != domain.CapsAll || r.Remove != tc.remove || r.EDIDID != tc.edidID {
					t.Errorf("%s: %+v", tc.wire, r)
				}
				r.Reply <- ports.ControlReply{}
			}()
			announced := false
			if r := s.dispatch(context.Background(), req, out, &announced); !r.OK {
				t.Fatal(r)
			}
		}
		for _, mode := range []string{"bad", "0x2", "2x3@NaN", "2x3@Inf"} {
			if _, err := domain.ParseOutputMode(mode); err == nil {
				t.Fatal(mode)
			}
		}
	})
}

// A supplied edid_id must be a valid identity on output-add, and is refused
// on output-rm; nothing reaches core.
func TestOutputDispatchRejectsEDIDID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{ids: workspaceid.WithPrefix("test")}
		out := make(chan ports.ControlRequest)
		for _, wire := range []string{
			`{"op":"output-add","mode":"1920x1080@60","edid_id":""}`,
			`{"op":"output-add","mode":"1920x1080@60","edid_id":"a b"}`,
			`{"op":"output-rm","name":"X","edid_id":"aa01"}`,
			`{"op":"output-rm","name":"X","edid_id":""}`,
		} {
			var req request
			if err := json.Unmarshal([]byte(wire), &req); err != nil {
				t.Fatal(err)
			}
			announced := false
			if r := s.dispatch(context.Background(), req, out, &announced); r.OK || r.Error != ports.ControlErrBadRequest {
				t.Fatalf("%s: %+v", wire, r)
			}
		}
	})
}
