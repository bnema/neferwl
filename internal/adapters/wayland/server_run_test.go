package wayland

import (
	"context"
	"sync/atomic"
	"testing"
)

var runningTestServers atomic.Int32

// guardedServerRun reserves the process-wide display slot before launching a
// goroutine. purego-libwayland's server globals require a single event loop.
// Release on Run's return, not Cleanup: callers must actually join the old loop
// before starting another one, even within the same test.
func guardedServerRun(t testing.TB, s *Server, ctx context.Context) func() error {
	t.Helper()
	if runningTestServers.Add(1) != 1 {
		runningTestServers.Add(-1)
		t.Fatal("overlapping test Server.Run: purego-libwayland requires one running display per process")
	}
	return func() error {
		defer runningTestServers.Add(-1)
		return s.Run(ctx)
	}
}
