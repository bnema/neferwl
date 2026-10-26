package wayland

import "time"

// waylandDeadline bounds a wait for events from a real Wayland display or client.
// Keep socket and descriptor tests outside synctest: kernel readiness is not a
// durably blocked operation in a fake-time bubble.
//
//testpolicy:realtime real Wayland unix sockets, display descriptors, and external protocol clients
func waylandDeadline(d time.Duration) <-chan time.Time {
	return time.After(d)
}
