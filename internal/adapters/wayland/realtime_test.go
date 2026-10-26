package wayland

import (
	"testing"
	"time"

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
//testpolicy:realtime Server.Run waits on its libwayland epoll fd through the netpoller and Do/Post wake it with an eventfd; that wait is not durably blocked, so synctest.Wait hangs even with no client socket (verified)
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
//testpolicy:realtime Server.Run waits on its libwayland epoll fd through the netpoller and Do/Post wake it with an eventfd; that wait is not durably blocked, so synctest.Wait hangs even with no client socket (verified)
func flushCaptured(t *testing.T, s *Server, replies chan<- ports.CaptureDone) {
	t.Helper()
	const marker = ^uint64(0)
	ran := make(chan struct{})
	if !s.display.Do(func() { s.captureReplies[marker] = func(ports.CaptureDone) { close(ran) } }) {
		t.Fatal("display stopped")
	}
	select {
	case replies <- ports.CaptureDone{ID: marker}:
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
// dispatchUntil's single deadline.
func recvWire[T any](t *testing.T, c *wlturbo.Display, ch <-chan T) T {
	t.Helper()
	var v T
	dispatchUntil(t, c, func() bool {
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
