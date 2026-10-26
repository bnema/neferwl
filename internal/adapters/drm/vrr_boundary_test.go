package drm

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestVRRHoldExpiresAtBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &Output{vrrProp: 1, vrrOn: true, composedSince: time.Now()}
		time.Sleep(vrrHold - time.Nanosecond)
		if !o.wantVRR(false) {
			t.Fatal("VRR expired before the hold boundary")
		}
		time.Sleep(time.Nanosecond)
		if o.wantVRR(false) {
			t.Fatal("VRR retained at exactly vrrHold; a zero-delay timer would spin")
		}
	})
}
