package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
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

// flushCommands joins commands sent before this call. The forwarder pushes
// each command before receiving the next; cap+2 sends ensure it has received
// a marker after pushing all preceding commands. Its drain was therefore
// posted before our display.Do. ID zero is not a window, so markers are inert.
//
//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func flushCommands(t *testing.T, s *Server, commands chan<- ports.ClientCommand) {
	t.Helper()
	deadline := waylandDeadline(2 * time.Second)
	for range cap(commands) + 2 {
		select {
		case commands <- ports.ConfigureWindow{}:
		case <-deadline:
			t.Fatal("command forwarder stalled")
		}
	}
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
}

// flushCaptured sends a reply callback marker through the real reply path.
//
//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func flushCaptured(t *testing.T, s *Server, replies chan<- domain.CaptureDone) {
	t.Helper()
	const marker = ^uint64(0)
	ran := make(chan struct{})
	if !s.display.Do(func() { s.captureReplies[marker] = func(domain.CaptureDone) { close(ran) } }) {
		t.Fatal("display stopped")
	}
	select {
	case replies <- domain.CaptureDone{ID: marker}:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("reply forwarder stalled")
	}
	select {
	case <-ran:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("reply marker not applied")
	}
}

// recvWire dispatches wire events until a value arrives on ch, bounded by
// one deadline; what names the awaited event in the failure message.
func recvWire[T any](t *testing.T, c *wlturbo.Display, what string, ch <-chan T) T {
	t.Helper()
	var v T
	dispatchWire(t, c, what, func() bool {
		select {
		case v = <-ch:
			return true
		default:
			return false
		}
	})
	return v
}

// dispatchUntil reads actual wire events until the expected callback ran.
// Closing the connection on timeout releases a blocked Dispatch; there is no
// background dispatcher left running when this helper returns.
func dispatchUntil(t *testing.T, c *wlturbo.Display, ready func() bool) {
	t.Helper()
	dispatchWire(t, c, "wire event", ready)
}

// dispatchWire is dispatchUntil with a label for the failure message.
//
//testpolicy:realtime real Wayland unix socket between server and wlturbo client
func dispatchWire(t *testing.T, c *wlturbo.Display, what string, ready func() bool) {
	t.Helper()
	timer := time.AfterFunc(2*time.Second, func() { _ = c.Close() })
	defer timer.Stop()
	for !ready() {
		if err := c.Dispatch(); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
	}
}
