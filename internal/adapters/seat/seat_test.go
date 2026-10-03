package seat

import (
	"testing"

	"github.com/bnema/zerowrap"
)

// A seat disable lets the outputs reset the planes first, and only then
// acks the seat manager: after the ack the devices are revoked.
func TestDisableRunsBeforeTheAck(t *testing.T) {
	d := newMockdisabler(t)
	s := &Seat{log: zerowrap.Default(), disabler: d}
	var calls []string
	s.SetBeforeDisable(func() { calls = append(calls, "before") })
	d.EXPECT().disable().Run(func() { calls = append(calls, "ack") }).Return().Once()
	ch := s.Subscribe()
	<-ch // the initial state
	s.changed(false)
	if len(calls) != 2 || calls[0] != "before" || calls[1] != "ack" {
		t.Fatalf("order %v", calls)
	}
	if got := <-ch; got {
		t.Fatal("subscribers not told of the disable")
	}
}

// An enable needs neither: nothing is revoked.
func TestEnableDoesNotDisable(t *testing.T) {
	s := &Seat{log: zerowrap.Default(), disabler: newMockdisabler(t)}
	s.SetBeforeDisable(func() { t.Fatal("prepared on enable") })
	s.changed(true)
	if !s.active {
		t.Fatal("not active")
	}
}

// Without a registered callback the disable is acked as before.
func TestDisableWithoutCallback(t *testing.T) {
	d := newMockdisabler(t)
	d.EXPECT().disable().Return().Once()
	(&Seat{log: zerowrap.Default(), disabler: d}).changed(false)
}
