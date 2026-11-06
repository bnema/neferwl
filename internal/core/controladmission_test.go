package core

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestControlInjectionAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		admission := make(chan ports.ControlAdmission, 1)
		reply := make(chan ports.ControlReply, 1)
		for range maxControlWaits {
			c.waitControl(c.state(), c.sceneSeqs(), domain.WaitCommitted, &domain.Until{Kind: domain.UntilWindowMapped, AppID: "missing"}, time.Minute, make(chan ports.ControlReply, 1), nil)
		}
		for _, tc := range []struct {
			caps domain.Caps
			want string
		}{
			{0, ports.ControlErrForbidden}, {domain.CapInput, ports.ControlErrBusy},
		} {
			err := c.controlInject(t.Context(), ports.ControlInject{Caps: tc.caps, Wait: domain.WaitSettled, Admission: admission, Reply: reply, Events: []ports.InputEvent{ports.InputDeviceAdded{ID: 77, Name: "not-admitted"}}})
			if err != nil {
				t.Fatal(err)
			}
			if a := <-admission; a.Err != tc.want {
				t.Fatalf("admission: %+v", a)
			}
			if r := <-reply; r.Err != tc.want {
				t.Fatalf("reply: %+v", r)
			}
			if c.inputNames[77] != "" {
				t.Fatal("refused events executed")
			}
		}
		// Admission is available while the completion predicate is still pending.
		c.controlWaits = nil
		err := c.controlInject(t.Context(), ports.ControlInject{Caps: domain.CapInput, Wait: domain.WaitSettled, Until: &domain.Until{Kind: domain.UntilWindowMapped, AppID: "missing"}, Admission: admission, Reply: reply})
		if err != nil {
			t.Fatal(err)
		}
		if a := <-admission; a.Err != "" {
			t.Fatalf("admission: %+v", a)
		}
		select {
		case r := <-reply:
			t.Fatalf("premature completion: %+v", r)
		default:
		}
	})
}
