package wayland

import (
	"testing"
	"time"

	"github.com/bnema/wlturbo"
)

// waylandDeadline bounds a wait for events from a real Wayland display or client.
// Keep socket and descriptor tests outside synctest: kernel readiness is not a
// durably blocked operation in a fake-time bubble.
//
//testpolicy:realtime real Wayland unix sockets, display descriptors, and external protocol clients
func waylandDeadline(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// dispatchUntil reads actual wire events until the expected callback ran.
// Closing the connection on timeout releases a blocked Dispatch; there is no
// background dispatcher left running when this helper returns.
//
//testpolicy:realtime real Wayland unix socket between server and wlturbo client
func dispatchUntil(t *testing.T, c *wlturbo.Display, ready func() bool) {
	t.Helper()
	timer := time.AfterFunc(2*time.Second, func() { _ = c.Close() })
	defer timer.Stop()
	for !ready() {
		if err := c.Dispatch(); err != nil {
			t.Fatalf("waiting for wire event: %v", err)
		}
	}
}
